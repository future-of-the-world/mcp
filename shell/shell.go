// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package shell implements an MCP source that executes shell commands.
// Connect decodes the source's `connect:` map (working_dir, timeout,
// max_output_bytes, shell, env) and returns a single run_command tool
// which spawns `sh -c <command>` and returns the captured output.
//
// This is a thin drop-in replacement for the shell tooling built into
// MCP hosts (notably Zed): the LLM writes shell syntax and gets back
// stdout, stderr, and exit code. The surrounding guardrails — working-
// directory confinement, explicit env, hard timeout, output cap with
// truncation, and honest DestructiveHint=true annotations — are the
// safety surface the rest of this meta-server provides.
//
// Security model: this source can execute arbitrary code. The MVP
// trusts the operator (same trust model as the postgres source for SQL)
// and the LLM (same trust model as Zed's built-in shell tool). The
// guardrails narrow the blast radius but do not eliminate it. Operators
// who want a stricter model should pair this source with a host that
// gates tool invocation on DestructiveHint.
package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "embed"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/tool"
)

// --- Embedded input/output JSON Schemas ---

//go:embed schemas/shell_run_input.json
var shellRunInput json.RawMessage

//go:embed schemas/shell_output.json
var shellOutput json.RawMessage

// --- Sentinel errors ---

var (
	errWorkingDirEmpty = errors.New("shell: connect.working_dir is required")
	errTimeoutNegative = errors.New("shell: connect.timeout must be non-negative")
	errMaxBytesNonPos  = errors.New("shell: connect.max_output_bytes must be positive")
	errEnvWrongType    = errors.New("shell: connect.env must be a string map")
)

// --- Duration codec (shared with websearch/websearch.go pattern) ---

// Duration is a time.Duration that unmarshals from a human-readable
// string (e.g. "30s", "1m30s") in both YAML and JSON configs. Numbers
// and other non-string values are rejected at decode time — there is no
// implicit stringification path.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler for Duration.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var input string

	var err error

	err = node.Decode(&input)
	if err != nil {
		return fmt.Errorf("decode duration string: %w", err)
	}

	parsed, err := time.ParseDuration(input)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", input, err)
	}

	*d = Duration(parsed)

	return nil
}

// UnmarshalJSON implements json.Unmarshaler for Duration.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var input string

	var err error

	err = json.Unmarshal(data, &input)
	if err != nil {
		return fmt.Errorf("decode duration string: %w", err)
	}

	parsed, err := time.ParseDuration(input)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", input, err)
	}

	*d = Duration(parsed)

	return nil
}

// Duration returns the underlying time.Duration value.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// --- Configuration decoding ---

// defaultShellFlags is the argv inserted between the shell binary
// and the command string when connect.shell_flags is absent or empty.
// The shape matches the POSIX convention `<shell> -c <command>` and
// applies to sh, bash, zsh, and dash. Operators that want rc-file
// sourcing override this with `shell_flags: ["-lic"]` (zsh login
// + interactive) or `["-l"]` (bash login) and the meta-server passes
// those flags through to exec.CommandContext unchanged.
var defaultShellFlags = []string{"-c"}

// config holds the decoded `connect:` map for a shell source. WorkingDir
// is required; the remaining fields have defaults applied by validate
// or by the handler at call time.
type config struct {
	WorkingDir     string
	Timeout        Duration
	MaxOutputBytes int
	Shell          string
	Flags          []string
	Env            map[string]string
}

// decodeConnect decodes the source's `connect:` map into a config.
// Scalar string fields are decoded through decode.AsString so
// YAML-natural values (numbers, bools, null) are accepted and
// stringified; non-scalar values produce a wrapped decode.ErrWrongType
// error so genuine config bugs surface as a clear message rather than a
// silent "field is empty" downstream.
func decodeConnect(connect map[string]any) (config, error) {
	var cfg config

	workDir, err := decode.AsString(connect["working_dir"])
	switch {
	case err == nil:
		cfg.WorkingDir = workDir

	case errors.Is(err, decode.ErrNotSet):
		// working_dir is required; validate() surfaces the error.

	default:
		return cfg, fmt.Errorf("connect.working_dir: %w", err)
	}

	timeout, decodeErr := decodeDuration(connect, "timeout")
	if decodeErr != nil {
		return cfg, fmt.Errorf("connect.timeout: %w", decodeErr)
	}

	cfg.Timeout = timeout

	maxBytes, decodeErr := decodeInt(connect, "max_output_bytes")
	if decodeErr != nil {
		return cfg, fmt.Errorf("connect.max_output_bytes: %w", decodeErr)
	}

	cfg.MaxOutputBytes = maxBytes

	shell, err := decode.AsString(connect["shell"])
	switch {
	case err == nil:
		cfg.Shell = shell

	case errors.Is(err, decode.ErrNotSet):
		// shell is optional; defaults to /bin/sh.

	default:
		return cfg, fmt.Errorf("connect.shell: %w", err)
	}

	env, decodeErr := decodeStringMap(connect, "env")
	if decodeErr != nil {
		return cfg, fmt.Errorf("connect.env: %w", decodeErr)
	}

	cfg.Env = env

	flags, decodeErr := decodeStringSlice(connect, "shell_flags")
	if decodeErr != nil {
		return cfg, fmt.Errorf("connect.shell_flags: %w", decodeErr)
	}

	cfg.Flags = flags

	return cfg, nil
}

// decodeDuration decodes a string→Duration field from connect. A
// missing key yields the zero Duration (the caller decides whether that
// is acceptable). Non-scalar values produce a wrapped
// decode.ErrWrongType error.
func decodeDuration(connect map[string]any, key string) (Duration, error) {
	raw, ok := connect[key]
	if !ok {
		return 0, nil
	}

	str, err := decode.AsString(raw)
	if err != nil {
		return 0, fmt.Errorf("connect.%s: %w", key, err)
	}

	parsed, parseErr := time.ParseDuration(str)
	if parseErr != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", str, parseErr)
	}

	return Duration(parsed), nil
}

// decodeInt decodes an integer field from connect. Accepts int, int64,
// and float64 (YAML and JSON both produce float64 for non-typed
// numerics); other types produce an error.
func decodeInt(connect map[string]any, key string) (int, error) {
	raw, ok := connect[key]
	if !ok {
		return 0, nil
	}

	switch val := raw.(type) {
	case int:
		return val, nil

	case int64:
		return int(val), nil

	case float64:
		return int(val), nil

	default:
		return 0, fmt.Errorf("must be an integer, got %T", raw)
	}
}

// decodeStringMap decodes a string→string map field from connect. Each
// value is coerced through decode.AsString; non-scalar values produce
// a wrapped decode.ErrWrongType so the caller sees the actual Go type
// in the error message.
func decodeStringMap(connect map[string]any, key string) (map[string]string, error) {
	raw, ok := connect[key]
	if !ok {
		return make(map[string]string), nil
	}

	switch val := raw.(type) {
	case map[string]any:
		out := make(map[string]string, len(val))

		for mapKey, v := range val {
			str, err := decode.AsString(v)
			if err != nil {
				return nil, errors.Join(
					errEnvWrongType,
					fmt.Errorf("entry %q: %w", mapKey, err),
				)
			}

			out[mapKey] = str
		}

		return out, nil

	case nil:
		return make(map[string]string), nil

	default:
		return nil, fmt.Errorf("%w: got %T", errEnvWrongType, raw)
	}
}

// decodeStringSlice decodes a string slice field from connect. Each
// element must be a scalar (string or numeric); non-string elements
// produce an error so a misconfigured value surfaces as a clear
// message rather than silently dropping the slice.
//
// Both `[]any` (the YAML/JSON decoder's natural output) and
// `[]string` (what callers in tests hand in directly) are accepted.
// The []string case is treated as already-typed — elements skip the
// decode.AsString numeric-coercion path — but the result is identical
// because every element in a Go []string is already a string.
func decodeStringSlice(connect map[string]any, key string) ([]string, error) {
	raw, ok := connect[key]
	if !ok {
		return []string{}, nil
	}

	switch val := raw.(type) {
	case []string:
		return val, nil

	case []any:
		out := make([]string, 0, len(val))

		for i, v := range val {
			str, err := decode.AsString(v)
			if err != nil {
				return nil, fmt.Errorf("connect.%s[%d]: %w", key, i, err)
			}

			out = append(out, str)
		}

		return out, nil

	default:
		return nil, fmt.Errorf("connect.%s must be a list of strings, got %T", key, raw)
	}
}

// validate checks that the decoded config is usable: WorkingDir is
// required, Timeout must be non-negative when set, MaxOutputBytes must
// be non-negative when set. The zero values for the optional fields
// are not flagged here — defaults are applied at call time.
func (c *config) validate() error {
	if c.WorkingDir == "" {
		return errWorkingDirEmpty
	}

	if c.Timeout < 0 {
		return errTimeoutNegative
	}

	if c.MaxOutputBytes < 0 {
		return errMaxBytesNonPos
	}

	return nil
}

// --- Connect entry point ---

// Connect decodes the source's `connect:` map, validates it, and
// returns the run_command tool. The tool advertises
// ReadOnlyHint=false, DestructiveHint=true, IdempotentHint=false,
// OpenWorldHint=false — host implementations that gate tool invocation
// on these hints will treat shell correctly as a write/destructive,
// local-scoped capability.
//
// After validation, Connect canonicalizes connect.working_dir via
// filepath.EvalSymlinks and opens an os.Root at the canonical path.
// Both the root and the canonical path are captured in the handler
// closure so per-call directory overrides can be validated against
// the same root every invocation, regardless of whether the LLM
// passes the symlinked or canonical form of any path inside it.
func Connect(
	_ context.Context,
	connect map[string]any,
	_ ...tool.Option,
) (tool.Response, error) {
	cfg, err := decodeConnect(connect)
	if err != nil {
		return tool.Response{}, fmt.Errorf("shell: decode: %w", err)
	}

	validateErr := cfg.validate()
	if validateErr != nil {
		return tool.Response{}, fmt.Errorf("shell: validate: %w", validateErr)
	}

	canonWorking, canonErr := filepath.EvalSymlinks(cfg.WorkingDir)
	if canonErr != nil {
		return tool.Response{}, fmt.Errorf(
			"shell: resolve working_dir %q: %w",
			cfg.WorkingDir,
			canonErr,
		)
	}

	root, rootErr := os.OpenRoot(canonWorking)
	if rootErr != nil {
		return tool.Response{}, fmt.Errorf("shell: open root %q: %w", canonWorking, rootErr)
	}

	destructive := true
	notOpenWorld := false

	return tool.Response{
		Tools: []tool.Tool{
			{
				Tool: &mcp.Tool{
					Name:         "run_command",
					Description:  runCommandDescription,
					InputSchema:  shellRunInput,
					OutputSchema: shellOutput,
					Annotations: &mcp.ToolAnnotations{
						Title:           "",
						ReadOnlyHint:    false,
						DestructiveHint: &destructive,
						IdempotentHint:  false,
						OpenWorldHint:   &notOpenWorld,
					},
				},
				Handler: handleRunCommand(&cfg, root, canonWorking),
			},
		},
	}, nil
}
