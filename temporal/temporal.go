// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package temporal implements the Temporal workflow orchestration source
// for the MCP server. It exposes a focused subset of the Temporal Go SDK
// (go.temporal.io/sdk/client) as MCP tools, covering workflow lifecycle,
// standalone activity lifecycle, query/signal, batch operations, and
// schedule management.
//
// All five feature areas share the same clientManager (built in
// client.go) and feed off the same `connect:` map. Feature-specific
// tools live in their own handler file:
//
//	schedule_handlers.go      → scheduleTools(manager)
//	workflow_handlers.go      → workflowTools(manager)
//	activity_handlers.go      → activityTools(manager)
//	query_signal_handlers.go  → querySignalTools(manager)
//	batch_handlers.go         → batchTools(manager)
//
// This file is the package wiring: the package doc, the embedded
// input/output JSON Schemas for every tool, the shared default
// values, the per-tool description preambles, the unified
// temporalClient interface (the handler-test seam), the
// activityHandle interface, the config struct, the connect-map
// decoder, and the Connect entry point that registers all thirty
// tools.
//
// The thirty tools are:
//
//	// 7 schedule
//	temporal_create_schedule     (mutating, non-idempotent)  → register a new schedule
//	temporal_list_schedules      (read-only)                  → discover schedule_id
//	temporal_describe_schedule   (read-only)                  → full ScheduleDescription
//	temporal_pause_schedule      (mutating, idempotent)       → gate firing
//	temporal_unpause_schedule    (mutating, idempotent)       → resume firing
//	temporal_trigger_schedule    (mutating, non-idempotent)   → fire immediately
//	temporal_delete_schedule     (mutating, idempotent, destructive) → permanently remove
//
//	// 8 workflow
//	temporal_start_workflow                (mutating, non-idempotent) → new execution
//	temporal_cancel_workflow               (mutating, idempotent, destructive) → cooperative stop
//	temporal_terminate_workflow            (mutating, idempotent, destructive) → forced stop
//	temporal_get_workflow_result           (read-only)              → block on completion
//	temporal_describe_workflow             (read-only)              → server-side description
//	temporal_list_workflows                (read-only)              → Visibility query
//	temporal_get_workflow_history          (read-only)              → event stream
//	temporal_continue_as_new               (mutating, non-idempotent) → signal CAN
//
//	// 8 activity
//	temporal_start_activity                (mutating, non-idempotent) → new standalone activity
//	temporal_execute_activity              (mutating, non-idempotent) → start + await
//	temporal_get_activity_result           (read-only)              → block on completion
//	temporal_describe_activity             (read-only)              → state + attempt
//	temporal_list_activities               (read-only)              → Visibility query
//	temporal_count_activities              (read-only)              → aggregate count
//	temporal_cancel_activity               (mutating, idempotent, destructive) → cooperative stop
//	temporal_terminate_activity            (mutating, idempotent, destructive) → forced stop
//
//	// 2 query/signal
//	temporal_query_workflow                (read-only)              → read state via named handler
//	temporal_signal_workflow               (mutating)               → deliver event
//
//	// 5 batch
//	temporal_batch_signal                  (mutating, non-idempotent) → fan out over Visibility
//
// query
//
//	temporal_batch_cancel                  (mutating, idempotent, destructive) → fan out cancel
//	temporal_batch_terminate               (mutating, idempotent, destructive) → fan out terminate
//	temporal_batch_cancel_activities       (mutating, idempotent, destructive) → fan out activity
//
// cancel 	temporal_batch_terminate_activities    (mutating, idempotent, destructive) → fan out
// activity terminate
//
// All thirty share the same `connect:` map: a Temporal frontend
// address (host:port) and an optional namespace; plus optional TLS
// materials (api_key, tls_client_cert_path, tls_client_key_path,
// tls_enabled).
package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "embed"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/tool"
)

// =====================================================================
//   Embedded input/output JSON Schemas — 30 tools × 2 files = 60 embeds
// =====================================================================

// --- schedule (14) ---

//go:embed schemas/create_schedule.json
var createScheduleInput json.RawMessage

//go:embed schemas/create_schedule_output.json
var createScheduleOutput json.RawMessage

//go:embed schemas/list_schedules.json
var listSchedulesInput json.RawMessage

//go:embed schemas/list_schedules_output.json
var listSchedulesOutput json.RawMessage

//go:embed schemas/pause_schedule.json
var pauseScheduleInput json.RawMessage

//go:embed schemas/pause_schedule_output.json
var pauseScheduleOutput json.RawMessage

//go:embed schemas/unpause_schedule.json
var unpauseScheduleInput json.RawMessage

//go:embed schemas/unpause_schedule_output.json
var unpauseScheduleOutput json.RawMessage

//go:embed schemas/delete_schedule.json
var deleteScheduleInput json.RawMessage

//go:embed schemas/delete_schedule_output.json
var deleteScheduleOutput json.RawMessage

//go:embed schemas/trigger_schedule.json
var triggerScheduleInput json.RawMessage

//go:embed schemas/trigger_schedule_output.json
var triggerScheduleOutput json.RawMessage

//go:embed schemas/describe_schedule.json
var describeScheduleInput json.RawMessage

//go:embed schemas/describe_schedule_output.json
var describeScheduleOutput json.RawMessage

// --- workflow (16) ---

//go:embed schemas/start_workflow_input.json
var startWorkflowInput json.RawMessage

//go:embed schemas/start_workflow_output.json
var startWorkflowOutput json.RawMessage

//go:embed schemas/cancel_workflow_input.json
var cancelWorkflowInput json.RawMessage

//go:embed schemas/cancel_workflow_output.json
var cancelWorkflowOutput json.RawMessage

//go:embed schemas/terminate_workflow_input.json
var terminateWorkflowInput json.RawMessage

//go:embed schemas/terminate_workflow_output.json
var terminateWorkflowOutput json.RawMessage

//go:embed schemas/get_workflow_result_input.json
var getWorkflowResultInput json.RawMessage

//go:embed schemas/get_workflow_result_output.json
var getWorkflowResultOutput json.RawMessage

//go:embed schemas/describe_workflow_input.json
var describeWorkflowInput json.RawMessage

//go:embed schemas/describe_workflow_output.json
var describeWorkflowOutput json.RawMessage

//go:embed schemas/list_workflows_input.json
var listWorkflowsInput json.RawMessage

//go:embed schemas/list_workflows_output.json
var listWorkflowsOutput json.RawMessage

//go:embed schemas/get_workflow_history_input.json
var getWorkflowHistoryInput json.RawMessage

//go:embed schemas/get_workflow_history_output.json
var getWorkflowHistoryOutput json.RawMessage

//go:embed schemas/continue_as_new_input.json
var continueAsNewInput json.RawMessage

//go:embed schemas/continue_as_new_output.json
var continueAsNewOutput json.RawMessage

// --- activity (16) ---

//go:embed schemas/start_activity.json
var startActivityInput json.RawMessage

//go:embed schemas/start_activity_output.json
var startActivityOutput json.RawMessage

//go:embed schemas/execute_activity.json
var executeActivityInput json.RawMessage

//go:embed schemas/execute_activity_output.json
var executeActivityOutput json.RawMessage

//go:embed schemas/get_activity_result.json
var getActivityResultInput json.RawMessage

//go:embed schemas/get_activity_result_output.json
var getActivityResultOutput json.RawMessage

//go:embed schemas/describe_activity.json
var describeActivityInput json.RawMessage

//go:embed schemas/describe_activity_output.json
var describeActivityOutput json.RawMessage

//go:embed schemas/list_activities.json
var listActivitiesInput json.RawMessage

//go:embed schemas/list_activities_output.json
var listActivitiesOutput json.RawMessage

//go:embed schemas/count_activities.json
var countActivitiesInput json.RawMessage

//go:embed schemas/count_activities_output.json
var countActivitiesOutput json.RawMessage

//go:embed schemas/cancel_activity.json
var cancelActivityInput json.RawMessage

//go:embed schemas/cancel_activity_output.json
var cancelActivityOutput json.RawMessage

//go:embed schemas/terminate_activity.json
var terminateActivityInput json.RawMessage

//go:embed schemas/terminate_activity_output.json
var terminateActivityOutput json.RawMessage

// --- query/signal (4) ---

//go:embed schemas/query_workflow_input.json
var queryWorkflowInput json.RawMessage

//go:embed schemas/query_workflow_output.json
var queryWorkflowOutput json.RawMessage

//go:embed schemas/signal_workflow_input.json
var signalWorkflowInput json.RawMessage

//go:embed schemas/signal_workflow_output.json
var signalWorkflowOutput json.RawMessage

// --- batch (10) ---

//go:embed schemas/batch_signal.json
var batchSignalInput json.RawMessage

//go:embed schemas/batch_signal_output.json
var batchSignalOutput json.RawMessage

//go:embed schemas/batch_cancel.json
var batchCancelInput json.RawMessage

//go:embed schemas/batch_cancel_output.json
var batchCancelOutput json.RawMessage

//go:embed schemas/batch_terminate.json
var batchTerminateInput json.RawMessage

//go:embed schemas/batch_terminate_output.json
var batchTerminateOutput json.RawMessage

//go:embed schemas/batch_cancel_activities.json
var batchCancelActivitiesInput json.RawMessage

//go:embed schemas/batch_cancel_activities_output.json
var batchCancelActivitiesOutput json.RawMessage

//go:embed schemas/batch_terminate_activities.json
var batchTerminateActivitiesInput json.RawMessage

//go:embed schemas/batch_terminate_activities_output.json
var batchTerminateActivitiesOutput json.RawMessage

// =====================================================================
// Default values — applied when the connect map omits the
// corresponding field. Mirrors the upstream Python temporal-mcp
// defaults so users migrating from the standalone server see
// identical behavior.
// =====================================================================

const (
	// defaultHost is the Temporal frontend address Connect dials
	// when the user omits `host`. Matches the Python upstream
	// default.
	defaultHost = "localhost:7233"

	// defaultNamespace is the Temporal namespace Connect targets
	// when the user omits `namespace`. Matches the Python upstream
	// default.
	defaultNamespace = "default"

	// defaultListWorkflowsPageSize is the default page size for
	// the list_workflows tool when the caller omits page_size.
	defaultListWorkflowsPageSize = 100

	// defaultHistoryMaxEvents is the default max_events cap for
	// the get_workflow_history tool when the caller omits
	// max_events.
	defaultHistoryMaxEvents = 1000
)

// =====================================================================
// Tool description preambles — prepended to every tool in each
// feature group so the agent sees the canonical sequence
// regardless of which tool it discovers first via `tools.list`.
// =====================================================================

const (
	// scheduleLoop is the package-level preamble explaining the
	// schedule lifecycle. It is prepended to every schedule tool's
	// description so the agent sees it regardless of which
	// schedule tool it discovers first via `tools.list`. Mirrors
	// the Woodpecker investigationWorkflow pattern.
	scheduleLoop = "Temporal schedule lifecycle:\n" +
		"1. temporal_list_schedules (read-only) → discover schedule_id.\n" +
		"2. temporal_describe_schedule({schedule_id}) → inspect spec, action,\n" +
		"recent executions.\n" +
		"3. Use temporal_pause_schedule / temporal_unpause_schedule to gate firing " +
		"without losing the schedule.\n" +
		"4. temporal_trigger_schedule fires the workflow immediately without waiting " +
		"for the next cron tick.\n" +
		"5. temporal_delete_schedule permanently removes the schedule; this is NOT " +
		"reversible.\n" +
		"Cron strings use standard 5-field syntax (e.g. '0 12 * * *'); " +
		"the server validates the expression."

	// workflowLoop is the package-level narrative explaining how
	// the eight workflow tools are meant to be used together. It
	// is prepended to each tool's description so the agent sees
	// the canonical sequence (list → describe → history)
	// regardless of which tool it discovers first via `tools.list`.
	workflowLoop = "Temporal workflow investigation loop:\n" +
		"1. temporal_list_workflows (read-only) → find workflow_id by query or recency.\n" +
		"2. temporal_describe_workflow({workflow_id}) → status, run_id, search attributes.\n" +
		"3. temporal_get_workflow_history({workflow_id, run_id, max_events}) → " +
		"decoded event stream.\n" +
		"4. Diagnose from the history; then either temporal_cancel_workflow / " +
		"temporal_terminate_workflow (irrecoverable) or surface a fix to the user.\n" +
		"Pair temporal_continue_as_new with a workflow that handles the signal via " +
		"workflow.NewContinueAsNewError.\n" +
		"This loop removes the need for the user to copy-paste CLI output into the session."

	// standaloneActivityLoop is the package-level preamble
	// explaining how the eight standalone activity tools are
	// meant to be used together. It mirrors the workflow loop but
	// for the activity-only surface — list → describe → cancel /
	// terminate when an activity is stuck.
	standaloneActivityLoop = "Temporal standalone activity lifecycle:\n" +
		"1. temporal_list_activities (read-only) → discover activity_id by query.\n" +
		"2. temporal_describe_activity({activity_id}) → status, attempt, last failure, worker.\n" +
		"3. temporal_get_activity_result({activity_id, run_id, timeout_seconds}) → " +
		"block on the result (only fires once the activity finishes).\n" +
		"4. Diagnose from the description / result; then either " +
		"temporal_cancel_activity (cooperative) or temporal_terminate_activity (forced).\n" +
		"Pair temporal_start_activity (fire-and-forget) with temporal_get_activity_result " +
		"(block) by intent; use temporal_execute_activity when the return value matters " +
		"immediately.\n" +
		"This loop removes the need for the user to copy-paste activity-history CLI " +
		"output into the session."

	// querySignalLoop is the package-level narrative explaining
	// how the query/signal pair is meant to be used together. It
	// is prepended to each tool's description so the agent sees
	// the read-only vs. mutating contrast regardless of which
	// tool it discovers first via tools.list.
	querySignalLoop = "Temporal workflow query + signal:\n" +
		"1. temporal_query_workflow (read-only) → ask the workflow for " +
		"state via a named query handler. The workflow must register a " +
		"handler via workflow.SetQueryHandler for the query type to be " +
		"recognized.\n" +
		"2. temporal_signal_workflow (mutating) → deliver an event the " +
		"workflow reacts to via workflow.SetSignalHandler. Signals are " +
		"NOT idempotent — re-sending the same signal triggers a separate " +
		"handler invocation each time.\n" +
		"Use the query workflow_id first to confirm the target workflow " +
		"is still running before signaling."

	// batchLoop is the shared preamble prepended to every
	// per-tool description so the agent learns the
	// visibility-query → fan-out pipeline once and applies it to
	// all five tools. Mirrors the upstream Python temporal-mcp's
	// batch_handlers.py docstring at the top of each handler.
	batchLoop = "Temporal batch operation loop:\n" +
		"1. Run a visibility query via ListWorkflow / ListActivities (server-side filter).\n" +
		"2. Fan out the operation (signal/cancel/terminate) over the matched executions\n" +
		"with bounded concurrency.\n" +
		"Default concurrency is 50; cap is 100 to protect the Temporal frontend from overload.\n" +
		"On the first error, no new work starts, but already-in-flight ops run to\n" +
		"completion (no orphans).\n" +
		"Return payload: {matched, succeeded, failed, errors[]} — errors[] is capped at 50 entries."
)

// =====================================================================
// Sentinel errors
// =====================================================================

var (
	// errMTLSPartialCert is returned when exactly one of
	// tls_client_cert_path and tls_client_key_path is set. mTLS
	// requires both halves.
	errMTLSPartialCert = errors.New(
		"temporal tool: tls_client_cert_path and tls_client_key_path must be set together",
	)

	// errHostEmpty is returned when the decoded host string is
	// empty after defaulting. Defensive — the connect-map decoder
	// fills in the default, so this only fires if a future caller
	// passes an empty string explicitly.
	errHostEmpty = errors.New("temporal tool: host is empty")

	// errNamespaceEmpty is returned when the decoded namespace
	// string is empty after defaulting. Same defensive rationale
	// as errHostEmpty.
	errNamespaceEmpty = errors.New("temporal tool: namespace is empty")

	// errTLSCertMissing is returned when a configured cert or key
	// path does not exist on disk.
	errTLSCertMissing = errors.New("temporal tool: tls client cert or key file is missing")

	// errTLSCertUnreadable is returned when os.ReadFile fails on
	// a configured cert or key path for any reason other than the
	// file being absent.
	errTLSCertUnreadable = errors.New("temporal tool: tls client cert or key file is unreadable")
)

// =====================================================================
//   Package-level interface — handler factories consume this
//   seam so tests can inject fakes without standing up a real
//   Temporal server. Workflow + activity handlers use it
//   directly; query/signal + batch declare narrower scopes
//   (querySignalClient, batchClient) for the same purpose.
// =====================================================================

// temporalClient is the subset of the Temporal Go SDK Client
// interface that handler factories consume. It exists so handler
// tests can inject a fake without standing up a real Temporal
// server. The interface is implemented by *client.Client (the
// SDK's lazy client returned from clientManager.client) and by
// *clientManager (which delegates to its inner client.Client).
//
// The interface is intentionally restricted to the workflow +
// activity surface — handlers in query_signal_handlers.go and
// batch_handlers.go define narrower per-file interfaces
// (querySignalClient, batchClient) so that adding more
// functionality stays a per-PR decision tied to that file.
//
//nolint:interfacebloat // unified pass-through surface covers all 5 tool groups' client needs.
type temporalClient interface {
	// Close releases the underlying connection.
	Close()

	// ScheduleClient returns the schedule sub-client. The seven
	// schedule tools use this.
	ScheduleClient() client.ScheduleClient

	// StartWorkflow starts a workflow execution and returns the
	// run handle plus any error.
	StartWorkflow(
		ctx context.Context,
		opts client.StartWorkflowOptions,
		workflow string,
		args ...any,
	) (client.WorkflowRun, error)

	// CancelWorkflow requests cancellation of a workflow run.
	CancelWorkflow(ctx context.Context, workflowID, runID string) error

	// TerminateWorkflow forcibly terminates a workflow run.
	TerminateWorkflow(
		ctx context.Context,
		workflowID, runID, reason string,
		details ...any,
	) error

	// GetWorkflow returns a WorkflowRun handle for the given
	// execution, which the caller can use to fetch the result
	// via .Get.
	GetWorkflow(ctx context.Context, workflowID, runID string) client.WorkflowRun

	// DescribeWorkflow returns detailed information about a
	// workflow execution.
	DescribeWorkflow(
		ctx context.Context, workflowID, runID string,
	) (*client.WorkflowExecutionDescription, error)

	// ListWorkflow queries the Visibility store for workflow
	// executions.
	ListWorkflow(
		ctx context.Context,
		request *workflowservice.ListWorkflowExecutionsRequest,
	) (*workflowservice.ListWorkflowExecutionsResponse, error)

	// GetWorkflowHistory returns an iterator over the
	// workflow's history events.
	GetWorkflowHistory(
		ctx context.Context,
		workflowID, runID string,
		isLongPoll bool,
		filterType enums.HistoryEventFilterType,
	) client.HistoryEventIterator

	// SignalWorkflow delivers a signal to a workflow run. Used by
	// the continue_as_new tool (the Go SDK has no dedicated
	// continue-as-new client API; the workflow itself decides
	// whether to honor the signal by calling
	// workflow.NewContinueAsNewError).
	SignalWorkflow(
		ctx context.Context,
		workflowID, runID, signalName string,
		arg any,
	) error

	// ExecuteActivity schedules a standalone activity and returns
	// a handle (satisfying activityHandle) plus any error.
	ExecuteActivity(
		ctx context.Context,
		opts client.StartActivityOptions,
		activity any,
		args ...any,
	) (client.ActivityHandle, error)

	// GetActivityHandle returns a handle for a previously
	// scheduled standalone activity. RunID may be empty to refer
	// to the most recent execution.
	GetActivityHandle(options client.GetActivityHandleOptions) client.ActivityHandle

	// ListActivities returns an iterator over standalone
	// activity executions matching the given Query. The full
	// iterator is paginated internally by the SDK; per-page
	// pagination (skip / pageSize) is implemented in
	// handleListActivities by collecting up to pageSize items
	// and returning a base64-encoded skip count as the
	// next_page_token.
	ListActivities(
		ctx context.Context,
		options client.ListActivitiesOptions,
	) (client.ListActivitiesResult, error)

	// CountActivities returns the count of standalone activity
	// executions matching the given Query, optionally grouped by
	// an aggregation field.
	CountActivities(
		ctx context.Context,
		options client.CountActivitiesOptions,
	) (*client.CountActivitiesResult, error)
}

// activityHandle is the subset of client.ActivityHandle that the
// activity handler factories depend on. The SDK's
// client.ActivityHandle satisfies this interface structurally
// (the SDK exposes six methods: GetID, GetRunID, Get, Describe,
// Cancel, Terminate; the five operation methods are sufficient
// here). The interface exists so test fakes can implement just
// the five methods the handlers call without depending on the
// full SDK surface.
type activityHandle interface {
	// GetRunID returns the run ID the handle was created with.
	// May be empty when the handle was created without an
	// explicit run ID.
	GetRunID() string

	// Get blocks until the activity completes and decodes its
	// result into valuePtr. The Temporal data converter
	// JSON-decodes the payload, so *json.RawMessage round-trips
	// the raw payload unchanged.
	Get(ctx context.Context, valuePtr any) error

	// Describe returns detailed state for the activity execution.
	Describe(
		ctx context.Context,
		options client.DescribeActivityOptions,
	) (*client.ActivityExecutionDescription, error)

	// Cancel requests cancellation. Idempotent on the server.
	Cancel(ctx context.Context, options client.CancelActivityOptions) error

	// Terminate forcefully terminates the activity. Idempotent on
	// the server. Use only when Cancel is not enough.
	Terminate(ctx context.Context, options client.TerminateActivityOptions) error
}

// =====================================================================
// config struct
// =====================================================================

// config holds the decoded `connect:` map for a temporal source.
//
// Field semantics match the upstream Python temporal-mcp:
//   - Host defaults to "localhost:7233".
//   - Namespace defaults to "default".
//   - TLSEnabled is a tri-state: nil means "auto-detect from host/api_key",
//     true means "force TLS on", false means "force TLS off".
//   - TLSClientCertPath and TLSClientKeyPath are mTLS materials. Both
//     must be set together; setting either alone is a validation error.
//   - APIKey forces TLS on (Temporal Cloud requires it). When set, it
//     is passed as client.NewAPIKeyStaticCredentials.
//
// Fields are unexported because the only legitimate way to construct
// a config is via decodeConnect + validate.
type config struct {
	Host              string
	Namespace         string
	TLSEnabled        *bool
	TLSClientCertPath string
	TLSClientKeyPath  string
	APIKey            string
}

// =====================================================================
// Connect entry point
// =====================================================================

// Connect decodes the source's `connect:` map, validates it, builds
// a clientManager, and registers all thirty tools across the five
// feature areas (schedule, workflow, activity, query/signal,
// batch).
//
// The function is invoked from source/dispatcher.go's `case "temporal":`
// arm alongside the existing per-type Connect calls. The connect map
// is free-form — keys not listed in decodeConnect are ignored.
//
// handler at the validate step keeps the wrapped-prefix chain clean.
//
//nolint:cyclop,noinlineerr // Connect is the single seam; the inline error
func Connect(ctx context.Context, connect map[string]any, _ ...tool.Option) (tool.Response, error) {
	cfg, err := decodeConnect(connect)
	if err != nil {
		return tool.Response{}, fmt.Errorf("temporal: decode: %w", err)
	}

	if err = cfg.validate(); err != nil {
		return tool.Response{}, fmt.Errorf("temporal: validate: %w", err)
	}

	manager, err := newClientManager(ctx, &cfg)
	if err != nil {
		return tool.Response{}, fmt.Errorf("temporal: client: %w", err)
	}

	tools := scheduleTools(manager)

	tools = append(tools, workflowTools(manager)...)
	tools = append(tools, activityTools(manager)...)
	tools = append(tools, querySignalTools(manager)...)
	tools = append(tools, batchTools(manager)...)

	return tool.Response{Tools: tools}, nil
}

// =====================================================================
// Per-tool description suffixes for the seven schedule tools.
// =====================================================================

const (
	createScheduleDescription = "\n\nCreate a new schedule. The schedule is " +
		"identified by schedule_id (must be unique) and fires the workflow " +
		"name on task_queue according to cron (5-field standard syntax). " +
		"Optional args is a JSON object passed to the workflow on every fire; " +
		"notes is a free-form human-readable string attached to the schedule."

	listSchedulesDescription = "\n\nList schedules with lightweight " +
		"summaries (id, spec, paused, recent actions). page_size defaults " +
		"to 100; next_page_token from a previous call pages through the rest. " +
		"Use describe_schedule for the full record of one schedule."

	pauseScheduleDescription = "\n\nPause a schedule so it stops firing. " +
		"The schedule stays registered and can be unpaused later. " +
		"Pausing an already-paused schedule is a no-op on the server."

	unpauseScheduleDescription = "\n\nResume a previously-paused schedule. " +
		"Unpausing an active schedule is a no-op on the server."

	deleteScheduleDescription = "\n\nPermanently delete a schedule. " +
		"This is NOT reversible — recent and future workflow executions " +
		"triggered by the schedule are NOT canceled, but no new ones will fire. " +
		"Deleting an already-deleted schedule is a no-op on the server."

	triggerScheduleDescription = "\n\nFire the workflow immediately " +
		"without waiting for the next cron tick. Each call starts a new " +
		"workflow execution. The schedule's spec / overlap policy still " +
		"apply to the manual trigger."

	describeScheduleDescription = "\n\nFetch the full ScheduleDescription " +
		"for a schedule — spec, action (workflow name + args + task queue), " +
		"state (paused, note), and recent executions."
)

// scheduleTools builds the seven schedule tools from a manager. The
// per-tool handler factories live in schedule_handlers.go; this
// function only wires the annotations and embeds the schemas. The
// preamble is prepended to every tool's description so the agent
// sees the lifecycle regardless of which schedule tool it discovers
// first.
//
// Schedule tools share three annotation shapes:
//   - read-only (list, describe): ReadOnlyHint=true; IdempotentHint
//     and DestructiveHint left as nil/absent per the MCP spec.
//   - mutating-idempotent (pause, unpause, delete):
//     ReadOnlyHint=false, DestructiveHint points to its per-tool
//     truth value, IdempotentHint=true. Delete is destructive=true
//     (deletes the schedule); pause and unpause are
//     destructive=false.
//   - mutating-non-idempotent (create, trigger):
//     ReadOnlyHint=false, DestructiveHint points to false,
//     IdempotentHint=false. Each call produces a new schedule
//     (create) or starts a new workflow execution (trigger).
//
// All seven tools set OpenWorldHint=true because every call talks
// to a remote Temporal frontend.
func scheduleTools(manager *clientManager) []tool.Tool {
	openWorld := new(true)

	readOnlyAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    true,
		DestructiveHint: (*bool)(nil),
		IdempotentHint:  false,
		OpenWorldHint:   openWorld,
	}

	// pause and unpause: same shape. Idempotent — pausing a
	// paused schedule is a no-op on the server.
	pauseUnpauseAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    false,
		DestructiveHint: new(false),
		IdempotentHint:  true,
		OpenWorldHint:   openWorld,
	}

	// delete: destructive (removes the schedule) but idempotent —
	// deleting an already-deleted schedule is a no-op on the
	// server.
	deleteAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    false,
		DestructiveHint: new(true),
		IdempotentHint:  true,
		OpenWorldHint:   openWorld,
	}

	// create and trigger: non-destructive, non-idempotent.
	// create registers a new schedule; trigger starts a new
	// workflow execution.
	mutateAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    false,
		DestructiveHint: new(false),
		IdempotentHint:  false,
		OpenWorldHint:   openWorld,
	}

	return []tool.Tool{
		{
			Tool: &mcp.Tool{
				Name:         "create_schedule",
				Description:  scheduleLoop + createScheduleDescription,
				InputSchema:  createScheduleInput,
				OutputSchema: createScheduleOutput,
				Annotations:  mutateAnnotations,
			},
			Handler: handleCreateSchedule(manager),
		},
		{
			Tool: &mcp.Tool{
				Name:         "list_schedules",
				Description:  scheduleLoop + listSchedulesDescription,
				InputSchema:  listSchedulesInput,
				OutputSchema: listSchedulesOutput,
				Annotations:  readOnlyAnnotations,
			},
			Handler: handleListSchedules(manager),
		},
		{
			Tool: &mcp.Tool{
				Name:         "pause_schedule",
				Description:  scheduleLoop + pauseScheduleDescription,
				InputSchema:  pauseScheduleInput,
				OutputSchema: pauseScheduleOutput,
				Annotations:  pauseUnpauseAnnotations,
			},
			Handler: handlePauseSchedule(manager),
		},
		{
			Tool: &mcp.Tool{
				Name:         "unpause_schedule",
				Description:  scheduleLoop + unpauseScheduleDescription,
				InputSchema:  unpauseScheduleInput,
				OutputSchema: unpauseScheduleOutput,
				Annotations:  pauseUnpauseAnnotations,
			},
			Handler: handleUnpauseSchedule(manager),
		},
		{
			Tool: &mcp.Tool{
				Name:         "delete_schedule",
				Description:  scheduleLoop + deleteScheduleDescription,
				InputSchema:  deleteScheduleInput,
				OutputSchema: deleteScheduleOutput,
				Annotations:  deleteAnnotations,
			},
			Handler: handleDeleteSchedule(manager),
		},
		{
			Tool: &mcp.Tool{
				Name:         "trigger_schedule",
				Description:  scheduleLoop + triggerScheduleDescription,
				InputSchema:  triggerScheduleInput,
				OutputSchema: triggerScheduleOutput,
				Annotations:  mutateAnnotations,
			},
			Handler: handleTriggerSchedule(manager),
		},
		{
			Tool: &mcp.Tool{
				Name:         "describe_schedule",
				Description:  scheduleLoop + describeScheduleDescription,
				InputSchema:  describeScheduleInput,
				OutputSchema: describeScheduleOutput,
				Annotations:  readOnlyAnnotations,
			},
			Handler: handleDescribeSchedule(manager),
		},
	}
}

// =====================================================================
// decodeConnect / applyString / applyBool / validate — the
// connect-map decoder shared by every per-feature tool.
// =====================================================================

// decodeConnect decodes the source's `connect:` map into a config.
// Scalar string fields are decoded through decode.AsString so
// YAML-natural values (numbers, bools, null) are accepted and
// stringified; non-scalar values produce a wrapped
// decode.ErrWrongType error so genuine config bugs surface as a
// clear message rather than a silent "field is empty"
// downstream. Optional fields (tls_enabled, tls_client_cert_path,
// tls_client_key_path, api_key) distinguish absent (ErrNotSet)
// from present-but-empty (valid for tls_enabled's tri-state,
// treated as empty for the rest). Errors here are wrapped by
// Connect as "temporal: decode: <reason>".
//
// decodeConnect is split across the applyString helper to keep its
// statement count and cognitive complexity under the linter's
// thresholds.
func decodeConnect(connect map[string]any) (config, error) {
	cfg := config{}

	cfg.Host = defaultHost

	cfg.Namespace = defaultNamespace

	var err error

	err = applyString(connect, "host", &cfg.Host)
	if err != nil {
		return cfg, err
	}

	err = applyString(connect, "namespace", &cfg.Namespace)
	if err != nil {
		return cfg, err
	}

	err = applyString(connect, "api_key", &cfg.APIKey)
	if err != nil {
		return cfg, err
	}

	err = applyString(connect, "tls_client_cert_path", &cfg.TLSClientCertPath)
	if err != nil {
		return cfg, err
	}

	err = applyString(connect, "tls_client_key_path", &cfg.TLSClientKeyPath)
	if err != nil {
		return cfg, err
	}

	err = applyBool(connect, "tls_enabled", &cfg.TLSEnabled)
	if err != nil {
		return cfg, err
	}

	return cfg, nil
}

// applyString decodes a string-typed field from the connect map
// into *target. Absent (nil) values leave *target unchanged.
// Non-scalar values produce a wrapped decode.ErrWrongType error so
// genuine config bugs surface clearly. The function is the
// decode-loop building block that keeps decodeConnect under the
// linter's statement and complexity thresholds.
func applyString(connect map[string]any, key string, target *string) error {
	raw, ok := connect[key]
	if !ok || raw == nil {
		return nil
	}

	str, err := decode.AsString(raw)
	switch {
	case err == nil:
		*target = str

	case errors.Is(err, decode.ErrNotSet):
		// no-op (absent value)

	default:
		return fmt.Errorf("connect.%s: %w", key, err)
	}

	return nil
}

// applyBool decodes a bool-typed field from the connect map into
// **target (a pointer-to-pointer so nil can represent the
// tri-state "absent"). Absent (nil) values leave *target as nil.
// Non-bool values produce a wrapped error.
func applyBool(connect map[string]any, key string, target **bool) error {
	raw, ok := connect[key]
	if !ok || raw == nil {
		return nil
	}

	asBool, ok := raw.(bool)
	if !ok {
		return fmt.Errorf("connect.%s: expected bool, got %T", key, raw)
	}

	*target = &asBool

	return nil
}

// validate checks that the decoded config is internally
// consistent. It is split from decodeConnect so the public Connect
// function's wrapped error chain stays single-segment
// ("temporal: validate: <reason>") without double-nesting.
//
// Rules:
//   - host and namespace must be non-empty (defensive —
//     decodeConnect fills in defaults, so this only fires for
//     explicit empty strings).
//   - tls_client_cert_path and tls_client_key_path must be set
//     together. Setting either alone is rejected.
func (c *config) validate() error {
	if c.Host == "" {
		return errHostEmpty
	}

	if c.Namespace == "" {
		return errNamespaceEmpty
	}

	certSet := c.TLSClientCertPath != ""
	keySet := c.TLSClientKeyPath != ""

	if certSet != keySet {
		return errMTLSPartialCert
	}

	return nil
}
