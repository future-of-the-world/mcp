// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:gocognit,cyclop,lll,noinlineerr // per-tool factories cluster validation + dispatch; inline errors read better here
package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"go.temporal.io/sdk/client"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"golang.org/x/sync/errgroup"

	workflowpb "go.temporal.io/api/workflow/v1"

	"go.amidman.dev/mcp/tool"
)

// --- Constants ---

// batchDefaults is the package-level defaults applied when the
// connect map omits the corresponding field. They mirror the
// upstream Python temporal-mcp defaults.
const (
	// batchDefaultLimit is the per-tool maximum number of executions
	// the visibility query returns in a single page.
	batchDefaultLimit = 100

	// batchDefaultConcurrency is the per-tool fan-out concurrency.
	batchDefaultConcurrency = 50

	// batchHardCapConcurrency is the upper bound on the concurrency
	// the handler passes to errgroup.SetLimit. The cap protects the
	// Temporal frontend from a single batch run gRPC-storming it.
	batchHardCapConcurrency = 100

	// batchMaxErrors is the cap on len(errors[]) in the response
	// payload. Above this, the response carries truncated=true and
	// dropped=N. Picked to match the spec body; comfortable for the
	// 95th-percentile batch run, low enough to keep payloads small
	// even on 1000-item batches that all fail.
	batchMaxErrors = 50

	// batchIDSep separates a workflow/activity ID from its run ID
	// inside error entries: "workflowID/runID" or "activityID/runID".
	batchIDSep = "/"
)

// Tool names — central constants so the per-tool factory block,
// the test in connect_test.go (asserting presence in the response),
// and the per-tool annotations stay in lock-step.
const (
	nameBatchSignal              = "batch_signal"
	nameBatchCancel              = "batch_cancel"
	nameBatchTerminate           = "batch_terminate"
	nameBatchCancelActivities    = "batch_cancel_activities"
	nameBatchTerminateActivities = "batch_terminate_activities"
)

// batchLoop is the package-level preamble for the batch tool
// group, defined alongside scheduleLoop/workflowLoop/etc. in
// temporal.go. Reference it here when building each tool's
// Description so the per-tool describer suffix is appended after
// the shared lifecycle narrative.

// --- batchClient interface ---
//
// batchClient is the focused subset of the Temporal SDK that the
// batch handlers consume. *clientManager implements it via the
// pass-through methods in client.go. Tests provide their own fake.
// The type is intentionally local to batch_handlers.go (not
// exported, not added to temporalClient) so adding more methods
// stays a per-PR decision tied to the batch tool surface only.

type batchClient interface {
	batchSignal(ctx context.Context, workflowID, runID, signalName string, arg any) error
	batchCancelWorkflow(ctx context.Context, workflowID, runID string) error
	batchTerminateWorkflow(
		ctx context.Context,
		workflowID, runID, reason string,
		details []any,
	) error
	batchListWorkflows(
		ctx context.Context,
		query string,
		limit int,
	) ([]*workflowpb.WorkflowExecutionInfo, error)
	batchListActivities(
		ctx context.Context,
		query string,
		limit int,
	) ([]*client.ActivityExecutionInfo, error)
	batchGetActivityHandle(activityID, runID string) (client.ActivityHandle, error)
}

// --- Argument shapes ---

// batchQueryArgs is the JSON input shared by all five batch tools:
// query, limit, concurrency. Per-tool structs embed it and add the
// tool-specific fields.
type batchQueryArgs struct {
	Query       string `json:"query"`
	Limit       int    `json:"limit,omitempty"`
	Concurrency int    `json:"concurrency,omitempty"`
}

// batchSignalArgs is batch_signal's input. signal_name and args are
// required-or-optional respectively.
type batchSignalArgs struct {
	batchQueryArgs

	SignalName string `json:"signal_name"`
	Args       any    `json:"args,omitempty"`
}

// batchTerminateArgs is batch_terminate and batch_terminate_activities'
// shared input shape (workflow vs activity divergence happens inside
// the handler, not in the JSON). Reason is required for the workflow
// tool and the activity tool alike; Details is optional.
type batchTerminateArgs struct {
	batchQueryArgs

	Reason  string `json:"reason"`
	Details any    `json:"details,omitempty"`
}

// batchCancelActivitiesArgs is batch_cancel_activities' input; Reason
// is optional. Canceling a workflow does not surface in the SDK as
// taking a reason, so batch_cancel reuses batchQueryArgs directly.
type batchCancelActivitiesArgs struct {
	batchQueryArgs

	Reason string `json:"reason,omitempty"`
}

// batchTerminateActivitiesArgs is batch_terminate_activities' input.
// Reason is required; Details is silently ignored — the activity SDK
// surface only takes Reason.
type batchTerminateActivitiesArgs struct {
	batchQueryArgs

	Reason string `json:"reason"`
}

// --- batchResponse envelope ---

// batchResponse is the JSON shape returned by every batch_* tool.
// The output schemas pin matched/succeeded/failed as required, and
// allow errors/truncated/dropped to be absent when nothing failed.
// Truncated and Dropped always travel together.
type batchResponse struct {
	Matched   int             `json:"matched"`
	Succeeded int             `json:"succeeded"`
	Failed    int             `json:"failed"`
	Errors    []batchErrorOut `json:"errors,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
	Dropped   int             `json:"dropped,omitempty"`
}

// batchErrorOut is the per-failed-id entry inside batchResponse.Errors.
type batchErrorOut struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// --- batchExec generic helper ---

// batchExec fans op across items with bounded concurrency and
// collects a per-item success/failure summary. T is the per-item
// value passed to op; the closure over the caller's batchClient and
// any captured args (signalName, reason, ...) completes the dispatch.
//
// The function returns (matched, succeeded, failed, errs):
//   - matched is len(items) — the handler picks this up after the
//     visibility query returns its result slice.
//   - succeeded is the count of op calls that returned nil.
//   - failed is succeeded's complement.
//   - errs is the per-failure (id, message) pairs, untruncated. The
//     handler applies the 50-entry cap and emits the envelope.
//
// errgroup semantics: when one goroutine returns a non-nil error,
// Wait returns that first error; new Go calls block until the limit
// frees up but do not abort already-running goroutines. This is
// exactly the "partial completion on failure, no thundering herd"
// behavior the spec calls for. The summarized error count is built
// from the failed[] slice we accumulate locally, not from g.Wait().
//
//nolint:revive // argument-limit: 6-arg generic helper is idiomatic for fan-out workers; result-count: error-truncation needs the slice alongside the counts
func batchExec[T any](
	ctx context.Context,
	logger *slog.Logger,
	concurrency int,
	items []T,
	operation func(context.Context, T) error,
	resolveID func(T) string,
) (succeeded, failed int, errs []batchErrorOut) {
	if len(items) == 0 {
		return 0, 0, nil
	}

	capped := clampConcurrency(concurrency)

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(capped)

	type itemResult struct {
		id  string
		err error
	}

	results := make([]itemResult, len(items))

	for i := range items {
		idx := i

		group.Go(func() error {
			err := operation(groupCtx, items[idx])

			results[idx] = itemResult{
				id:  resolveID(items[idx]),
				err: err,
			}

			// Returning err cancels new Go calls; we still let
			// running goroutines complete (errgroup's default
			// behavior). Returning nil here is also valid; we only
			// want the cancellation signal.
			if err != nil {
				logger.DebugContext(groupCtx, "batch operation error",
					"id", results[idx].id,
					"error", err.Error(),
				)
			}

			return nil
		})
	}

	// Wait drains all in-flight goroutines before we collapse the
	// results. We discard g.Wait()'s error because we already keep
	// every failure in the results slice — errgroup's aggregate
	// "first error" is uninteresting for the response shape.
	_ = group.Wait() //nolint:errcheck // failures already captured in results[]; errgroup's first-error is redundant

	for _, result := range results {
		if result.err == nil {
			succeeded++

			continue
		}

		failed++

		errs = append(errs, batchErrorOut{
			ID:    result.id,
			Error: result.err.Error(),
		})
	}

	return succeeded, failed, errs
}

// clampConcurrency applies the spec's [default, hard-cap] rules
// to a user-supplied concurrency value:
//   - <= 0 → defaults to batchDefaultConcurrency (50).
//   - > batchHardCapConcurrency (100) → clamped to the cap.
//
// Returns the resolved positive integer suitable for
// errgroup.Group.SetLimit.
func clampConcurrency(requested int) int {
	if requested <= 0 {
		return batchDefaultConcurrency
	}

	if requested > batchHardCapConcurrency {
		return batchHardCapConcurrency
	}

	return requested
}

// clampLimit applies the spec's per-tool limit rule. <= 0 falls back
// to batchDefaultLimit (100). There is no upper bound beyond the
// server-side PageSize cap on the SDK.
func clampLimit(requested int) int {
	if requested <= 0 {
		return batchDefaultLimit
	}

	return requested
}

// envelope assembles the final batchResponse, applying the 50-entry
// error truncation. truncated/dropped are emitted only when the cap
// was actually hit — they stay absent otherwise so the response
// stays compact on the happy path.
func envelope(matched, succeeded, failed int, errs []batchErrorOut) batchResponse {
	out := batchResponse{ //nolint:exhaustruct // Errors/Truncated/Dropped are filled only when needed
		Matched:   matched,
		Succeeded: succeeded,
		Failed:    failed,
	}

	if len(errs) == 0 {
		return out
	}

	if len(errs) <= batchMaxErrors {
		out.Errors = errs

		return out
	}

	out.Errors = errs[:batchMaxErrors]
	out.Truncated = true
	out.Dropped = len(errs) - batchMaxErrors

	return out
}

// --- Argument decoders ---

// decodeBatchArgs unmarshals the JSON arguments for a single tool
// into target. Returns a wrapped error so the per-tool handler can
// surface it as a parse-failure response.
func decodeBatchArgs(req *mcp.CallToolRequest, target any) error {
	if req == nil || len(req.Params.Arguments) == 0 {
		return nil
	}

	//nolint:wrapcheck // external json.Unmarshal error returned verbatim for handler parsing failures
	return json.Unmarshal(req.Params.Arguments, target)
}

// batchErr wraps a parse failure with the tool-name prefix so the
// model sees which argument shape failed.
func batchErr(toolName string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("temporal tool: parse %s args: %w", toolName, err)
}

// --- Shared "found executions → items" helpers ---

// workflowItem is the per-execution value passed through batchExec
// for batch_signal / batch_cancel / batch_terminate.
type workflowItem struct {
	WorkflowID string
	RunID      string
}

// workflowItemID satisfies the resolveID closure batchExec needs.
func workflowItemID(item workflowItem) string {
	if item.RunID == "" {
		return item.WorkflowID
	}

	return item.WorkflowID + batchIDSep + item.RunID
}

// activityItem is the per-execution value passed through batchExec
// for batch_cancel_activities / batch_terminate_activities.
type activityItem struct {
	ActivityID string
	RunID      string
}

// activityItemID satisfies the resolveID closure batchExec needs.
func activityItemID(item activityItem) string {
	if item.RunID == "" {
		return item.ActivityID
	}

	return item.ActivityID + batchIDSep + item.RunID
}

// --- Per-tool handlers ---

// handleBatchSignal returns the mcp.ToolHandler for batch_signal.
// Strategy:
//  1. Decode + validate args (signalName required).
//  2. Run batchListWorkflows(query, limit) to materialize items.
//  3. Build the per-item closure over batchSignal(signalName, args).
//  4. Call batchExec and emit the envelope.
func handleBatchSignal(batch batchClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args batchSignalArgs
		if err := decodeBatchArgs(req, &args); err != nil {
			return nil, batchErr("batch_signal", err)
		}

		if args.Query == "" {
			return nil, errors.New("temporal tool: batch_signal: query is required")
		}

		if args.SignalName == "" {
			return nil, errors.New("temporal tool: batch_signal: signal_name is required")
		}

		infos, err := batch.batchListWorkflows(ctx, args.Query, clampLimit(args.Limit))
		if err != nil {
			return nil, fmt.Errorf("temporal tool: batch_signal: list: %w", err)
		}

		items := make([]workflowItem, 0, len(infos))
		for _, info := range infos {
			if info == nil || info.Execution == nil {
				continue
			}

			items = append(items, workflowItem{
				WorkflowID: info.Execution.WorkflowId,
				RunID:      info.Execution.RunId,
			})
		}

		signalName := args.SignalName
		arg := args.Args

		succeeded, failed, errs := batchExec(
			ctx,
			noopLogger(),
			args.Concurrency,
			items,
			func(opCtx context.Context, item workflowItem) error {
				return batch.batchSignal(opCtx, item.WorkflowID, item.RunID, signalName, arg)
			},
			workflowItemID,
		)

		return marshalBatchToolResult(envelope(len(items), succeeded, failed, errs))
	}
}

// handleBatchCancel returns the mcp.ToolHandler for batch_cancel.
func handleBatchCancel(batch batchClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args batchQueryArgs
		if err := decodeBatchArgs(req, &args); err != nil {
			return nil, batchErr("batch_cancel", err)
		}

		if args.Query == "" {
			return nil, errors.New("temporal tool: batch_cancel: query is required")
		}

		infos, err := batch.batchListWorkflows(ctx, args.Query, clampLimit(args.Limit))
		if err != nil {
			return nil, fmt.Errorf("temporal tool: batch_cancel: list: %w", err)
		}

		items := workflowItemsFromList(infos)

		succeeded, failed, errs := batchExec(
			ctx,
			noopLogger(),
			args.Concurrency,
			items,
			func(opCtx context.Context, item workflowItem) error {
				return batch.batchCancelWorkflow(opCtx, item.WorkflowID, item.RunID)
			},
			workflowItemID,
		)

		return marshalBatchToolResult(envelope(len(items), succeeded, failed, errs))
	}
}

// handleBatchTerminate returns the mcp.ToolHandler for batch_terminate.
func handleBatchTerminate(batch batchClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args batchTerminateArgs
		if err := decodeBatchArgs(req, &args); err != nil {
			return nil, batchErr("batch_terminate", err)
		}

		if args.Query == "" {
			return nil, errors.New("temporal tool: batch_terminate: query is required")
		}

		if args.Reason == "" {
			return nil, errors.New("temporal tool: batch_terminate: reason is required")
		}

		infos, err := batch.batchListWorkflows(ctx, args.Query, clampLimit(args.Limit))
		if err != nil {
			return nil, fmt.Errorf("temporal tool: batch_terminate: list: %w", err)
		}

		items := workflowItemsFromList(infos)

		reason := args.Reason
		details := batchDetailsAsSlice(args.Details)

		succeeded, failed, errs := batchExec(
			ctx,
			noopLogger(),
			args.Concurrency,
			items,
			func(opCtx context.Context, item workflowItem) error {
				return batch.batchTerminateWorkflow(opCtx,
					item.WorkflowID, item.RunID, reason, details)
			},
			workflowItemID,
		)

		return marshalBatchToolResult(envelope(len(items), succeeded, failed, errs))
	}
}

// handleBatchCancelActivities returns the mcp.ToolHandler for
// batch_cancel_activities.
func handleBatchCancelActivities(batch batchClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args batchCancelActivitiesArgs
		if err := decodeBatchArgs(req, &args); err != nil {
			return nil, batchErr("batch_cancel_activities", err)
		}

		if args.Query == "" {
			return nil, errors.New("temporal tool: batch_cancel_activities: query is required")
		}

		infos, err := batch.batchListActivities(ctx, args.Query, clampLimit(args.Limit))
		if err != nil {
			return nil, fmt.Errorf("temporal tool: batch_cancel_activities: list: %w", err)
		}

		items := activityItemsFromList(infos)

		reason := args.Reason

		succeeded, failed, errs := batchExec(
			ctx,
			noopLogger(),
			args.Concurrency,
			items,
			func(opCtx context.Context, item activityItem) error {
				handle, handleErr := batch.batchGetActivityHandle(item.ActivityID, item.RunID)
				if handleErr != nil {
					return handleErr
				}

				return handle.Cancel(opCtx, client.CancelActivityOptions{Reason: reason})
			},
			activityItemID,
		)

		return marshalBatchToolResult(envelope(len(items), succeeded, failed, errs))
	}
}

// handleBatchTerminateActivities returns the mcp.ToolHandler for
// batch_terminate_activities.
func handleBatchTerminateActivities(batch batchClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args batchTerminateActivitiesArgs
		if err := decodeBatchArgs(req, &args); err != nil {
			return nil, batchErr("batch_terminate_activities", err)
		}

		if args.Query == "" {
			return nil, errors.New("temporal tool: batch_terminate_activities: query is required")
		}

		if args.Reason == "" {
			return nil, errors.New("temporal tool: batch_terminate_activities: reason is required")
		}

		infos, err := batch.batchListActivities(ctx, args.Query, clampLimit(args.Limit))
		if err != nil {
			return nil, fmt.Errorf("temporal tool: batch_terminate_activities: list: %w", err)
		}

		items := activityItemsFromList(infos)

		reason := args.Reason

		succeeded, failed, errs := batchExec(
			ctx,
			noopLogger(),
			args.Concurrency,
			items,
			func(opCtx context.Context, item activityItem) error {
				handle, handleErr := batch.batchGetActivityHandle(item.ActivityID, item.RunID)
				if handleErr != nil {
					return handleErr
				}

				return handle.Terminate(opCtx, client.TerminateActivityOptions{Reason: reason})
			},
			activityItemID,
		)

		return marshalBatchToolResult(envelope(len(items), succeeded, failed, errs))
	}
}

// --- Conversion helpers ---

// workflowItemsFromList projects a slice of *WorkflowExecutionInfo
// into the []workflowItem that batchExec consumes. Nil entries are
// dropped — they cannot happen on a clean SDK response but the
// guard is cheap.
func workflowItemsFromList(infos []*workflowpb.WorkflowExecutionInfo) []workflowItem {
	out := make([]workflowItem, 0, len(infos))
	for _, info := range infos {
		if info == nil || info.Execution == nil {
			continue
		}

		out = append(out, workflowItem{
			WorkflowID: info.Execution.WorkflowId,
			RunID:      info.Execution.RunId,
		})
	}

	return out
}

// activityItemsFromList projects a slice of *ActivityExecutionInfo
// into the []activityItem that batchExec consumes.
func activityItemsFromList(infos []*client.ActivityExecutionInfo) []activityItem {
	out := make([]activityItem, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			continue
		}

		out = append(out, activityItem{
			ActivityID: info.ActivityID,
			RunID:      info.ActivityRunID,
		})
	}

	return out
}

// batchDetailsAsSlice normalizes the user's `details` JSON value into
// the []any shape that the SDK's variadic TerminateWorkflow expects.
// nil / empty stays a nil slice (the SDK accepts nil), JSON arrays
// pass through as-is, scalar values become a single-element slice so
// the SDK encodes them as one detail payload.
func batchDetailsAsSlice(details any) []any {
	switch val := details.(type) {
	case nil:
		return nil

	case []any:
		if len(val) == 0 {
			return nil
		}

		return val

	default:
		return []any{val}
	}
}

// marshalBatchToolResult wraps payload as a CallToolResult. Identical
// shape to the woodpecker helper, duplicated here so this file does
// not import the woodpecker package just for one function. Stays a
// method-free shared helper for the five batch handlers above.
func marshalBatchToolResult(payload any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("temporal tool: marshal response: %w", err)
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

// noopLogger returns a *slog.Logger that drops everything. The batch
// handlers accept an injected logger via Connect (wired from
// tool.NewOptions); this package-local helper lets the per-tool
// handlers compile when batchExec is called outside Connect (i.e.
// in tests that don't go through Dispatcher).
func noopLogger() *slog.Logger {
	return slog.New(
		slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}),
	)
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// --- Connect-time registration ---

// batchAnnotationsSignal corresponds to batch_signal:
// non-destructive, non-idempotent (each call delivers a fresh signal
// payload to every matched execution), open-world.
func batchAnnotationsSignal() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           "Batch signal",
		ReadOnlyHint:    false,
		DestructiveHint: new(false),
		IdempotentHint:  false,
		OpenWorldHint:   new(true),
	}
}

// batchAnnotationsCancel corresponds to batch_cancel AND
// batch_cancel_activities: destructive (kills in-flight work),
// idempotent (canceling an already-canceled or finished execution is
// a no-op on the server).
func batchAnnotationsCancel() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           "Batch cancel",
		ReadOnlyHint:    false,
		DestructiveHint: new(true),
		IdempotentHint:  true,
		OpenWorldHint:   new(true),
	}
}

// batchAnnotationsTerminate corresponds to batch_terminate AND
// batch_terminate_activities: destructive (forces execution end
// immediately, no cleanup), idempotent (terminating an already-finished
// execution is a no-op on the server).
func batchAnnotationsTerminate() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           "Batch terminate",
		ReadOnlyHint:    false,
		DestructiveHint: new(true),
		IdempotentHint:  true,
		OpenWorldHint:   new(true),
	}
}

// batchTool returns the per-tool response envelope used by Connect.
// It returns one tool.Tool per batch_* entry in registration order.
func batchTools(batch batchClient) []tool.Tool {
	return []tool.Tool{
		{
			Tool: &mcp.Tool{
				Name:         nameBatchSignal,
				Description:  batchLoop + "\n\nFan out a workflow signal over every execution that matches a visibility query.",
				InputSchema:  batchSignalInput,
				OutputSchema: batchSignalOutput,
				Annotations:  batchAnnotationsSignal(),
			},
			Handler: handleBatchSignal(batch),
		},
		{
			Tool: &mcp.Tool{
				Name:         nameBatchCancel,
				Description:  batchLoop + "\n\nFan out a workflow cancel request over every execution that matches a visibility query.",
				InputSchema:  batchCancelInput,
				OutputSchema: batchCancelOutput,
				Annotations:  batchAnnotationsCancel(),
			},
			Handler: handleBatchCancel(batch),
		},
		{
			Tool: &mcp.Tool{
				Name:         nameBatchTerminate,
				Description:  batchLoop + "\n\nFan out a workflow terminate request over every execution that matches a visibility query.",
				InputSchema:  batchTerminateInput,
				OutputSchema: batchTerminateOutput,
				Annotations:  batchAnnotationsTerminate(),
			},
			Handler: handleBatchTerminate(batch),
		},
		{
			Tool: &mcp.Tool{
				Name:         nameBatchCancelActivities,
				Description:  batchLoop + "\n\nFan out an activity cancel request over every standalone activity that matches a visibility query.",
				InputSchema:  batchCancelActivitiesInput,
				OutputSchema: batchCancelActivitiesOutput,
				Annotations:  batchAnnotationsCancel(),
			},
			Handler: handleBatchCancelActivities(batch),
		},
		{
			Tool: &mcp.Tool{
				Name:         nameBatchTerminateActivities,
				Description:  batchLoop + "\n\nFan out an activity terminate request over every standalone activity that matches a visibility query.",
				InputSchema:  batchTerminateActivitiesInput,
				OutputSchema: batchTerminateActivitiesOutput,
				Annotations:  batchAnnotationsTerminate(),
			},
			Handler: handleBatchTerminateActivities(batch),
		},
	}
}
