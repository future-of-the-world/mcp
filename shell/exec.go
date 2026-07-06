// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync/atomic"
	"time"
)

const (
	// defaultShellBinary is the absolute path to the shell invoked with -c.
	// Hardcoded to /bin/sh — overrides are surfaced via connect.shell for
	// testing or exotic environments only.
	defaultShellBinary = "/bin/sh"

	// defaultTimeout is the per-call timeout applied when connect.timeout
	// is absent or invalid.
	defaultTimeout = 30 * time.Second

	// defaultMaxOutputBytes caps combined stdout+stderr bytes per
	// invocation when connect.max_output_bytes is absent or invalid.
	defaultMaxOutputBytes = 1 << 20 // 1 MiB
)

var errEmptyCommand = errors.New("shell: command is empty")

// runOpts are the parameters for a single shell invocation. The handler
// builds these from the per-source config plus the per-call request
// arguments; runShell treats them as already-validated.
type runOpts struct {
	Command  string
	CWD      string
	Env      map[string]string
	Shell    string
	Flags    []string
	Timeout  time.Duration
	MaxBytes int
}

// Result is the captured outcome of one shell invocation.
type Result struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Truncated  bool   `json:"truncated"`
}

// runShell spawns `sh -c <command>` with the configured working dir,
// env, timeout, and output cap, and returns the captured output and exit
// code.
//
// Non-zero exit codes are returned as Result{ExitCode: <code>} with a
// nil error — the handler treats non-zero as a result, not a tool error,
// matching the behavior of Zed's built-in shell tool. Timeouts and other
// Go-level failures (signal kills, exec errors) return
// Result{ExitCode: -1} plus a non-nil wrapped error.
//
//nolint:gocritic // opts is a value receiver on purpose
func runShell(ctx context.Context, opts runOpts) (Result, error) {
	if opts.Command == "" {
		return Result{}, errEmptyCommand
	}

	defaults := resolveRunDefaults(opts)

	runCtx, cancel := context.WithTimeout(ctx, defaults.Timeout)

	defer cancel()

	argv := buildArgv(defaults.Shell, defaults.Flags, opts.Command)

	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)

	if opts.CWD != "" {
		cmd.Dir = opts.CWD
	}

	if len(opts.Env) > 0 {
		cmd.Env = envMapToSlice(opts.Env)
	}

	var (
		stdoutBuf bytes.Buffer
		stderrBuf bytes.Buffer

		used      atomic.Int64
		truncated atomic.Bool
	)

	stdoutCap := &capWriter{
		used:      &used,
		truncated: &truncated,
		limit:     int64(defaults.MaxBytes),
		dest:      &stdoutBuf,
	}

	stderrCap := &capWriter{
		used:      &used,
		truncated: &truncated,
		limit:     int64(defaults.MaxBytes),
		dest:      &stderrBuf,
	}

	cmd.Stdout = stdoutCap
	cmd.Stderr = stderrCap

	start := time.Now()

	err := cmd.Run()

	duration := time.Since(start).Milliseconds()

	result := Result{
		Stdout:     encodeOutput(stdoutBuf.Bytes()),
		Stderr:     encodeOutput(stderrBuf.Bytes()),
		ExitCode:   -1, // overridden below for normal exits
		DurationMS: duration,
		Truncated:  truncated.Load(),
	}

	if err == nil {
		result.ExitCode = 0

		return result, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode := exitErr.ExitCode()

		// ExitError is returned for both "process exited with a non-zero
		// status" AND "process was terminated by a signal". When killed by
		// a signal — which is what exec.CommandContext does on timeout
		// or context cancellation — ExitCode() returns -1. Surface that
		// as the run-killed signal so the handler can report it as a
		// tool error rather than a non-zero exit data point.
		if exitCode == -1 {
			result.ExitCode = -1

			return result, fmt.Errorf("shell: run: %w", err)
		}

		result.ExitCode = exitCode

		return result, nil
	}

	// Process was killed (timeout, signal, or other Go-level error).
	// Report exit_code = -1 plus a wrapped error so the handler can
	// surface it as a tool error.
	result.ExitCode = -1

	return result, fmt.Errorf("shell: run: %w", err)
}

// runDefaults is the post-default shape of runOpts — the values
// runShell actually consumes after applying per-field defaults.
// Returning a struct (rather than 4 positional values) keeps the
// revive function-result-limit satisfied and gives the helper a
// readable signature.
type runDefaults struct {
	Shell    string
	Flags    []string
	Timeout  time.Duration
	MaxBytes int
}

// resolveRunDefaults applies the per-field defaults for shell binary,
// flag list, timeout, and max bytes so the runShell body stays
// focused on lifecycle and output capture. Each defaulting rule is
// identical to the inline version it replaces; pulling them into a
// helper keeps the runShell function under the 40-statement funlen
// cap while leaving the contract obvious.
//
//nolint:gocritic // same value receiver on purpose
func resolveRunDefaults(opts runOpts) runDefaults {
	defaults := runDefaults{
		Shell:    opts.Shell,
		Flags:    opts.Flags,
		Timeout:  opts.Timeout,
		MaxBytes: opts.MaxBytes,
	}

	if defaults.Shell == "" {
		defaults.Shell = defaultShellBinary
	}

	if len(defaults.Flags) == 0 {
		defaults.Flags = defaultShellFlags
	}

	if defaults.Timeout <= 0 {
		defaults.Timeout = defaultTimeout
	}

	if defaults.MaxBytes <= 0 {
		defaults.MaxBytes = defaultMaxOutputBytes
	}

	return defaults
}

// envMapToSlice flattens a string→string env map into the
// "key=value" slice form exec.Cmd.Env expects. Order is non-deterministic
// (Go map iteration); shells and POSIX tools do not depend on env order
// beyond PATH-equivalent lookups, and cmd.Env is consulted via the
// kernel's linear scan during execve.
func envMapToSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))

	for key, value := range env {
		out = append(out, key+"="+value)
	}

	return out
}

// capWriter counts bytes written across one or more streams against a
// shared cap. Once the cap is exceeded, subsequent writes succeed
// silently so the child process never blocks on a full pipe — the bytes
// are dropped, and `truncated` is set the first time the cap is hit.
//
// Both writers (stdout, stderr) share the same used/truncated counters
// so the cap is enforced across combined output, as the spec requires.
// The used counter is atomic because both writers are called from the
// exec.Cmd internals on the same goroutine, but writing is forward-only
// and racy reads cannot over- or under-count past the cap in a way that
// affects wire output.
type capWriter struct {
	used      *atomic.Int64
	truncated *atomic.Bool
	limit     int64
	dest      io.Writer
}

// Write satisfies io.Writer. It writes as many bytes as the cap allows
// to the underlying buffer and discards the rest. It always reports the
// full len(payload) as consumed so cmd.Run does not propagate a
// short-write error from the writer side.
//
// The parameter names (payload, count) match the io.Writer contract's
// `Write(p []byte) (n int, err error)` while satisfying varnamelen's
// min-name-length requirement for non-interface methods.
func (w *capWriter) Write(payload []byte) (int, error) {
	pre := w.used.Load()

	if pre >= w.limit {
		// Already at or past the cap. Drop everything silently.
		w.used.Add(int64(len(payload)))
		w.truncated.Store(true)

		return len(payload), nil
	}

	remaining := w.limit - pre

	if int64(len(payload)) > remaining {
		// Partial: keep the first `remaining` bytes, drop the rest.
		// The underlying writer is a bytes.Buffer in production; the
		// Write call cannot fail in a way the caller could recover from.
		//nolint:errcheck // bytes.Buffer.Write never errors
		_, _ = w.dest.Write(payload[:remaining])
		w.used.Add(int64(len(payload)))
		w.truncated.Store(true)

		return len(payload), nil
	}

	count, err := w.dest.Write(payload)
	w.used.Add(int64(count))

	// capWriter implements io.Writer; wrapping here would violate the
	// contract that downstream callers can compare against the original
	// error.
	//nolint:wrapcheck // passthrough required by io.Writer
	return count, err
}

// buildArgv assembles the argv passed to exec.CommandContext for a
// shell invocation. The shape is always `[shell, flags..., command]`:
//
//   - The shell binary is argv[0]; exec.CommandContext uses it to
//     resolve the executable path via $PATH (or the absolute path
//     when the operator set connect.shell).
//   - The configured flags are inserted as-is, in order. The common
//     case is `["-c"]` (POSIX command-string mode); zsh operators
//     typically use `["-lic"]` for login + interactive + command
//     so .zprofile and .zshrc are sourced before the command runs.
//   - The command string is the final element. The POSIX convention
//     `<shell> [flags...] -c <command>` is preserved as long as
//     the operator's flag list ends with `-c` (or whatever the shell
//     expects as the "next arg is the command" sentinel).
//
// buildArgv is a free function rather than inlined so exec tests can
// pin the exact shape without spawning a real process.
func buildArgv(shell string, flags []string, command string) []string {
	argv := make([]string, 0, 2+len(flags))

	argv = append(argv, shell)
	argv = append(argv, flags...)
	argv = append(argv, command)

	return argv
}
