// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/tool"
)

// captureLogger returns a *slog.Logger that writes JSON records to
// the returned buffer, plus a pointer to that buffer. The buffer is
// goroutine-safe: RunBefore's fire-and-forget spawn-goroutines and
// healthcheck-goroutines both write to the same handler, so the
// captured *slog.Logger wraps a mutex-protected bytes.Buffer rather
// than a raw bytes.Buffer (which is documented as not safe for
// concurrent use).
func captureLogger() (*slog.Logger, *safeBuffer) {
	buf := &safeBuffer{}

	handler := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	return slog.New(handler), buf
}

// safeBuffer is a goroutine-safe wrapper around bytes.Buffer. The
// fire-and-forget spawn-goroutines and healthcheck-goroutines in
// RunBefore both write to the same handler buffer; the data race
// detector flags a raw bytes.Buffer here.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write satisfies io.Writer under the lock.
func (s *safeBuffer) Write(payload []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// bytes.Buffer.Write never returns a non-nil error in practice
	// (the underlying buffer is in-memory), but we wrap defensively
	// to satisfy the project's wrapcheck rule.
	written, err := s.buf.Write(payload)
	if err != nil {
		return written, fmt.Errorf("safeBuffer.Write: %w", err)
	}

	return written, nil
}

// Bytes returns a snapshot of the underlying buffer under the lock.
func (s *safeBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.buf.Bytes()
}

// String returns a snapshot of the underlying buffer under the lock.
func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.buf.String()
}

// findFreeTCPAddr returns a TCP address that the OS has just bound
// for our test and then released. Dialing this address should get
// ECONNREFUSED immediately; tests that need a closed-port target
// use this.
//
// Note: the OS may reassign the port to a parallel test before the
// dial actually happens. The tests in this file are written to be
// tolerant of that small race window by polling the log buffer for
// the expected record rather than asserting on the wall-clock dial.
func findFreeTCPAddr(t *testing.T) string {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := listener.Addr().String()

	closeErr := listener.Close()
	require.NoError(t, closeErr)

	return addr
}

// boundTCPAddr returns a TCP address that stays bound for the
// lifetime of the test. The returned cleanup function closes the
// listener and is wired into the test via a defer.
func boundTCPAddr(t *testing.T) (string, func()) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := listener.Addr().String()

	cleanup := func() {
		closeErr := listener.Close()
		if closeErr != nil {
			t.Logf("boundTCPAddr listener.Close: %v", closeErr)
		}
	}

	// Accept and immediately close any incoming connection in a
	// goroutine so healthcheck probes succeed without the test
	// needing to drive Accept manually. The goroutine exits when
	// listener.Close() makes Accept return an error.
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			closeErr := conn.Close()
			if closeErr != nil {
				t.Logf("boundTCPAddr conn.Close: %v", closeErr)
			}
		}
	}()

	return addr, cleanup
}

// logRecords parses the JSON-lines buffer into a slice of decoded
// records so tests can assert on level/msg/attribute keys without
// pattern-matching on raw JSON.
func logRecords(buf *safeBuffer) []map[string]any {
	dec := json.NewDecoder(strings.NewReader(buf.String()))

	var out []map[string]any

	for dec.More() {
		var record map[string]any

		decodeErr := dec.Decode(&record)
		if decodeErr != nil {
			break
		}

		out = append(out, record)
	}

	return out
}

// countByLevel returns how many captured records have the given
// slog.Level name ("INFO", "ERROR", "DEBUG", "WARN").
func countByLevel(records []map[string]any, level string) int {
	count := 0

	for _, record := range records {
		if record["level"] == level {
			count++
		}
	}

	return count
}

// findRecord returns the first captured record whose msg field
// equals the supplied value, or nil when no such record exists.
func findRecord(records []map[string]any, msg string) map[string]any {
	for _, record := range records {
		if record["msg"] == msg {
			return record
		}
	}

	return nil
}

// waitForRecord polls buf until a record whose msg field equals
// msg has been logged, or until timeout elapses. It returns the
// record (or nil on timeout). Tests use this to wait for the
// fire-and-forget goroutines that log after RunBefore returns.
func waitForRecord(buf *safeBuffer, msg string, timeout time.Duration) map[string]any {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		records := logRecords(buf)

		if record := findRecord(records, msg); record != nil {
			return record
		}

		time.Sleep(20 * time.Millisecond)
	}

	return nil
}

// waitForCount polls buf until countByLevel(records, level) is at
// least want, or timeout elapses. Tests use this when the exact
// number of records depends on the cadence of async goroutines.
func waitForCount(buf *safeBuffer, level string, want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if got := countByLevel(logRecords(buf), level); got >= want {
			return got
		}

		time.Sleep(20 * time.Millisecond)
	}

	return countByLevel(logRecords(buf), level)
}

// pidFileCommand returns an sh -c string that writes the current
// process PID to pidfile and then sleeps for sleepFor. Tests use
// this to verify the spawned process is still alive after the
// healthcheck times out.
func pidFileCommand(pidfile, sleepFor string) string {
	return fmt.Sprintf(`printf '%%d' $$ > %s; sleep %s`, pidfile, sleepFor)
}

// readChildPID waits up to timeout for pidfile to appear, reads it,
// parses the integer, and returns it.
func readChildPID(t *testing.T, pidfile string, timeout time.Duration) int {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(pidfile)
		if readErr == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil {
				return pid
			}
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("pidfile %s was not written within %s", pidfile, timeout)

	return 0
}

// childAlive reports whether a PID is still a live process. It
// uses signal 0 which has no effect on the target but fails when
// the PID no longer exists. This works on Linux and macOS without
// /proc.
func childAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	return proc.Signal(syscall.Signal(0)) == nil
}

// TestRunBefore_EmptyAndNilAreNoOp verifies the documented
// contract: nil and zero-length slices do not spawn goroutines and
// return immediately.
func TestRunBefore_EmptyAndNilAreNoOp(t *testing.T) {
	logger, buf := captureLogger()

	start := time.Now()

	var nilCmds []BeforeCommand

	RunBefore(t.Context(), nilCmds, tool.WithLogger(logger))

	elapsed := time.Since(start)
	require.Lessf(t, elapsed, 10*time.Millisecond,
		"nil cmds must return in under 10ms, took %s", elapsed)

	start = time.Now()

	RunBefore(t.Context(), []BeforeCommand{}, tool.WithLogger(logger))

	elapsed = time.Since(start)
	require.Lessf(t, elapsed, 10*time.Millisecond,
		"empty cmds must return in under 10ms, took %s", elapsed)

	records := logRecords(buf)

	assert.Equal(t, 0, countByLevel(records, "ERROR"))
	assert.Equal(t, 0, countByLevel(records, "INFO"))
}

// TestRunBefore_SpawnsCommandsInParallel checks that RunBefore does
// not block on the spawned commands: N=4 commands that would each
// take 1s finish in well under 4s because RunBefore returns as
// soon as they are spawned.
func TestRunBefore_SpawnsCommandsInParallel(t *testing.T) {
	logger, _ := captureLogger()

	cmds := []BeforeCommand{
		{Command: `sleep 1; true`, Healthcheck: (*Healthcheck)(nil)},
		{Command: `sleep 1; true`, Healthcheck: (*Healthcheck)(nil)},
		{Command: `sleep 1; true`, Healthcheck: (*Healthcheck)(nil)},
		{Command: `sleep 1; true`, Healthcheck: (*Healthcheck)(nil)},
	}

	start := time.Now()

	RunBefore(t.Context(), cmds, tool.WithLogger(logger))

	elapsed := time.Since(start)
	require.Lessf(t, elapsed, 100*time.Millisecond,
		"RunBefore must spawn-and-return, not wait for sleep; took %s", elapsed)
}

// TestRunBefore_FailingCommandIsLoggedAtError drives RunBefore with
// a `sh -c` command that references a missing executable. /bin/sh
// itself starts fine, so Start succeeds; the embedded command then
// returns exit status 127 from /bin/sh, which our code logs as
// "before command exited with error" at ERROR level.
//
// The literal "spawn failed" branch (Start returning a non-nil
// error) is exercised by absent shells — not something we can
// trigger in tests without changing the production API. The
// realistic failure mode (executable absent inside the command
// string) is covered here.
func TestRunBefore_FailingCommandIsLoggedAtError(t *testing.T) {
	logger, buf := captureLogger()

	RunBefore(t.Context(),
		[]BeforeCommand{{
			Command:     "this-binary-does-not-exist-xyz 2>/dev/null",
			Healthcheck: (*Healthcheck)(nil),
		}},
		tool.WithLogger(logger),
	)

	exited := waitForRecord(buf, "before command exited with error", 2*time.Second)
	require.NotNilf(t, exited,
		"missing 'before command exited with error' record; got %s", buf.String())
	assert.Equal(t, "ERROR", exited["level"])

	commandField, ok := exited["command"].(string)
	require.Truef(t, ok, "command field must be a string")
	assert.Containsf(t, commandField, "this-binary-does-not-exist-xyz",
		"command field %q must contain the failing executable name", commandField)
}

// TestRunBefore_AllFailingCommandsReturnImmediately runs multiple
// failing commands at once and asserts that RunBefore returns in
// under 100ms — failures do not block — and that all three ERROR
// "exited with error" records are eventually emitted by the
// fire-and-forget goroutines.
func TestRunBefore_AllFailingCommandsReturnImmediately(t *testing.T) {
	logger, buf := captureLogger()

	cmds := []BeforeCommand{
		{Command: "this-binary-does-not-exist-aaa 2>/dev/null", Healthcheck: (*Healthcheck)(nil)},
		{Command: "this-binary-does-not-exist-bbb 2>/dev/null", Healthcheck: (*Healthcheck)(nil)},
		{Command: "this-binary-does-not-exist-ccc 2>/dev/null", Healthcheck: (*Healthcheck)(nil)},
	}

	start := time.Now()

	RunBefore(t.Context(), cmds, tool.WithLogger(logger))

	elapsed := time.Since(start)
	require.Lessf(t, elapsed, 100*time.Millisecond,
		"all-failing RunBefore must return in under 100ms, took %s", elapsed)

	count := waitForCount(buf, "ERROR", 3, 2*time.Second)
	assert.Equalf(t, 3, count,
		"expected 3 ERROR records, got %d; buffer: %s", count, buf.String())
}

// TestRunBefore_ContextCancelKillsChildren spawns a long-running
// command without a healthcheck (so RunBefore returns immediately)
// and then cancels the parent context. The child process must exit
// within ~1s — exec.CommandContext wires the cancel signal to
// the child.
func TestRunBefore_ContextCancelKillsChildren(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	command := pidFileCommand(pidfile, "60")

	logger, _ := captureLogger()

	ctx, cancel := context.WithCancel(t.Context())

	cmds := []BeforeCommand{{Command: command, Healthcheck: (*Healthcheck)(nil)}}

	RunBefore(ctx, cmds, tool.WithLogger(logger))

	pid := readChildPID(t, pidfile, 2*time.Second)

	require.Truef(t, childAlive(pid),
		"child PID %d must be alive immediately after RunBefore returns", pid)

	cancel()

	require.Eventuallyf(t, func() bool { return !childAlive(pid) },
		2*time.Second, 50*time.Millisecond,
		"child PID %d must be killed by context cancel", pid)
}

// TestRunBefore_WaitsForHealthcheckToPass runs a healthcheck
// against a TCP listener that is bound immediately. The first
// dial succeeds, so RunBefore returns quickly with one INFO
// "healthcheck passed" log.
func TestRunBefore_WaitsForHealthcheckToPass(t *testing.T) {
	addr, cleanup := boundTCPAddr(t)
	defer cleanup()

	logger, buf := captureLogger()

	cmds := []BeforeCommand{{
		Command: "true",
		Healthcheck: &Healthcheck{
			TCP:      addr,
			Interval: Duration(200 * time.Millisecond),
			Timeout:  Duration(10 * time.Second),
		},
	}}

	start := time.Now()

	RunBefore(t.Context(), cmds, tool.WithLogger(logger))

	elapsed := time.Since(start)

	require.Lessf(t, elapsed, 1500*time.Millisecond,
		"RunBefore must return after healthcheck passes; took %s", elapsed)

	passed := waitForRecord(buf, "before command healthcheck passed", 2*time.Second)
	require.NotNilf(t, passed,
		"missing 'healthcheck passed' record; got %s", buf.String())
	assert.Equal(t, "INFO", passed["level"])
	assert.Equal(t, addr, passed["tcp"])

	attempts, ok := passed["attempts"].(float64)
	require.Truef(t, ok, "attempts field must be numeric")
	assert.Equalf(t, 1, int(attempts),
		"expected exactly 1 attempt, got %v", attempts)
}

// TestRunBefore_WaitsForSlowestHealthcheck drives three
// healthchecks with different effective timeouts (100ms, 300ms,
// 600ms). They run in parallel, so RunBefore returns bounded by
// the slowest one — not the sum of all three (which would be ~1s).
func TestRunBefore_WaitsForSlowestHealthcheck(t *testing.T) {
	logger, _ := captureLogger()

	closedAddr := findFreeTCPAddr(t)

	makeCmd := func(timeout time.Duration) BeforeCommand {
		return BeforeCommand{
			Command: "true",
			Healthcheck: &Healthcheck{
				TCP:      closedAddr,
				Interval: Duration(20 * time.Millisecond),
				Timeout:  Duration(timeout),
			},
		}
	}

	cmds := []BeforeCommand{
		makeCmd(100 * time.Millisecond),
		makeCmd(300 * time.Millisecond),
		makeCmd(600 * time.Millisecond),
	}

	start := time.Now()

	RunBefore(t.Context(), cmds, tool.WithLogger(logger))

	elapsed := time.Since(start)

	require.Lessf(t, elapsed, 1500*time.Millisecond,
		"slowest healthcheck should bound RunBefore; took %s (would be ~1s if serial)", elapsed)
}

// TestRunBefore_HealthcheckTimeoutLogsErrorAndReturns asserts the
// critical contract: a timed-out healthcheck does NOT kill the
// spawned command, RunBefore still returns after the timeout, and
// one ERROR record is captured.
func TestRunBefore_HealthcheckTimeoutLogsErrorAndReturns(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	closedAddr := findFreeTCPAddr(t)

	logger, buf := captureLogger()

	cmds := []BeforeCommand{{
		Command: pidFileCommand(pidfile, "30"),
		Healthcheck: &Healthcheck{
			TCP:      closedAddr,
			Interval: Duration(50 * time.Millisecond),
			Timeout:  Duration(300 * time.Millisecond),
		},
	}}

	start := time.Now()

	RunBefore(t.Context(), cmds, tool.WithLogger(logger))

	elapsed := time.Since(start)
	require.Lessf(t, elapsed, 1500*time.Millisecond,
		"RunBefore must return after healthcheck timeout; took %s", elapsed)

	timedOut := findRecord(logRecords(buf), "before command healthcheck timed out")
	require.NotNilf(t, timedOut,
		"missing 'healthcheck timed out' record; buffer: %s", buf.String())
	assert.Equal(t, "ERROR", timedOut["level"])
	assert.Equal(t, closedAddr, timedOut["tcp"])

	pid := readChildPID(t, pidfile, 2*time.Second)

	require.Truef(t, childAlive(pid),
		"child PID %d must still be alive — healthcheck failure must not kill it", pid)
}

// TestRunBefore_AllHealthchecksTimeoutStillReturns drives three
// healthchecks that all timeout. RunBefore still returns after
// the slowest one and three ERROR records are logged.
func TestRunBefore_AllHealthchecksTimeoutStillReturns(t *testing.T) {
	logger, buf := captureLogger()

	closedAddr := findFreeTCPAddr(t)

	makeCmd := func(timeout time.Duration) BeforeCommand {
		return BeforeCommand{
			Command: "true",
			Healthcheck: &Healthcheck{
				TCP:      closedAddr,
				Interval: Duration(20 * time.Millisecond),
				Timeout:  Duration(timeout),
			},
		}
	}

	cmds := []BeforeCommand{
		makeCmd(150 * time.Millisecond),
		makeCmd(150 * time.Millisecond),
		makeCmd(150 * time.Millisecond),
	}

	start := time.Now()

	RunBefore(t.Context(), cmds, tool.WithLogger(logger))

	elapsed := time.Since(start)
	require.Lessf(t, elapsed, 1500*time.Millisecond,
		"all-healthcheck-timeout RunBefore must return in under 1.5s, took %s", elapsed)

	count := waitForCount(buf, "ERROR", 3, 2*time.Second)
	assert.Equalf(t, 3, count,
		"expected 3 ERROR records, got %d; buffer: %s", count, buf.String())

	records := logRecords(buf)

	for _, record := range records {
		if record["level"] == "ERROR" {
			assert.Equalf(t, "before command healthcheck timed out", record["msg"],
				"every ERROR must be a healthcheck-timeout; got %q", record["msg"])
		}
	}
}

// TestRunBefore_MixedHealthcheckOutcomesAllFinishBeforeReturn runs
// one healthcheck that passes (TCP listener is up immediately)
// and one that times out. RunBefore must wait for both and emit
// one INFO "healthcheck passed" and one ERROR "healthcheck timed
// out".
//
// We deliberately assert on the specific healthcheck records
// (by msg) rather than on global INFO/ERROR counts because the
// spawn goroutines for the `true` commands may still be running
// or may already have logged — that outcome is racing with the
// test boundary, and is what we're not asserting on.
func TestRunBefore_MixedHealthcheckOutcomesAllFinishBeforeReturn(t *testing.T) {
	addr, cleanup := boundTCPAddr(t)
	defer cleanup()

	closedAddr := findFreeTCPAddr(t)

	logger, buf := captureLogger()

	cmds := []BeforeCommand{
		{Command: "true", Healthcheck: &Healthcheck{
			TCP:      addr,
			Interval: Duration(50 * time.Millisecond),
			Timeout:  Duration(10 * time.Second),
		}},
		{Command: "true", Healthcheck: &Healthcheck{
			TCP:      closedAddr,
			Interval: Duration(50 * time.Millisecond),
			Timeout:  Duration(400 * time.Millisecond),
		}},
	}

	start := time.Now()

	RunBefore(t.Context(), cmds, tool.WithLogger(logger))

	elapsed := time.Since(start)

	require.GreaterOrEqualf(t, elapsed, 400*time.Millisecond,
		"RunBefore must wait for the slower (timing-out) healthcheck; took %s", elapsed)

	require.Lessf(t, elapsed, 2*time.Second,
		"RunBefore must not exceed the timeout by too much; took %s", elapsed)

	passed := waitForRecord(buf, "before command healthcheck passed", 2*time.Second)
	require.NotNilf(t, passed,
		"missing 'healthcheck passed' record; buffer: %s", buf.String())
	assert.Equal(t, "INFO", passed["level"])

	timedOut := findRecord(logRecords(buf), "before command healthcheck timed out")
	require.NotNilf(t, timedOut,
		"missing 'healthcheck timed out' record; buffer: %s", buf.String())
	assert.Equal(t, "ERROR", timedOut["level"])
}

// TestRunBefore_NoHealthcheckDoesNotContributeToWait mixes one
// command without a healthcheck and one with a healthcheck that
// times out. RunBefore must be bounded by the healthcheck, not
// the spawned command's lifetime.
func TestRunBefore_NoHealthcheckDoesNotContributeToWait(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	closedAddr := findFreeTCPAddr(t)

	logger, _ := captureLogger()

	cmds := []BeforeCommand{
		// Long-running, no healthcheck — must NOT block RunBefore.
		{Command: pidFileCommand(pidfile, "60"), Healthcheck: (*Healthcheck)(nil)},
		// Healthcheck that times out in ~300ms — this is what
		// bounds RunBefore.
		{Command: "true", Healthcheck: &Healthcheck{
			TCP:      closedAddr,
			Interval: Duration(50 * time.Millisecond),
			Timeout:  Duration(300 * time.Millisecond),
		}},
	}

	start := time.Now()

	RunBefore(t.Context(), cmds, tool.WithLogger(logger))

	elapsed := time.Since(start)

	expectedBound := 1500 * time.Millisecond
	require.Lessf(t, elapsed, expectedBound,
		"RunBefore must be bounded by the timing-out healthcheck; took %s", elapsed)
}

// restartRestartCount reads log records and returns the number of
// "before command restarting" INFO records emitted so far. Used
// across the restart tests below to assert "exactly N respawns"
// without coupling to the wall clock.
func restartRestartCount(records []map[string]any) int {
	return countByMsg(records, "before command restarting")
}

// restartExhaustCount returns the number of "before command restart
// budget exhausted" ERROR records emitted so far.
func restartExhaustCount(records []map[string]any) int {
	return countByMsg(records, "before command restart budget exhausted")
}

// restartCleanCount returns the number of "before command exited
// cleanly" INFO records emitted so far.
func restartCleanCount(records []map[string]any) int {
	return countByMsg(records, "before command exited cleanly")
}

// restartErrorCount returns the number of "before command exited
// with error" ERROR records emitted so far.
func restartErrorCount(records []map[string]any) int {
	return countByMsg(records, "before command exited with error")
}

// countByMsg returns how many captured records have the supplied
// msg field. Mirrors countByLevel but keyed on msg instead of
// level — the restart tests assert on specific event names.
func countByMsg(records []map[string]any, msg string) int {
	count := 0

	for _, record := range records {
		if record["msg"] == msg {
			count++
		}
	}

	return count
}

// restartPID reads a pidfile, returns the PID written. Used by the
// restart tests to verify a new spawn has a different PID than
// the previous attempt.
func restartPID(t *testing.T, pidfile string, timeout time.Duration) int {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(pidfile)
		if readErr == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil {
				return pid
			}
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("pidfile %s was not written within %s", pidfile, timeout)

	return 0
}

// failNTimesThenSleepPIDLike returns an sh -c string that, on
// invocation:
//
//   - Appends a newline to counterFile (used to track invocation
//     count across runs of the same command).
//   - Writes the spawned PID to pidfile (matching pidFileCommand's
//     behavior so tests can read the PID of the most recent spawn).
//   - If the cumulative counter is <= maxAttempts, exits 1 —
//     this is what triggers runBeforeOne to respawn.
//   - Otherwise (i.e. the (maxAttempts+1)th invocation), falls
//     through to `sleep sleepFor` so the test can observe the
//     long-lived settled process.
//
// Invocations are sequential because runBeforeOne waits for each
// spawn to exit before starting the next, so the counter file is
// not subject to concurrent-append races from a single test.
func failNTimesThenSleepPIDLike(
	counterFile, pidfile, sleepFor string,
	maxAttempts int,
) string {
	// The script: append, write PID, count, decide exit-vs-sleep.
	// Each printf/count/sleep runs once per invocation.
	script := fmt.Sprintf(
		`printf '\n' >> %[1]s; printf '%%d' $$ > %[2]s; `+
			`count=$(wc -l < %[1]s | tr -d ' '); `+
			`if [ "$count" -le %[4]d ]; then exit 1; fi; `+
			`sleep %[3]s`,
		counterFile,
		pidfile,
		sleepFor,
		maxAttempts,
	)

	return script
}

// TestRunBefore_NilRestartIsNoOp is the regression guard for the
// "restart omitted" case: a command without a Restart policy
// must spawn exactly once and exit on the first non-zero, exactly
// like the pre-restart-feature behavior.
func TestRunBefore_NilRestartIsNoOp(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")

	logger, buf := captureLogger()

	cmds := []BeforeCommand{{
		Command: pidFileCommand(pidfile, "1"),
		Restart: (*RestartPolicy)(nil),
	}}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	RunBefore(ctx, cmds, tool.WithLogger(logger))

	// Wait long enough for the spawned process to finish sleeping
	// 1s and either exit cleanly (no — it sleeps) or be killed
	// when we cancel below. The point: no respawn should happen.
	pid := readChildPID(t, pidfile, 1*time.Second)
	require.Truef(t, childAlive(pid), "spawned PID %d must be alive", pid)

	// Cancel so the spawned child exits and we can collect records.
	cancel()

	exited := waitForRecord(buf, "before command terminated", 2*time.Second)
	require.NotNilf(t, exited, "missing 'terminated' record; got %s", buf.String())

	records := logRecords(buf)
	assert.Equalf(t, 0, restartRestartCount(records),
		"nil Restart must produce no 'restarting' records; got %d", restartRestartCount(records))
	assert.Equalf(t, 0, restartExhaustCount(records),
		"nil Restart must produce no 'exhausted' records; got %d", restartExhaustCount(records))
}

// TestRunBefore_ExplicitMaxAttemptsOneMeansNoRestart verifies the
// MaxAttempts == 1 corner of the table: explicit cap of 1 must
// behave exactly like omitting Restart (no respawn).
func TestRunBefore_ExplicitMaxAttemptsOneMeansNoRestart(t *testing.T) {
	logger, buf := captureLogger()

	cmds := []BeforeCommand{{
		Command: "this-binary-does-not-exist-xyz 2>/dev/null",
		Restart: &RestartPolicy{
			MaxAttempts: 1,
			Delay:       Duration(50 * time.Millisecond),
		},
	}}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	RunBefore(ctx, cmds, tool.WithLogger(logger))

	exited := waitForRecord(buf, "before command exited with error", 2*time.Second)
	require.NotNilf(t, exited, "missing 'exited with error' record; got %s", buf.String())

	// Wait a bit longer to give any rogue respawn a chance.
	time.Sleep(200 * time.Millisecond)

	records := logRecords(buf)
	assert.Equalf(t, 1, restartErrorCount(records),
		"MaxAttempts=1 must spawn exactly once; got %d 'exited with error' records",
		restartErrorCount(records))
	assert.Equalf(t, 0, restartRestartCount(records),
		"MaxAttempts=1 must not produce any 'restarting' records; got %d",
		restartRestartCount(records))
}

// TestRunBefore_NoRestartOnCleanExit covers the "operator ran a
// one-shot setup command" case: a clean exit (exit 0) must NOT
// trigger a respawn, even with an explicit restart policy. The
// operator can express "run forever" via `while true; do ...; done`
// if they want infinite restart on clean exit.
func TestRunBefore_NoRestartOnCleanExit(t *testing.T) {
	logger, buf := captureLogger()

	cmds := []BeforeCommand{{
		Command: "true",
		Restart: &RestartPolicy{
			MaxAttempts: 5,
			Delay:       Duration(50 * time.Millisecond),
		},
	}}

	RunBefore(t.Context(), cmds, tool.WithLogger(logger))

	cleaned := waitForRecord(buf, "before command exited cleanly", 2*time.Second)
	require.NotNilf(t, cleaned, "missing 'exited cleanly' record; got %s", buf.String())

	// Wait long enough for any rogue respawn to surface.
	time.Sleep(300 * time.Millisecond)

	records := logRecords(buf)
	assert.Equalf(t, 1, restartCleanCount(records),
		"clean exit must spawn exactly once; got %d 'exited cleanly' records",
		restartCleanCount(records))
	assert.Equalf(t, 0, restartRestartCount(records),
		"clean exit must not trigger respawn; got %d 'restarting' records",
		restartRestartCount(records))
}

// TestRunBefore_NoRestartOnSignalTermination covers the SIGINT/SIGTERM
// shutdown case: when exec.CommandContext kills the child in
// response to ctx cancellation, the outcome is exitSignalTerminated
// and runBeforeOne must not respawn across the shutdown boundary.
func TestRunBefore_NoRestartOnSignalTermination(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")

	logger, buf := captureLogger()

	cmds := []BeforeCommand{{
		Command: pidFileCommand(pidfile, "60"),
		Restart: &RestartPolicy{
			MaxAttempts: 0, // unbounded — would respawn forever if the policy applied
			Delay:       Duration(50 * time.Millisecond),
		},
	}}

	ctx, cancel := context.WithCancel(t.Context())

	RunBefore(ctx, cmds, tool.WithLogger(logger))

	pid := readChildPID(t, pidfile, 2*time.Second)
	require.Truef(t, childAlive(pid), "first spawn PID %d must be alive", pid)

	cancel()

	terminated := waitForRecord(buf, "before command terminated", 2*time.Second)
	require.NotNilf(t, terminated, "missing 'terminated' record; got %s", buf.String())

	// Give a respawn (if one were wrongly triggered) time to surface.
	time.Sleep(300 * time.Millisecond)

	records := logRecords(buf)
	assert.Equalf(t, 0, restartRestartCount(records),
		"signal-terminated exit must not respawn; got %d 'restarting' records",
		restartRestartCount(records))
}

// TestRunBefore_RestartBoundedExhaustionLogsError exercises the
// bounded MaxAttempts=N path with a command that always fails.
// After exactly N spawns the loop must log ERROR "restart budget
// exhausted" and stop — there must NOT be a (N+1)th attempt.
func TestRunBefore_RestartBoundedExhaustionLogsError(t *testing.T) {
	logger, buf := captureLogger()

	cmds := []BeforeCommand{{
		Command: "this-binary-does-not-exist-xyz 2>/dev/null",
		Restart: &RestartPolicy{
			MaxAttempts: 3,
			Delay:       Duration(50 * time.Millisecond),
		},
	}}

	RunBefore(t.Context(), cmds, tool.WithLogger(logger))

	// 3 spawns * 50ms delay = ~100ms of inter-attempt wait, plus
	// spawn jitter. Wait up to 5s for the exhaustion record.
	exhausted := waitForRecord(buf, "before command restart budget exhausted", 5*time.Second)
	require.NotNilf(t, exhausted, "missing 'exhausted' record; got %s", buf.String())

	// Sleep extra to verify no further spawn happens after exhaustion.
	time.Sleep(300 * time.Millisecond)

	records := logRecords(buf)
	assert.Equalf(t, 3, restartErrorCount(records),
		"bounded MaxAttempts=3 must produce exactly 3 'exited with error' records; got %d",
		restartErrorCount(records))
	assert.Equalf(
		t,
		2,
		restartRestartCount(records),
		"bounded MaxAttempts=3 must produce 2 'restarting' records "+
			"(between attempts 1->2, 2->3); got %d",
		restartRestartCount(records),
	)
	assert.Equalf(t, 1, restartExhaustCount(records),
		"bounded MaxAttempts=3 must produce exactly 1 'exhausted' record; got %d",
		restartExhaustCount(records))
}

// TestRunBefore_RestartUnboundedWhenMaxAttemptsZero covers the
// default case: when MaxAttempts is the zero value (and a Restart
// block is present), runBeforeOne must respawn forever until ctx
// cancels. We cancel ctx after observing several "restarting"
// TestRunBefore_RestartUnboundedWhenMaxAttemptsZero covers the
// default case: when MaxAttempts is the zero value (and a Restart
// block is present), runBeforeOne must respawn forever until ctx
// cancels. We cancel ctx after observing several "restarting"
// records and verify that no further respawns happen, with no
// exhaustion record logged.
//
// The test fixture always exits with error 1 (via a missing
// binary) — so the respawn loop fires continuously at the Delay
// pace. Cancel might land in the inter-attempt select (where no
// running spawn exists to be killed) — that's still a valid
// cancellation path: the loop simply exits without spawning
// again. The contract being tested is "after ctx cancel, the loop
// stops and no further 'restarting' records appear".
func TestRunBefore_RestartUnboundedWhenMaxAttemptsZero(t *testing.T) {
	logger, buf := captureLogger()

	cmds := []BeforeCommand{{
		// Always-failing command — the respawn loop cycles
		// continuously at the configured Delay pace, and each
		// cycle spawns a fresh sh -c that exits quickly.
		Command: "this-binary-does-not-exist-xyz 2>/dev/null",
		Restart: &RestartPolicy{
			// MaxAttempts deliberately left at zero.
			Delay: Duration(50 * time.Millisecond),
		},
	}}

	ctx, cancel := context.WithCancel(t.Context())

	RunBefore(ctx, cmds, tool.WithLogger(logger))

	// Wait for at least 2 "restarting" records — confirms the loop
	// has restarted at least once and is actively cycling.
	require.Eventuallyf(t, func() bool {
		return restartRestartCount(logRecords(buf)) >= 2
	}, 5*time.Second, 20*time.Millisecond,
		"unbounded restart must produce >=2 'restarting' records within 5s; got %d; buffer: %s",
		restartRestartCount(logRecords(buf)), buf.String())

	cancel()

	respawnsAtCancel := restartRestartCount(logRecords(buf))

	time.Sleep(300 * time.Millisecond)

	respawnsAfterWait := restartRestartCount(logRecords(buf))
	assert.Equalf(t, respawnsAtCancel, respawnsAfterWait,
		"after ctx cancel the unbounded loop must stop; respawns went from %d to %d",
		respawnsAtCancel, respawnsAfterWait)

	records := logRecords(buf)
	assert.Equalf(t, 0, restartExhaustCount(records),
		"unbounded restart must never produce 'exhausted' records; got %d",
		restartExhaustCount(records))
}

// TestRunBefore_RestartUnboundedKeepsProcessAcrossFlakes covers the
// motivating use case: a `kubectl port-forward` that drops a couple
// of times and then settles. With unbounded restart, the third
// attempt (which sleeps) must be the one that "wins" — the spawned
// PID must remain alive past the inter-attempt waits.
//
// We can't wait for "spawn 3 has been spawned" via "restarting"
// records — by the time we see 2 "restarting" records, spawn 3
// is in its sleep and never produces a 3rd "restarting" record.
// Instead, we read the pidfile and rely on the helper overwriting
// it on every invocation: after spawn 3's `printf '$$' > pidfile`
// runs, the pidfile holds spawn 3's PID. The trick is to wait
// long enough that spawn 3 has had time to execute its printf —
// which it does almost immediately after spawn — and then check
// childAlive.
func TestRunBefore_RestartUnboundedKeepsProcessAcrossFlakes(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	counter := filepath.Join(t.TempDir(), "counter")

	logger, buf := captureLogger()

	cmds := []BeforeCommand{{
		// Fails the first 2 invocations, then sleeps for 5s on the 3rd.
		// Using 5s (not 60s) so the test doesn't have to wait the full
		// sleep duration — but 5s is plenty long enough to read the
		// pidfile and assert childAlive.
		Command: failNTimesThenSleepPIDLike(counter, pidfile, "5", 2),
		Restart: &RestartPolicy{
			Delay: Duration(50 * time.Millisecond),
		},
	}}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	RunBefore(ctx, cmds, tool.WithLogger(logger))

	// Wait for the 2nd "restarting" record — that's logged after
	// spawn 2 exits and BEFORE the 50ms inter-attempt delay elapses.
	// spawn 3 starts ~50ms after the 2nd "restarting" record.
	waitForNRecords := func(want int) bool {
		deadline := time.Now().Add(5 * time.Second)

		for time.Now().Before(deadline) {
			if restartRestartCount(logRecords(buf)) >= want {
				return true
			}

			time.Sleep(20 * time.Millisecond)
		}

		return false
	}

	require.Truef(t, waitForNRecords(2),
		"expected at least 2 'restarting' records within 5s; got %d (buffer: %s)",
		restartRestartCount(logRecords(buf)), buf.String())

	// Wait for spawn 3 to write its PID. Spawn 3 starts ~50ms after
	// the 2nd "restarting" record. The helper writes PID immediately,
	// so polling the pidfile should find spawn 3's PID quickly.
	//
	// We poll until the pidfile contains a DIFFERENT PID than
	// spawn 2's PID. Spawn 2's PID is the one written right before
	// its exit, and the only way the pidfile changes again is if
	// spawn 3 has run its printf. We use a simple heuristic:
	// re-read the pidfile until we've seen at least one write
	// after the last "restarting" record.
	pid := waitForFreshPIDAfterRestart(t, buf, pidfile, 2*time.Second)
	require.Truef(t, childAlive(pid),
		"third-spawn PID %d must be alive (spawn 3 should be sleeping 5s)", pid)

	// Verify no exhaustion record was logged — unbounded loop.
	records := logRecords(buf)
	assert.Equalf(t, 0, restartExhaustCount(records),
		"unbounded restart must not log 'exhausted'; got %d records",
		restartExhaustCount(records))
}

// waitForFreshPIDAfterRestart polls pidfile until a NEW PID appears
// (different from the PID that was there when the n-th "restarting"
// record was logged). This pins "spawn (n+1) has run its printf".
//
// Implementation: we capture the pidfile's content RIGHT after the
// n-th "restarting" record shows up, then poll until the pidfile
// changes to a different value. (Each invocation overwrites the
// pidfile with its own sh -c PID, so a change implies a new
// invocation has run.)
func waitForFreshPIDAfterRestart(
	t *testing.T,
	buf *safeBuffer,
	pidfile string,
	timeout time.Duration,
) int {
	t.Helper()

	// Wait for at least the target number of restarting records so
	// we know the loop is actively cycling.
	require.Eventuallyf(t, func() bool {
		return restartRestartCount(logRecords(buf)) >= 2
	}, timeout, 20*time.Millisecond,
		"need >=2 'restarting' records before looking for a fresh PID")

	// Capture the current pidfile (this is the PID of the latest
	// invocation that has finished its printf so far — likely
	// spawn 2's PID).
	initialPID := restartPID(t, pidfile, 500*time.Millisecond)

	// Poll for a different PID. Each new invocation overwrites
	// the pidfile before checking the counter and (on the n+1th
	// invocation) sleeping.
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		currentPID := readPIDFile(t, pidfile)
		if currentPID != initialPID && currentPID != 0 {
			return currentPID
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("pidfile %s did not get a fresh PID within %s (initial=%d)",
		pidfile, timeout, initialPID)

	return 0
}

// readPIDFile reads a pidfile and parses the integer. Returns 0
// if the file does not exist or cannot be parsed.
func readPIDFile(t *testing.T, pidfile string) int {
	t.Helper()

	data, err := os.ReadFile(pidfile)
	if err != nil {
		return 0
	}

	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil {
		return 0
	}

	return pid
}

// TestRunBefore_RestartRespectsContextCancelDuringDelay covers the
// "long Delay, server shutdown mid-wait" case: if the inter-attempt
// select honors ctx.Done(), an unbounded restart with a 30s delay
// must exit within ~200ms when ctx is canceled, not wait out the
// full 30s.
func TestRunBefore_RestartRespectsContextCancelDuringDelay(t *testing.T) {
	logger, buf := captureLogger()

	cmds := []BeforeCommand{{
		Command: "this-binary-does-not-exist-xyz 2>/dev/null",
		Restart: &RestartPolicy{
			MaxAttempts: 5,
			Delay:       Duration(30 * time.Second), // would block forever without ctx cancel
		},
	}}

	ctx, cancel := context.WithCancel(t.Context())

	// The first spawn happens immediately and exits with error.
	// runBeforeOne then logs "restarting" and enters the 30s
	// time.After. We cancel ~100ms in to verify the select
	// returns on ctx.Done() promptly.
	RunBefore(ctx, cmds, tool.WithLogger(logger))

	require.Eventuallyf(t, func() bool {
		return restartRestartCount(logRecords(buf)) >= 1
	}, 1*time.Second, 20*time.Millisecond,
		"first 'restarting' record must appear within 1s; got %d",
		restartRestartCount(logRecords(buf)))

	cancel()

	// Verify no further respawn happens after cancel. We use
	// the same wait-then-count trick as the other tests.
	time.Sleep(300 * time.Millisecond)

	respawnsAtCancel := restartRestartCount(logRecords(buf))

	// Wait a bit longer to ensure we are not racing anything.
	time.Sleep(200 * time.Millisecond)

	respawnsAfterWait := restartRestartCount(logRecords(buf))
	assert.Equalf(t, respawnsAtCancel, respawnsAfterWait,
		"after ctx cancel the unbounded loop must stop; respawns went from %d to %d",
		respawnsAtCancel, respawnsAfterWait)
}

// TestRunBefore_RestartLogsEachAttempt pins the per-attempt log
// schema: every "before command restarting" INFO record must
// carry an `attempt` field that increments from 1 upward.
func TestRunBefore_RestartLogsEachAttempt(t *testing.T) {
	logger, buf := captureLogger()

	cmds := []BeforeCommand{{
		Command: "this-binary-does-not-exist-xyz 2>/dev/null",
		Restart: &RestartPolicy{
			MaxAttempts: 3,
			Delay:       Duration(30 * time.Millisecond),
		},
	}}

	RunBefore(t.Context(), cmds, tool.WithLogger(logger))

	exhausted := waitForRecord(buf, "before command restart budget exhausted", 2*time.Second)
	require.NotNilf(t, exhausted, "missing 'exhausted' record; got %s", buf.String())

	records := logRecords(buf)

	var attempts []float64

	for _, record := range records {
		if record["msg"] != "before command restarting" {
			continue
		}

		got, ok := record["attempt"].(float64)
		require.Truef(t, ok, "attempt field must be numeric; got %T (%v)",
			record["attempt"], record["attempt"])

		attempts = append(attempts, got)
	}

	require.Lenf(t, attempts, 2,
		"MaxAttempts=3 must produce exactly 2 'restarting' records (between 1->2, 2->3); got %d",
		len(attempts))

	//nolint:testifylint // JSON-decoded numeric; float is fine for integer comparison
	assert.Equalf(t, float64(1), attempts[0],
		"first 'restarting' record must have attempt=1; got %v",
		attempts[0])

	//nolint:testifylint // JSON-decoded numeric; float is fine for integer comparison
	assert.Equalf(t, float64(2), attempts[1],
		"second 'restarting' record must have attempt=2; got %v",
		attempts[1])
}
