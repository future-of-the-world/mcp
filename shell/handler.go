// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runCommandDescription is the LLM-facing summary of the run_command tool.
// It is the same text the agent sees via tools.list, so the model can
// recall the contract without consulting external docs.
const runCommandDescription = `Execute a shell command and return its captured output.

The command string is passed to /bin/sh -c, so all shell features
(pipes, redirects, globs, command substitution) work. The working
directory must be passed on every call as an absolute directory path
inside connect.working_dir; an empty or omitted directory is rejected
with a tool error. Environment variables come from connect.env plus
any per-call env overrides; nothing is inherited from the parent
process.

Output is capped at connect.max_output_bytes (default 1 MiB) across
both streams. stdout and stderr are returned as UTF-8 text when
possible; non-UTF-8 output is returned as a base64 string with a
'b64:' prefix.

Non-zero exit codes are returned in exit_code, not as a tool error.
This matches the behavior of the host's built-in shell tool and lets
the model inspect the result without retrying. A timeout, signal, or
other kill returns exit_code = -1 plus a tool error.
`

// runCommandArgs is the JSON input to the run_command tool. Command is
// required; Directory, Timeout, and Env are optional per-call
// overrides. Directory must be an absolute path inside the source's
// connect.working_dir — see resolveDirectory for the validation rules.
type runCommandArgs struct {
	Command   string            `json:"command"`
	Directory string            `json:"directory"`
	Timeout   string            `json:"timeout,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// Sentinel errors for per-call directory resolution. The handler
// surfaces these to the LLM as tool errors with stable, parseable
// messages; tests use errors.Is to assert the category without
// depending on the message text.
var (
	errDirectoryRequired = errors.New("shell: directory is required")
	errDirectoryEscapes  = errors.New("shell: directory escapes working_dir")
	errDirectoryNotExist = errors.New("shell: directory does not exist within working_dir")
)

// handleRunCommand returns the mcp.ToolHandler that drives the
// run_command tool. It captures the per-source config in its closure
// and applies per-call overrides on top of the source-level defaults.
// root is the os.Root opened at the canonical working_dir; canonWorking
// is that canonical path. Together they let resolveDirectory enforce
// the "must be inside working_dir" containment rule per call.
func handleRunCommand(cfg *config, root *os.Root, canonWorking string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args runCommandArgs

		parseErr := json.Unmarshal(req.Params.Arguments, &args)
		if parseErr != nil {
			return nil, fmt.Errorf("shell: parse run_command args: %w", parseErr)
		}

		if args.Command == "" {
			return nil, errEmptyCommand
		}

		opts, optsErr := buildRunOpts(cfg, args, root, canonWorking)
		if optsErr != nil {
			return nil, optsErr
		}

		result, runErr := runShell(ctx, opts)
		if runErr != nil {
			return nil, fmt.Errorf("shell: run: %w", runErr)
		}

		return textResult(result)
	}
}

// buildRunOpts merges per-source defaults with per-call overrides into
// a runOpts ready to hand to runShell. Validation that depends on the
// per-call shape (timeout parse, env key types, directory confinement)
// happens here so the handler stays focused on dispatch.
func buildRunOpts(
	cfg *config,
	args runCommandArgs,
	root *os.Root,
	canonWorking string,
) (runOpts, error) {
	shell := cfg.Shell
	if shell == "" {
		shell = defaultShellBinary
	}

	flags := cfg.Flags
	if len(flags) == 0 {
		// Default applied here so handlers and tests see the same
		// effective argv without each having to remember the fallback.
		flags = defaultShellFlags
	}

	timeout := time.Duration(cfg.Timeout)
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	maxBytes := cfg.MaxOutputBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxOutputBytes
	}

	cwd, cwdErr := resolveDirectory(root, canonWorking, args.Directory)
	if cwdErr != nil {
		return runOpts{}, cwdErr
	}

	if args.Timeout != "" {
		parsed, err := time.ParseDuration(args.Timeout)
		if err != nil {
			return runOpts{}, fmt.Errorf("shell: invalid timeout %q: %w", args.Timeout, err)
		}

		timeout = parsed
	}

	mergedEnv := make(map[string]string, len(cfg.Env)+len(args.Env))
	maps.Copy(mergedEnv, cfg.Env)
	maps.Copy(mergedEnv, args.Env)

	return runOpts{
		Command:  args.Command,
		CWD:      cwd,
		Env:      mergedEnv,
		Shell:    shell,
		Flags:    flags,
		Timeout:  timeout,
		MaxBytes: maxBytes,
	}, nil
}

// resolveDirectory interprets the per-call `directory` request and
// returns the absolute path to pass to cmd.Dir. The contract is:
//
//   - empty/missing   → errDirectoryRequired (mandatory per-call field
//     — the schema `required` array catches this too, but the Go
//     guard is here so direct callers stay honest)
//   - non-absolute    → errDirectoryEscapes (LLM confused the contract)
//   - absolute, exists, canonical form inside canonWorking, is a
//     directory → returned unchanged
//   - absolute, canonical form outside canonWorking → errDirectoryEscapes
//   - absolute, points through a symlink that leaves the root →
//     errDirectoryEscapes (caught by os.Root.Stat via RESOLVE_BENEATH)
//   - absolute, does not exist → errDirectoryNotExist
//   - absolute, exists but is not a directory → errDirectoryNotExist
//
// Both sides are canonicalized via filepath.EvalSymlinks before the
// relative-path computation so that hosts where working_dir itself
// contains a symlink (e.g. /var → /private/var on macOS) produce a
// consistent relative path regardless of which form the LLM passes.
func resolveDirectory(root *os.Root, canonWorking, requested string) (string, error) {
	if requested == "" {
		return "", fmt.Errorf("%w", errDirectoryRequired)
	}

	if !filepath.IsAbs(requested) {
		return "", fmt.Errorf("%w: %q (must be an absolute path)", errDirectoryEscapes, requested)
	}

	// EvalSymlinks first: ENOENT here means the path genuinely does not
	// exist, which we surface as a not-exist (distinct from escape). Any
	// other error (permission, IO) is treated as an escape because we
	// cannot prove the path is inside the root.
	canonRequested, err := filepath.EvalSymlinks(requested)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %q", errDirectoryNotExist, requested)
		}

		return "", fmt.Errorf("%w: %q: %w", errDirectoryEscapes, requested, err)
	}

	// filepath.Rel is purely lexical. Because both sides are
	// canonicalized above, Rel on a path-inside-the-root produces a
	// path like "sub" or "."; a path-outside-the-root produces a path
	// starting with ".." which os.Root.Stat will then reject.
	rel, relErr := filepath.Rel(canonWorking, canonRequested)
	if relErr != nil {
		return "", fmt.Errorf("%w: %q: %w", errDirectoryEscapes, requested, relErr)
	}

	// os.Root.Stat is the security boundary. RESOLVE_BENEATH (on Linux)
	// and equivalents on macOS / Windows ensure that every symlink in
	// the chain (up to __POSIX_SYMLOOP_MAX = 8) stays inside the root.
	// ENOENT is a clean not-exist; anything else (EXDEV from symlink
	// escape, EACCES, etc.) is an escape / inaccessible case from the
	// operator's perspective.
	info, statErr := root.Stat(rel)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %q", errDirectoryNotExist, requested)
		}

		return "", fmt.Errorf("%w: %q: %w", errDirectoryEscapes, requested, statErr)
	}

	if !info.IsDir() {
		return "", fmt.Errorf("%w: %q", errDirectoryNotExist, requested)
	}

	return requested, nil
}

// textResult marshals value to JSON and returns a *mcp.CallToolResult
// containing a single TextContent. It mirrors the websearch.textResult
// helper but lives here to avoid an unrelated package import.
//
// Per the MCP spec, StructuredContent must marshal to a JSON object. The
// unmarshal to map[string]any succeeds only when data is a valid JSON
// object; a JSON array, primitive, or malformed value returns Content
// only (StructuredContent stays nil).
func textResult(value any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}

	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}

	var probe map[string]any
	if json.Unmarshal(data, &probe) == nil {
		result.StructuredContent = json.RawMessage(data)
	}

	return result, nil
}
