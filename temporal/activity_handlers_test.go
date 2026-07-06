// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:exhaustruct,revive,wsl_v5 // test fixtures use partial structs and cluster assertions
package temporal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test doubles ---

// fakeActivityClient is the test double for temporalClient. Each
// method records the args it was called with and returns the
// configured response (or a configured error). Unset response fields
// fall back to zero values so the happy-path tests can leave them
// uninitialized.
type fakeActivityClient struct {
	// executeActivity response
	execActivityHandle client.ActivityHandle
	execActivityErr    error
	lastExecOpts       client.StartActivityOptions
	lastExecActivity   any
	lastExecArgs       []any

	// getActivityHandle response — overrides execActivityHandle when set
	activityHandle client.ActivityHandle

	// listActivities response
	listResult client.ListActivitiesResult
	listErr    error

	// countActivities response
	countResult *client.CountActivitiesResult
	countErr    error
}

func (fake *fakeActivityClient) Close() {}

func (fake *fakeActivityClient) ScheduleClient() client.ScheduleClient { return nil }

//nolint:gocritic // SDK signature requires value; test fake mirrors it.
func (fake *fakeActivityClient) ExecuteActivity(
	_ context.Context,
	opts client.StartActivityOptions,
	activity any,
	args ...any,
) (client.ActivityHandle, error) {
	fake.lastExecOpts = opts
	fake.lastExecActivity = activity
	fake.lastExecArgs = args

	if fake.execActivityErr != nil {
		return nil, fake.execActivityErr
	}

	return fake.execActivityHandle, nil
}

func (fake *fakeActivityClient) GetActivityHandle(
	_ client.GetActivityHandleOptions,
) client.ActivityHandle {
	return fake.activityHandle
}

func (fake *fakeActivityClient) ListActivities(
	_ context.Context,
	_ client.ListActivitiesOptions,
) (client.ListActivitiesResult, error) {
	return fake.listResult, fake.listErr
}

func (fake *fakeActivityClient) CountActivities(
	_ context.Context,
	_ client.CountActivitiesOptions,
) (*client.CountActivitiesResult, error) {
	if fake.countErr != nil {
		return nil, fake.countErr
	}

	return fake.countResult, nil
}

// =====================================================================
// Workflow + query/signal method stubs
//
// fakeActivityClient exists to drive the activity handlers, which
// (post-integration) take a `temporalClient` interface that includes
// workflow + query/signal methods. The activity tests never invoke
// those methods, so we add no-op stubs that satisfy the interface
// without growing the fake's behavioral surface.
//
// All stubs panic if called — calling them is a test bug, not a
// production code path. The panic is loud so the failing assertion
// surfaces during `go test` rather than producing a confusing
// zero-value.
// =====================================================================

//nolint:gocritic // SDK signature is heavy; tests must mirror it.
func (fake *fakeActivityClient) StartWorkflow(
	_ context.Context, _ client.StartWorkflowOptions, _ string, _ ...any,
) (client.WorkflowRun, error) {
	panic("fakeActivityClient.StartWorkflow: not implemented")
}

func (fake *fakeActivityClient) CancelWorkflow(_ context.Context, _, _ string) error {
	panic("fakeActivityClient.CancelWorkflow: not implemented")
}

func (fake *fakeActivityClient) TerminateWorkflow(
	_ context.Context, _, _, _ string, _ ...any,
) error {
	panic("fakeActivityClient.TerminateWorkflow: not implemented")
}

func (fake *fakeActivityClient) GetWorkflow(_ context.Context, _, _ string) client.WorkflowRun {
	panic("fakeActivityClient.GetWorkflow: not implemented")
}

func (fake *fakeActivityClient) DescribeWorkflow(
	_ context.Context, _, _ string,
) (*client.WorkflowExecutionDescription, error) {
	panic("fakeActivityClient.DescribeWorkflow: not implemented")
}

func (fake *fakeActivityClient) ListWorkflow(
	_ context.Context, _ *workflowservice.ListWorkflowExecutionsRequest,
) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	panic("fakeActivityClient.ListWorkflow: not implemented")
}

func (fake *fakeActivityClient) GetWorkflowHistory(
	_ context.Context, _, _ string, _ bool, _ enums.HistoryEventFilterType,
) client.HistoryEventIterator {
	panic("fakeActivityClient.GetWorkflowHistory: not implemented")
}

func (fake *fakeActivityClient) SignalWorkflow(
	_ context.Context, _, _, _ string, _ any,
) error {
	panic("fakeActivityClient.SignalWorkflow: not implemented")
}

// fakeActivityHandle is the test double for activityHandle. The
// `fake` receiver name is intentionally short so the body lines stay
// under the lll line-length cap.
type fakeActivityHandle struct {
	id    string
	runID string

	getErr      error
	describeR   *client.ActivityExecutionDescription
	describeErr error
	cancelErr   error
	termErr     error

	lastDescribeOpts client.DescribeActivityOptions
	lastCancelOpts   client.CancelActivityOptions
	lastTermOpts     client.TerminateActivityOptions

	gotResult *json.RawMessage
}

func (fake *fakeActivityHandle) GetID() string    { return fake.id }
func (fake *fakeActivityHandle) GetRunID() string { return fake.runID }

func (fake *fakeActivityHandle) Get(_ context.Context, valuePtr any) error {
	if fake.getErr != nil {
		return fake.getErr
	}

	if valuePtr == nil {
		return nil
	}

	// Mirror the JSON round-trip the SDK would perform: the caller
	// passed a *json.RawMessage; we write the configured gotResult
	// (or null) into it.
	raw, ok := valuePtr.(*json.RawMessage)
	if !ok {
		return nil
	}

	if fake.gotResult != nil {
		*raw = *fake.gotResult
	} else {
		*raw = json.RawMessage("null")
	}

	return nil
}

func (fake *fakeActivityHandle) Describe(
	_ context.Context,
	options client.DescribeActivityOptions,
) (*client.ActivityExecutionDescription, error) {
	fake.lastDescribeOpts = options

	if fake.describeErr != nil {
		return nil, fake.describeErr
	}

	return fake.describeR, nil
}

func (fake *fakeActivityHandle) Cancel(
	_ context.Context,
	options client.CancelActivityOptions,
) error {
	fake.lastCancelOpts = options

	return fake.cancelErr
}

func (fake *fakeActivityHandle) Terminate(
	_ context.Context,
	options client.TerminateActivityOptions,
) error {
	fake.lastTermOpts = options

	return fake.termErr
}

// activityHandleFromFake returns a client.ActivityHandle from a
// fakeActivityHandle so it satisfies the SDK interface (which has six
// methods, four of which the fake implements directly).
func activityHandleFromFake(handle *fakeActivityHandle) client.ActivityHandle {
	return handle
}

// --- Helpers ---

// callActivityHandler invokes the named handler factory with the
// supplied fake client.
func callActivityHandler(
	t *testing.T,
	factory func(temporalClient) mcp.ToolHandler,
	cli *fakeActivityClient,
	args json.RawMessage,
) (*mcp.CallToolResult, error) {
	t.Helper()

	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta{},
			Name:      "test",
			Arguments: args,
		},
	}

	return factory(cli)(t.Context(), req)
}

// extractActivityText unmarshals the first TextContent from a
// CallToolResult and returns its decoded JSON value.
func extractActivityText(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()

	require.NotNil(t, result)
	require.NotEmpty(t, result.Content)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected *mcp.TextContent, got %T", result.Content[0])

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &got))

	return got
}

// --- start_activity ---

func TestHandler_StartActivity_HappyPath(t *testing.T) {
	t.Parallel()

	handle := &fakeActivityHandle{id: "act-1", runID: "run-1"}
	fake := &fakeActivityClient{execActivityHandle: handle}

	result, err := callActivityHandler(t, handleStartActivity, fake, json.RawMessage(`{
		"activity": "EchoGreeting",
		"activity_id": "act-1",
		"task_queue": "default",
		"args": {"name": "world"}
	}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Falsef(t, result.IsError, "happy path must not be an error result")

	got := extractActivityText(t, result)
	assert.Equalf(t, "act-1", got["activity_id"], "echoes activity_id")
	assert.Equalf(t, "run-1", got["run_id"], "echoes run_id from handle")
	assert.Equalf(t, "started", got["status"], "status is 'started' (no await)")

	assert.Equalf(t, "EchoGreeting", fake.lastExecActivity, "activity type name forwarded")
	assert.Equalf(t, "act-1", fake.lastExecOpts.ID, "ID forwarded to SDK")
	assert.Equalf(t, "default", fake.lastExecOpts.TaskQueue, "TaskQueue forwarded to SDK")
	assert.Equalf(t, defaultActivityStartToClose, fake.lastExecOpts.StartToCloseTimeout,
		"start_to_close defaults to %s", defaultActivityStartToClose)
	require.Lenf(t, fake.lastExecArgs, 1, "single object arg → single-element positional slice")
	assert.Equalf(t, map[string]any{"name": "world"}, fake.lastExecArgs[0],
		"object args wrapped as a single positional arg")
}

func TestHandler_StartActivity_ExplicitTimeout(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{execActivityHandle: &fakeActivityHandle{}}

	_, err := callActivityHandler(t, handleStartActivity, fake, json.RawMessage(`{
		"activity": "EchoGreeting",
		"activity_id": "act-1",
		"task_queue": "default",
		"start_to_close_timeout_seconds": 120
	}`))
	require.NoError(t, err)
	assert.Equalf(
		t,
		120*time.Second,
		fake.lastExecOpts.StartToCloseTimeout,
		"explicit timeout applied",
	)
}

func TestHandler_StartActivity_InvalidTimeout(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{execActivityHandle: &fakeActivityHandle{}}

	_, err := callActivityHandler(t, handleStartActivity, fake, json.RawMessage(`{
		"activity": "EchoGreeting",
		"activity_id": "act-1",
		"task_queue": "default",
		"start_to_close_timeout_seconds": 0
	}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, errStartToCloseTimeoutInvalid)
}

func TestHandler_StartActivity_MissingActivityID(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{}

	_, err := callActivityHandler(t, handleStartActivity, fake, json.RawMessage(`{
		"activity": "EchoGreeting",
		"task_queue": "default"
	}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, errActivityIDRequired)
}

func TestHandler_StartActivity_MissingActivityName(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{}

	_, err := callActivityHandler(t, handleStartActivity, fake, json.RawMessage(`{
		"activity_id": "act-1",
		"task_queue": "default"
	}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, errActivityNameRequired)
}

func TestHandler_StartActivity_MissingTaskQueue(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{}

	_, err := callActivityHandler(t, handleStartActivity, fake, json.RawMessage(`{
		"activity": "EchoGreeting",
		"activity_id": "act-1"
	}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, errActivityTaskQueueRequired)
}

func TestHandler_StartActivity_DecodeError(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{}

	_, err := callActivityHandler(t, handleStartActivity, fake, json.RawMessage(`not json`))
	require.Error(t, err)
	assert.Containsf(t, err.Error(), "parse start_activity args",
		"decode error wrapped with tool prefix")
}

func TestHandler_StartActivity_SDKErrorPropagated(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("temporal: namespace not found")
	fake := &fakeActivityClient{execActivityErr: sdkErr}

	_, err := callActivityHandler(t, handleStartActivity, fake, json.RawMessage(`{
		"activity": "EchoGreeting",
		"activity_id": "act-1",
		"task_queue": "default"
	}`))
	require.Error(t, err)
	require.ErrorIsf(t, err, sdkErr, "SDK error surfaces through to the handler")
	assert.Containsf(t, err.Error(), "start_activity: execute:",
		"wrapped with start_activity prefix")
}

func TestHandler_StartActivity_ArrayArgsForwardedPositionally(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{execActivityHandle: &fakeActivityHandle{}}

	_, err := callActivityHandler(t, handleStartActivity, fake, json.RawMessage(`{
		"activity": "Sum",
		"activity_id": "act-1",
		"task_queue": "default",
		"args": [1, 2, 3]
	}`))
	require.NoError(t, err)
	assert.Equalf(t, []any{float64(1), float64(2), float64(3)}, fake.lastExecArgs,
		"array args forwarded positionally")
}

// --- execute_activity ---

func TestHandler_ExecuteActivity_HappyPath(t *testing.T) {
	t.Parallel()

	greeting := json.RawMessage(`"hello"`)
	handle := &fakeActivityHandle{id: "act-2", runID: "run-2", gotResult: &greeting}
	fake := &fakeActivityClient{execActivityHandle: handle}

	result, err := callActivityHandler(t, handleExecuteActivity, fake, json.RawMessage(`{
		"activity": "EchoGreeting",
		"activity_id": "act-2",
		"task_queue": "default"
	}`))
	require.NoError(t, err)
	require.NotNil(t, result)

	got := extractActivityText(t, result)
	assert.Equal(t, "act-2", got["activity_id"])
	assert.Equalf(t, "completed", got["status"], "execute_activity awaits the result")
	assert.Equalf(t, "hello", got["result"], "result decoded from the handle")
}

func TestHandler_ExecuteActivity_NilResult(t *testing.T) {
	t.Parallel()

	// gotResult is nil → handler should marshal result as JSON null.
	handle := &fakeActivityHandle{id: "act-2", runID: "run-2"}
	fake := &fakeActivityClient{execActivityHandle: handle}

	result, err := callActivityHandler(t, handleExecuteActivity, fake, json.RawMessage(`{
		"activity": "Noop",
		"activity_id": "act-2",
		"task_queue": "default"
	}`))
	require.NoError(t, err)

	got := extractActivityText(t, result)
	assert.Nilf(t, got["result"], "nil result marshaled as JSON null → nil in decoded map")
}

func TestHandler_ExecuteActivity_GetErrorPropagated(t *testing.T) {
	t.Parallel()

	getErr := errors.New("temporal: poll activity: deadline exceeded")
	handle := &fakeActivityHandle{getErr: getErr}
	fake := &fakeActivityClient{execActivityHandle: handle}

	_, err := callActivityHandler(t, handleExecuteActivity, fake, json.RawMessage(`{
		"activity": "EchoGreeting",
		"activity_id": "act-2",
		"task_queue": "default"
	}`))
	require.Error(t, err)
	require.ErrorIs(t, err, getErr)
	assert.Containsf(t, err.Error(), "execute_activity: await result:",
		"wrapped with await-result prefix")
}

func TestHandler_ExecuteActivity_MissingActivityID(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{execActivityHandle: &fakeActivityHandle{}}

	_, err := callActivityHandler(t, handleExecuteActivity, fake, json.RawMessage(`{
		"activity": "EchoGreeting",
		"task_queue": "default"
	}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, errActivityIDRequired)
}

func TestHandler_ExecuteActivity_SDKErrorPropagated(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("temporal: activity already running")
	fake := &fakeActivityClient{execActivityErr: sdkErr}

	_, err := callActivityHandler(t, handleExecuteActivity, fake, json.RawMessage(`{
		"activity": "EchoGreeting",
		"activity_id": "act-2",
		"task_queue": "default"
	}`))
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
}

func TestHandler_ExecuteActivity_DecodeError(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{}

	_, err := callActivityHandler(t, handleExecuteActivity, fake, json.RawMessage(`{not json}`))
	require.Error(t, err)
	assert.Containsf(t, err.Error(), "parse execute_activity args",
		"decode error wrapped with tool prefix")
}

// --- get_activity_result ---

func TestHandler_GetActivityResult_HappyPath(t *testing.T) {
	t.Parallel()

	payload := json.RawMessage(`{"answer": 42}`)
	handle := &fakeActivityHandle{id: "act-3", runID: "run-3", gotResult: &payload}
	fake := &fakeActivityClient{activityHandle: activityHandleFromFake(handle)}

	result, err := callActivityHandler(t, handleGetActivityResult, fake, json.RawMessage(`{
		"activity_id": "act-3"
	}`))
	require.NoError(t, err)

	got := extractActivityText(t, result)
	assert.Equal(t, "act-3", got["activity_id"])
	assert.Equalf(t, map[string]any{"answer": float64(42)}, got["result"],
		"result decoded as JSON object")
}

func TestHandler_GetActivityResult_MissingActivityID(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{activityHandle: activityHandleFromFake(&fakeActivityHandle{})}

	_, err := callActivityHandler(t, handleGetActivityResult, fake, json.RawMessage(`{}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, errActivityIDRequired)
}

func TestHandler_GetActivityResult_InvalidTimeout(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{activityHandle: activityHandleFromFake(&fakeActivityHandle{})}

	_, err := callActivityHandler(t, handleGetActivityResult, fake, json.RawMessage(`{
		"activity_id": "act-3",
		"timeout_seconds": 0
	}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, errTimeoutInvalid)
}

func TestHandler_GetActivityResult_GetErrorPropagated(t *testing.T) {
	t.Parallel()

	getErr := errors.New("temporal: poll activity: not found")
	handle := &fakeActivityHandle{getErr: getErr}
	fake := &fakeActivityClient{activityHandle: activityHandleFromFake(handle)}

	_, err := callActivityHandler(t, handleGetActivityResult, fake, json.RawMessage(`{
		"activity_id": "act-3"
	}`))
	require.Error(t, err)
	require.ErrorIs(t, err, getErr)
}

func TestHandler_GetActivityResult_DecodeError(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{}

	_, err := callActivityHandler(t, handleGetActivityResult, fake, json.RawMessage(`not json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse get_activity_result args")
}

// --- describe_activity ---

func TestHandler_DescribeActivity_HappyPath(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	closedAt := now.Add(2 * time.Second)

	desc := &client.ActivityExecutionDescription{
		ClientActivityExecutionInfo: client.ActivityExecutionInfo{
			ActivityID:        "act-4",
			ActivityRunID:     "run-4",
			ActivityType:      "EchoGreeting",
			TaskQueue:         "default",
			Status:            enums.ACTIVITY_EXECUTION_STATUS_COMPLETED,
			ScheduleTime:      now.Add(-time.Second),
			CloseTime:         closedAt,
			ExecutionDuration: 2 * time.Second,
		},
		LastStartedTime:    now,
		Attempt:            1,
		LastWorkerIdentity: "worker-7@host",
	}
	handle := &fakeActivityHandle{describeR: desc}
	fake := &fakeActivityClient{activityHandle: activityHandleFromFake(handle)}

	result, err := callActivityHandler(t, handleDescribeActivity, fake, json.RawMessage(`{
		"activity_id": "act-4"
	}`))
	require.NoError(t, err)

	got := extractActivityText(t, result)
	assert.Equal(t, "act-4", got["activity_id"])
	assert.Equal(t, "run-4", got["run_id"])
	assert.Equal(t, "EchoGreeting", got["activity_type"])
	assert.Equal(t, "default", got["task_queue"])
	assert.Equal(t, "COMPLETED", got["status"])
	assert.InDelta(t, float64(enums.ACTIVITY_EXECUTION_STATUS_COMPLETED), got["status_code"], 0)
	assert.InDelta(t, float64(1), got["attempt"], 0)
	assert.Equal(t, "worker-7@host", got["last_worker_identity"])
	assert.Equal(t, now.Format(time.RFC3339Nano), got["start_time"])
	assert.Equal(t, closedAt.Format(time.RFC3339Nano), got["close_time"])
	assert.InDelta(t, float64(2000), got["execution_duration_ms"], 0)
	assert.Emptyf(t, got["last_failure"], "no last failure → empty string")
}

func TestHandler_DescribeActivity_DescribeErrorPropagated(t *testing.T) {
	t.Parallel()

	descErr := errors.New("temporal: describe: not found")
	handle := &fakeActivityHandle{describeErr: descErr}
	fake := &fakeActivityClient{activityHandle: activityHandleFromFake(handle)}

	_, err := callActivityHandler(t, handleDescribeActivity, fake, json.RawMessage(`{
		"activity_id": "act-4"
	}`))
	require.Error(t, err)
	require.ErrorIs(t, err, descErr)
}

func TestHandler_DescribeActivity_MissingActivityID(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{activityHandle: activityHandleFromFake(&fakeActivityHandle{})}

	_, err := callActivityHandler(t, handleDescribeActivity, fake, json.RawMessage(`{}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, errActivityIDRequired)
}

func TestHandler_DescribeActivity_DecodeError(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{}

	_, err := callActivityHandler(t, handleDescribeActivity, fake, json.RawMessage(`not json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse describe_activity args")
}

// --- list_activities ---

// listActivitiesFixtureSeq converts a slice of ClientActivityExecutionInfo
// plus an error into the SDK's iter.Seq2[*ClientActivityExecutionInfo, error]
// shape.
func listActivitiesFixtureSeq(
	items []*client.ActivityExecutionInfo,
	yieldErr error,
) client.ListActivitiesResult {
	return client.ListActivitiesResult{
		Results: func(yield func(*client.ActivityExecutionInfo, error) bool) {
			if yieldErr != nil {
				var nilInfo *client.ActivityExecutionInfo
				yield(nilInfo, yieldErr)

				return
			}

			for _, item := range items {
				var nilErr error
				if !yield(item, nilErr) {
					return
				}
			}
		},
	}
}

func TestHandler_ListActivities_DefaultPagination(t *testing.T) {
	t.Parallel()

	mkInfo := func(activityID string) *client.ActivityExecutionInfo {
		return &client.ActivityExecutionInfo{
			ActivityID:   activityID,
			ActivityType: "EchoGreeting",
			TaskQueue:    "default",
			Status:       enums.ACTIVITY_EXECUTION_STATUS_COMPLETED,
			ScheduleTime: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
			CloseTime:    time.Date(2026, time.January, 1, 0, 0, 5, 0, time.UTC),
		}
	}

	items := []*client.ActivityExecutionInfo{
		mkInfo("act-a"), mkInfo("act-b"), mkInfo("act-c"),
	}

	fake := &fakeActivityClient{listResult: listActivitiesFixtureSeq(items, error(nil))}

	result, err := callActivityHandler(t, handleListActivities, fake, json.RawMessage(`{}`))
	require.NoError(t, err)

	got := extractActivityText(t, result)
	activities, ok := got["activities"].([]any)
	require.Truef(t, ok, "activities must be an array")
	assert.InDeltaf(t, float64(3), got["count"], 0, "default pageSize 100 collects all three")
	require.Lenf(t, activities, 3, "all three items returned")
	assert.Emptyf(t, got["next_page_token"], "no more results → empty token")

	first := activities[0].(map[string]any)
	assert.Equal(t, "act-a", first["activity_id"])
	assert.Equal(t, "COMPLETED", first["status"])
}

func TestHandler_ListActivities_PageSizeRespected(t *testing.T) {
	t.Parallel()

	mkInfo := func(activityID string) *client.ActivityExecutionInfo {
		return &client.ActivityExecutionInfo{
			ActivityID:   activityID,
			ActivityType: "EchoGreeting",
			TaskQueue:    "default",
			Status:       enums.ACTIVITY_EXECUTION_STATUS_RUNNING,
		}
	}

	items := []*client.ActivityExecutionInfo{
		mkInfo("a"), mkInfo("b"), mkInfo("c"), mkInfo("d"),
	}

	fake := &fakeActivityClient{listResult: listActivitiesFixtureSeq(items, error(nil))}

	result, err := callActivityHandler(t, handleListActivities, fake, json.RawMessage(`{
		"page_size": 2
	}`))
	require.NoError(t, err)

	got := extractActivityText(t, result)
	assert.InDeltaf(t, float64(2), got["count"], 0, "page_size=2 returns 2 items")
	activities := got["activities"].([]any)
	require.Len(t, activities, 2)
	assert.Equal(t, "a", activities[0].(map[string]any)["activity_id"])
	assert.Equal(t, "b", activities[1].(map[string]any)["activity_id"])

	token, ok := got["next_page_token"].(string)
	require.Truef(t, ok, "next_page_token is a string")
	assert.NotEmptyf(t, token, "page was full → next_page_token set")

	// Decode the token back into a skip count.
	raw, decodeErr := base64.StdEncoding.DecodeString(token)
	require.NoErrorf(t, decodeErr, "token is base64")
	skip, atoiErr := strconv.Atoi(string(raw))
	require.NoErrorf(t, atoiErr, "token decodes to int")
	assert.Equalf(t, 2, skip, "token encodes skip count of 2")
}

func TestHandler_ListActivities_NextPageTokenResumesFromSkip(t *testing.T) {
	t.Parallel()

	mkInfo := func(activityID string) *client.ActivityExecutionInfo {
		return &client.ActivityExecutionInfo{
			ActivityID:   activityID,
			ActivityType: "EchoGreeting",
			TaskQueue:    "default",
			Status:       enums.ACTIVITY_EXECUTION_STATUS_COMPLETED,
		}
	}

	items := []*client.ActivityExecutionInfo{
		mkInfo("a"), mkInfo("b"), mkInfo("c"), mkInfo("d"),
	}

	// Token encoding skip=2.
	token := base64.StdEncoding.EncodeToString([]byte("2"))
	fake := &fakeActivityClient{listResult: listActivitiesFixtureSeq(items, error(nil))}

	result, err := callActivityHandler(t, handleListActivities, fake, json.RawMessage(`{
		"page_size": 2,
		"next_page_token": "`+token+`"
	}`))
	require.NoError(t, err)

	got := extractActivityText(t, result)
	activities := got["activities"].([]any)
	require.Lenf(t, activities, 2, "skip 2 + page_size 2 → items 3 and 4")
	assert.Equal(t, "c", activities[0].(map[string]any)["activity_id"])
	assert.Equal(t, "d", activities[1].(map[string]any)["activity_id"])
}

func TestHandler_ListActivities_BadTokenRejected(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{}

	_, err := callActivityHandler(t, handleListActivities, fake, json.RawMessage(`{
		"next_page_token": "!!!not-base64!!!"
	}`))
	require.Error(t, err)
	assert.Containsf(t, err.Error(), "next_page_token", "error names the token field")
}

func TestHandler_ListActivities_InvalidPageSize(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{}

	_, err := callActivityHandler(t, handleListActivities, fake, json.RawMessage(`{
		"page_size": 0
	}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, errPageSizeInvalid)
}

func TestHandler_ListActivities_SDKErrorPropagated(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("temporal: list: invalid query")
	fake := &fakeActivityClient{listErr: sdkErr}

	_, err := callActivityHandler(t, handleListActivities, fake, json.RawMessage(`{}`))
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
}

func TestHandler_ListActivities_IteratorErrorPropagated(t *testing.T) {
	t.Parallel()

	iterErr := errors.New("temporal: page fetch failed")
	fake := &fakeActivityClient{
		listResult: listActivitiesFixtureSeq([]*client.ActivityExecutionInfo(nil), iterErr),
	}

	_, err := callActivityHandler(t, handleListActivities, fake, json.RawMessage(`{}`))
	require.Error(t, err)
	require.ErrorIs(t, err, iterErr)
}

func TestHandler_ListActivities_DecodeError(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{}

	_, err := callActivityHandler(t, handleListActivities, fake, json.RawMessage(`not json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse list_activities args")
}

// --- count_activities ---

func TestHandler_CountActivities_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{
		countResult: &client.CountActivitiesResult{
			Count: 17,
			Groups: []client.CountActivitiesAggregationGroup{
				{GroupValues: []any{"default"}, Count: 17},
			},
		},
	}

	result, err := callActivityHandler(t, handleCountActivities, fake, json.RawMessage(`{
		"query": "TaskQueue = \"default\""
	}`))
	require.NoError(t, err)

	got := extractActivityText(t, result)
	assert.Equalf(t, "TaskQueue = \"default\"", got["query"], "echoes query")
	assert.InDelta(t, float64(17), got["count"], 0)
	groups, ok := got["groups"].([]any)
	require.True(t, ok)
	require.Len(t, groups, 1)
	first := groups[0].(map[string]any)
	assert.Equal(t, []any{"default"}, first["group_values"])
	assert.InDelta(t, float64(17), first["count"], 0)
}

func TestHandler_CountActivities_NoGroups(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{
		countResult: &client.CountActivitiesResult{Count: 0},
	}

	result, err := callActivityHandler(t, handleCountActivities, fake, json.RawMessage(`{}`))
	require.NoError(t, err)

	got := extractActivityText(t, result)
	assert.InDelta(t, float64(0), got["count"], 0)
	groups, ok := got["groups"].([]any)
	require.True(t, ok)
	assert.Emptyf(t, groups, "empty groups → empty array")
}

func TestHandler_CountActivities_SDKErrorPropagated(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("temporal: count: invalid query")
	fake := &fakeActivityClient{countErr: sdkErr}

	_, err := callActivityHandler(t, handleCountActivities, fake, json.RawMessage(`{}`))
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
}

func TestHandler_CountActivities_DecodeError(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{}

	_, err := callActivityHandler(t, handleCountActivities, fake, json.RawMessage(`not json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse count_activities args")
}

// --- cancel_activity ---

func TestHandler_CancelActivity_HappyPath(t *testing.T) {
	t.Parallel()

	handle := &fakeActivityHandle{id: "act-5", runID: "run-5"}
	fake := &fakeActivityClient{activityHandle: activityHandleFromFake(handle)}

	result, err := callActivityHandler(t, handleCancelActivity, fake, json.RawMessage(`{
		"activity_id": "act-5",
		"reason": "user requested"
	}`))
	require.NoError(t, err)

	got := extractActivityText(t, result)
	assert.Equal(t, "canceled", got["status"])
	assert.Equal(t, "act-5", got["activity_id"])
	assert.Equal(t, "run-5", got["run_id"])
	assert.Equal(t, "user requested", got["reason"])

	assert.Equalf(t, "user requested", handle.lastCancelOpts.Reason,
		"reason forwarded to SDK")
}

func TestHandler_CancelActivity_MissingActivityID(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{activityHandle: activityHandleFromFake(&fakeActivityHandle{})}

	_, err := callActivityHandler(t, handleCancelActivity, fake, json.RawMessage(`{}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, errActivityIDRequired)
}

func TestHandler_CancelActivity_SDKErrorPropagated(t *testing.T) {
	t.Parallel()

	cancelErr := errors.New("temporal: cancel: not found")
	handle := &fakeActivityHandle{cancelErr: cancelErr}
	fake := &fakeActivityClient{activityHandle: activityHandleFromFake(handle)}

	_, err := callActivityHandler(t, handleCancelActivity, fake, json.RawMessage(`{
		"activity_id": "act-5"
	}`))
	require.Error(t, err)
	require.ErrorIs(t, err, cancelErr)
}

func TestHandler_CancelActivity_DecodeError(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{}

	_, err := callActivityHandler(t, handleCancelActivity, fake, json.RawMessage(`not json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse cancel_activity args")
}

// --- terminate_activity ---

func TestHandler_TerminateActivity_HappyPath(t *testing.T) {
	t.Parallel()

	handle := &fakeActivityHandle{id: "act-6", runID: "run-6"}
	fake := &fakeActivityClient{activityHandle: activityHandleFromFake(handle)}

	result, err := callActivityHandler(t, handleTerminateActivity, fake, json.RawMessage(`{
		"activity_id": "act-6",
		"reason": "stuck worker"
	}`))
	require.NoError(t, err)

	got := extractActivityText(t, result)
	assert.Equal(t, "terminated", got["status"])
	assert.Equal(t, "act-6", got["activity_id"])
	assert.Equal(t, "run-6", got["run_id"])
	assert.Equal(t, "stuck worker", got["reason"])

	assert.Equalf(t, "stuck worker", handle.lastTermOpts.Reason,
		"reason forwarded to SDK")
}

func TestHandler_TerminateActivity_MissingActivityID(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{activityHandle: activityHandleFromFake(&fakeActivityHandle{})}

	_, err := callActivityHandler(t, handleTerminateActivity, fake, json.RawMessage(`{}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, errActivityIDRequired)
}

func TestHandler_TerminateActivity_SDKErrorPropagated(t *testing.T) {
	t.Parallel()

	termErr := errors.New("temporal: terminate: not found")
	handle := &fakeActivityHandle{termErr: termErr}
	fake := &fakeActivityClient{activityHandle: activityHandleFromFake(handle)}

	_, err := callActivityHandler(t, handleTerminateActivity, fake, json.RawMessage(`{
		"activity_id": "act-6"
	}`))
	require.Error(t, err)
	require.ErrorIs(t, err, termErr)
}

func TestHandler_TerminateActivity_DecodeError(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{}

	_, err := callActivityHandler(t, handleTerminateActivity, fake, json.RawMessage(`not json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse terminate_activity args")
}

// --- Shared helpers ---

func TestResolveStartToCloseTimeout_Default(t *testing.T) {
	t.Parallel()

	got, err := resolveStartToCloseTimeout((*int)(nil))
	require.NoError(t, err)
	assert.Equalf(t, defaultActivityStartToClose, got, "nil pointer → default")
}

func TestResolveStartToCloseTimeout_Explicit(t *testing.T) {
	t.Parallel()

	v := 30
	got, err := resolveStartToCloseTimeout(&v)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, got)
}

func TestResolveStartToCloseTimeout_RejectsZero(t *testing.T) {
	t.Parallel()

	v := 0
	_, err := resolveStartToCloseTimeout(&v)
	assert.ErrorIs(t, err, errStartToCloseTimeoutInvalid)
}

func TestResolveStartToCloseTimeout_RejectsNegative(t *testing.T) {
	t.Parallel()

	v := -1
	_, err := resolveStartToCloseTimeout(&v)
	assert.ErrorIs(t, err, errStartToCloseTimeoutInvalid)
}

func TestDecodeActivityArguments_NilOrEmpty(t *testing.T) {
	t.Parallel()

	assert.Nilf(t, decodeActivityArguments(json.RawMessage(nil)), "nil raw → nil")
	assert.Nilf(t, decodeActivityArguments(json.RawMessage("")), "empty raw → nil")
}

func TestDecodeActivityArguments_ArrayForwarded(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`[1, 2, 3]`)
	got := decodeActivityArguments(raw)
	require.Lenf(t, got, 3, "three-element array → three args")
	assert.InDeltaf(t, float64(1), got[0], 0, "json numbers decoded as float64")
}

func TestDecodeActivityArguments_ObjectWrapped(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"x": 7}`)
	got := decodeActivityArguments(raw)
	require.Lenf(t, got, 1, "object wrapped as single positional arg")
	obj, ok := got[0].(map[string]any)
	require.True(t, ok)
	assert.InDelta(t, float64(7), obj["x"], 0)
}

func TestDecodeActivityArguments_MalformedFallsBackToRaw(t *testing.T) {
	t.Parallel()

	// Not valid JSON; decoder returns the raw bytes wrapped in a slice
	// so the SDK's data converter surfaces a clear decode error.
	raw := json.RawMessage(`not json`)
	got := decodeActivityArguments(raw)
	require.Len(t, got, 1)
	_, ok := got[0].(json.RawMessage)
	require.Truef(t, ok, "malformed raw wrapped as json.RawMessage for the SDK to decode")
}

func TestEncodeDecodeActivityToken_RoundTrip(t *testing.T) {
	t.Parallel()

	token := encodeActivityToken(42)
	require.NotEmptyf(t, token, "non-zero skip encodes to non-empty token")

	skip, err := decodeActivityToken(token)
	require.NoError(t, err)
	assert.Equalf(t, 42, skip, "round-trips through base64")
}

func TestEncodeActivityToken_ZeroIsEmpty(t *testing.T) {
	t.Parallel()

	assert.Emptyf(t, encodeActivityToken(0), "zero skip → empty token (no more results)")
}

func TestEncodeActivityToken_NegativeIsEmpty(t *testing.T) {
	t.Parallel()

	assert.Emptyf(t, encodeActivityToken(-1), "negative skip → empty token")
}

func TestDecodeActivityToken_EmptyIsZero(t *testing.T) {
	t.Parallel()

	skip, err := decodeActivityToken("")
	require.NoError(t, err)
	assert.Equal(t, 0, skip)
}

func TestDecodeActivityToken_BadBase64Rejected(t *testing.T) {
	t.Parallel()

	_, err := decodeActivityToken("not-base64-!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "next_page_token")
}

func TestDecodeActivityToken_NonIntRejected(t *testing.T) {
	t.Parallel()

	token := base64.StdEncoding.EncodeToString([]byte("not-a-number"))
	_, err := decodeActivityToken(token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "next_page_token")
}

func TestDecodeActivityToken_NegativeRejected(t *testing.T) {
	t.Parallel()

	token := base64.StdEncoding.EncodeToString([]byte("-5"))
	_, err := decodeActivityToken(token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "negative skip")
}

func TestActivityExecutionStatusName_Known(t *testing.T) {
	t.Parallel()

	assert.Equalf(
		t,
		"COMPLETED",
		activityExecutionStatusName(enums.ACTIVITY_EXECUTION_STATUS_COMPLETED),
		"known enum → enum name with prefix stripped",
	)
}

func TestActivityExecutionStatusName_Unknown(t *testing.T) {
	t.Parallel()

	// Pick an enum value the SDK doesn't know about to exercise the
	// fallback path. We use a value outside the documented range.
	unknown := enums.ActivityExecutionStatus(99999)
	got := activityExecutionStatusName(unknown)
	assert.Equalf(t, "99999", got, "unknown enum → numeric form")
}

// --- activityTools — sanity check that all 8 are registered ---

func TestTemporalActivityTools_ReturnsEightInOrder(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{execActivityHandle: &fakeActivityHandle{}}
	tools := activityTools(fake)
	require.Lenf(t, tools, 8, "eight activity tools registered")

	expected := []string{
		"start_activity",
		"execute_activity",
		"get_activity_result",
		"describe_activity",
		"list_activities",
		"count_activities",
		"cancel_activity",
		"terminate_activity",
	}

	for i, want := range expected {
		require.NotNilf(t, tools[i].Tool, "tool[%d] must have non-nil *mcp.Tool", i)
		assert.Equalf(t, want, tools[i].Name,
			"tool[%d] name registration order", i)
		require.NotNilf(t, tools[i].Handler, "tool[%d] must have a Handler", i)
	}
}

func TestTemporalActivityTools_Annotations(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{execActivityHandle: &fakeActivityHandle{}}
	tools := activityTools(fake)
	require.Len(t, tools, 8)

	// Helpers to read the annotations.
	readOnly := func(idx int) bool { return tools[idx].Annotations.ReadOnlyHint }
	openWorld := func(idx int) bool {
		if tools[idx].Annotations.OpenWorldHint == nil {
			return false
		}

		return *tools[idx].Annotations.OpenWorldHint
	}
	destructive := func(idx int) (bool, bool) {
		hint := tools[idx].Annotations.DestructiveHint
		if hint == nil {
			return false, false
		}

		return *hint, true
	}
	idempotent := func(idx int) bool { return tools[idx].Annotations.IdempotentHint }

	for i, tool := range tools {
		require.Truef(t, openWorld(i), "tool[%d] %s: OpenWorldHint must be true", i, tool.Name)
	}

	// start_activity, execute_activity: mutating, non-destructive, non-idempotent.
	for _, idx := range []int{0, 1} {
		assert.Falsef(t, readOnly(idx), "tool[%d] %s: ReadOnlyHint false", idx, tools[idx].Name)
		isDestructive, has := destructive(idx)
		require.Truef(t, has, "tool[%d] %s: DestructiveHint set", idx, tools[idx].Name)
		assert.Falsef(t, isDestructive, "tool[%d] %s: DestructiveHint(false)", idx, tools[idx].Name)
		assert.Falsef(t, idempotent(idx), "tool[%d] %s: IdempotentHint false", idx, tools[idx].Name)
	}

	// get_activity_result, describe_activity, list_activities, count_activities: read-only.
	for _, idx := range []int{2, 3, 4, 5} {
		assert.Truef(t, readOnly(idx), "tool[%d] %s: ReadOnlyHint true", idx, tools[idx].Name)
	}

	// cancel_activity, terminate_activity: destructive, idempotent.
	for _, idx := range []int{6, 7} {
		assert.Falsef(t, readOnly(idx), "tool[%d] %s: ReadOnlyHint false", idx, tools[idx].Name)
		isDestructive, has := destructive(idx)
		require.Truef(t, has, "tool[%d] %s: DestructiveHint set", idx, tools[idx].Name)
		assert.Truef(t, isDestructive, "tool[%d] %s: DestructiveHint(true)", idx, tools[idx].Name)
		assert.Truef(t, idempotent(idx), "tool[%d] %s: IdempotentHint true", idx, tools[idx].Name)
	}
}

func TestTemporalActivityTools_DescriptionsIncludePreamble(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{execActivityHandle: &fakeActivityHandle{}}
	tools := activityTools(fake)
	require.Len(t, tools, 8)

	for _, tool := range tools {
		require.NotNilf(t, tool.Description, "tool %s must have a description", tool.Name)
		assert.Containsf(t, tool.Description, standaloneActivityLoop,
			"tool %s description starts with the standaloneActivityLoop preamble", tool.Name)
	}
}

func TestTemporalActivityTools_SchemasEmbedded(t *testing.T) {
	t.Parallel()

	fake := &fakeActivityClient{execActivityHandle: &fakeActivityHandle{}}
	tools := activityTools(fake)
	require.Len(t, tools, 8)

	for _, tool := range tools {
		require.NotNilf(t, tool.InputSchema, "tool %s: InputSchema embedded", tool.Name)
		require.NotNilf(t, tool.OutputSchema, "tool %s: OutputSchema embedded", tool.Name)

		// Each schema must be a valid JSON object. mcp.Tool.InputSchema
		// is typed as any — cast to json.RawMessage because we know
		// our embeds produce raw JSON bytes.
		inputSchema, ok := tool.InputSchema.(json.RawMessage)
		require.Truef(
			t,
			ok,
			"tool %s: InputSchema must be json.RawMessage, got %T",
			tool.Name,
			tool.InputSchema,
		)
		outputSchema, ok := tool.OutputSchema.(json.RawMessage)
		require.Truef(
			t,
			ok,
			"tool %s: OutputSchema must be json.RawMessage, got %T",
			tool.Name,
			tool.OutputSchema,
		)

		var probe map[string]any
		require.NoErrorf(t, json.Unmarshal(inputSchema, &probe),
			"tool %s: InputSchema is a JSON object", tool.Name)
		require.NoErrorf(t, json.Unmarshal(outputSchema, &probe),
			"tool %s: OutputSchema is a JSON object", tool.Name)
	}
}

// TestListActivitiesFixtureSeq_YieldError confirms the test helper
// surfaces iterator errors to its consumer. Without this check the
// TestHandler_ListActivities_IteratorErrorPropagated test would
// silently pass on an empty result.
func TestListActivitiesFixtureSeq_YieldError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("fixture iter error")
	res := listActivitiesFixtureSeq([]*client.ActivityExecutionInfo(nil), wantErr)

	var (
		seenErr error
		seen    int
	)
	for _, err := range res.Results {
		seen++
		if err != nil {
			seenErr = err
		}
	}

	require.Equalf(t, 1, seen, "yield called exactly once for the error")
	require.ErrorIs(t, seenErr, wantErr)
}
