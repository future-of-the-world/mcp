// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:exhaustruct,revive,wsl_v5 // test fixtures cluster fake SDK method recordings
package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Fakes ---
//
// fakeScheduleClient and fakeScheduleHandle implement the SDK's
// ScheduleClient / ScheduleHandle interfaces with just enough behavior
// to drive the handlers end-to-end without dialing Temporal. Each fake
// records the arguments of the most recent call so tests can assert on
// what the handlers passed through. Errors are configurable via the
// returnErr fields; when set, the matching call returns the error
// without inspecting inputs.

type fakeScheduleClient struct {
	createHandle client.ScheduleHandle
	createErr    error
	createCalled *client.ScheduleOptions

	listIter client.ScheduleListIterator
	listErr  error
	listOpts *client.ScheduleListOptions

	getHandle client.ScheduleHandle
}

//nolint:gocritic // matches SDK's ScheduleClient.Create signature; can't take pointer
func (f *fakeScheduleClient) Create(
	_ context.Context,
	options client.ScheduleOptions,
) (client.ScheduleHandle, error) {
	stored := options
	f.createCalled = &stored

	if f.createErr != nil {
		return nil, f.createErr
	}

	if f.createHandle != nil {
		return f.createHandle, nil
	}

	return &fakeScheduleHandle{id: options.ID}, nil
}

func (f *fakeScheduleClient) List(
	_ context.Context,
	options client.ScheduleListOptions,
) (client.ScheduleListIterator, error) {
	f.listOpts = &options

	if f.listErr != nil {
		return nil, f.listErr
	}

	if f.listIter != nil {
		return f.listIter, nil
	}

	return &fakeScheduleListIterator{}, nil
}

func (f *fakeScheduleClient) GetHandle(_ context.Context, schedID string) client.ScheduleHandle {
	if f.getHandle != nil {
		return f.getHandle
	}

	return &fakeScheduleHandle{id: schedID}
}

// fakeScheduleHandle records the per-handle method calls and returns
// the configured errors. Multiple goroutines never touch the same
// fake (each test makes its own), so plain field reads/writes are
// race-free — no mutex needed.
type fakeScheduleHandle struct {
	id string

	pauseErr    error
	pauseCalled *client.SchedulePauseOptions

	unpauseErr    error
	unpauseCalled *client.ScheduleUnpauseOptions

	triggerErr error

	deleteErr error

	describeOut *client.ScheduleDescription
	describeErr error
}

func (f *fakeScheduleHandle) GetID() string {
	return f.id
}

func (f *fakeScheduleHandle) Pause(_ context.Context, options client.SchedulePauseOptions) error {
	f.pauseCalled = &options

	return f.pauseErr
}

func (f *fakeScheduleHandle) Unpause(
	_ context.Context,
	options client.ScheduleUnpauseOptions,
) error {
	f.unpauseCalled = &options

	return f.unpauseErr
}

func (f *fakeScheduleHandle) Trigger(_ context.Context, _ client.ScheduleTriggerOptions) error {
	return f.triggerErr
}

func (f *fakeScheduleHandle) Delete(_ context.Context) error {
	return f.deleteErr
}

func (f *fakeScheduleHandle) Describe(_ context.Context) (*client.ScheduleDescription, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}

	if f.describeOut != nil {
		return f.describeOut, nil
	}

	return &client.ScheduleDescription{
		Schedule: client.Schedule{
			State: &client.ScheduleState{Paused: false},
		},
		Info: client.ScheduleInfo{},
	}, nil
}

func (f *fakeScheduleHandle) Backfill(_ context.Context, _ client.ScheduleBackfillOptions) error {
	return nil
}

func (f *fakeScheduleHandle) Update(_ context.Context, _ client.ScheduleUpdateOptions) error {
	return nil
}

// fakeScheduleListIterator returns the configured entries in order.
// An empty entries slice makes HasNext return false on the first call,
// so handlers see "no schedules" cleanly.
type fakeScheduleListIterator struct {
	entries []*client.ScheduleListEntry
	index   int
	err     error
}

func (f *fakeScheduleListIterator) HasNext() bool {
	return f.index < len(f.entries)
}

func (f *fakeScheduleListIterator) Next() (*client.ScheduleListEntry, error) {
	if f.err != nil {
		return nil, f.err
	}

	entry := f.entries[f.index]
	f.index++

	return entry, nil
}

// --- manager wrapper for handler tests ---

// --- manager wrapper for handler tests ---

// newFakeManager constructs a *clientManager whose ScheduleClient
// returns the supplied fake. The seam lives in
// (*clientManager).withScheduleClient (see client.go): the manager
// carries a *client.ScheduleClient override that takes precedence
// over the real SDK sub-client. Production code never sets the
// override; only handler tests do.
func newFakeManager(sched *fakeScheduleClient) *clientManager {
	mgr := &clientManager{}

	return mgr.withScheduleClient(sched)
}

// --- Tool-call helper ---

// callHandler drives a single tool handler with the given arguments
// and returns the result + error. Handlers are produced by the
// per-tool factory functions (handleCreateSchedule etc.) bound to the
// supplied fake manager so the test exercises the same handler shape
// the dispatcher would invoke in production.
//
//nolint:cyclop // per-tool name dispatch lives in scheduleHandlerByName
func callHandler(
	t *testing.T, toolName string, args json.RawMessage, mgr *clientManager,
) (*mcp.CallToolResult, error) {
	t.Helper()

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta{},
			Name:      toolName,
			Arguments: args,
		},
		Extra: (*mcp.RequestExtra)(nil),
	}

	handler := scheduleHandlerByName(t, toolName, mgr)

	return handler(t.Context(), req)
}

// scheduleHandlerByName returns the per-tool handler factory's output
// for the named tool. The factories live in schedule_handlers.go; this
// helper exists so tests can drive each tool without going through
// the full Connect → dispatcher pipeline (which would force every
// test to also build a real clientManager).
func scheduleHandlerByName(t *testing.T, name string, mgr *clientManager) mcp.ToolHandler {
	t.Helper()

	switch name {
	case "create_schedule":
		return handleCreateSchedule(mgr)
	case "list_schedules":
		return handleListSchedules(mgr)
	case "pause_schedule":
		return handlePauseSchedule(mgr)
	case "unpause_schedule":
		return handleUnpauseSchedule(mgr)
	case "delete_schedule":
		return handleDeleteSchedule(mgr)
	case "trigger_schedule":
		return handleTriggerSchedule(mgr)
	case "describe_schedule":
		return handleDescribeSchedule(mgr)
	default:
		t.Fatalf("unknown tool: %s", name)

		return nil
	}
}

// extractTextContent reads the first text content block from a
// CallToolResult. Mirrors the woodpecker test helper. The MCP SDK
// returns errors via the IsError flag, but the JSON body lives in
// TextContent regardless of IsError; tests that assert on error
// shape inspect the returned error separately.
func extractTextContent(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, result)
	require.NotEmptyf(t, result.Content, "result has no content blocks")

	tcText, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "first content is %T, want *mcp.TextContent", result.Content[0])

	return tcText.Text
}

// --- create_schedule ---

func TestCreateSchedule_HappyPath(t *testing.T) {
	t.Parallel()

	sched := &fakeScheduleClient{}
	mgr := newFakeManager(sched)

	args := json.RawMessage(`{
		"schedule_id": "every-5m",
		"workflow_name": "CleanupWorkflow",
		"task_queue": "cleanup",
		"cron": "*/5 * * * *",
		"args": [{"force": true}, 42],
		"notes": "housekeeping"
	}`)

	result, err := callHandler(t, "create_schedule", args, mgr)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.NotNilf(t, sched.createCalled, "ScheduleClient.Create was not invoked")
	assert.Equal(t, "every-5m", sched.createCalled.ID)
	assert.Equal(t, []string{"*/5 * * * *"}, sched.createCalled.Spec.CronExpressions)
	assert.Equal(t, "housekeeping", sched.createCalled.Note)

	action, ok := sched.createCalled.Action.(*client.ScheduleWorkflowAction)
	require.Truef(
		t,
		ok,
		"action is %T, want *client.ScheduleWorkflowAction",
		sched.createCalled.Action,
	)
	assert.Equal(t, "CleanupWorkflow", action.Workflow)
	assert.Equal(t, "cleanup", action.TaskQueue)
	assert.Equal(t, []any{map[string]any{"force": true}, float64(42)}, action.Args)

	text := extractTextContent(t, result)
	assert.Contains(t, text, `"created":true`)
	assert.Contains(t, text, `"schedule_id":"every-5m"`)
}

func TestCreateSchedule_OptionalArgs(t *testing.T) {
	t.Parallel()

	sched := &fakeScheduleClient{}
	mgr := newFakeManager(sched)

	// No args / notes at all — the handler should treat them as
	// absent (omitted JSON keys) and forward an empty Action.Args.
	args := json.RawMessage(`{
		"schedule_id": "s1",
		"workflow_name": "W",
		"task_queue": "q",
		"cron": "0 12 * * *"
	}`)

	_, err := callHandler(t, "create_schedule", args, mgr)
	require.NoError(t, err)

	require.NotNil(t, sched.createCalled)
	action, ok := sched.createCalled.Action.(*client.ScheduleWorkflowAction)
	require.True(t, ok)
	assert.Emptyf(t, action.Args, "absent args → nil/empty Args forwarded")
	assert.Empty(t, sched.createCalled.Note)
}

func TestCreateSchedule_ArgsNullIsEmpty(t *testing.T) {
	t.Parallel()

	sched := &fakeScheduleClient{}
	mgr := newFakeManager(sched)

	// Explicit "args":null should be treated the same as omitted.
	args := json.RawMessage(`{
		"schedule_id": "s1",
		"workflow_name": "W",
		"task_queue": "q",
		"cron": "0 12 * * *",
		"args": null
	}`)

	_, err := callHandler(t, "create_schedule", args, mgr)
	require.NoError(t, err)

	action, ok := sched.createCalled.Action.(*client.ScheduleWorkflowAction)
	require.True(t, ok)
	assert.Emptyf(t, action.Args, "null args → nil/empty Args forwarded")
}

func TestCreateSchedule_ArgsDecodeError(t *testing.T) {
	t.Parallel()

	sched := &fakeScheduleClient{}
	mgr := newFakeManager(sched)

	// args is a string, not an array — decode must fail.
	args := json.RawMessage(`{
		"schedule_id": "s1",
		"workflow_name": "W",
		"task_queue": "q",
		"cron": "0 12 * * *",
		"args": "not-an-array"
	}`)

	_, err := callHandler(t, "create_schedule", args, mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "args")
	assert.Nilf(t, sched.createCalled, "Create must not be called when args decode fails")
}

func TestCreateSchedule_MissingFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		args    string
		wantErr error
	}{
		{
			"missing schedule_id",
			`{"workflow_name":"W","task_queue":"q","cron":"0 12 * * *"}`,
			errScheduleIDRequired,
		},
		{
			"missing workflow_name",
			`{"schedule_id":"s","task_queue":"q","cron":"0 12 * * *"}`,
			errWorkflowNameRequired,
		},
		{
			"missing task_queue",
			`{"schedule_id":"s","workflow_name":"W","cron":"0 12 * * *"}`,
			errTaskQueueRequired,
		},
		{
			"missing cron",
			`{"schedule_id":"s","workflow_name":"W","task_queue":"q"}`,
			errCronRequired,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			sched := &fakeScheduleClient{}
			mgr := newFakeManager(sched)

			_, err := callHandler(t, "create_schedule", json.RawMessage(testCase.args), mgr)
			require.Error(t, err)
			require.ErrorIsf(
				t,
				err,
				testCase.wantErr,
				"want sentinel %v, got %v",
				testCase.wantErr,
				err,
			)
			assert.Nilf(
				t,
				sched.createCalled,
				"Create must not be called on missing required field",
			)
		})
	}
}

// TestCreateSchedule_MalformedCron exercises the "we don't validate
// cron client-side" contract: a syntactically malformed cron string
// passes through the handler and reaches the SDK. The fake returns an
// error from Create to simulate what the real server does.
func TestCreateSchedule_MalformedCron(t *testing.T) {
	t.Parallel()

	sched := &fakeScheduleClient{createErr: errors.New("invalid cron expression")}
	mgr := newFakeManager(sched)

	args := json.RawMessage(`{
		"schedule_id": "s",
		"workflow_name": "W",
		"task_queue": "q",
		"cron": "this is not cron"
	}`)

	_, err := callHandler(t, "create_schedule", args, mgr)
	require.Error(t, err)
	assert.Containsf(t, err.Error(), "temporal: schedule create:",
		"SDK error must be wrapped under the create prefix")
	require.ErrorIs(t, err, sched.createErr)

	require.NotNil(t, sched.createCalled)
	assert.Equal(t, []string{"this is not cron"}, sched.createCalled.Spec.CronExpressions)
}

func TestCreateSchedule_CreateError(t *testing.T) {
	t.Parallel()

	sched := &fakeScheduleClient{createErr: errors.New("server unavailable")}
	mgr := newFakeManager(sched)

	args := json.RawMessage(`{
		"schedule_id": "s",
		"workflow_name": "W",
		"task_queue": "q",
		"cron": "0 12 * * *"
	}`)

	_, err := callHandler(t, "create_schedule", args, mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporal: schedule create:")
	require.ErrorIs(t, err, sched.createErr)
}

func TestCreateSchedule_MalformedJSONArgs(t *testing.T) {
	t.Parallel()

	sched := &fakeScheduleClient{}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "create_schedule", json.RawMessage(`{`), mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse create_schedule args")
}

// --- list_schedules ---

func TestListSchedules_HappyPath(t *testing.T) {
	t.Parallel()

	next := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)

	sched := &fakeScheduleClient{
		listIter: &fakeScheduleListIterator{
			entries: []*client.ScheduleListEntry{
				func() *client.ScheduleListEntry {
					entry := &client.ScheduleListEntry{
						ID:              "sched-a",
						Paused:          false,
						Note:            "first",
						NextActionTimes: []time.Time{next},
					}
					// client.WorkflowType is internal-only, so we
					// set the field's Name through the public field
					// path. This exercises the same accessor the
					// handler uses (entry.WorkflowType.Name).
					entry.WorkflowType.Name = "WorkflowA"

					return entry
				}(),
				{
					ID:     "sched-b",
					Paused: true,
				},
			},
		},
	}
	mgr := newFakeManager(sched)

	args := json.RawMessage(`{"page_size": 50}`)

	result, err := callHandler(t, "list_schedules", args, mgr)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.NotNilf(t, sched.listOpts, "ScheduleClient.List was not invoked")
	assert.Equal(t, 50, sched.listOpts.PageSize)

	text := extractTextContent(t, result)
	assert.Contains(t, text, `"id":"sched-a"`)
	assert.Contains(t, text, `"id":"sched-b"`)
	assert.Contains(t, text, `"paused":true`)
	assert.Contains(t, text, `"workflow_type":"WorkflowA"`)
}

func TestListSchedules_EmptyIterator(t *testing.T) {
	t.Parallel()

	sched := &fakeScheduleClient{
		listIter: &fakeScheduleListIterator{},
	}
	mgr := newFakeManager(sched)

	result, err := callHandler(t, "list_schedules", json.RawMessage(`{}`), mgr)
	require.NoError(t, err)

	text := extractTextContent(t, result)
	assert.Containsf(
		t,
		text,
		`"schedules":[]`,
		"empty iterator → empty schedules array, got %s",
		text,
	)
}

func TestListSchedules_ListError(t *testing.T) {
	t.Parallel()

	sched := &fakeScheduleClient{listErr: errors.New("namespace not found")}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "list_schedules", json.RawMessage(`{}`), mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporal: schedule list:")
	require.ErrorIs(t, err, sched.listErr)
}

func TestListSchedules_NextError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("stream broken")
	sched := &fakeScheduleClient{
		listIter: &fakeScheduleListIterator{
			err: wantErr,
			entries: []*client.ScheduleListEntry{
				{ID: "a"},
			},
		},
	}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "list_schedules", json.RawMessage(`{}`), mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporal: schedule list:")
	require.ErrorIs(t, err, wantErr)
}

func TestListSchedules_MalformedJSONArgs(t *testing.T) {
	t.Parallel()

	sched := &fakeScheduleClient{}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "list_schedules", json.RawMessage(`{`), mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse list_schedules args")
}

// --- pause_schedule ---

func TestPauseSchedule_HappyPath(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	args := json.RawMessage(`{"schedule_id":"s","note":"investigating"}`)

	result, err := callHandler(t, "pause_schedule", args, mgr)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.NotNilf(t, handle.pauseCalled, "handle.Pause was not invoked")
	assert.Equalf(t, "investigating", handle.pauseCalled.Note,
		"note must be forwarded to SchedulePauseOptions.Note")

	text := extractTextContent(t, result)
	assert.Contains(t, text, `"paused":true`)
	assert.Contains(t, text, `"schedule_id":"s"`)
}

func TestPauseSchedule_EmptyNote(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	args := json.RawMessage(`{"schedule_id":"s"}`)

	_, err := callHandler(t, "pause_schedule", args, mgr)
	require.NoError(t, err)

	require.NotNil(t, handle.pauseCalled)
	assert.Emptyf(
		t,
		handle.pauseCalled.Note,
		"empty/missing note → empty Note forwarded (SDK uses default)",
	)
}

func TestPauseSchedule_MissingScheduleID(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "pause_schedule", json.RawMessage(`{}`), mgr)
	require.Error(t, err)
	require.ErrorIs(t, err, errScheduleIDRequired)
	assert.Nilf(t, handle.pauseCalled, "Pause must not be called when schedule_id is missing")
}

func TestPauseSchedule_PauseError(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{pauseErr: errors.New("not found")}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "pause_schedule", json.RawMessage(`{"schedule_id":"missing"}`), mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporal: schedule pause:")
	require.ErrorIs(t, err, handle.pauseErr)
}

// --- unpause_schedule ---

func TestUnpauseSchedule_HappyPath(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	args := json.RawMessage(`{"schedule_id":"s","note":"resumed"}`)

	result, err := callHandler(t, "unpause_schedule", args, mgr)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.NotNilf(t, handle.unpauseCalled, "handle.Unpause was not invoked")
	assert.Equal(t, "resumed", handle.unpauseCalled.Note)

	text := extractTextContent(t, result)
	assert.Contains(t, text, `"unpaused":true`)
}

func TestUnpauseSchedule_MissingScheduleID(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "unpause_schedule", json.RawMessage(`{}`), mgr)
	require.Error(t, err)
	require.ErrorIs(t, err, errScheduleIDRequired)
}

func TestUnpauseSchedule_UnpauseError(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{unpauseErr: errors.New("not found")}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "unpause_schedule", json.RawMessage(`{"schedule_id":"missing"}`), mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporal: schedule unpause:")
	require.ErrorIs(t, err, handle.unpauseErr)
}

// --- delete_schedule ---

func TestDeleteSchedule_HappyPath(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	result, err := callHandler(t, "delete_schedule", json.RawMessage(`{"schedule_id":"s"}`), mgr)
	require.NoError(t, err)

	text := extractTextContent(t, result)
	assert.Contains(t, text, `"deleted":true`)
	assert.Contains(t, text, `"schedule_id":"s"`)
}

func TestDeleteSchedule_MissingScheduleID(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "delete_schedule", json.RawMessage(`{}`), mgr)
	require.Error(t, err)
	require.ErrorIs(t, err, errScheduleIDRequired)
}

func TestDeleteSchedule_DeleteError(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{deleteErr: errors.New("permission denied")}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "delete_schedule", json.RawMessage(`{"schedule_id":"s"}`), mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporal: schedule delete:")
	require.ErrorIs(t, err, handle.deleteErr)
}

// --- trigger_schedule ---

func TestTriggerSchedule_HappyPath(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	result, err := callHandler(t, "trigger_schedule", json.RawMessage(`{"schedule_id":"s"}`), mgr)
	require.NoError(t, err)

	text := extractTextContent(t, result)
	assert.Contains(t, text, `"triggered":true`)
}

func TestTriggerSchedule_MissingScheduleID(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "trigger_schedule", json.RawMessage(`{}`), mgr)
	require.Error(t, err)
	require.ErrorIs(t, err, errScheduleIDRequired)
}

func TestTriggerSchedule_TriggerError(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{triggerErr: errors.New("overlap policy skip")}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "trigger_schedule", json.RawMessage(`{"schedule_id":"s"}`), mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporal: schedule trigger:")
	require.ErrorIs(t, err, handle.triggerErr)
}

// --- describe_schedule ---

func TestDescribeSchedule_HappyPath(t *testing.T) {
	t.Parallel()

	expected := &client.ScheduleDescription{
		Schedule: client.Schedule{
			Action: &client.ScheduleWorkflowAction{
				Workflow:  "Cleanup",
				TaskQueue: "cleanup",
			},
			Spec: &client.ScheduleSpec{
				CronExpressions: []string{"*/15 * * * *"},
			},
			State: &client.ScheduleState{Paused: false, Note: "active"},
		},
		Info: client.ScheduleInfo{NumActions: 42},
	}

	handle := &fakeScheduleHandle{describeOut: expected}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	result, err := callHandler(t, "describe_schedule", json.RawMessage(`{"schedule_id":"s"}`), mgr)
	require.NoError(t, err)

	text := extractTextContent(t, result)
	assert.Contains(t, text, `"schedule_id":"s"`)
	assert.Contains(t, text, `"NumActions":42`)
	assert.Contains(t, text, `"Workflow":"Cleanup"`)
}

func TestDescribeSchedule_MissingScheduleID(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "describe_schedule", json.RawMessage(`{}`), mgr)
	require.Error(t, err)
	require.ErrorIs(t, err, errScheduleIDRequired)
}

func TestDescribeSchedule_DescribeError(t *testing.T) {
	t.Parallel()

	handle := &fakeScheduleHandle{describeErr: errors.New("not found")}
	sched := &fakeScheduleClient{getHandle: handle}
	mgr := newFakeManager(sched)

	_, err := callHandler(t, "describe_schedule", json.RawMessage(`{"schedule_id":"missing"}`), mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporal: schedule describe:")
	require.ErrorIs(t, err, handle.describeErr)
}

// --- Annotations ---

// TestScheduleToolAnnotations asserts the per-tool annotation matrix
// from the issue description. ReadOnly, Destructive (where set), and
// Idempotent hints are checked. OpenWorld is asserted to be true on
// all seven tools (the mcp.Tool annotations use *bool for
// DestructiveHint and OpenWorldHint so the test is careful about
// nil-vs-set).
func TestScheduleToolAnnotations(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{})
	require.NoError(t, err)

	type wantShape struct {
		readOnly    bool
		destructive *bool // nil = don't assert, &true / &false = exact
		idempotent  bool
	}

	want := map[string]wantShape{
		"create_schedule":   {readOnly: false, destructive: new(false), idempotent: false},
		"list_schedules":    {readOnly: true, destructive: nil, idempotent: false},
		"pause_schedule":    {readOnly: false, destructive: new(false), idempotent: true},
		"unpause_schedule":  {readOnly: false, destructive: new(false), idempotent: true},
		"delete_schedule":   {readOnly: false, destructive: new(true), idempotent: true},
		"trigger_schedule":  {readOnly: false, destructive: new(false), idempotent: false},
		"describe_schedule": {readOnly: true, destructive: nil, idempotent: false},
	}

	for _, entry := range resp.Tools {
		exp, ok := want[entry.Name]
		if !ok {
			// Tool belongs to a different feature group
			// (workflow / activity / query-signal / batch).
			// Those have their own annotation tests; skip here.
			continue
		}

		assert.Equalf(
			t,
			exp.readOnly,
			entry.Annotations.ReadOnlyHint,
			"ReadOnlyHint mismatch for %s",
			entry.Name,
		)
		assert.Equalf(
			t,
			exp.idempotent,
			entry.Annotations.IdempotentHint,
			"IdempotentHint mismatch for %s",
			entry.Name,
		)

		if exp.destructive != nil {
			require.NotNilf(t, entry.Annotations.DestructiveHint,
				"DestructiveHint nil but expected %v for %s", *exp.destructive, entry.Name)
			assert.Equalf(t, *exp.destructive, *entry.Annotations.DestructiveHint,
				"DestructiveHint mismatch for %s", entry.Name)
		}

		require.NotNilf(
			t,
			entry.Annotations.OpenWorldHint,
			"OpenWorldHint must be set for %s - every schedule tool talks to a remote server",
			entry.Name,
		)
		assert.Truef(
			t,
			*entry.Annotations.OpenWorldHint,
			"OpenWorldHint must be true for %s",
			entry.Name,
		)

		delete(want, entry.Name)
	}

	assert.Emptyf(t, want, "missing schedule tools: %v", want)
}

// --- Preamble ---

// TestSchedulePreambleEveryTool confirms every schedule tool's
// description starts with the shared `scheduleLoop` preamble. The
// preamble is the agent's first signal that the seven tools form a
// lifecycle together.
//
// After the temporal-integration PR landed, every feature group
// (schedule / workflow / activity / query-signal / batch) registers
// its own tools, each with its own per-group preamble. The
// `strings.HasPrefix(entry.Description, scheduleLoop)` check now
// applies only to the seven schedule tools.
func TestSchedulePreambleEveryTool(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{})
	require.NoError(t, err)

	scheduleToolNames := map[string]struct{}{
		"create_schedule":   {},
		"list_schedules":    {},
		"pause_schedule":    {},
		"unpause_schedule":  {},
		"delete_schedule":   {},
		"trigger_schedule":  {},
		"describe_schedule": {},
	}

	for _, entry := range resp.Tools {
		if _, ok := scheduleToolNames[entry.Name]; !ok {
			continue
		}

		assert.Truef(t, strings.HasPrefix(entry.Description, scheduleLoop),
			"tool %s description does not start with scheduleLoop preamble", entry.Name)
	}
}

// --- Cron parsing ---

// TestCronStringPassesThrough verifies that the handler does not
// validate or transform the cron expression client-side. The string
// goes straight into ScheduleSpec.CronExpressions and is the SDK /
// server's job to reject invalid expressions. This test exercises the
// "we plumb the string through unchanged" contract.
func TestCronStringPassesThrough(t *testing.T) {
	t.Parallel()

	cases := []string{
		"0 12 * * *",
		"*/5 * * * *",
		"@hourly",
		"@every 5m",
		"CRON_TZ=UTC 0 12 * * MON-WED",
	}

	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			t.Parallel()

			sched := &fakeScheduleClient{}
			mgr := newFakeManager(sched)

			args := json.RawMessage(`{
				"schedule_id": "s",
				"workflow_name": "W",
				"task_queue": "q",
				"cron": "` + expr + `"
			}`)

			_, err := callHandler(t, "create_schedule", args, mgr)
			require.NoError(t, err)
			require.NotNil(t, sched.createCalled)
			assert.Equal(t, []string{expr}, sched.createCalled.Spec.CronExpressions)
		})
	}
}
