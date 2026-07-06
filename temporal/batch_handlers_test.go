// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:exhaustruct,wsl_v5,lll,modernize,gocritic // test fixtures prefer classic loops for clarity
package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonpb "go.temporal.io/api/common/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
)

// --- Test harness: fakeBatchClient implements batchClient ---

// fakeBatchClient is the test double for batchClient. Each method
// can be replaced with a per-test closure; the default behavior is
// "record the call and return nil." Tests that need delays, errors,
// or different result shapes provide their own closures.
//
// Counting is done with atomics so the concurrency-cap test can run
// under -race without races on the counter.
type fakeBatchClient struct {
	mu sync.Mutex

	// signalFn / cancelFn / terminateFn override the default zero-return
	// behavior. If nil, the default records the call and returns nil.
	signalFn    func(ctx context.Context, workflowID, runID, signalName string, arg any) error
	cancelFn    func(ctx context.Context, workflowID, runID string) error
	terminateFn func(ctx context.Context, workflowID, runID, reason string, details []any) error
	listWFsFn   func(ctx context.Context, query string, limit int) ([]*workflowpb.WorkflowExecutionInfo, error)
	listActsFn  func(ctx context.Context, query string, limit int) ([]*client.ActivityExecutionInfo, error)
	getHandleFn func(activityID, runID string) (client.ActivityHandle, error)

	// listWorkflowsCallCount is incremented per ListWorkflow call.
	listWorkflowsCallCount atomic.Int32

	// signalCount / cancelCount / terminateCount track per-ID
	// success/failure call counts. Read with mu held.
	signalCalls    []batchCall
	cancelCalls    []batchCall
	terminateCalls []batchCall
	cancelActCalls []batchCall
	termActCalls   []batchCall

	// inflight + peak are the live signal/terminate counter pair
	// the concurrency-cap test asserts on. Touched under atomic
	// only — no mu.
	inflight atomic.Int32
	peak     atomic.Int32

	// delay is set by the concurrency test to force every signal
	// call to block on it. zero means "no delay".
	delay time.Duration
}

// batchCall records the argument tuple for a single op invocation.
type batchCall struct {
	ID    string
	Name  string // signalName / reason — only populated for signal/terminate
	Arg   any    // only populated for signal
	RunID string // only populated when non-empty
}

// fakeBatchActivityHandle is a client.ActivityHandle for tests that don't
// care about the activity lifecycle (just cancel/terminate).
type fakeBatchActivityHandle struct {
	parent    *fakeBatchClient
	id        string
	runID     string
	cancelFn  func(ctx context.Context, opts client.CancelActivityOptions) error
	terminate func(ctx context.Context, opts client.TerminateActivityOptions) error
}

func (f *fakeBatchActivityHandle) GetID() string { return f.id }

func (f *fakeBatchActivityHandle) GetRunID() string { return f.runID }

func (*fakeBatchActivityHandle) Get(_ context.Context, _ any) error { return nil }

func (*fakeBatchActivityHandle) Describe(
	_ context.Context,
	_ client.DescribeActivityOptions,
) (*client.ActivityExecutionDescription, error) {
	return &client.ActivityExecutionDescription{}, nil
}

func (f *fakeBatchActivityHandle) Cancel(
	ctx context.Context,
	opts client.CancelActivityOptions,
) error {
	if f.parent != nil {
		f.parent.mu.Lock()
		f.parent.cancelActCalls = append(f.parent.cancelActCalls, batchCall{
			ID:    f.id,
			RunID: f.runID,
			Name:  opts.Reason,
		})
		f.parent.mu.Unlock()
	}

	if f.cancelFn != nil {
		return f.cancelFn(ctx, opts)
	}

	return nil
}

func (f *fakeBatchActivityHandle) Terminate(
	ctx context.Context,
	opts client.TerminateActivityOptions,
) error {
	if f.parent != nil {
		f.parent.mu.Lock()
		f.parent.termActCalls = append(f.parent.termActCalls, batchCall{
			ID:    f.id,
			RunID: f.runID,
			Name:  opts.Reason,
		})
		f.parent.mu.Unlock()
	}

	if f.terminate != nil {
		return f.terminate(ctx, opts)
	}

	return nil
}

//nolint:revive // argument-limit: batchClient batchSignal shape (workflowID, runID, signalName, arg) is fixed by the interface contract
func (f *fakeBatchClient) batchSignal(
	ctx context.Context,
	workflowID, runID, signalName string,
	arg any,
) error {
	if f.delay > 0 {
		// Bump the live counters atomically, sleep, then decrement
		// before returning so the peak observation is monotonic.
		now := f.inflight.Add(1)
		for {
			oldPeak := f.peak.Load()
			if now <= oldPeak || f.peak.CompareAndSwap(oldPeak, now) {
				break
			}
		}

		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			f.inflight.Add(-1)

			//nolint:wrapcheck // g.Wait drains in-flight ops; cause carries ctx state forward
			return context.Cause(ctx)
		}

		f.inflight.Add(-1)
	}

	f.mu.Lock()
	f.signalCalls = append(
		f.signalCalls,
		batchCall{ID: workflowID, RunID: runID, Name: signalName, Arg: arg},
	)
	f.mu.Unlock()

	if f.signalFn != nil {
		return f.signalFn(ctx, workflowID, runID, signalName, arg)
	}

	return nil
}

func (f *fakeBatchClient) batchCancelWorkflow(
	ctx context.Context,
	workflowID, runID string,
) error {
	f.mu.Lock()
	f.cancelCalls = append(f.cancelCalls, batchCall{ID: workflowID, RunID: runID})
	f.mu.Unlock()

	if f.cancelFn != nil {
		return f.cancelFn(ctx, workflowID, runID)
	}

	return nil
}

//nolint:revive // argument-limit: batchClient batchTerminateWorkflow shape (workflowID, runID, reason, details) is fixed by the interface contract
func (f *fakeBatchClient) batchTerminateWorkflow(
	ctx context.Context,
	workflowID, runID, reason string,
	details []any,
) error {
	if f.delay > 0 {
		now := f.inflight.Add(1)
		for {
			oldPeak := f.peak.Load()
			if now <= oldPeak || f.peak.CompareAndSwap(oldPeak, now) {
				break
			}
		}

		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			f.inflight.Add(-1)

			return context.Cause(ctx) //nolint:wrapcheck // pass-through
		}

		f.inflight.Add(-1)
	}

	f.mu.Lock()
	f.terminateCalls = append(
		f.terminateCalls,
		batchCall{ID: workflowID, RunID: runID, Name: reason, Arg: details},
	)
	f.mu.Unlock()

	if f.terminateFn != nil {
		return f.terminateFn(ctx, workflowID, runID, reason, details)
	}

	return nil
}

func (f *fakeBatchClient) batchListWorkflows(
	ctx context.Context,
	query string,
	limit int,
) ([]*workflowpb.WorkflowExecutionInfo, error) {
	f.listWorkflowsCallCount.Add(1)

	if f.listWFsFn != nil {
		return f.listWFsFn(ctx, query, limit)
	}

	out := make([]*workflowpb.WorkflowExecutionInfo, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{
				WorkflowId: fmt.Sprintf("wf-%d", i),
				RunId:      fmt.Sprintf("run-%d", i),
			},
		})
	}

	return out, nil
}

func (f *fakeBatchClient) batchListActivities(
	ctx context.Context,
	query string,
	limit int,
) ([]*client.ActivityExecutionInfo, error) {
	if f.listActsFn != nil {
		return f.listActsFn(ctx, query, limit)
	}

	out := make([]*client.ActivityExecutionInfo, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, &client.ActivityExecutionInfo{
			ActivityID:    fmt.Sprintf("act-%d", i),
			ActivityRunID: fmt.Sprintf("arun-%d", i),
		})
	}

	return out, nil
}

func (f *fakeBatchClient) batchGetActivityHandle(
	activityID, runID string,
) (client.ActivityHandle, error) {
	if f.getHandleFn != nil {
		return f.getHandleFn(activityID, runID)
	}

	return &fakeBatchActivityHandle{parent: f, id: activityID, runID: runID}, nil
}

// --- Standard test helpers ---

// makeArgsJSON marshals any struct into a json.RawMessage suitable
// for mcp.CallToolRequest.Params.Arguments. Returns an empty
// RawMessage when value is nil, which is the same shape the SDK gives
// for a "no arguments" request.
func makeArgsJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	if value == nil {
		return json.RawMessage(nil)
	}

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return data
}

// callBatchTool invokes handler with the supplied args and returns the
// decoded batchResponse. Helper so test bodies stay focused on the
// assertion rather than the boilerplate.
func callBatchTool(t *testing.T, handler mcp.ToolHandler, args any) (*mcp.CallToolResult, error) {
	t.Helper()

	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Arguments: makeArgsJSON(t, args),
		},
	}

	return handler(t.Context(), req)
}

// decodeBatchResult unmarshals the StructuredContent JSON
// CallToolResult fields back into a batchResponse.
func decodeBatchResult(t *testing.T, result *mcp.CallToolResult) batchResponse {
	t.Helper()
	require.NotNilf(t, result, "handler returned nil result")
	require.NotNilf(t, result.StructuredContent, "handler returned nil structured content")

	data, err := json.Marshal(result.StructuredContent)
	require.NoErrorf(t, err, "re-marshal structured content")

	var resp batchResponse
	require.NoErrorf(t, json.Unmarshal(data, &resp), "decode structured content")

	return resp
}

// --- clampConcurrency tests ---

// TestClampConcurrency exercises the per-PR claim from the spec body:
// <= 0 defaults to 50, > 100 clamps to 100, otherwise pass through.
func TestClampConcurrency(t *testing.T) {
	t.Parallel()

	t.Run("negative default", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, batchDefaultConcurrency, clampConcurrency(-1))
	})

	t.Run("zero default", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, batchDefaultConcurrency, clampConcurrency(0))
	})

	t.Run("pass through", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, 17, clampConcurrency(17))
	})

	t.Run("hard cap", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, batchHardCapConcurrency, clampConcurrency(1000))
	})

	t.Run("at cap", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, batchHardCapConcurrency, clampConcurrency(batchHardCapConcurrency))
	})
}

// --- clampLimit tests ---

// TestClampLimit exercises the per-PR claim from the spec body:
// <= 0 defaults to 100, otherwise pass through.
func TestClampLimit(t *testing.T) {
	t.Parallel()

	t.Run("negative default", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, batchDefaultLimit, clampLimit(-1))
	})

	t.Run("zero default", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, batchDefaultLimit, clampLimit(0))
	})

	t.Run("pass through", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, 42, clampLimit(42))
	})
}

// --- envelope / truncation tests ---

// TestEnvelope_NoErrors confirms the happy-path envelope: matched /
// succeeded / failed are emitted; errors/truncated/dropped stay
// absent. Empty errors slice must NOT round-trip through
// marshalToolResult as an empty array — omitempty drops it.
func TestEnvelope_NoErrors(t *testing.T) {
	t.Parallel()

	out := envelope(3, 3, 0, []batchErrorOut(nil))
	assert.Equal(t, 3, out.Matched)
	assert.Equal(t, 3, out.Succeeded)
	assert.Equal(t, 0, out.Failed)
	assert.Empty(t, out.Errors)
	assert.False(t, out.Truncated)
	assert.Equal(t, 0, out.Dropped)
}

// TestEnvelope_Truncation fills errors beyond batchMaxErrors; expects
// the first batchMaxErrors in errors[], truncated=true, dropped=N.
func TestEnvelope_Truncation(t *testing.T) {
	t.Parallel()

	errs := make([]batchErrorOut, 0, batchMaxErrors+15)
	for i := 0; i < batchMaxErrors+15; i++ {
		errs = append(errs, batchErrorOut{ID: fmt.Sprintf("id-%d", i), Error: "boom"})
	}

	out := envelope(70, 5, 65, errs)
	assert.Equal(t, 65, out.Failed)
	assert.Lenf(t, out.Errors, batchMaxErrors, "errors[] capped at batchMaxErrors")
	assert.True(t, out.Truncated)
	assert.Equalf(t, out.Dropped, len(errs)-batchMaxErrors,
		"dropped = total failures - cap")
	assert.Equal(t, 15, out.Dropped)
}

// TestEnvelope_WithinCap confirms the envelope below the cap: errors[]
// is left intact, truncated and dropped stay zero/false.
func TestEnvelope_WithinCap(t *testing.T) {
	t.Parallel()

	errs := []batchErrorOut{
		{ID: "a", Error: "x"},
		{ID: "b", Error: "y"},
	}

	out := envelope(5, 3, 2, errs)
	assert.False(t, out.Truncated)
	assert.Equal(t, 0, out.Dropped)
	assert.Equal(t, errs, out.Errors)
}

// --- batchExec tests ---

// TestBatchExec_Happy confirms the baseline: with no errors,
// failed = 0 and errs is nil; succeeded equals len(items).
func TestBatchExec_Happy(t *testing.T) {
	t.Parallel()

	items := []workflowItem{
		{WorkflowID: "w-1"},
		{WorkflowID: "w-2"},
		{WorkflowID: "w-3"},
	}

	succeeded, failed, errs := batchExec(
		t.Context(),
		noopLogger(),
		2,
		items,
		func(_ context.Context, _ workflowItem) error { return nil },
		workflowItemID,
	)

	assert.Equal(t, 3, succeeded)
	assert.Equal(t, 0, failed)
	assert.Empty(t, errs)
}

// TestBatchExec_PartialFailure confirms that one failing item
// produces a per-id entry in errs while the rest succeed. matched
// is computed by the caller (not by batchExec) — the test asserts
// the failed / succeeded split that batchExec itself returns.
func TestBatchExec_PartialFailure(t *testing.T) {
	t.Parallel()

	items := []workflowItem{
		{WorkflowID: "ok-1"},
		{WorkflowID: "bad-1"},
		{WorkflowID: "ok-2"},
	}

	succeeded, failed, errs := batchExec(
		t.Context(),
		noopLogger(),
		3,
		items,
		func(_ context.Context, item workflowItem) error {
			if item.WorkflowID == "bad-1" {
				return errors.New("synthetic failure")
			}

			return nil
		},
		workflowItemID,
	)

	assert.Equal(t, 2, succeeded)
	assert.Equal(t, 1, failed)
	require.Len(t, errs, 1)
	assert.Equal(t, "bad-1", errs[0].ID)
	assert.Equal(t, "synthetic failure", errs[0].Error)
}

// TestBatchExec_EmptyItems confirms the boundary: with no items the
// helper returns zeros and no errgroup work is scheduled.
func TestBatchExec_EmptyItems(t *testing.T) {
	t.Parallel()

	succeeded, failed, errs := batchExec(
		t.Context(),
		noopLogger(),
		10,
		nil,
		func(_ context.Context, _ workflowItem) error { return nil },
		workflowItemID,
	)

	assert.Equal(t, 0, succeeded)
	assert.Equal(t, 0, failed)
	assert.Empty(t, errs)
}

// --- per-tool tests: batch_signal ---

// TestBatchSignal_HappyPath drives a 2-item visibility query, both
// signal calls succeed.
func TestBatchSignal_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{}
	handler := handleBatchSignal(fake)

	result, err := callBatchTool(t, handler, batchSignalArgs{
		batchQueryArgs: batchQueryArgs{Query: `WorkflowType = "X"`},
		SignalName:     "tick",
		Args:           map[string]int{"n": 7},
	})

	require.NoError(t, err)

	resp := decodeBatchResult(t, result)
	assert.Equal(t, batchDefaultLimit, resp.Matched)
	assert.Equal(t, batchDefaultLimit, resp.Succeeded)
	assert.Equal(t, 0, resp.Failed)
	assert.Empty(t, resp.Errors)

	require.Len(t, fake.signalCalls, batchDefaultLimit)
	assert.Equal(t, "tick", fake.signalCalls[0].Name)
}

// TestBatchSignal_PartialFailure asserts the matched/succeeded/failed
// split when one op errors.
func TestBatchSignal_PartialFailure(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{
		signalFn: func(_ context.Context, _, runID, _ string, _ any) error {
			if runID == "run-3" {
				return errors.New("not_found")
			}

			return nil
		},
		listWFsFn: func(_ context.Context, _ string, limit int) ([]*workflowpb.WorkflowExecutionInfo, error) {
			out := make([]*workflowpb.WorkflowExecutionInfo, 0, limit)
			for i := 0; i < limit; i++ {
				out = append(out, &workflowpb.WorkflowExecutionInfo{
					Execution: &commonpb.WorkflowExecution{
						WorkflowId: fmt.Sprintf("w-%d", i),
						RunId:      fmt.Sprintf("run-%d", i),
					},
				})
			}

			return out, nil
		},
	}
	handler := handleBatchSignal(fake)

	result, err := callBatchTool(t, handler, batchSignalArgs{
		batchQueryArgs: batchQueryArgs{Query: "X", Limit: 5, Concurrency: 5},
		SignalName:     "ping",
	})

	require.NoError(t, err)

	resp := decodeBatchResult(t, result)
	assert.Equal(t, 5, resp.Matched)
	assert.Equal(t, 4, resp.Succeeded)
	assert.Equal(t, 1, resp.Failed)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, "w-3/run-3", resp.Errors[0].ID)
	assert.Equal(t, "not_found", resp.Errors[0].Error)
}

// TestBatchSignal_LimitEnforcement caps the returned matched count.
// The fake returns 50 items when asked for any limit; the handler
// passes its limit through unchanged. This asserts that the
// handler does not silently widen.
func TestBatchSignal_LimitEnforcement(t *testing.T) {
	t.Parallel()

	requestedLimit := 7

	fake := &fakeBatchClient{
		listWFsFn: func(_ context.Context, _ string, limit int) ([]*workflowpb.WorkflowExecutionInfo, error) {
			assert.Equalf(t, requestedLimit, limit,
				"handler must pass the user-supplied limit through to ListWorkflow")
			// Return exactly the limit items (zero beyond).
			out := make([]*workflowpb.WorkflowExecutionInfo, 0, limit)
			for i := 0; i < limit; i++ {
				out = append(out, &workflowpb.WorkflowExecutionInfo{
					Execution: &commonpb.WorkflowExecution{
						WorkflowId: fmt.Sprintf("w-%d", i),
						RunId:      fmt.Sprintf("run-%d", i),
					},
				})
			}

			return out, nil
		},
	}
	handler := handleBatchSignal(fake)

	result, err := callBatchTool(t, handler, batchSignalArgs{
		batchQueryArgs: batchQueryArgs{Query: "X", Limit: requestedLimit, Concurrency: 3},
		SignalName:     "tick",
	})

	require.NoError(t, err)

	resp := decodeBatchResult(t, result)
	assert.Equal(t, requestedLimit, resp.Matched)
	assert.Equal(t, requestedLimit, resp.Succeeded)
}

// TestBatchSignal_EmptyArgsPassesNil asserts that omitting args
// (omitting the field entirely from the JSON) results in a nil
// arg handed to the SDK call.
func TestBatchSignal_EmptyArgsPassesNil(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{}
	handler := handleBatchSignal(fake)

	result, err := callBatchTool(t, handler, batchSignalArgs{
		batchQueryArgs: batchQueryArgs{Query: "X", Limit: 1, Concurrency: 1},
		SignalName:     "tick",
		// Args omitted.
	})

	require.NoError(t, err)
	decodeBatchResult(t, result)

	require.Len(t, fake.signalCalls, 1)
	assert.Nilf(t, fake.signalCalls[0].Arg, "absent args → nil arg passed to SignalWorkflow")
}

// TestBatchSignal_RequiredFieldGaps confirms validation: missing
// query or missing signal_name produces a parse/validate error.
func TestBatchSignal_RequiredFieldGaps(t *testing.T) {
	t.Parallel()

	handler := handleBatchSignal(&fakeBatchClient{})

	t.Run("missing query", func(t *testing.T) {
		t.Parallel()

		_, err := callBatchTool(t, handler, batchSignalArgs{
			SignalName: "tick",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "query is required")
	})

	t.Run("missing signal_name", func(t *testing.T) {
		t.Parallel()

		_, err := callBatchTool(t, handler, batchSignalArgs{
			batchQueryArgs: batchQueryArgs{Query: "X"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "signal_name is required")
	})
}

// TestBatchSignal_DecodeError confirms that malformed JSON in
// Arguments surfaces as a wrapped parse error.
func TestBatchSignal_DecodeError(t *testing.T) {
	t.Parallel()

	handler := handleBatchSignal(&fakeBatchClient{})

	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Arguments: json.RawMessage(`{"query": 12}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporal tool")
}

// --- per-tool tests: batch_cancel ---

// TestBatchCancel_HappyPath: 2 items both canceled.
func TestBatchCancel_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{
		listWFsFn: func(_ context.Context, _ string, limit int) ([]*workflowpb.WorkflowExecutionInfo, error) {
			out := make([]*workflowpb.WorkflowExecutionInfo, 0, limit)
			for i := 0; i < 2; i++ {
				out = append(out, &workflowpb.WorkflowExecutionInfo{
					Execution: &commonpb.WorkflowExecution{WorkflowId: fmt.Sprintf("w-%d", i)},
				})
			}

			return out, nil
		},
	}
	handler := handleBatchCancel(fake)

	result, err := callBatchTool(t, handler, batchQueryArgs{Query: "X", Limit: 2, Concurrency: 2})

	require.NoError(t, err)

	resp := decodeBatchResult(t, result)
	assert.Equal(t, 2, resp.Matched)
	assert.Equal(t, 2, resp.Succeeded)
	assert.Equal(t, 0, resp.Failed)
	assert.Empty(t, resp.Errors)
	require.Len(t, fake.cancelCalls, 2)
}

// TestBatchCancel_MissingQuery confirms the query-required validation.
func TestBatchCancel_MissingQuery(t *testing.T) {
	t.Parallel()

	handler := handleBatchCancel(&fakeBatchClient{})

	_, err := callBatchTool(t, handler, batchQueryArgs{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query is required")
}

// --- per-tool tests: batch_terminate ---

// TestBatchTerminate_HappyPath: 2 items, both terminated.
func TestBatchTerminate_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{
		listWFsFn: func(_ context.Context, _ string, limit int) ([]*workflowpb.WorkflowExecutionInfo, error) {
			out := make([]*workflowpb.WorkflowExecutionInfo, 0, limit)
			for i := 0; i < 2; i++ {
				out = append(out, &workflowpb.WorkflowExecutionInfo{
					Execution: &commonpb.WorkflowExecution{WorkflowId: fmt.Sprintf("w-%d", i)},
				})
			}

			return out, nil
		},
	}
	handler := handleBatchTerminate(fake)

	result, err := callBatchTool(t, handler, batchTerminateArgs{
		batchQueryArgs: batchQueryArgs{Query: "X", Limit: 2, Concurrency: 2},
		Reason:         "test cleanup",
	})

	require.NoError(t, err)

	resp := decodeBatchResult(t, result)
	assert.Equal(t, 2, resp.Succeeded)
	assert.Equal(t, 0, resp.Failed)
	require.Len(t, fake.terminateCalls, 2)
	assert.Equal(t, "test cleanup", fake.terminateCalls[0].Name)
}

// TestBatchTerminate_ReasonRequired: missing reason fails fast.
func TestBatchTerminate_ReasonRequired(t *testing.T) {
	t.Parallel()

	handler := handleBatchTerminate(&fakeBatchClient{})

	_, err := callBatchTool(t, handler, batchTerminateArgs{
		batchQueryArgs: batchQueryArgs{Query: "X"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reason is required")
}

// TestBatchTerminate_DetailsMapForwarded: a non-nil, non-slice
// details payload is forwarded as a single-element slice to the SDK.
func TestBatchTerminate_DetailsMapForwarded(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{
		listWFsFn: func(_ context.Context, _ string, limit int) ([]*workflowpb.WorkflowExecutionInfo, error) {
			out := make([]*workflowpb.WorkflowExecutionInfo, 0, limit)
			for i := 0; i < limit; i++ {
				out = append(out, &workflowpb.WorkflowExecutionInfo{
					Execution: &commonpb.WorkflowExecution{WorkflowId: fmt.Sprintf("w-%d", i)},
				})
			}

			return out, nil
		},
	}
	handler := handleBatchTerminate(fake)

	result, err := callBatchTool(t, handler, batchTerminateArgs{
		batchQueryArgs: batchQueryArgs{Query: "X", Limit: 1, Concurrency: 1},
		Reason:         "housekeeping",
		Details:        map[string]string{"ticket": "TC-1"},
	})

	require.NoError(t, err)
	decodeBatchResult(t, result)

	require.Len(t, fake.terminateCalls, 1)
	details, ok := fake.terminateCalls[0].Arg.([]any)
	require.Truef(t, ok, "details forwarded as []any")
	require.Len(t, details, 1)
}

// TestBatchTerminate_ListError: an error from the visibility query
// surfaces as a wrapped error.
func TestBatchTerminate_ListError(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{
		listWFsFn: func(_ context.Context, _ string, _ int) ([]*workflowpb.WorkflowExecutionInfo, error) {
			return nil, errors.New("ns_not_found")
		},
	}
	handler := handleBatchTerminate(fake)

	_, err := callBatchTool(t, handler, batchTerminateArgs{
		batchQueryArgs: batchQueryArgs{Query: "X"},
		Reason:         "test",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list")
	assert.Contains(t, err.Error(), "ns_not_found")
}

// --- per-tool tests: batch_cancel_activities ---

// TestBatchCancelActivities_HappyPath: 2 activities, both canceled.
func TestBatchCancelActivities_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{
		listActsFn: func(_ context.Context, _ string, limit int) ([]*client.ActivityExecutionInfo, error) {
			out := make([]*client.ActivityExecutionInfo, 0, limit)
			for i := 0; i < 2; i++ {
				out = append(out, &client.ActivityExecutionInfo{
					ActivityID: fmt.Sprintf("a-%d", i),
				})
			}

			return out, nil
		},
	}
	handler := handleBatchCancelActivities(fake)

	result, err := callBatchTool(t, handler, batchCancelActivitiesArgs{
		batchQueryArgs: batchQueryArgs{Query: "actType = 'X'", Limit: 2, Concurrency: 2},
	})

	require.NoError(t, err)

	resp := decodeBatchResult(t, result)
	assert.Equal(t, 2, resp.Matched)
	assert.Equal(t, 2, resp.Succeeded)
	assert.Equal(t, 0, resp.Failed)
	require.Len(t, fake.cancelActCalls, 2)
}

// TestBatchCancelActivities_PartialFailure: handle.Cancel fails for
// one entry; the rest succeed.
func TestBatchCancelActivities_PartialFailure(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{
		listActsFn: func(_ context.Context, _ string, limit int) ([]*client.ActivityExecutionInfo, error) {
			out := make([]*client.ActivityExecutionInfo, 0, limit)
			for i := 0; i < 3; i++ {
				out = append(out, &client.ActivityExecutionInfo{
					ActivityID: fmt.Sprintf("a-%d", i),
				})
			}

			return out, nil
		},
	}
	handler := handleBatchCancelActivities(fake)

	// Override getHandleFn so one specific activity returns a Cancel
	// that always errors. The other two return a successful default
	// handle; their cancelActCalls still get recorded by the
	// parent-aware fake.
	fake.getHandleFn = func(actID, _ string) (client.ActivityHandle, error) {
		if actID == "a-1" {
			return &fakeBatchActivityHandle{
				parent:   fake,
				id:       actID,
				cancelFn: func(_ context.Context, _ client.CancelActivityOptions) error { return errors.New("already_completed") },
			}, nil
		}

		return &fakeBatchActivityHandle{parent: fake, id: actID}, nil
	}

	result, err := callBatchTool(t, handler, batchCancelActivitiesArgs{
		batchQueryArgs: batchQueryArgs{Query: "actType = 'X'", Limit: 3, Concurrency: 3},
	})

	require.NoError(t, err)

	resp := decodeBatchResult(t, result)
	assert.Equal(t, 3, resp.Matched)
	assert.Equal(t, 2, resp.Succeeded)
	assert.Equal(t, 1, resp.Failed)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, "a-1", resp.Errors[0].ID)
}

// TestBatchCancelActivities_MissingQuery: empty query fails fast.
func TestBatchCancelActivities_MissingQuery(t *testing.T) {
	t.Parallel()

	handler := handleBatchCancelActivities(&fakeBatchClient{})

	_, err := callBatchTool(t, handler, batchCancelActivitiesArgs{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query is required")
}

// --- per-tool tests: batch_terminate_activities ---

// TestBatchTerminateActivities_HappyPath: 2 activities, both terminated.
func TestBatchTerminateActivities_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{
		listActsFn: func(_ context.Context, _ string, limit int) ([]*client.ActivityExecutionInfo, error) {
			out := make([]*client.ActivityExecutionInfo, 0, limit)
			for i := 0; i < 2; i++ {
				out = append(out, &client.ActivityExecutionInfo{
					ActivityID: fmt.Sprintf("a-%d", i),
				})
			}

			return out, nil
		},
	}
	handler := handleBatchTerminateActivities(fake)

	result, err := callBatchTool(t, handler, batchTerminateActivitiesArgs{
		batchQueryArgs: batchQueryArgs{Query: "actType = 'X'", Limit: 2, Concurrency: 2},
		Reason:         "stopping all",
	})

	require.NoError(t, err)

	resp := decodeBatchResult(t, result)
	assert.Equal(t, 2, resp.Matched)
	assert.Equal(t, 2, resp.Succeeded)
	assert.Equal(t, 0, resp.Failed)
	require.Len(t, fake.termActCalls, 2)
}

// TestBatchTerminateActivities_ReasonRequired: missing reason fails fast.
func TestBatchTerminateActivities_ReasonRequired(t *testing.T) {
	t.Parallel()

	handler := handleBatchTerminateActivities(&fakeBatchClient{})

	_, err := callBatchTool(t, handler, batchTerminateActivitiesArgs{
		batchQueryArgs: batchQueryArgs{Query: "X"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reason is required")
}

// --- concurrency cap test ---

// TestBatchExec_ConcurrencyCap drives 50 items through
// batchExec with concurrency=4 and a 50ms per-op delay. Asserts
// that the in-flight peak never exceeds the cap.
//
// The fake bumps an inflight counter on entry, sleeps 50ms, then
// decrements. errgroup.SetLimit(4) means at most 4 goroutines run
// concurrently; the peak observed must be <= 4 even with 50 items.
func TestBatchExec_ConcurrencyCap(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{delay: 50 * time.Millisecond}

	const (
		numItems    = 50
		concurrency = 4
	)

	items := make([]workflowItem, numItems)
	for i := 0; i < numItems; i++ {
		items[i] = workflowItem{WorkflowID: fmt.Sprintf("w-%d", i)}
	}

	succeeded, failed, errs := batchExec(
		t.Context(),
		noopLogger(),
		concurrency,
		items,
		func(ctx context.Context, item workflowItem) error {
			return fake.batchSignal(ctx, item.WorkflowID, item.RunID, "tick", any(nil))
		},
		workflowItemID,
	)

	assert.Equal(t, numItems, succeeded)
	assert.Equal(t, 0, failed)
	assert.Empty(t, errs)

	// Peak reflects max simultaneous live ops.
	assert.LessOrEqualf(t, fake.peak.Load(), int32(concurrency),
		"in-flight goroutines must never exceed the concurrency cap")
	assert.GreaterOrEqualf(t, fake.peak.Load(), int32(1),
		"at least one op must have been in flight")
}

// TestBatchSignal_ConcurrencyCapEndToEnd exercises the same
// cap enforcement at the tool-handler level — the limit flows from
// the user's args through to errgroup.SetLimit.
func TestBatchSignal_ConcurrencyCapEndToEnd(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{
		delay: 30 * time.Millisecond,
		listWFsFn: func(_ context.Context, _ string, limit int) ([]*workflowpb.WorkflowExecutionInfo, error) {
			out := make([]*workflowpb.WorkflowExecutionInfo, 0, limit)
			for i := 0; i < limit; i++ {
				out = append(out, &workflowpb.WorkflowExecutionInfo{
					Execution: &commonpb.WorkflowExecution{WorkflowId: fmt.Sprintf("w-%d", i)},
				})
			}

			return out, nil
		},
	}
	handler := handleBatchSignal(fake)

	result, err := callBatchTool(t, handler, batchSignalArgs{
		batchQueryArgs: batchQueryArgs{Query: "X", Limit: 30, Concurrency: 5},
		SignalName:     "tick",
	})

	require.NoError(t, err)

	resp := decodeBatchResult(t, result)
	assert.Equal(t, 30, resp.Matched)
	assert.Equal(t, 30, resp.Succeeded)
	assert.Equal(t, 0, resp.Failed)
	assert.LessOrEqualf(t, fake.peak.Load(), int32(5),
		"handler must honor the user-supplied concurrency cap")
}

// --- limit-enforcement test (workflow side) ---

// TestBatchCancel_LimitEnforcement asserts that the per-PR limit
// enforcement goes through. fake.listWFsFn returns limit+ items so
// the handler is forced to apply its clamp.
func TestBatchCancel_LimitEnforcement(t *testing.T) {
	t.Parallel()

	const requested = 4

	fake := &fakeBatchClient{
		listWFsFn: func(_ context.Context, _ string, limit int) ([]*workflowpb.WorkflowExecutionInfo, error) {
			assert.Equalf(t, requested, limit,
				"handler must pass the user-supplied limit through")
			out := make([]*workflowpb.WorkflowExecutionInfo, 0, limit)
			for i := 0; i < limit; i++ {
				out = append(out, &workflowpb.WorkflowExecutionInfo{
					Execution: &commonpb.WorkflowExecution{WorkflowId: fmt.Sprintf("w-%d", i)},
				})
			}

			return out, nil
		},
	}
	handler := handleBatchCancel(fake)

	result, err := callBatchTool(
		t,
		handler,
		batchQueryArgs{Query: "X", Limit: requested, Concurrency: 2},
	)

	require.NoError(t, err)
	resp := decodeBatchResult(t, result)
	assert.Equal(t, requested, resp.Matched)
}

// --- error-truncation test ---

// TestBatchSignal_Truncation drives 60 signal calls that all fail;
// asserts errors[] has batchMaxErrors entries, truncated=true,
// dropped=10.
func TestBatchSignal_Truncation(t *testing.T) {
	t.Parallel()

	const numItems = 60

	fake := &fakeBatchClient{
		signalFn: func(_ context.Context, _, _, _ string, _ any) error {
			return errors.New("always_fail")
		},
		listWFsFn: func(_ context.Context, _ string, limit int) ([]*workflowpb.WorkflowExecutionInfo, error) {
			out := make([]*workflowpb.WorkflowExecutionInfo, 0, limit)
			for i := 0; i < limit; i++ {
				out = append(out, &workflowpb.WorkflowExecutionInfo{
					Execution: &commonpb.WorkflowExecution{WorkflowId: fmt.Sprintf("w-%d", i)},
				})
			}

			return out, nil
		},
	}
	handler := handleBatchSignal(fake)

	result, err := callBatchTool(t, handler, batchSignalArgs{
		batchQueryArgs: batchQueryArgs{Query: "X", Limit: numItems, Concurrency: 10},
		SignalName:     "tick",
	})

	require.NoError(t, err)
	resp := decodeBatchResult(t, result)
	assert.Equal(t, numItems, resp.Matched)
	assert.Equal(t, 0, resp.Succeeded)
	assert.Equal(t, numItems, resp.Failed)
	assert.Lenf(t, resp.Errors, batchMaxErrors, "errors[] capped at batchMaxErrors")
	assert.Truef(t, resp.Truncated, "truncated=true when errors exceed cap")
	assert.Equal(t, numItems-batchMaxErrors, resp.Dropped)
}

// --- decode-error test (additional per-tool) ---

// TestBatchCancelActivities_DecodeError: malformed JSON surfaces
// as a wrapped error.
func TestBatchCancelActivities_DecodeError(t *testing.T) {
	t.Parallel()

	handler := handleBatchCancelActivities(&fakeBatchClient{})

	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Arguments: json.RawMessage(`not-json`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

// TestBatchTerminate_DecodeError: ensure every tool's decode-error
// path returns the expected wrap.
func TestBatchTerminate_DecodeError(t *testing.T) {
	t.Parallel()

	handler := handleBatchTerminate(&fakeBatchClient{})

	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Arguments: json.RawMessage(`{"limit": "not-an-int"}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "batch_terminate")
}

// --- batchTools / registration test ---

// TestBatchTools_ReturnsAllFive asserts the registration order
// matches the spec body: batch_signal, batch_cancel, batch_terminate,
// batch_cancel_activities, batch_terminate_activities.
func TestBatchTools_ReturnsAllFive(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{}

	tools := batchTools(fake)

	require.Lenf(t, tools, 5, "five batch_* tools must be registered")
	want := []string{
		"batch_signal",
		"batch_cancel",
		"batch_terminate",
		"batch_cancel_activities",
		"batch_terminate_activities",
	}
	for i, name := range want {
		assert.Equalf(t, name, tools[i].Name,
			"tool #%d must be %s (registration order matters per spec)", i, name)
	}
}

// TestBatchTools_Annotations asserts the per-PR annotation rules:
// batch_signal — non-destructive + non-idempotent; batch_cancel /
// batch_terminate (and their activity counterparts) — destructive
// + idempotent. All five carry OpenWorldHint=true.
func TestBatchTools_Annotations(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{}
	tools := batchTools(fake)

	wantDestructive := map[string]bool{
		"batch_signal":               false,
		"batch_cancel":               true,
		"batch_terminate":            true,
		"batch_cancel_activities":    true,
		"batch_terminate_activities": true,
	}

	wantIdempotent := map[string]bool{
		"batch_signal":               false,
		"batch_cancel":               true,
		"batch_terminate":            true,
		"batch_cancel_activities":    true,
		"batch_terminate_activities": true,
	}

	for _, toolEntry := range tools {
		ann := toolEntry.Annotations
		require.NotNilf(t, ann, "%s must declare Annotations", toolEntry.Name)

		require.NotNilf(
			t,
			ann.DestructiveHint,
			"%s: DestructiveHint must not be nil",
			toolEntry.Name,
		)
		assert.Equalf(
			t,
			wantDestructive[toolEntry.Name],
			*ann.DestructiveHint,
			"%s: DestructiveHint=%v (expected %v)",
			toolEntry.Name,
			*ann.DestructiveHint,
			wantDestructive[toolEntry.Name],
		)

		assert.Equalf(
			t,
			wantIdempotent[toolEntry.Name],
			ann.IdempotentHint,
			"%s: IdempotentHint=%v (expected %v)",
			toolEntry.Name,
			ann.IdempotentHint,
			wantIdempotent[toolEntry.Name],
		)

		require.NotNilf(t, ann.OpenWorldHint, "%s: OpenWorldHint must not be nil", toolEntry.Name)
		assert.Truef(t, *ann.OpenWorldHint, "%s: OpenWorldHint must be true", toolEntry.Name)

		assert.Falsef(t, ann.ReadOnlyHint, "%s: ReadOnlyHint must be false", toolEntry.Name)
	}
}

// TestBatchTools_Preamble asserts every tool's description starts
// with the shared batchLoop preamble.
func TestBatchTools_Preamble(t *testing.T) {
	t.Parallel()

	fake := &fakeBatchClient{}
	for _, toolEntry := range batchTools(fake) {
		assert.Truef(
			t,
			len(toolEntry.Description) >= len(batchLoop) &&
				toolEntry.Description[:len(batchLoop)] == batchLoop,
			"%s description must start with the batchLoop preamble",
			toolEntry.Name,
		)
	}
}

// --- helpers ---

// Compile-time interface check: *fakeBatchClient must satisfy
// batchClient. Catches drift if a method is added to batchClient
// that the fake hasn't mirrored.
var _ batchClient = (*fakeBatchClient)(nil)
