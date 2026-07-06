// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:exhaustruct,lll,wsl_v5 // fakeWorkflowClient mirrors the production interface; SDK fixture types force long lines; test fixtures cluster assertions
package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdkclient "go.temporal.io/sdk/client"
)

// -----------------------------------------------------------------------
// Test seam: fakeWorkflowClient
// -----------------------------------------------------------------------

// fakeWorkflowClient satisfies the temporalClient interface by routing
// every method through an optional function field. Tests set only the
// fields they care about; the rest fall through to defaults (zero
// values + nil errors). The pattern mirrors woodpecker's httptest
// server in spirit but lives in-process so the tests can run without
// the network.
type fakeWorkflowClient struct {
	closeFn func()

	scheduleClientFn func() sdkclient.ScheduleClient

	startWorkflowFn          startWorkflowFnType
	cancelWorkflowFn         func(ctx context.Context, workflowID, runID string) error
	terminateWorkflowFn      terminateWorkflowFnType
	getWorkflowFn            func(ctx context.Context, workflowID, runID string) sdkclient.WorkflowRun
	describeWorkflowFn       describeWorkflowFnType
	listWorkflowFn           listWorkflowFnType
	getWorkflowHistoryFn     getWorkflowHistoryFnType
	workflowSignalWorkflowFn workflowSignalWorkflowFnType
}

// Per-method function types used by fakeWorkflowClient. Hoisted as
// named types so the fakeWorkflowClient struct fields stay under the
// lll line-length limit and the SDK signatures don't trip revive's
// argument-limit rule on every field declaration.
type (
	startWorkflowFnType = func(
		ctx context.Context,
		opts sdkclient.StartWorkflowOptions,
		workflowName string,
		args ...any,
	) (sdkclient.WorkflowRun, error)

	terminateWorkflowFnType = func(
		ctx context.Context,
		workflowID, runID, reason string,
		details ...any,
	) error

	describeWorkflowFnType = func(
		ctx context.Context,
		workflowID, runID string,
	) (*sdkclient.WorkflowExecutionDescription, error)

	listWorkflowFnType = func(
		ctx context.Context,
		request *workflowservice.ListWorkflowExecutionsRequest,
	) (*workflowservice.ListWorkflowExecutionsResponse, error)

	getWorkflowHistoryFnType = func(
		ctx context.Context,
		workflowID, runID string,
		isLongPoll bool,
		filterType enums.HistoryEventFilterType,
	) sdkclient.HistoryEventIterator

	workflowSignalWorkflowFnType = func(
		ctx context.Context,
		workflowID, runID, signalName string,
		arg any,
	) error
)

func (f *fakeWorkflowClient) Close() {
	if f.closeFn != nil {
		f.closeFn()
	}
}

func (f *fakeWorkflowClient) ScheduleClient() sdkclient.ScheduleClient {
	if f.scheduleClientFn != nil {
		return f.scheduleClientFn()
	}

	return nil
}

//nolint:gocritic // SDK signature; StartWorkflowOptions is the SDK's heavy struct.
func (f *fakeWorkflowClient) StartWorkflow(
	ctx context.Context,
	opts sdkclient.StartWorkflowOptions,
	workflowName string,
	args ...any,
) (sdkclient.WorkflowRun, error) {
	if f.startWorkflowFn == nil {
		return nil, errors.New("fakeWorkflowClient: startWorkflowFn not set")
	}

	return f.startWorkflowFn(ctx, opts, workflowName, args...)
}

func (f *fakeWorkflowClient) CancelWorkflow(ctx context.Context, workflowID, runID string) error {
	if f.cancelWorkflowFn == nil {
		return nil
	}

	return f.cancelWorkflowFn(ctx, workflowID, runID)
}

//nolint:revive // SDK signature; argument count is fixed by the upstream interface.
func (f *fakeWorkflowClient) TerminateWorkflow(
	ctx context.Context, workflowID, runID, reason string, details ...any,
) error {
	if f.terminateWorkflowFn == nil {
		return nil
	}

	return f.terminateWorkflowFn(ctx, workflowID, runID, reason, details...)
}

func (f *fakeWorkflowClient) GetWorkflow(
	ctx context.Context, workflowID, runID string,
) sdkclient.WorkflowRun {
	if f.getWorkflowFn == nil {
		return &fakeWorkflowRun{}
	}

	return f.getWorkflowFn(ctx, workflowID, runID)
}

func (f *fakeWorkflowClient) DescribeWorkflow(
	ctx context.Context, workflowID, runID string,
) (*sdkclient.WorkflowExecutionDescription, error) {
	if f.describeWorkflowFn == nil {
		return nil, errors.New("fakeWorkflowClient: describeWorkflowFn not set")
	}

	return f.describeWorkflowFn(ctx, workflowID, runID)
}

func (f *fakeWorkflowClient) ListWorkflow(
	ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest,
) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	if f.listWorkflowFn == nil {
		return &workflowservice.ListWorkflowExecutionsResponse{}, nil
	}

	return f.listWorkflowFn(ctx, request)
}

//nolint:revive // SDK signature; argument count is fixed by the upstream interface.
func (f *fakeWorkflowClient) GetWorkflowHistory(
	ctx context.Context,
	workflowID, runID string,
	isLongPoll bool,
	filterType enums.HistoryEventFilterType,
) sdkclient.HistoryEventIterator {
	if f.getWorkflowHistoryFn == nil {
		return &fakeHistoryIter{}
	}

	return f.getWorkflowHistoryFn(ctx, workflowID, runID, isLongPoll, filterType)
}

//nolint:revive // SDK signature; argument count is fixed by the upstream interface.
func (f *fakeWorkflowClient) SignalWorkflow(
	ctx context.Context, workflowID, runID, signalName string, arg any,
) error {
	if f.workflowSignalWorkflowFn == nil {
		return nil
	}

	return f.workflowSignalWorkflowFn(ctx, workflowID, runID, signalName, arg)
}

// =====================================================================
// Activity method stubs
//
// fakeWorkflowClient exists to drive the workflow handlers, which
// (post-integration) take a `temporalClient` interface that includes
// the activity methods. Workflow tests never call the activity
// methods, so we add panic stubs that satisfy the interface without
// growing the fake's surface. The panic surfaces any accidental
// call as a loud test failure rather than silent zero-return.
// =====================================================================

//nolint:gocritic,lll // SDK signature is heavy; tests must mirror it.
func (*fakeWorkflowClient) ExecuteActivity(
	_ context.Context, _ sdkclient.StartActivityOptions, _ any, _ ...any,
) (sdkclient.ActivityHandle, error) {
	panic("fakeWorkflowClient.ExecuteActivity: not implemented")
}

func (*fakeWorkflowClient) GetActivityHandle(
	_ sdkclient.GetActivityHandleOptions,
) sdkclient.ActivityHandle {
	panic("fakeWorkflowClient.GetActivityHandle: not implemented")
}

func (*fakeWorkflowClient) ListActivities(
	_ context.Context, _ sdkclient.ListActivitiesOptions,
) (sdkclient.ListActivitiesResult, error) {
	panic("fakeWorkflowClient.ListActivities: not implemented")
}

func (*fakeWorkflowClient) CountActivities(
	_ context.Context, _ sdkclient.CountActivitiesOptions,
) (*sdkclient.CountActivitiesResult, error) {
	panic("fakeWorkflowClient.CountActivities: not implemented")
}

// fakeWorkflowRun satisfies sdkclient.WorkflowRun. GetID/GetRunID return
// the configured values; Get fills the caller's valuePtr with the
// configured result via *any (tests always pass *any to Get).
type fakeWorkflowRun struct {
	id     string
	runID  string
	result any
	getFn  func(ctx context.Context, valuePtr any) error
}

func (f *fakeWorkflowRun) GetID() string    { return f.id }
func (f *fakeWorkflowRun) GetRunID() string { return f.runID }

func (f *fakeWorkflowRun) Get(ctx context.Context, valuePtr any) error {
	if f.getFn != nil {
		return f.getFn(ctx, valuePtr)
	}

	if valuePtr == nil {
		return nil
	}

	if p, ok := valuePtr.(*any); ok {
		*p = f.result
	}

	return nil
}

// GetWithOptions is part of sdkclient.WorkflowRun but is not exercised
// by the workflow handler factories. The default returns nil so the
// interface stays satisfied.
func (*fakeWorkflowRun) GetWithOptions(
	_ context.Context,
	_ any,
	_ sdkclient.WorkflowRunGetOptions,
) error {
	return nil
}

// fakeHistoryIter satisfies sdkclient.HistoryEventIterator by
// returning events one-by-one from a configured slice.
type fakeHistoryIter struct {
	events  []*history.HistoryEvent
	idx     int
	nextErr error
}

func (f *fakeHistoryIter) HasNext() bool { return f.idx < len(f.events) }

func (f *fakeHistoryIter) Next() (*history.HistoryEvent, error) {
	if f.nextErr != nil {
		return nil, f.nextErr
	}

	if f.idx >= len(f.events) {
		return nil, errors.New("fakeHistoryIter: no more events")
	}

	event := f.events[f.idx]
	f.idx++

	return event, nil
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// extractWorkflowTextContent returns the TextContent payload from a
// CallToolResult, failing the test if the result has no text content.
// Mirrors the helper in woodpecker/woodpecker_test.go.
func extractWorkflowTextContent(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	require.NotEmptyf(t, result.Content, "CallToolResult has no content")

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected TextContent, got %T", result.Content[0])

	return textContent.Text
}

// callWorkflowHandler drives a single tool's handler directly with the given
// fake client. Used in per-tool tests below.
func callWorkflowHandler(
	t *testing.T,
	handler mcp.ToolHandler,
	args json.RawMessage,
) (*mcp.CallToolResult, error) {
	t.Helper()

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta{},
			Name:      "test",
			Arguments: args,
		},
		Extra: (*mcp.RequestExtra)(nil),
	}

	return handler(t.Context(), req)
}

// makeTimestamp builds a *timestamppb.Timestamp suitable for inclusion
// in fake history event fixtures.
func makeTimestamp(seconds int64) *timestamppb.Timestamp {
	return &timestamppb.Timestamp{Seconds: seconds}
}

// workflowExecutionInfoFromJSON unmarshals a JSON payload into a
// *workflow.WorkflowExecutionInfo. Going through protojson (instead of
// a struct literal) keeps the test free of exhaustruct complaints about
// the proto type's embedded internal fields.
func workflowExecutionInfoFromJSON(t *testing.T, payload string) *workflow.WorkflowExecutionInfo {
	t.Helper()

	var info workflow.WorkflowExecutionInfo
	require.NoErrorf(t, protojson.Unmarshal([]byte(payload), &info),
		"unmarshal WorkflowExecutionInfo fixture: %s", payload)

	return &info
}

// historyEventFromJSON unmarshals a JSON payload into a
// *history.HistoryEvent. Same protojson rationale as
// workflowExecutionInfoFromJSON.
func historyEventFromJSON(t *testing.T, payload string) *history.HistoryEvent {
	t.Helper()

	var event history.HistoryEvent
	require.NoErrorf(t, protojson.Unmarshal([]byte(payload), &event),
		"unmarshal HistoryEvent fixture: %s", payload)

	return &event
}

// workflowExecutionDescriptionFromJSON unmarshals a JSON payload into
// a *sdkclient.WorkflowExecutionDescription. The SDK's WorkflowExecution
// and WorkflowType types live in the internal package and are not
// re-exported as named types — going through JSON keeps test bodies
// free of exhaustruct complaints about the embedded internal fields.
// The payload uses the SDK's exported field names (workflowExecution,
// workflowType, status, workflowStartTime, workflowCloseTime) so the
// JSON keys match the visible Go field names.
func workflowExecutionDescriptionFromJSON(
	t *testing.T, payload string,
) *sdkclient.WorkflowExecutionDescription {
	t.Helper()

	// The SDK's WorkflowExecutionDescription embeds WorkflowExecutionMetadata
	// which uses time.Time for the timestamps (not *timestamppb.Timestamp).
	// encoding/json unmarshals time.Time from RFC3339 strings, so
	// standard JSON works here.
	var desc sdkclient.WorkflowExecutionDescription
	require.NoErrorf(t, json.Unmarshal([]byte(payload), &desc),
		"unmarshal WorkflowExecutionDescription fixture: %s", payload)

	return &desc
}

// -----------------------------------------------------------------------
// Connect: tool inventory + annotations
// -----------------------------------------------------------------------

// TestConnect_RegistersEightWorkflowTools confirms that Connect
// registers the eight workflow tools with the documented
// annotations. After the temporal-integration PR landed, Connect
// returns all thirty tools; this test now filters the registered
// tools down to the workflow set and asserts each is wired
// correctly.
func TestConnect_RegistersEightWorkflowTools(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), make(map[string]any))
	require.NoError(t, err)

	expected := map[string]struct {
		readOnly bool
		destroy  *bool // new(true) ⇒ DestructiveHint true; new(false) ⇒ false; nil ⇒ don't assert
		idem     bool
	}{
		"start_workflow":       {readOnly: false, destroy: new(false), idem: false},
		"cancel_workflow":      {readOnly: false, destroy: new(true), idem: true},
		"terminate_workflow":   {readOnly: false, destroy: new(true), idem: true},
		"get_workflow_result":  {readOnly: true, destroy: nil, idem: false},
		"describe_workflow":    {readOnly: true, destroy: nil, idem: false},
		"list_workflows":       {readOnly: true, destroy: nil, idem: false},
		"get_workflow_history": {readOnly: true, destroy: nil, idem: false},
		"continue_as_new":      {readOnly: false, destroy: new(false), idem: false},
	}

	for _, entry := range resp.Tools {
		// Filter to workflow-only assertions.
		if _, isWorkflow := expected[entry.Name]; !isWorkflow {
			continue
		}

		require.NotNilf(t, entry.Tool, "tool has nil *mcp.Tool: %s", entry.Name)
		require.NotNilf(t, entry.Handler, "tool has nil handler: %s", entry.Name)
		require.NotEmptyf(t, entry.Description, "tool has empty description: %s", entry.Name)
		require.NotEmptyf(t, entry.InputSchema, "tool has nil input schema: %s", entry.Name)
		require.NotEmptyf(t, entry.OutputSchema, "tool has nil output schema: %s", entry.Name)
		require.NotNilf(t, entry.Annotations, "tool has nil annotations: %s", entry.Name)
		require.NotNilf(t, entry.Annotations.OpenWorldHint, "OpenWorldHint nil for %s", entry.Name)
		assert.Truef(t, *entry.Annotations.OpenWorldHint,
			"OpenWorldHint should be true for %s", entry.Name)

		// Every tool's description must start with the workflow loop
		// preamble so the model sees the canonical sequence regardless
		// of which tool it discovers first.
		assert.Containsf(t, entry.Description, "Temporal workflow investigation loop",
			"description missing workflow preamble: %s", entry.Name)

		exp, ok := expected[entry.Name]
		require.Truef(t, ok, "unexpected tool name: %s", entry.Name)
		assert.Equalf(t, exp.readOnly, entry.Annotations.ReadOnlyHint,
			"ReadOnlyHint mismatch for %s", entry.Name)
		assert.Equalf(t, exp.idem, entry.Annotations.IdempotentHint,
			"IdempotentHint mismatch for %s", entry.Name)
		if exp.destroy != nil {
			require.NotNilf(t, entry.Annotations.DestructiveHint,
				"DestructiveHint nil but expected %v for %s", *exp.destroy, entry.Name)
			assert.Equalf(t, *exp.destroy, *entry.Annotations.DestructiveHint,
				"DestructiveHint mismatch for %s", entry.Name)
		}

		delete(expected, entry.Name)
	}

	assert.Emptyf(t, expected, "missing workflow tools: %v", expected)
}

// TestConnect_ToolRegistrationOrder confirms the workflow tools
// are registered in the documented order within Connect's tool
// list. Connect returns all thirty tools across five feature
// groups; the workflow subset occupies a contiguous segment of
// the registration order. We assert each registered workflow
// tool's position matches the documented order, allowing
// schedule, activity, query-signal, and batch tools to occupy
// the indices before / after the workflow block.
func TestConnect_ToolRegistrationOrder(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), make(map[string]any))
	require.NoError(t, err)

	want := []string{
		"start_workflow",
		"cancel_workflow",
		"terminate_workflow",
		"get_workflow_result",
		"describe_workflow",
		"list_workflows",
		"get_workflow_history",
		"continue_as_new",
	}

	wantIdx := 0
	for _, entry := range resp.Tools {
		if wantIdx >= len(want) {
			break
		}

		if entry.Name == want[wantIdx] {
			wantIdx++
		}
	}

	assert.Equalf(t, len(want), wantIdx,
		"workflow tools missing or out of order: registered seq did not contain %v in order",
		want)
}

// -----------------------------------------------------------------------
// start_workflow
// -----------------------------------------------------------------------

// TestHandler_StartWorkflow_HappyPath confirms that a complete argument
// set reaches the SDK and the resulting run handle's id + run_id are
// echoed in the tool output.
func TestHandler_StartWorkflow_HappyPath(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{
		startWorkflowFn: func(
			_ context.Context, opts sdkclient.StartWorkflowOptions, workflowName string, args ...any,
		) (sdkclient.WorkflowRun, error) {
			assert.Equal(t, "GreetingWorkflow", workflowName)
			assert.Equal(t, "wf-123", opts.ID)
			assert.Equal(t, "default", opts.TaskQueue)
			require.Lenf(t, args, 2, "expected two positional args, got %v", args)
			assert.Equal(t, "Alice", args[0])
			assert.EqualValues(t, 42, args[1])

			return &fakeWorkflowRun{id: "wf-123", runID: "run-456"}, nil
		},
	}

	result, err := callWorkflowHandler(t, handleStartWorkflow(client),
		json.RawMessage(`{
			"workflow_name": "GreetingWorkflow",
			"workflow_id":   "wf-123",
			"task_queue":    "default",
			"args":          ["Alice", 42]
		}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	text := extractWorkflowTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	assert.Equal(t, "wf-123", got["workflow_id"])
	assert.Equal(t, "run-456", got["run_id"])
	assert.Equal(t, "started", got["status"])
}

// TestHandler_StartWorkflow_MissingTaskQueue confirms that an empty
// task_queue triggers errTaskQueueRequired.
func TestHandler_StartWorkflow_MissingTaskQueue(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{}
	_, err := callWorkflowHandler(t, handleStartWorkflow(client), json.RawMessage(`{
		"workflow_name": "GreetingWorkflow",
		"workflow_id":   "wf-123"
	}`))
	require.ErrorIs(t, err, errTaskQueueRequired)
}

// TestHandler_StartWorkflow_MissingWorkflowName confirms that an empty
// workflow_name triggers errWorkflowNameRequired.
func TestHandler_StartWorkflow_MissingWorkflowName(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{}
	_, err := callWorkflowHandler(t, handleStartWorkflow(client), json.RawMessage(`{
		"workflow_id": "wf-123",
		"task_queue":  "default"
	}`))
	require.ErrorIs(t, err, errWorkflowNameRequired)
}

// TestHandler_StartWorkflow_MissingWorkflowID confirms that an empty
// workflow_id triggers errWorkflowIDRequired.
func TestHandler_StartWorkflow_MissingWorkflowID(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{}
	_, err := callWorkflowHandler(t, handleStartWorkflow(client), json.RawMessage(`{
		"workflow_name": "GreetingWorkflow",
		"task_queue":    "default"
	}`))
	require.ErrorIs(t, err, errWorkflowIDRequired)
}

// TestHandler_StartWorkflow_ArgsDecodeError confirms that a malformed
// args array surfaces a parse error rather than silently passing an
// empty slice to the SDK.
func TestHandler_StartWorkflow_ArgsDecodeError(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{}
	_, err := callWorkflowHandler(t, handleStartWorkflow(client), json.RawMessage(`{
		"workflow_name": "GreetingWorkflow",
		"workflow_id":   "wf-123",
		"task_queue":    "default",
		"args":          "not-an-array"
	}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse args")
}

// TestHandler_StartWorkflow_SDKError confirms that an error from the
// SDK is wrapped with the tool name.
func TestHandler_StartWorkflow_SDKError(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("WorkflowExecutionAlreadyStarted")
	client := &fakeWorkflowClient{
		startWorkflowFn: func(
			_ context.Context, _ sdkclient.StartWorkflowOptions, _ string, _ ...any,
		) (sdkclient.WorkflowRun, error) {
			return nil, sdkErr
		},
	}

	_, err := callWorkflowHandler(t, handleStartWorkflow(client), json.RawMessage(`{
		"workflow_name": "GreetingWorkflow",
		"workflow_id":   "wf-123",
		"task_queue":    "default"
	}`))
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
	assert.Containsf(t, err.Error(), "start_workflow",
		"SDK error should be wrapped with the tool name")
}

// TestHandler_StartWorkflow_NoArgsField confirms that omitting args
// entirely is equivalent to passing no args (variadic with zero entries).
func TestHandler_StartWorkflow_NoArgsField(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{
		startWorkflowFn: func(
			_ context.Context, _ sdkclient.StartWorkflowOptions, _ string, args ...any,
		) (sdkclient.WorkflowRun, error) {
			assert.Emptyf(t, args, "no args field → empty variadic")

			return &fakeWorkflowRun{id: "wf-x", runID: "run-y"}, nil
		},
	}

	_, err := callWorkflowHandler(t, handleStartWorkflow(client), json.RawMessage(`{
		"workflow_name": "GreetingWorkflow",
		"workflow_id":   "wf-123",
		"task_queue":    "default"
	}`))
	require.NoError(t, err)
}

// -----------------------------------------------------------------------
// cancel_workflow
// -----------------------------------------------------------------------

// TestHandler_CancelWorkflow_HappyPath confirms the SDK call is made
// with the supplied ids and the response envelope reports success.
func TestHandler_CancelWorkflow_HappyPath(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{
		cancelWorkflowFn: func(_ context.Context, workflowID, runID string) error {
			assert.Equal(t, "wf-123", workflowID)
			assert.Equal(t, "run-456", runID)

			return nil
		},
	}

	result, err := callWorkflowHandler(t, handleCancelWorkflow(client), json.RawMessage(`{
		"workflow_id": "wf-123",
		"run_id":      "run-456"
	}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	text := extractWorkflowTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	assert.Equal(t, true, got["canceled"])
	assert.Equal(t, "wf-123", got["workflow_id"])
	assert.Equal(t, "run-456", got["run_id"])
}

// TestHandler_CancelWorkflow_MissingWorkflowID confirms the
// errWorkflowIDRequired branch.
func TestHandler_CancelWorkflow_MissingWorkflowID(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{}
	_, err := callWorkflowHandler(t, handleCancelWorkflow(client), json.RawMessage(`{}`))
	require.ErrorIs(t, err, errWorkflowIDRequired)
}

// TestHandler_CancelWorkflow_SDKError confirms that a non-nil error
// from CancelWorkflow is wrapped with the tool name.
func TestHandler_CancelWorkflow_SDKError(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("WorkflowNotFound")
	client := &fakeWorkflowClient{
		cancelWorkflowFn: func(_ context.Context, _, _ string) error { return sdkErr },
	}

	_, err := callWorkflowHandler(t, handleCancelWorkflow(client),
		json.RawMessage(`{"workflow_id": "wf-missing"}`))
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
	assert.Contains(t, err.Error(), "cancel_workflow")
}

// -----------------------------------------------------------------------
// terminate_workflow
// -----------------------------------------------------------------------

// TestHandler_TerminateWorkflow_HappyPath confirms the SDK call is made
// with the supplied ids + reason and the response echoes them.
func TestHandler_TerminateWorkflow_HappyPath(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{
		terminateWorkflowFn: func(
			_ context.Context, workflowID, runID, reason string, details ...any,
		) error {
			assert.Equal(t, "wf-123", workflowID)
			assert.Equal(t, "run-456", runID)
			assert.Equal(t, "user requested", reason)
			require.Len(t, details, 1)

			return nil
		},
	}

	result, err := callWorkflowHandler(t, handleTerminateWorkflow(client), json.RawMessage(`{
		"workflow_id": "wf-123",
		"run_id":      "run-456",
		"reason":      "user requested",
		"details":     [{"hint": "stuck"}]
	}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	text := extractWorkflowTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	assert.Equal(t, true, got["terminated"])
	assert.Equal(t, "user requested", got["reason"])
}

// TestHandler_TerminateWorkflow_DefaultReason confirms that omitting
// the reason field produces defaultTerminateReason.
func TestHandler_TerminateWorkflow_DefaultReason(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{
		terminateWorkflowFn: func(
			_ context.Context, _, _, reason string, _ ...any,
		) error {
			assert.Equal(t, defaultTerminateReason, reason)

			return nil
		},
	}

	_, err := callWorkflowHandler(t, handleTerminateWorkflow(client),
		json.RawMessage(`{"workflow_id": "wf-123"}`))
	require.NoError(t, err)
}

// TestHandler_TerminateWorkflow_MissingWorkflowID confirms the
// errWorkflowIDRequired branch.
func TestHandler_TerminateWorkflow_MissingWorkflowID(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{}
	_, err := callWorkflowHandler(t, handleTerminateWorkflow(client), json.RawMessage(`{}`))
	require.ErrorIs(t, err, errWorkflowIDRequired)
}

// TestHandler_TerminateWorkflow_DetailsDecodeError confirms that a
// malformed details array surfaces a parse error.
func TestHandler_TerminateWorkflow_DetailsDecodeError(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{}
	_, err := callWorkflowHandler(t, handleTerminateWorkflow(client), json.RawMessage(`{
		"workflow_id": "wf-123",
		"details":     "not-an-array"
	}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse args")
}

// TestHandler_TerminateWorkflow_SDKError confirms that a non-nil error
// from TerminateWorkflow is wrapped with the tool name.
func TestHandler_TerminateWorkflow_SDKError(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("PermissionDenied")
	client := &fakeWorkflowClient{
		terminateWorkflowFn: func(_ context.Context, _, _, _ string, _ ...any) error {
			return sdkErr
		},
	}

	_, err := callWorkflowHandler(t, handleTerminateWorkflow(client),
		json.RawMessage(`{"workflow_id": "wf-123"}`))
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
	assert.Contains(t, err.Error(), "terminate_workflow")
}

// -----------------------------------------------------------------------
// get_workflow_result
// -----------------------------------------------------------------------

// TestHandler_GetWorkflowResult_HappyPath confirms that the result is
// passed through to the tool output and the run_id is echoed.
func TestHandler_GetWorkflowResult_HappyPath(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{
		getWorkflowFn: func(_ context.Context, workflowID, runID string) sdkclient.WorkflowRun {
			assert.Equal(t, "wf-123", workflowID)
			assert.Equal(t, "run-456", runID)

			return &fakeWorkflowRun{id: "wf-123", runID: "run-456", result: "ok"}
		},
	}

	result, err := callWorkflowHandler(t, handleGetWorkflowResult(client), json.RawMessage(`{
		"workflow_id": "wf-123",
		"run_id":      "run-456"
	}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	text := extractWorkflowTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	assert.Equal(t, "wf-123", got["workflow_id"])
	assert.Equal(t, "run-456", got["run_id"])
	assert.Equalf(t, "ok", got["result"], "result should round-trip through Get")
}

// TestHandler_GetWorkflowResult_MissingWorkflowID confirms the
// errWorkflowIDRequired branch.
func TestHandler_GetWorkflowResult_MissingWorkflowID(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{}
	_, err := callWorkflowHandler(t, handleGetWorkflowResult(client), json.RawMessage(`{}`))
	require.ErrorIs(t, err, errWorkflowIDRequired)
}

// TestHandler_GetWorkflowResult_SDKError confirms that an error from
// Get is wrapped with the tool name.
func TestHandler_GetWorkflowResult_SDKError(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("workflow still running")
	client := &fakeWorkflowClient{
		getWorkflowFn: func(_ context.Context, _, _ string) sdkclient.WorkflowRun {
			return &fakeWorkflowRun{getFn: func(_ context.Context, _ any) error { return sdkErr }}
		},
	}

	_, err := callWorkflowHandler(t, handleGetWorkflowResult(client),
		json.RawMessage(`{"workflow_id": "wf-123"}`))
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
	assert.Contains(t, err.Error(), "get_workflow_result")
}

// -----------------------------------------------------------------------
// describe_workflow
// -----------------------------------------------------------------------

// TestHandler_DescribeWorkflow_HappyPath confirms that the SDK's
// WorkflowExecutionMetadata fields are flattened into the documented
// JSON shape.
func TestHandler_DescribeWorkflow_HappyPath(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{
		describeWorkflowFn: func(_ context.Context, workflowID, runID string) (*sdkclient.WorkflowExecutionDescription, error) {
			assert.Equal(t, "wf-123", workflowID)
			assert.Equal(t, "run-456", runID)

			return workflowExecutionDescriptionFromJSON(t, `{
				"workflowExecution": {"id": "wf-123", "runId": "run-456"},
				"workflowType":      {"name": "GreetingWorkflow"},
				"status":            2,
				"workflowStartTime": "2026-07-05T12:00:00Z",
				"workflowCloseTime": "2026-07-05T12:05:00Z"
			}`), nil
		},
	}

	_ = time.Time{}

	result, err := callWorkflowHandler(t, handleDescribeWorkflow(client), json.RawMessage(`{
		"workflow_id": "wf-123",
		"run_id":      "run-456"
	}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	text := extractWorkflowTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	assert.Equal(t, "wf-123", got["workflow_id"])
	assert.Equal(t, "run-456", got["run_id"])
	assert.Equal(t, "GreetingWorkflow", got["workflow_type"])
	assert.Equalf(t, "Completed", got["status"],
		"status should use the Temporal enum's PascalCase String() form")
	assert.NotNil(t, got["start_time"])
	assert.NotNil(t, got["close_time"])
}

// TestHandler_DescribeWorkflow_OpenStatus confirms that an OPEN
// workflow description omits close_time (the SDK returns nil for the
// close timestamp).
func TestHandler_DescribeWorkflow_OpenStatus(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{
		describeWorkflowFn: func(_ context.Context, _, _ string) (*sdkclient.WorkflowExecutionDescription, error) {
			return workflowExecutionDescriptionFromJSON(t, `{
				"workflowExecution": {"id": "wf-123", "runId": "run-1"},
				"workflowType":      {"name": "GreetingWorkflow"},
				"status":            1,
				"workflowStartTime": "2026-07-05T12:00:00Z"
			}`), nil
		},
	}

	result, err := callWorkflowHandler(t, handleDescribeWorkflow(client),
		json.RawMessage(`{"workflow_id": "wf-123"}`))
	require.NoError(t, err)

	text := extractWorkflowTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	assert.Equal(t, "Running", got["status"])
	_, hasClose := got["close_time"]
	assert.Falsef(t, hasClose, "close_time should be absent for an open workflow")
}

// TestHandler_DescribeWorkflow_MissingWorkflowID confirms the
// errWorkflowIDRequired branch.
func TestHandler_DescribeWorkflow_MissingWorkflowID(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{}
	_, err := callWorkflowHandler(t, handleDescribeWorkflow(client), json.RawMessage(`{}`))
	require.ErrorIs(t, err, errWorkflowIDRequired)
}

// TestHandler_DescribeWorkflow_SDKError confirms that a non-nil error
// from DescribeWorkflow is wrapped with the tool name.
func TestHandler_DescribeWorkflow_SDKError(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("NotFound")
	client := &fakeWorkflowClient{
		describeWorkflowFn: func(_ context.Context, _, _ string) (*sdkclient.WorkflowExecutionDescription, error) {
			return nil, sdkErr
		},
	}

	_, err := callWorkflowHandler(t, handleDescribeWorkflow(client),
		json.RawMessage(`{"workflow_id": "wf-missing"}`))
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
	assert.Contains(t, err.Error(), "describe_workflow")
}

// -----------------------------------------------------------------------
// list_workflows
// -----------------------------------------------------------------------

// TestHandler_ListWorkflows_HappyPath confirms that the SDK request is
// populated correctly and the response is flattened.
func TestHandler_ListWorkflows_HappyPath(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{
		listWorkflowFn: func(_ context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			assert.Equal(t, `WorkflowType="GreetingWorkflow"`, request.Query)
			assert.Equal(t, int32(50), request.PageSize)

			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions: []*workflow.WorkflowExecutionInfo{
					workflowExecutionInfoFromJSON(t, `{
						"execution": {"workflow_id": "wf-1", "run_id": "run-1"},
						"type":      {"name": "GreetingWorkflow"},
						"status":    2,
						"startTime": "1970-01-01T00:16:40Z",
						"closeTime": "1970-01-01T00:17:40Z"
					}`),
				},
				NextPageToken: []byte("next-page"),
			}, nil
		},
	}

	result, err := callWorkflowHandler(t, handleListWorkflows(client), json.RawMessage(`{
		"query":     "WorkflowType=\"GreetingWorkflow\"",
		"page_size": 50
	}`))
	require.NoError(t, err)
	require.NotNil(t, result)

	text := extractWorkflowTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	executions, ok := got["executions"].([]any)
	require.True(t, ok)
	require.Len(t, executions, 1)

	first := executions[0].(map[string]any)
	assert.Equal(t, "wf-1", first["workflow_id"])
	assert.Equal(t, "run-1", first["run_id"])
	assert.Equal(t, "GreetingWorkflow", first["workflow_type"])
	assert.Equalf(t, "Completed", first["status"],
		"status should use the Temporal enum's PascalCase String() form")

	token, hasToken := got["next_page_token"]
	require.Truef(t, hasToken, "next_page_token should be present")
	assert.Equalf(t, "bmV4dC1wYWdl", token,
		"next_page_token should be base64-encoded in the JSON output")
}

// TestHandler_ListWorkflows_DefaultPageSize confirms that omitting
// page_size results in defaultListWorkflowsPageSize being passed to the
// SDK.
func TestHandler_ListWorkflows_DefaultPageSize(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{
		listWorkflowFn: func(_ context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			assert.Equal(t, int32(defaultListWorkflowsPageSize), request.PageSize)

			return &workflowservice.ListWorkflowExecutionsResponse{}, nil
		},
	}

	_, err := callWorkflowHandler(t, handleListWorkflows(client), json.RawMessage(`{}`))
	require.NoError(t, err)
}

// TestHandler_ListWorkflows_SDKError confirms that a non-nil error from
// ListWorkflow is wrapped with the tool name.
func TestHandler_ListWorkflows_SDKError(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("Visibility store unavailable")
	client := &fakeWorkflowClient{
		listWorkflowFn: func(_ context.Context, _ *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
			return nil, sdkErr
		},
	}

	_, err := callWorkflowHandler(t, handleListWorkflows(client), json.RawMessage(`{}`))
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
	assert.Contains(t, err.Error(), "list_workflows")
}

// -----------------------------------------------------------------------
// get_workflow_history
// -----------------------------------------------------------------------

// TestHandler_GetWorkflowHistory_HappyPath confirms that history events
// are drained from the iterator and flattened into the documented JSON
// shape.
func TestHandler_GetWorkflowHistory_HappyPath(t *testing.T) {
	t.Parallel()

	events := []*history.HistoryEvent{
		historyEventFromJSON(t, `{
			"event_id":   1,
			"event_type": 1,
			"event_time": "1970-01-01T00:16:40Z"
		}`),
		historyEventFromJSON(t, `{
			"event_id":   2,
			"event_type": 2,
			"event_time": "1970-01-01T00:17:40Z"
		}`),
	}

	client := &fakeWorkflowClient{
		getWorkflowHistoryFn: func(_ context.Context, workflowID, runID string, _ bool, filterType enums.HistoryEventFilterType) sdkclient.HistoryEventIterator {
			assert.Equal(t, "wf-123", workflowID)
			assert.Equal(t, "run-456", runID)
			assert.Equal(t, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT, filterType)

			return &fakeHistoryIter{events: events}
		},
	}

	result, err := callWorkflowHandler(t, handleGetWorkflowHistory(client), json.RawMessage(`{
		"workflow_id": "wf-123",
		"run_id":      "run-456"
	}`))
	require.NoError(t, err)
	require.NotNil(t, result)

	text := extractWorkflowTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	eventList, ok := got["events"].([]any)
	require.True(t, ok)
	require.Len(t, eventList, 2)

	first := eventList[0].(map[string]any)
	assert.EqualValues(t, 1, first["event_id"])
	assert.Equal(t, enums.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED.String(), first["event_type"])

	assert.Falsef(t, got["truncated"].(bool),
		"truncated should be false when the iterator is exhausted")
}

// TestHandler_GetWorkflowHistory_DefaultMaxEvents confirms that
// omitting max_events uses defaultHistoryMaxEvents as the upper bound.
func TestHandler_GetWorkflowHistory_DefaultMaxEvents(t *testing.T) {
	t.Parallel()

	// Build more events than the default so truncated=true fires.
	events := make([]*history.HistoryEvent, 0, defaultHistoryMaxEvents+1)
	for i := range defaultHistoryMaxEvents + 1 {
		events = append(events, &history.HistoryEvent{
			EventId:   int64(i + 1),
			EventType: enums.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
			EventTime: makeTimestamp(int64(i)),
		})
	}

	client := &fakeWorkflowClient{
		getWorkflowHistoryFn: func(_ context.Context, _, _ string, _ bool, _ enums.HistoryEventFilterType) sdkclient.HistoryEventIterator {
			return &fakeHistoryIter{events: events}
		},
	}

	result, err := callWorkflowHandler(t, handleGetWorkflowHistory(client),
		json.RawMessage(`{"workflow_id": "wf-123"}`))
	require.NoError(t, err)

	text := extractWorkflowTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	eventList := got["events"].([]any)
	assert.Lenf(t, eventList, defaultHistoryMaxEvents,
		"events array should be capped at the default max_events")
	assert.Truef(t, got["truncated"].(bool),
		"truncated should be true when there are more events")
}

// TestHandler_GetWorkflowHistory_MissingWorkflowID confirms the
// errWorkflowIDRequired branch.
func TestHandler_GetWorkflowHistory_MissingWorkflowID(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{}
	_, err := callWorkflowHandler(t, handleGetWorkflowHistory(client), json.RawMessage(`{}`))
	require.ErrorIs(t, err, errWorkflowIDRequired)
}

// TestHandler_GetWorkflowHistory_NextError confirms that an iterator
// Next() error is wrapped with the tool name.
func TestHandler_GetWorkflowHistory_NextError(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("connection reset")
	client := &fakeWorkflowClient{
		getWorkflowHistoryFn: func(_ context.Context, _, _ string, _ bool, _ enums.HistoryEventFilterType) sdkclient.HistoryEventIterator {
			// events has one entry so HasNext() returns true and
			// Next() is called — which then returns nextErr.
			return &fakeHistoryIter{
				events:  []*history.HistoryEvent{{EventId: 1}},
				nextErr: sdkErr,
			}
		},
	}

	_, err := callWorkflowHandler(t, handleGetWorkflowHistory(client),
		json.RawMessage(`{"workflow_id": "wf-123"}`))
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
	assert.Contains(t, err.Error(), "get_workflow_history")
}

// -----------------------------------------------------------------------
// continue_as_new
// -----------------------------------------------------------------------

// TestHandler_ContinueAsNew_HappyPath confirms that the SDK receives a
// SignalWorkflow call with the supplied signal_name and the response
// envelope reports success.
func TestHandler_ContinueAsNew_HappyPath(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{
		workflowSignalWorkflowFn: func(_ context.Context, workflowID, runID, signalName string, arg any) error {
			assert.Equal(t, "wf-123", workflowID)
			assert.Emptyf(t, runID, "continue_as_new always targets the latest run via empty runID")
			assert.Equal(t, "continue", signalName)
			require.NotNil(t, arg)

			return nil
		},
	}

	result, err := callWorkflowHandler(t, handleContinueAsNew(client), json.RawMessage(`{
		"workflow_id": "wf-123",
		"signal_name": "continue"
	}`))
	require.NoError(t, err)
	require.NotNil(t, result)

	text := extractWorkflowTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	assert.Equal(t, true, got["signaled"])
	assert.Equal(t, "wf-123", got["workflow_id"])
	assert.Equal(t, "continue", got["signal_name"])
}

// TestHandler_ContinueAsNew_MissingWorkflowID confirms the
// errWorkflowIDRequired branch.
func TestHandler_ContinueAsNew_MissingWorkflowID(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{}
	_, err := callWorkflowHandler(t, handleContinueAsNew(client),
		json.RawMessage(`{"signal_name": "continue"}`))
	require.ErrorIs(t, err, errWorkflowIDRequired)
}

// TestHandler_ContinueAsNew_MissingSignalName confirms the
// errWorkflowSignalNameRequired branch.
func TestHandler_ContinueAsNew_MissingSignalName(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{}
	_, err := callWorkflowHandler(t, handleContinueAsNew(client),
		json.RawMessage(`{"workflow_id": "wf-123"}`))
	require.ErrorIs(t, err, errWorkflowSignalNameRequired)
}

// TestHandler_ContinueAsNew_SignalArgsDecodeError confirms that a
// malformed signal_args array surfaces a parse error.
func TestHandler_ContinueAsNew_SignalArgsDecodeError(t *testing.T) {
	t.Parallel()

	client := &fakeWorkflowClient{}
	_, err := callWorkflowHandler(t, handleContinueAsNew(client), json.RawMessage(`{
		"workflow_id":  "wf-123",
		"signal_name":  "continue",
		"signal_args":  "not-an-array"
	}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse args")
}

// TestHandler_ContinueAsNew_SDKError confirms that a non-nil error
// from SignalWorkflow is wrapped with the tool name.
func TestHandler_ContinueAsNew_SDKError(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("Server timeout")
	client := &fakeWorkflowClient{
		workflowSignalWorkflowFn: func(_ context.Context, _, _, _ string, _ any) error { return sdkErr },
	}

	_, err := callWorkflowHandler(t, handleContinueAsNew(client), json.RawMessage(`{
		"workflow_id": "wf-123",
		"signal_name": "continue"
	}`))
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
	assert.Contains(t, err.Error(), "continue_as_new")
}

// -----------------------------------------------------------------------
// Arg decoding edge cases
// -----------------------------------------------------------------------

// TestDecodeArgsSlice_NilInput confirms that empty/nil JSON RawMessage
// produces an empty slice with no error. This is the path used by
// start_workflow when args is omitted.
func TestDecodeArgsSlice_NilInput(t *testing.T) {
	t.Parallel()

	out, err := decodeArgsSlice(json.RawMessage(nil))
	require.NoError(t, err)
	assert.Empty(t, out)

	out, err = decodeArgsSlice(json.RawMessage("null"))
	require.NoError(t, err)
	assert.Empty(t, out)
}

// TestDecodeArgsSlice_NonArray confirms that a JSON value that is not an
// array produces a parse error rather than a silent empty slice.
func TestDecodeArgsSlice_NonArray(t *testing.T) {
	t.Parallel()

	_, err := decodeArgsSlice(json.RawMessage(`"foo"`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse args")
}

// -----------------------------------------------------------------------
// executionSummary edge cases
// -----------------------------------------------------------------------

// TestExecutionSummary_NilInfo confirms that a nil WorkflowExecutionInfo
// produces a nil summary rather than panicking.
func TestExecutionSummary_NilInfo(t *testing.T) {
	t.Parallel()

	summary := executionSummary((*workflow.WorkflowExecutionInfo)(nil))
	assert.Nilf(t, summary, "nil info must produce a nil summary")
}

// TestExecutionSummary_FullRow confirms the documented field mapping
// for a populated WorkflowExecutionInfo.
func TestExecutionSummary_FullRow(t *testing.T) {
	t.Parallel()

	row := executionSummary(workflowExecutionInfoFromJSON(t, `{
		"execution": {"workflow_id": "wf-a", "run_id": "run-a"},
		"type":      {"name": "Foo"},
		"status":    1,
		"startTime": "1970-01-01T00:00:01Z"
	}`))

	assert.Equal(t, "wf-a", row["workflow_id"])
	assert.Equal(t, "run-a", row["run_id"])
	assert.Equal(t, "Foo", row["workflow_type"])
	assert.Equalf(t, "Running", row["status"],
		"status should use the Temporal enum's PascalCase String() form")
	assert.NotNil(t, row["start_time"])
	_, hasClose := row["close_time"]
	assert.Falsef(t, hasClose, "close_time should be absent when nil")
}

// -----------------------------------------------------------------------
// historyEventSummary edge cases
// -----------------------------------------------------------------------

// TestHistoryEventSummary_NilEvent confirms that a nil HistoryEvent
// produces a nil summary rather than panicking.
func TestHistoryEventSummary_NilEvent(t *testing.T) {
	t.Parallel()

	summary := historyEventSummary((*history.HistoryEvent)(nil))
	assert.Nilf(t, summary, "nil event must produce a nil summary")
}
