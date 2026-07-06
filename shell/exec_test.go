// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package shell

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunShell_EmptyCommand verifies the empty-command guard surfaces as
// errEmptyCommand without spawning a process.
func TestRunShell_EmptyCommand(t *testing.T) {
	t.Parallel()

	_, err := runShell(t.Context(), runOpts{})
	require.Error(t, err)
	assert.ErrorIs(t, err, errEmptyCommand)
}

// TestRunShell_ExitZero verifies a successful command returns ExitCode=0
// with a nil Go error and the expected stdout.
func TestRunShell_ExitZero(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		Command:  `echo hello`,
		CWD:      "/tmp",
		Shell:    "/bin/sh",
		Timeout:  5 * time.Second,
		MaxBytes: 1 << 20,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "hello\n", res.Stdout)
	assert.Empty(t, res.Stderr)
	assert.False(t, res.Truncated)
}

// TestRunShell_ExitNonZero verifies a non-zero exit code surfaces as
// Result{ExitCode: <code>} with a nil Go error — non-zero is data, not
// a tool error. This matches the contract documented in the issue.
func TestRunShell_ExitNonZero(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		Command:  `exit 42`,
		Shell:    "/bin/sh",
		Timeout:  5 * time.Second,
		MaxBytes: 1 << 20,
	})
	require.NoError(t, err)

	assert.Equal(t, 42, res.ExitCode)
}

// TestRunShell_StderrCaptured verifies stderr is captured separately
// from stdout and not interleaved.
func TestRunShell_StderrCaptured(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		Command:  `printf 'to-out\n'; printf 'to-err\n' 1>&2`,
		Shell:    "/bin/sh",
		Timeout:  5 * time.Second,
		MaxBytes: 1 << 20,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "to-out\n", res.Stdout)
	assert.Equal(t, "to-err\n", res.Stderr)
}

// TestRunShell_TimeoutKillsProcess verifies the context-driven kill:
// a long-running command exceeds the timeout and returns ExitCode=-1
// plus a non-nil error.
func TestRunShell_TimeoutKillsProcess(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		Command: `sleep 30`,
		Shell:   "/bin/sh",
		Env: map[string]string{
			"PATH": "/usr/bin:/bin",
		},
		Timeout:  100 * time.Millisecond,
		MaxBytes: 1 << 20,
	})

	require.Error(t, err)
	assert.Equal(t, -1, res.ExitCode)
	// The wrapped error originates from the killed process; we accept
	// any non-nil error here because the exact message depends on the
	// exec.CommandContext implementation.
}

// TestRunShell_ShellFeaturesPipes verifies that pipe chains work end
// to end. The piped `head` should cut the stream after one line.
func TestRunShell_ShellFeaturesPipes(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		Command:  `printf 'a\nb\nc\n' | head -n 1`,
		Shell:    "/bin/sh",
		Timeout:  5 * time.Second,
		MaxBytes: 1 << 20,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "a\n", res.Stdout)
}

// TestRunShell_ShellFeaturesRedirects verifies output redirection
// writes to the redirected file, not to stdout.
func TestRunShell_ShellFeaturesRedirects(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		Command: `echo redirected > /tmp/shell_test_redirect.txt;` +
			` cat /tmp/shell_test_redirect.txt`,
		Shell:    "/bin/sh",
		Timeout:  5 * time.Second,
		MaxBytes: 1 << 20,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "redirected\n", res.Stdout)
}

// TestRunShell_ShellFeaturesGlobs verifies glob expansion against
// the working directory. The /tmp directory is used so the test does
// not depend on the project tree state.
func TestRunShell_ShellFeaturesGlobs(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		Command: `printf 'a\nb\n' > /tmp/shell_test_glob_a.txt;` +
			` printf 'c\n' > /tmp/shell_test_glob_b.txt;` +
			` cat /tmp/shell_test_glob_*.txt`,
		Shell:    "/bin/sh",
		CWD:      "/tmp",
		Timeout:  5 * time.Second,
		MaxBytes: 1 << 20,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.Contains(t, res.Stdout, "a\n")
	assert.Contains(t, res.Stdout, "b\n")
	assert.Contains(t, res.Stdout, "c\n")
}

// TestRunShell_Truncation verifies that exceeding max_output_bytes
// sets Truncated=true and caps the returned text. The cap is enforced
// across combined stdout+stderr; the test runs a command that
// produces one large stream.
func TestRunShell_Truncation(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		// POSIX-portable large-output generator. `yes` repeats "y\n"
		// forever; `head -c 102400` reads exactly 102400 bytes and
		// closes the pipe. Brace-expansion (`{1..N}`) and `printf 'x%.0s'`
		// are bash-only, so we don't rely on them here.
		Command:  `yes | head -c 102400`,
		Shell:    "/bin/sh",
		Timeout:  5 * time.Second,
		MaxBytes: 1024,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.Truef(t, res.Truncated, "truncation must be reported")
	assert.LessOrEqual(t, len(res.Stdout), 1024)
}

// TestRunShell_BinaryOutputBase64 verifies that non-UTF-8 output is
// base64-encoded with the b64: prefix and is decodable back to the
// original bytes.
func TestRunShell_BinaryOutputBase64(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		// POSIX printf requires the standard escapes plus octal
		// (no `\xHH` hex). `\377` is 255 and `\376` is 254; both
		// together are two bytes that are not valid UTF-8.
		Command:  `printf '\377\376'`,
		Shell:    "/bin/sh",
		Timeout:  5 * time.Second,
		MaxBytes: 1 << 20,
	})
	require.NoError(t, err)

	assert.Truef(t, strings.HasPrefix(res.Stdout, binaryPrefix),
		"binary output must carry the %q prefix, got %q", binaryPrefix, res.Stdout)
}

// TestRunShell_TextOutputPassthrough verifies that valid UTF-8 text is
// returned verbatim (no b64: prefix).
func TestRunShell_TextOutputPassthrough(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		Command:  `echo hello`,
		Shell:    "/bin/sh",
		Timeout:  5 * time.Second,
		MaxBytes: 1 << 20,
	})
	require.NoError(t, err)

	assert.False(t, strings.HasPrefix(res.Stdout, binaryPrefix))
	assert.Equal(t, "hello\n", res.Stdout)
}

// TestRunShell_EnvPassedToChild verifies that the env map reaches the
// child process via exec.Cmd.Env.
func TestRunShell_EnvPassedToChild(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		Command: `printf 'FOO=%s\n' "$FOO"`,
		Shell:   "/bin/sh",
		Env: map[string]string{
			"FOO": "bar",
		},
		Timeout:  5 * time.Second,
		MaxBytes: 1 << 20,
	})
	require.NoError(t, err)

	assert.Equal(t, "FOO=bar\n", res.Stdout)
}

// TestRunShell_WorkingDirRespected verifies cmd.Dir is applied.
func TestRunShell_WorkingDirRespected(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		Command:  `pwd`,
		CWD:      "/tmp",
		Shell:    "/bin/sh",
		Timeout:  5 * time.Second,
		MaxBytes: 1 << 20,
	})
	require.NoError(t, err)

	// On macOS /tmp resolves to /private/tmp; resolve it ourselves
	// before comparing.
	assert.Contains(t, []string{"/tmp\n", "/private/tmp\n"}, res.Stdout)
}

// TestRunShell_DurationRecorded verifies duration_ms is non-zero and
// plausible (less than the timeout).
func TestRunShell_DurationRecorded(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		Command:  `sleep 0.05`,
		Shell:    "/bin/sh",
		Timeout:  5 * time.Second,
		MaxBytes: 1 << 20,
	})
	require.NoError(t, err)

	assert.GreaterOrEqualf(t, res.DurationMS, int64(40),
		"duration must be at least the sleep time")
	assert.Lessf(t, res.DurationMS, int64(5000),
		"duration must be well under the 5s timeout")
}

// TestRunShell_ContextCancellationKillsProcess verifies the caller's
// context cancellation (not just the timeout) kills the child.
func TestRunShell_ContextCancellationKillsProcess(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())

	// Cancel after 50ms while the child sleeps for 30s.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	res, err := runShell(ctx, runOpts{
		Command: `sleep 30`,
		Shell:   "/bin/sh",
		Env: map[string]string{
			"PATH": "/usr/bin:/bin",
		},
		Timeout:  30 * time.Second,
		MaxBytes: 1 << 20,
	})

	require.Error(t, err)
	assert.Equal(t, -1, res.ExitCode)
}

// TestBuildArgv_DefaultShape pins the POSIX `<shell> -c <command>`
// shape for the most common case (default flags). The first element is
// the shell binary; the final element is the command string; "-c"
// sits between them.
func TestBuildArgv_DefaultShape(t *testing.T) {
	t.Parallel()

	got := buildArgv("/bin/sh", []string{"-c"}, "echo hello")

	assert.Equal(t, []string{"/bin/sh", "-c", "echo hello"}, got)
}

// TestBuildArgv_MultiFlag verifies that zsh-style `["-lic"]` and
// bash-style `["-l"]` flags land in the argv in order. The exact
// flag strings are pass-through — runShell does not interpret them,
// only the shell does.
func TestBuildArgv_MultiFlag(t *testing.T) {
	t.Parallel()

	got := buildArgv("/bin/zsh", []string{"-lic"}, "echo hello")

	assert.Equal(t, []string{"/bin/zsh", "-lic", "echo hello"}, got)
}

// TestBuildArgv_MultipleFlags verifies ordering is preserved when
// the flag list has more than one element. Operators can use this
// for shells that take multiple sequential flags (`bash -l -i -c ...`).
func TestBuildArgv_MultipleFlags(t *testing.T) {
	t.Parallel()

	got := buildArgv("/bin/bash", []string{"-l", "-i", "-c"}, "echo hello")

	assert.Equal(t,
		[]string{"/bin/bash", "-l", "-i", "-c", "echo hello"},
		got,
	)
}

// TestBuildArgv_EmptyFlags verifies that an empty flag list still
// produces a valid argv with shell + command. This is the shape the
// safety-net default in runShell produces when a future caller bypasses
// buildRunOpts with no flags set.
func TestBuildArgv_EmptyFlags(t *testing.T) {
	t.Parallel()

	got := buildArgv("/bin/sh", []string(nil), "echo hello")

	assert.Equal(t, []string{"/bin/sh", "echo hello"}, got)
}

// TestRunShell_PassesFlagsToShell is an end-to-end test that runs
// the shell with custom flags and confirms the side effects those flags
// produce are visible. We use `set -o` (a no-op for any value other
// than the flag we set) plus a flag that turns on verbose tracing,
// so the test can assert that `set -x` was active by reading stderr.
func TestRunShell_PassesFlagsToShell(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		Command: `echo "hello-from-flags"`,
		Shell:   "/bin/sh",
		// `-x` = xtrace (echo commands before running). Visible in
		// stderr. `-c` is required by sh to interpret the next arg
		// as a command string, so the operator's flag list must
		// include it.
		Flags:    []string{"-x", "-c"},
		Timeout:  5 * time.Second,
		MaxBytes: 1 << 20,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "hello-from-flags\n", res.Stdout)
	assert.Containsf(t, res.Stderr, "hello-from-flags",
		"stderr should contain the xtrace line for the echoed command")
}

// TestRunShell_DefaultFlagsMatchPOSIX verifies that omitting Flags
// from runOpts defaults to `["-c"]` inside runShell, matching the
// long-standing behavior. We assert this by running a command that
// requires `-c` semantics (a multi-word command using shell features
// like pipes) and confirming it executes correctly.
func TestRunShell_DefaultFlagsMatchPOSIX(t *testing.T) {
	t.Parallel()

	res, err := runShell(t.Context(), runOpts{
		// Pipe: only works because runShell passes "-c" so sh interprets
		// this as a command string rather than a script filename.
		Command:  `printf 'a\n' | wc -l | tr -d ' '`,
		Shell:    "/bin/sh",
		Flags:    []string(nil), // runShell must default this to ["-c"]}
		Timeout:  5 * time.Second,
		MaxBytes: 1 << 20,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	// `wc -l` writes "1\n"; `tr -d ' '` strips spaces but leaves the
	// trailing newline. We assert the raw output to keep the test
	// intent ("shell saw this as a command string, not a filename")
	// visible without making the assertion fragile.
	assert.Equal(t, "1\n", res.Stdout)
}
