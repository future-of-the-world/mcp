// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package shell

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"go.amidman.dev/mcp/tool"
)

// Default durations applied to a Healthcheck when its Interval or
// Timeout fields are left at the zero value, and to a RestartPolicy
// when its Delay is left at the zero value. Operators can always
// override them per command in YAML/JSON.
const (
	defaultHealthcheckInterval = time.Second
	defaultHealthcheckTimeout  = 30 * time.Second
	defaultRestartDelay        = 5 * time.Second
)

// truncateCommandLen is the maximum number of bytes of an sh -c
// command string that may appear in a log line. Operators can
// inspect the full string via the spawned process's stdout/stderr.
const truncateCommandLen = 256

// ellipsisLen is the length of the "..." suffix truncate appends
// when truncation happens. Pulled out as a named constant so the
// revive add-constant rule accepts the literal.
const ellipsisLen = 3

// BeforeCommand spawns a single sh -c process at server start.
//
// When Restart is non-nil, a non-signal-error exit triggers an
// automatic respawn of the same command, with the configured
// Delay between attempts. Restart semantics:
//
//   - Restart omitted (Restart == nil): spawn exactly once, current
//     behavior preserved.
//   - Restart.MaxAttempts == 0 (the Go zero value, also the YAML
//     default when the block is present but empty): restart forever
//     until ctx cancels or the command exits cleanly.
//   - Restart.MaxAttempts == 1: equivalent to omitting Restart.
//   - Restart.MaxAttempts == N (N >= 2): up to N total spawn
//     attempts, then ERROR "restart budget exhausted".
type BeforeCommand struct {
	Command     string         `yaml:"command"               json:"command"`
	Healthcheck *Healthcheck   `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
	Restart     *RestartPolicy `yaml:"restart,omitempty"     json:"restart,omitempty"`
}

// RestartPolicy configures auto-respawn behavior for a BeforeCommand
// that exits non-zero during the server lifetime. The zero value
// (MaxAttempts == 0) means "restart forever" — this is the natural
// default for long-lived tunnels like kubectl port-forward. Set
// MaxAttempts to a positive N to cap the total number of spawn
// attempts; on exhaustion, runBeforeOne logs an ERROR and stops.
//
// Delay is the wait between attempts. The zero value uses the
// defaultRestartDelay constant. The inter-attempt wait honors
// ctx.Done(), so SIGINT/SIGTERM cancel the loop promptly.
type RestartPolicy struct {
	MaxAttempts int      `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
	Delay       Duration `yaml:"delay,omitempty"        json:"delay,omitempty"`
}

// Healthcheck is the per-BeforeCommand readiness probe. TCP is
// required and accepts the standard "host:port" form. Interval is
// the per-attempt deadline and the sleep between attempts; Timeout
// is the overall budget. The two defaults (1s and 30s) are applied
// when the corresponding field is the zero Duration.
type Healthcheck struct {
	TCP      string   `yaml:"tcp"                json:"tcp"`
	Interval Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout  Duration `yaml:"timeout,omitempty"  json:"timeout,omitempty"`
}

// RunBefore spawns every command in cmds as a long-lived background
// process under the supplied context, then returns only after
// every declared Healthcheck has finished — passed, timed out, or
// canceled.
//
// The contract for failures is intentionally lenient: a failed
// spawn, a non-zero process exit, a timed-out healthcheck, or a
// canceled context all log at ERROR (or DEBUG for in-progress
// healthcheck retries) without aborting the function. RunBefore
// itself returns nothing; its caller (server-start in cmd/main.go)
// proceeds to source.Apply regardless of outcomes.
//
// A nil or empty cmds slice is a no-op: no goroutines are started
// and RunBefore returns immediately.
//
// Spawned children are tied to the supplied context: when the
// server context cancels (SIGINT/SIGTERM via signal.NotifyContext),
// every running `sh -c` is killed via the underlying
// exec.CommandContext machinery, and every in-flight healthcheck
// dial is canceled.
//
// RunBefore.Wait tracks ONLY healthcheck goroutines. Spawned
// commands are fire-and-forget — their lifetime is the server
// context, not RunBefore's wait, because in the typical case they
// are long-running daemons (kubectl port-forward, etc.) that the
// operator intends to outlive the wait.
func RunBefore(ctx context.Context, cmds []BeforeCommand, opts ...tool.Option) {
	if len(cmds) == 0 {
		return
	}

	logger := tool.NewOptions(opts...).Logger()

	var healthchecks sync.WaitGroup

	for idx, cmd := range cmds {
		// Spawned command: fire-and-forget. NOT registered with the
		// WaitGroup — these processes outlive RunBefore.Wait by
		// design. The modernize waitgroup-go rule wants
		// healthchecks.Go here, but doing so would gate RunBefore's
		// return on the spawned process's lifetime, which is the
		// opposite of the documented contract.
		go func() { //nolint:waitgroup // fire-and-forget spawn; ctx cleanup handles shutdown
			runBeforeOne(ctx, idx, cmd, logger)
		}()

		if cmd.Healthcheck != nil {
			// Only commands with a Healthcheck contribute to the
			// RunBefore wait. healthchecks.Go increments the
			// counter before launching the goroutine and
			// decrements it when the goroutine returns, matching
			// the documented semantics.
			healthchecks.Go(func() {
				runBeforeHealthcheck(ctx, idx, cmd, logger)
			})
		}
	}

	healthchecks.Wait()
}

// exitOutcome classifies how a single runBeforeOneAttempt ended.
// The restart loop in runBeforeOne uses this to decide whether to
// respawn — re-deriving the meaning from waitErr at the loop site
// would tangle policy and observation.
type exitOutcome int

const (
	// exitClean: the process exited with status 0. No respawn.
	exitClean exitOutcome = iota
	// exitErrored: the process exited non-zero, was killed by a
	// signal other than the one exec.CommandContext uses, or the
	// initial spawn failed. Respawns when a restart policy is set
	// and the server ctx has not been canceled.
	exitErrored
	// exitSignalTerminated: the process was terminated by a signal
	// (ExitCode() == -1 on *exec.ExitError) — typically the server
	// ctx cancellation propagated through exec.CommandContext. No
	// respawn: the server is shutting down.
	exitSignalTerminated
)

// restartAttempt carries the per-attempt inputs that runBeforeOneAttempt
// needs. Bundled into one value so the function signature stays
// under the project's revive-argument-limit (max 4).
type restartAttempt struct {
	idx     int
	attempt int
	cmd     BeforeCommand
	logger  *slog.Logger
}

// restartPolicy bundles the resolved values from BeforeCommand.Restart
// so the runBeforeOne loop can carry them as a single value rather
// than three locals (which would tip the function past the
// gocognit limit).
type restartPolicy struct {
	delay       time.Duration
	maxAttempts int
	unbounded   bool
}

// resolveRestartPolicy computes the effective restart policy from
// a BeforeCommand when Restart is non-nil. Two cases:
//
//   - Restart.MaxAttempts == 0: unbounded=true — restart forever
//     until ctx cancels or clean exit.
//   - Restart.MaxAttempts == N (N >= 1): bounded — up to N total
//     spawn attempts.
//
// The Delay default (5s) is applied when the field is zero.
//
// When BeforeCommand.Restart == nil, runBeforeOne bypasses the
// policy loop entirely (runOnce) — see runBeforeOne. That path
// preserves the pre-restart-feature behavior: spawn once, log
// the outcome, no respawn, no "restart budget exhausted" error.
func resolveRestartPolicy(cmd BeforeCommand) restartPolicy {
	policy := restartPolicy{
		delay:       defaultRestartDelay,
		maxAttempts: cmd.Restart.MaxAttempts,
		unbounded:   cmd.Restart.MaxAttempts <= 0,
	}

	if d := cmd.Restart.Delay.Duration(); d > 0 {
		policy.delay = d
	}

	return policy
}

// runBeforeOne drives the lifecycle of a single BeforeCommand.
// When cmd.Restart is nil, it spawns exactly once (preserving
// pre-restart-feature behavior). When cmd.Restart is non-nil, it
// runs runBeforeOneAttempt in a loop bounded by the resolved
// policy — unbounded when MaxAttempts == 0, capped when > 0.
//
// Restart triggers only on exitErrored and only when the server
// ctx has not been canceled. Clean exits and signal-terminated
// exits both return immediately. The inter-attempt wait honors
// ctx.Done() so SIGINT/SIGTERM cancels the loop promptly.
//
// runBeforeOne never returns an error: it logs and exits, leaving
// RunBefore to continue with whatever else is in flight.
func runBeforeOne(ctx context.Context, idx int, cmd BeforeCommand, logger *slog.Logger) {
	if cmd.Restart == nil {
		// runBeforeOneAttempt never returns an error worth acting
		// on — it logs the outcome internally and returns
		// (outcome, wrappedErr) only so the policy loop can attach
		// a "restart budget exhausted" record. Without a policy
		// there's no such record, so the wrapped error is dropped.
		_, _ = runBeforeOneAttempt(ctx, &restartAttempt{ //nolint:errcheck // see comment above
			idx: idx, attempt: 1, cmd: cmd, logger: logger,
		})

		return
	}

	policy := resolveRestartPolicy(cmd)

	for attempt := 1; shouldKeepRestarting(attempt, policy); attempt++ {
		attemptInput := &restartAttempt{
			idx: idx, attempt: attempt, cmd: cmd, logger: logger,
		}
		outcome, waitErr := runBeforeOneAttempt(ctx, attemptInput)

		if !shouldRespawnAfter(ctx, outcome, attempt, policy) {
			logRestartTerminalEvent(&restartTerminalEvent{
				ctx: ctx, logger: logger, idx: idx, cmd: cmd,
				outcome: outcome, attempt: attempt, policy: policy, waitErr: waitErr,
			})

			return
		}

		logger.InfoContext(ctx, "before command restarting",
			"index", idx,
			"command", truncate(cmd.Command),
			"attempt", attempt,
			"delay", policy.delay.String(),
		)

		select {
		case <-time.After(policy.delay):
		case <-ctx.Done():
			return
		}
	}
}

// shouldKeepRestarting reports whether the policy loop should
// run another attempt given the current attempt count.
func shouldKeepRestarting(attempt int, policy restartPolicy) bool {
	if policy.unbounded {
		return true
	}

	return attempt <= policy.maxAttempts
}

// shouldRespawnAfter reports whether the loop should spawn
// another attempt after the one that just finished. False means
// the loop terminates here (clean exit, signal termination, ctx
// cancellation, or bounded exhaustion).
func shouldRespawnAfter(
	ctx context.Context,
	outcome exitOutcome,
	attempt int,
	policy restartPolicy,
) bool {
	// Server is shutting down — never respawn across the
	// shutdown boundary. Check before the "should I restart?"
	// branch because exitSignalTerminated only tells us the
	// child saw a signal; the ctx check is the authoritative
	// "are we shutting down" signal.
	if context.Cause(ctx) != nil {
		return false
	}

	if outcome == exitClean || outcome == exitSignalTerminated {
		return false
	}

	// Bounded policy and this was the last allowed attempt —
	// no respawn, the loop terminates.
	if !policy.unbounded && attempt == policy.maxAttempts {
		return false
	}

	return true
}

// restartTerminalEvent bundles the per-event fields that the
// terminal-event helper needs to log the bounded-exhaustion case.
// Used to keep logRestartTerminalEvent under the project's
// revive-argument-limit (max 4).
type restartTerminalEvent struct {
	ctx     context.Context
	logger  *slog.Logger
	idx     int
	cmd     BeforeCommand
	outcome exitOutcome
	attempt int
	policy  restartPolicy
	waitErr error
}

// logRestartTerminalEvent logs the appropriate terminal event
// when shouldRespawnAfter returns false. Currently only the
// bounded-exhaustion case logs; the other paths already logged
// their own "exited cleanly" / "terminated" / "exited with error"
// records inside runBeforeOneAttempt.
func logRestartTerminalEvent(event *restartTerminalEvent) {
	// Bounded policy and this was the last allowed attempt —
	// log ERROR and stop. Unbounded loops never hit this branch.
	boundedExhausted := event.outcome == exitErrored &&
		!event.policy.unbounded &&
		event.attempt == event.policy.maxAttempts

	if boundedExhausted {
		event.logger.ErrorContext(event.ctx, "before command restart budget exhausted",
			"index", event.idx,
			"command", truncate(event.cmd.Command),
			"attempts", event.attempt,
			"max_attempts", event.policy.maxAttempts,
			"error", event.waitErr,
		)
	}
}

// runBeforeOneAttempt spawns a single sh -c process for cmd via
// exec.CommandContext (so the server ctx cancellation kills the
// child), waits for it to exit, and logs the outcome. It returns
// the classification of the exit and the underlying error (if any)
// so the caller can decide whether to respawn.
//
// Each call builds a fresh *exec.Cmd — exec.Cmd is single-use and
// a second Start() on a Waited command is undefined behavior. A
// fresh exec.Cmd also gives each attempt its own stderr/stdout
// pipes, so an operator tailing logs doesn't see one process's
// output mixed into the next's pipe buffer.
//
// runBeforeOneAttempt never returns an error: spawn / exit /
// terminate events are logged at ERROR or INFO before the
// function returns.
func runBeforeOneAttempt(ctx context.Context, attemptInput *restartAttempt) (exitOutcome, error) {
	idx := attemptInput.idx
	cmd := attemptInput.cmd
	logger := attemptInput.logger

	command := exec.CommandContext(ctx, "sh", "-c", cmd.Command)

	command.Stdout = os.Stderr
	command.Stderr = os.Stderr

	startErr := command.Start()
	if startErr != nil {
		logger.ErrorContext(ctx, "before command spawn failed",
			"index", idx,
			"attempt", attemptInput.attempt,
			"command", truncate(cmd.Command),
			"error", startErr,
		)

		return exitErrored, fmt.Errorf("before command spawn: %w", startErr)
	}

	waitErr := command.Wait()
	if waitErr == nil {
		logger.InfoContext(ctx, "before command exited cleanly",
			"index", idx,
			"attempt", attemptInput.attempt,
			"command", truncate(cmd.Command),
			"pid", command.Process.Pid,
		)

		return exitClean, nil
	}

	// exec.ExitError reports ExitCode() == -1 when the process
	// was terminated by a signal (which is what exec.CommandContext
	// does on timeout or context cancellation). Distinguish that
	// case from "exited non-zero".
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) && exitErr.ExitCode() == -1 {
		logger.ErrorContext(ctx, "before command terminated",
			"index", idx,
			"attempt", attemptInput.attempt,
			"command", truncate(cmd.Command),
			"pid", command.Process.Pid,
			"error", waitErr,
		)

		return exitSignalTerminated, fmt.Errorf("before command terminated: %w", waitErr)
	}

	logger.ErrorContext(ctx, "before command exited with error",
		"index", idx,
		"attempt", attemptInput.attempt,
		"command", truncate(cmd.Command),
		"pid", command.Process.Pid,
		"error", waitErr,
	)

	return exitErrored, fmt.Errorf("before command exited: %w", waitErr)
}

// runBeforeHealthcheck runs the TCP probe loop for a single
// BeforeCommand. The loop retries dialing Healthcheck.TCP every
// Healthcheck.Interval until either the dial succeeds or
// Healthcheck.Timeout fires.
//
// runBeforeHealthcheck never returns an error to its caller;
// outcomes are logged at INFO (success) or ERROR (timeout).
func runBeforeHealthcheck(ctx context.Context, idx int, cmd BeforeCommand, logger *slog.Logger) {
	healthcheck := cmd.Healthcheck

	interval := healthcheck.Interval.Duration()
	if interval <= 0 {
		interval = defaultHealthcheckInterval
	}

	timeout := healthcheck.Timeout.Duration()
	if timeout <= 0 {
		timeout = defaultHealthcheckTimeout
	}

	startTime := time.Now()

	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for attempt := 1; ; attempt++ {
		// exhaustruct and gocritic's typed-nil rule conflict on
		// net.Dialer{} (typed nil is invalid for interfaces/funcs).
		// Disable exhaustruct once.
		dialer := net.Dialer{Timeout: interval} //nolint:exhaustruct // Timeout is only field used

		conn, dialErr := dialer.DialContext(deadlineCtx, "tcp", healthcheck.TCP)
		attemptInput := &healthcheckAttempt{
			idx: idx, cmd: cmd, attempt: attempt, startTime: startTime,
			conn: conn, dialErr: dialErr,
		}

		if tryHealthcheckAttempt(ctx, deadlineCtx, logger, attemptInput) {
			return
		}

		select {
		case <-time.After(interval):
		case <-deadlineCtx.Done():
			logHealthcheckTimeout(ctx, logger, &healthcheckRecord{
				idx: idx, cmd: cmd, attempt: attempt,
				startTime: startTime, cause: context.Cause(deadlineCtx),
			})

			return
		}
	}
}

// tryHealthcheckAttempt runs one dial and handles the three possible
// outcomes — pass, deadline timeout, or transient failure. It
// returns true when the caller should stop looping (either we
// passed or the deadline expired); false means the dial failed
// for a non-deadline reason and the caller should sleep and retry.
//
// Extracted from the loop body to keep runBeforeHealthcheck under
// the project's gocognit limit. All per-attempt inputs and the
// dial result live on a single healthcheckAttempt value so the
// function stays under the project's revive-argument-limit
// (max 4). Both context.Context parameters sit at the front of
// the param list, before any other arguments, to satisfy revive's
// context-as-argument rule.
func tryHealthcheckAttempt(
	ctx, deadlineCtx context.Context,
	logger *slog.Logger,
	attempt *healthcheckAttempt,
) bool {
	if attempt.dialErr == nil {
		closeErr := attempt.conn.Close()
		if closeErr != nil {
			logger.DebugContext(ctx, "before command healthcheck close failed",
				"index", attempt.idx,
				"tcp", attempt.cmd.Healthcheck.TCP,
				"error", closeErr,
			)
		}

		logger.InfoContext(ctx, "before command healthcheck passed",
			"index", attempt.idx,
			"command", truncate(attempt.cmd.Command),
			"tcp", attempt.cmd.Healthcheck.TCP,
			"attempts", attempt.attempt,
			"elapsed", time.Since(attempt.startTime).String(),
		)

		return true
	}

	if context.Cause(deadlineCtx) != nil {
		logHealthcheckTimeout(ctx, logger, &healthcheckRecord{
			idx: attempt.idx, cmd: attempt.cmd, attempt: attempt.attempt,
			startTime: attempt.startTime, cause: attempt.dialErr,
		})

		return true
	}

	logger.DebugContext(ctx, "before command healthcheck attempt failed",
		"index", attempt.idx,
		"tcp", attempt.cmd.Healthcheck.TCP,
		"attempt", attempt.attempt,
		"error", attempt.dialErr,
	)

	return false
}

// healthcheckAttempt is the per-iteration input bundle for
// tryHealthcheckAttempt. It carries every value that varies across
// attempts (per-iteration metadata, dial result) so the helper
// itself only needs three parameters.
type healthcheckAttempt struct {
	idx       int
	cmd       BeforeCommand
	attempt   int
	startTime time.Time
	conn      net.Conn
	dialErr   error
}

// logHealthcheckTimeout emits the canonical "before command
// healthcheck timed out" ERROR record at the call sites that
// detect a deadline expiry (either at the dial site or after the
// sleep). All fields live on a healthcheckRecord so the helper
// signature stays small.
func logHealthcheckTimeout(
	ctx context.Context,
	logger *slog.Logger,
	record *healthcheckRecord,
) {
	logger.ErrorContext(ctx, "before command healthcheck timed out",
		"index", record.idx,
		"command", truncate(record.cmd.Command),
		"tcp", record.cmd.Healthcheck.TCP,
		"attempts", record.attempt,
		"elapsed", record.elapsed(),
		"error", record.cause,
	)
}

// healthcheckRecord is the bag of fields every "healthcheck timed
// out" log line needs. It exists so the call site can pass the
// fields as one value — the project's revive-argument-limit rule
// caps function arguments at 4.
type healthcheckRecord struct {
	idx       int
	cmd       BeforeCommand
	attempt   int
	startTime time.Time
	cause     error
}

// elapsed returns the wall-clock duration between startTime and
// now.
func (record *healthcheckRecord) elapsed() string {
	return time.Since(record.startTime).String()
}

// truncate shortens a string to at most truncateCommandLen bytes,
// appending an ellipsis when truncation happens. The ellipsis is
// clamped so the returned string never exceeds truncateCommandLen
// bytes in length.
func truncate(value string) string {
	if len(value) <= truncateCommandLen {
		return value
	}

	if truncateCommandLen <= ellipsisLen {
		return value[:truncateCommandLen]
	}

	return value[:truncateCommandLen-ellipsisLen] + "..."
}
