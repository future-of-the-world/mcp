// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:wsl_v5 // handler factories cluster SDK calls per tool
package temporal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- Constants ---

// defaultActivityStartToClose is the default start_to_close timeout
// applied when handleStartActivity / handleExecuteActivity receive no
// explicit value. 60s matches the upstream Python temporal-mcp default
// and is short enough to surface a stuck worker quickly while leaving
// enough room for typical short-lived activities.
const defaultActivityStartToClose = 60 * time.Second

// defaultListPageSize is the page_size applied when handleListActivities
// receives no explicit value. Matches the upstream Python default.
const defaultListPageSize = 100

// errActivityIDRequired is returned by every activity handler when the
// request omits a non-empty activity_id. Sharing the sentinel across
// handlers keeps the model-facing error message consistent.
var errActivityIDRequired = errors.New("temporal tool: activity_id is required")

// errActivityNameRequired is returned by start_activity and
// execute_activity when the request omits a non-empty activity type
// name. Sharing the sentinel keeps the model-facing message
// consistent.
var errActivityNameRequired = errors.New("temporal tool: activity is required")

// errActivityTaskQueueRequired is returned by start_activity and
// execute_activity when the request omits a non-empty task_queue.
var errActivityTaskQueueRequired = errors.New("temporal tool: task_queue is required")

// errStartToCloseTimeoutInvalid is returned when start_to_close_timeout_seconds
// is provided as a negative or zero integer.
var errStartToCloseTimeoutInvalid = errors.New(
	"temporal tool: start_to_close_timeout_seconds must be > 0 when provided",
)

// errPageSizeInvalid is returned when list_activities receives a
// page_size <= 0 (the schema declares minimum=1 but defensive
// validation belongs in the handler too).
var errPageSizeInvalid = errors.New("temporal tool: page_size must be > 0 when provided")

// nullLiteral is the literal "null" used to represent a missing
// activity return value in JSON. Declared as a const-like variable so
// the linter's add-constant rule does not flag the duplicate string
// literal across handleExecuteActivity and handleGetActivityResult.
var nullLiteral = json.RawMessage("null")

// Tool-name constants. Each tool is referenced by its name in two
// places — the description and the wrapped error — so we declare them
// once here to keep the linter's add-constant rule happy and to make
// rename-by-symbol-search reliable.
const (
	startActivityName     = "start_activity"
	executeActivityName   = "execute_activity"
	getActivityResultName = "get_activity_result"
	describeActivityName  = "describe_activity"
	listActivitiesName    = "list_activities"
	countActivitiesName   = "count_activities"
	cancelActivityName    = "cancel_activity"
	terminateActivityName = "terminate_activity"
)

// errTimeoutInvalid is returned when get_activity_result receives a
// timeout_seconds <= 0 (the schema declares minimum=1; defensive
// validation belongs in the handler too).
var errTimeoutInvalid = errors.New("temporal tool: timeout_seconds must be > 0 when provided")

// --- Argument shapes ---

// startExecuteActivityArgs is the shared JSON input to start_activity
// and execute_activity. The two tools share the same SDK call —
// handleExecuteActivity additionally awaits handle.Get — but the
// per-tool input schema mirrors the upstream Python (one tool per
// intent, no shared schema).
type startExecuteActivityArgs struct {
	Activity                   string          `json:"activity"`
	ActivityID                 string          `json:"activity_id"`
	TaskQueue                  string          `json:"task_queue"`
	Args                       json.RawMessage `json:"args,omitempty"`
	StartToCloseTimeoutSeconds *int            `json:"start_to_close_timeout_seconds,omitempty"`
}

// getActivityResultArgs is the JSON input to get_activity_result.
type getActivityResultArgs struct {
	ActivityID     string `json:"activity_id"`
	RunID          string `json:"run_id,omitempty"`
	TimeoutSeconds *int   `json:"timeout_seconds,omitempty"`
}

// describeActivityArgs is the JSON input to describe_activity.
type describeActivityArgs struct {
	ActivityID string `json:"activity_id"`
	RunID      string `json:"run_id,omitempty"`
}

// listActivitiesArgs is the JSON input to list_activities.
type listActivitiesArgs struct {
	Query         string `json:"query,omitempty"`
	PageSize      *int   `json:"page_size,omitempty"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

// countActivitiesArgs is the JSON input to count_activities.
type countActivitiesArgs struct {
	Query string `json:"query,omitempty"`
}

// cancelActivityArgs is the JSON input to cancel_activity.
type cancelActivityArgs struct {
	ActivityID string `json:"activity_id"`
	RunID      string `json:"run_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// terminateActivityArgs is the JSON input to terminate_activity.
type terminateActivityArgs struct {
	ActivityID string `json:"activity_id"`
	RunID      string `json:"run_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// --- Shared helpers ---

// decodeActivityArgs unmarshals the request's arguments JSON into
// target. It is the one place every tool handler does JSON-decode so
// the per-tool handlers stay focused on per-tool argument validation.
//
//nolint:wrapcheck // json.Unmarshal error returned verbatim for handler parsing failures
func decodeActivityArgs(req *mcp.CallToolRequest, target any) error {
	return json.Unmarshal(req.Params.Arguments, target)
}

// resolveStartToCloseTimeout returns the user-provided
// start_to_close_timeout or defaultActivityStartToClose when omitted.
// A pointer value of zero or negative is rejected with
// errStartToCloseTimeoutInvalid; the user omitting the field entirely
// is the only path that returns the default.
func resolveStartToCloseTimeout(provided *int) (time.Duration, error) {
	if provided == nil {
		return defaultActivityStartToClose, nil
	}

	if *provided <= 0 {
		return 0, errStartToCloseTimeoutInvalid
	}

	return time.Duration(*provided) * time.Second, nil
}

// decodeActivityArguments converts the JSON `args` field into a slice
// of positional arguments to forward to ExecuteActivity. Two shapes are
// accepted:
//
//   - JSON array `[a, b, c]` → returned as []any{a, b, c} (positional
//     dispatch against the activity function signature).
//   - Any other JSON value (object, number, string, boolean) → returned
//     as []any{value} (single positional arg).
//   - Empty / nil raw → nil (zero-arg activity).
//
// The function never returns an error: a malformed `args` payload is
// passed through unchanged and the SDK's data converter will reject it
// at execute time with a clear "could not decode" error.
func decodeActivityArguments(raw json.RawMessage) []any {
	if len(raw) == 0 {
		return nil
	}

	var arr []any

	err := json.Unmarshal(raw, &arr)
	if err == nil {
		return arr
	}

	var single any

	err = json.Unmarshal(raw, &single)
	if err == nil {
		return []any{single}
	}

	// Fall through: pass the raw bytes through so the SDK / data
	// converter surfaces a clear decode error instead of silently
	// dropping the field.
	return []any{raw}
}

// encodeActivityToken encodes an integer skip count as a base64 string
// for use as the next_page_token. The opaque-token contract is
// symmetric: handleListActivities produces the token and accepts the
// same token back to resume from the same skip count.
func encodeActivityToken(skip int) string {
	if skip <= 0 {
		return ""
	}

	return base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(skip)))
}

// decodeActivityToken parses a next_page_token back into an integer
// skip count. Empty / unparseable input returns 0 so a malformed
// token degrades to "start from the beginning" rather than failing
// the call — the alternative would silently block pagination.
func decodeActivityToken(token string) (int, error) {
	if token == "" {
		return 0, nil
	}

	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return 0, fmt.Errorf("temporal tool: next_page_token is not valid base64: %w", err)
	}

	skip, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, fmt.Errorf("temporal tool: next_page_token is not a valid skip count: %w", err)
	}

	if skip < 0 {
		return 0, fmt.Errorf("temporal tool: next_page_token resolves to negative skip %d", skip)
	}

	return skip, nil
}

// marshalActivityToolResult marshals value as JSON and wraps it in a
// CallToolResult. Per the MCP spec, StructuredContent is attached
// only when the marshaled value is a JSON object (so plain arrays,
// primitives, or malformed values are conveyed through Content alone).
// The same probe-and-set shape is used in woodpecker/woodpecker_handlers.go.
func marshalActivityToolResult(value any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
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

// activityExecutionStatusName maps the SDK's enum value to its
// canonical short name. The SDK's generated map keys include the
// "ACTIVITY_EXECUTION_STATUS_" prefix; we strip it so the model
// sees "COMPLETED" instead of "ACTIVITY_EXECUTION_STATUS_COMPLETED".
// Values that fall outside the documented enum range (defensive:
// future SDK versions) are returned as their numeric form so the
// model still receives a stable string.
func activityExecutionStatusName(status enums.ActivityExecutionStatus) string {
	if name, ok := enums.ActivityExecutionStatus_name[int32(status)]; ok {
		return strings.TrimPrefix(name, "ACTIVITY_EXECUTION_STATUS_")
	}

	return strconv.Itoa(int(status))
}

// --- Handler factories ---

// handleStartActivity returns the mcp.ToolHandler that drives the
// start_activity tool. It schedules the activity but does NOT await
// the result — use temporal_get_activity_result to block on completion.
func handleStartActivity(cli temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args startExecuteActivityArgs

		opts, err := decodeStartExecuteActivityArgs(req, startActivityName, &args)
		if err != nil {
			return nil, err
		}

		activityArgs := decodeActivityArguments(args.Args)

		handle, execErr := cli.ExecuteActivity(ctx, opts, args.Activity, activityArgs...)
		if execErr != nil {
			return nil, fmt.Errorf("%s: execute: %w", startActivityName, execErr)
		}

		runID := handleRunID(handle)

		return marshalActivityToolResult(map[string]any{
			"activity_id": args.ActivityID,
			"run_id":      runID,
			"status":      "started",
		})
	}
}

// handleExecuteActivity returns the mcp.ToolHandler that drives the
// execute_activity tool. It schedules the activity AND awaits its
// result via handle.Get before returning. The activity's return value
// is JSON-decoded into a json.RawMessage so the model receives the
// payload unchanged (Temporal's data converter is JSON).
func handleExecuteActivity(cli temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args startExecuteActivityArgs

		opts, err := decodeStartExecuteActivityArgs(req, executeActivityName, &args)
		if err != nil {
			return nil, err
		}

		activityArgs := decodeActivityArguments(args.Args)

		handle, execErr := cli.ExecuteActivity(ctx, opts, args.Activity, activityArgs...)
		if execErr != nil {
			return nil, fmt.Errorf("%s: execute: %w", executeActivityName, execErr)
		}

		var result json.RawMessage

		getErr := handle.Get(ctx, &result)
		if getErr != nil {
			return nil, fmt.Errorf("%s: await result: %w", executeActivityName, getErr)
		}

		runID := handleRunID(handle)

		result = normalizeNilResult(result)

		return marshalActivityToolResult(map[string]any{
			"activity_id": args.ActivityID,
			"run_id":      runID,
			"result":      result,
			"status":      "completed",
		})
	}
}

// decodeStartExecuteActivityArgs decodes the request into
// startExecuteActivityArgs, validates the required fields, and resolves
// the start-to-close timeout. toolName is used in the wrapped decode
// error. The decoded args are stored via the argsOut pointer; the
// returned StartActivityOptions is ready for cli.ExecuteActivity.
func decodeStartExecuteActivityArgs(
	req *mcp.CallToolRequest,
	toolName string,
	argsOut *startExecuteActivityArgs,
) (client.StartActivityOptions, error) {
	err := decodeActivityArgs(req, argsOut)
	if err != nil {
		return client.StartActivityOptions{}, fmt.Errorf("parse %s args: %w", toolName, err)
	}

	if argsOut.Activity == "" {
		return client.StartActivityOptions{}, errActivityNameRequired
	}

	if argsOut.ActivityID == "" {
		return client.StartActivityOptions{}, errActivityIDRequired
	}

	if argsOut.TaskQueue == "" {
		return client.StartActivityOptions{}, errActivityTaskQueueRequired
	}

	startToClose, err := resolveStartToCloseTimeout(argsOut.StartToCloseTimeoutSeconds)
	if err != nil {
		return client.StartActivityOptions{}, err
	}

	opts := client.StartActivityOptions{
		ID:                  argsOut.ActivityID,
		TaskQueue:           argsOut.TaskQueue,
		StartToCloseTimeout: startToClose,
	}

	return opts, nil
}

// handleRunID returns the handle's run ID, or "" when handle is nil.
// The SDK's client.ActivityHandle always returns a non-nil handle from
// ExecuteActivity / GetActivityHandle, but we guard defensively because
// the structural activityHandle interface permits nil for test fakes.
func handleRunID(handle activityHandle) string {
	if handle == nil {
		return ""
	}

	return handle.GetRunID()
}

// normalizeNilResult converts an empty json.RawMessage to JSON null so
// activities that return nothing produce a stable shape on the wire.
func normalizeNilResult(result json.RawMessage) json.RawMessage {
	if len(result) == 0 {
		return nullLiteral
	}

	return result
}

// handleGetActivityResult returns the mcp.ToolHandler that drives the
// get_activity_result tool. It blocks on the SDK's Get call (with an
// optional wall-clock timeout from the request) and returns the
// JSON-decoded payload as-is.
func handleGetActivityResult(cli temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args getActivityResultArgs

		err := decodeActivityArgs(req, &args)
		if err != nil {
			return nil, fmt.Errorf("parse get_activity_result args: %w", err)
		}

		if args.ActivityID == "" {
			return nil, errActivityIDRequired
		}

		if args.TimeoutSeconds != nil && *args.TimeoutSeconds <= 0 {
			return nil, errTimeoutInvalid
		}

		handle := cli.GetActivityHandle(client.GetActivityHandleOptions{
			ActivityID: args.ActivityID,
			RunID:      args.RunID,
		})

		result, getErr := pollActivityResult(ctx, handle, args.TimeoutSeconds)
		if getErr != nil {
			return nil, fmt.Errorf("get_activity_result: %w", getErr)
		}

		return marshalActivityToolResult(map[string]any{
			"activity_id": args.ActivityID,
			"run_id":      resolveActivityRunID(args.RunID, handle),
			"result":      normalizeNilResult(result),
		})
	}
}

// pollActivityResult calls handle.Get with an optional wall-clock
// timeout derived from timeoutSeconds (nil means no timeout). Extracted
// to keep handleGetActivityResult under the cognitive-complexity cap.
func pollActivityResult(
	ctx context.Context,
	handle activityHandle,
	timeoutSeconds *int,
) (json.RawMessage, error) {
	callCtx := ctx
	if timeoutSeconds != nil {
		var cancel context.CancelFunc

		callCtx, cancel = context.WithTimeout(
			ctx,
			time.Duration(*timeoutSeconds)*time.Second,
		)
		defer cancel()
	}

	var result json.RawMessage

	err := handle.Get(callCtx, &result)
	if err != nil {
		return nil, fmt.Errorf("poll activity result: %w", err)
	}

	return result, nil
}

// resolveActivityRunID returns args.RunID if non-empty, otherwise the
// handle's run ID (or "" if handle is nil).
func resolveActivityRunID(argsRunID string, handle activityHandle) string {
	if argsRunID != "" {
		return argsRunID
	}

	if handle == nil {
		return ""
	}

	return handle.GetRunID()
}

// handleDescribeActivity returns the mcp.ToolHandler that drives the
// describe_activity tool. It returns a flattened summary of the
// ActivityExecutionDescription — full ScheduledExecutionInfo / attempt
// details are projected to the fields the model needs to reason about
// status, attempt count, last failure, and worker identity.
func handleDescribeActivity(cli temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args describeActivityArgs

		err := decodeActivityArgs(req, &args)
		if err != nil {
			return nil, fmt.Errorf("parse describe_activity args: %w", err)
		}

		if args.ActivityID == "" {
			return nil, errActivityIDRequired
		}

		handle := cli.GetActivityHandle(client.GetActivityHandleOptions{
			ActivityID: args.ActivityID,
			RunID:      args.RunID,
		})

		description, descErr := handle.Describe(ctx, client.DescribeActivityOptions{})
		if descErr != nil {
			return nil, fmt.Errorf("describe_activity: %w", descErr)
		}

		return marshalActivityToolResult(activityDescriptionToMap(description, args.RunID))
	}
}

// activityDescriptionToMap projects an ActivityExecutionDescription
// into the model-facing response map. The fields are intentionally
// flat (no nested ActivityExecutionInfo) so the agent can read the
// tool output without unwrapping.
func activityDescriptionToMap(
	description *client.ActivityExecutionDescription,
	requestedRunID string,
) map[string]any {
	lastFailure := ""
	if description != nil {
		failureErr := description.GetLastFailure()
		if failureErr != nil {
			lastFailure = failureErr.Error()
		}
	}

	lastWorker := ""
	if description != nil {
		lastWorker = description.LastWorkerIdentity
	}

	runID := requestedRunID
	if runID == "" && description != nil {
		runID = description.ActivityRunID
	}

	startTime := ""
	if description != nil && !description.LastStartedTime.IsZero() {
		startTime = description.LastStartedTime.Format(time.RFC3339Nano)
	}

	closeTime := ""
	if description != nil && !description.CloseTime.IsZero() {
		closeTime = description.CloseTime.Format(time.RFC3339Nano)
	}

	return map[string]any{
		"activity_id":           description.ActivityID,
		"run_id":                runID,
		"activity_type":         description.ActivityType,
		"task_queue":            description.TaskQueue,
		"status":                activityExecutionStatusName(description.Status),
		"status_code":           int32(description.Status),
		"attempt":               description.Attempt,
		"start_time":            startTime,
		"close_time":            closeTime,
		"execution_duration_ms": description.ExecutionDuration.Milliseconds(),
		"last_failure":          lastFailure,
		"last_worker_identity":  lastWorker,
	}
}

// handleListActivities returns the mcp.ToolHandler that drives the
// list_activities tool. Pagination is implemented locally over the
// SDK iterator: skip up to `next_page_token` items, collect up to
// `page_size` items, then return a base64-encoded skip count as
// next_page_token if the page was full. Empty next_page_token means
// "no more results".
func handleListActivities(cli temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args listActivitiesArgs

		err := decodeActivityArgs(req, &args)
		if err != nil {
			return nil, fmt.Errorf("parse %s args: %w", listActivitiesName, err)
		}

		var (
			skip     int
			pageSize int
		)

		err = resolveListPagination(args, &skip, &pageSize)
		if err != nil {
			return nil, err
		}

		result, listErr := cli.ListActivities(ctx, client.ListActivitiesOptions{Query: args.Query})
		if listErr != nil {
			return nil, fmt.Errorf("%s: %w", listActivitiesName, listErr)
		}

		var hasMore bool

		activities, iterErr := collectActivitiesPage(result.Results, skip, pageSize, &hasMore)
		if iterErr != nil {
			return nil, fmt.Errorf("%s: iterate: %w", listActivitiesName, iterErr)
		}

		return marshalActivityToolResult(map[string]any{
			"activities": activities,
			"count":      len(activities),
			"next_page_token": encodeNextPageToken(pageCursor{
				Skip:     skip,
				PageSize: pageSize,
				HasMore:  hasMore,
			}),
		})
	}
}

// activitySummary is the per-record projection used in list_activities
// responses. Kept private to this file so future SDK changes don't
// leak into the public tool schema.
type activitySummary struct {
	ActivityID   string `json:"activity_id"`
	RunID        string `json:"run_id"`
	ActivityType string `json:"activity_type"`
	TaskQueue    string `json:"task_queue"`
	Status       string `json:"status"`
	StatusCode   int32  `json:"status_code"`
	ScheduleTime string `json:"schedule_time"`
	CloseTime    string `json:"close_time"`
}

// resolveListPagination decodes the pagination args (next_page_token +
// page_size) and writes the resulting (skip, pageSize) into the
// provided output pointer. The two-return shape keeps the function
// under the project-wide function-result-limit cap.
func resolveListPagination(args listActivitiesArgs, skipOut, pageSizeOut *int) error {
	skip, tokenErr := decodeActivityToken(args.NextPageToken)
	if tokenErr != nil {
		return tokenErr
	}

	*skipOut = skip

	pageSize := defaultListPageSize
	if args.PageSize != nil {
		if *args.PageSize <= 0 {
			return errPageSizeInvalid
		}

		pageSize = *args.PageSize
	}

	*pageSizeOut = pageSize

	return nil
}

// collectActivitiesPage iterates the SDK iterator, skipping `skip`
// items and collecting up to `pageSize` items. hasMoreOut is set to
// true when the iterator produced at least one record past the
// collected page (so callers can expose a next_page_token).
func collectActivitiesPage(
	results iter.Seq2[*client.ActivityExecutionInfo, error],
	skip, pageSize int,
	hasMoreOut *bool,
) ([]activitySummary, error) {
	activities := make([]activitySummary, 0, pageSize)
	seen := 0
	collected := 0

	*hasMoreOut = false

	for info, iterErr := range results {
		if iterErr != nil {
			return nil, iterErr
		}

		if seen < skip {
			seen++

			continue
		}

		if collected >= pageSize {
			*hasMoreOut = true

			break
		}

		activities = append(activities, activityInfoToSummary(info))
		seen++
		collected++
	}

	return activities, nil
}

// activityInfoToSummary projects a single ActivityExecutionInfo into
// the activitySummary JSON-friendly shape.
func activityInfoToSummary(info *client.ActivityExecutionInfo) activitySummary {
	scheduleTime := ""
	if !info.ScheduleTime.IsZero() {
		scheduleTime = info.ScheduleTime.Format(time.RFC3339Nano)
	}

	closeTime := ""
	if !info.CloseTime.IsZero() {
		closeTime = info.CloseTime.Format(time.RFC3339Nano)
	}

	return activitySummary{
		ActivityID:   info.ActivityID,
		RunID:        info.ActivityRunID,
		ActivityType: info.ActivityType,
		TaskQueue:    info.TaskQueue,
		Status:       activityExecutionStatusName(info.Status),
		StatusCode:   int32(info.Status),
		ScheduleTime: scheduleTime,
		CloseTime:    closeTime,
	}
}

// pageCursor bundles the skip + pageSize state used to compute a
// pagination cursor. The HasMore boolean is set by collectActivitiesPage;
// PageSize is the upper bound on items returned.
type pageCursor struct {
	Skip     int
	PageSize int
	HasMore  bool
}

// encodeNextPageToken returns the encoded skip count when cursor.HasMore
// is true, or "" otherwise. The skip + pageSize offset encodes the
// next-page start position so the same token can be passed back to
// resume from where the previous page ended.
func encodeNextPageToken(cursor pageCursor) string {
	if !cursor.HasMore {
		return ""
	}

	return encodeActivityToken(cursor.Skip + cursor.PageSize)
}

// handleCountActivities returns the mcp.ToolHandler that drives the
// count_activities tool. Returns the aggregate count and any
// aggregation groups the server returned.
func handleCountActivities(cli temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args countActivitiesArgs

		decodeErr := decodeActivityArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse count_activities args: %w", decodeErr)
		}

		result, countErr := cli.CountActivities(
			ctx,
			client.CountActivitiesOptions{Query: args.Query},
		)
		if countErr != nil {
			return nil, fmt.Errorf("count_activities: %w", countErr)
		}

		type groupSummary struct {
			GroupValues []any `json:"group_values"`
			Count       int64 `json:"count"`
		}

		groups := make([]groupSummary, 0, len(result.Groups))
		for _, group := range result.Groups {
			groups = append(groups, groupSummary{
				GroupValues: group.GroupValues,
				Count:       group.Count,
			})
		}

		return marshalActivityToolResult(map[string]any{
			"query":  args.Query,
			"count":  result.Count,
			"groups": groups,
		})
	}
}

// handleCancelActivity returns the mcp.ToolHandler that drives the
// cancel_activity tool. Cancellation is idempotent on the server —
// re-canceling an already-canceled or already-finished activity is a
// no-op success.
func handleCancelActivity(cli temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args cancelActivityArgs

		decodeErr := decodeActivityArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse %s args: %w", cancelActivityName, decodeErr)
		}

		params := lifecycleOpParams{
			cli:         cli,
			activityID:  args.ActivityID,
			runID:       args.RunID,
			reason:      args.Reason,
			toolName:    cancelActivityName,
			statusLabel: "canceled",
		}

		return runActivityLifecycleOp(&params, func(handle activityHandle) error {
			return handle.Cancel(ctx, client.CancelActivityOptions{Reason: args.Reason})
		})
	}
}

// handleTerminateActivity returns the mcp.ToolHandler that drives the
// terminate_activity tool. Termination is idempotent on the server and
// is the forceful counterpart to cancellation.
func handleTerminateActivity(cli temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args terminateActivityArgs

		decodeErr := decodeActivityArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse %s args: %w", terminateActivityName, decodeErr)
		}

		params := lifecycleOpParams{
			cli:         cli,
			activityID:  args.ActivityID,
			runID:       args.RunID,
			reason:      args.Reason,
			toolName:    terminateActivityName,
			statusLabel: "terminated",
		}

		return runActivityLifecycleOp(&params, func(handle activityHandle) error {
			return handle.Terminate(ctx, client.TerminateActivityOptions{Reason: args.Reason})
		})
	}
}

// runActivityLifecycleOp is the shared body of handleCancelActivity and
// handleTerminateActivity. It validates the activity_id, fetches the
// handle, calls opFunc, and marshals the result envelope. opFunc is the
// per-tool SDK call; toolName is used in the wrapped error message;
// statusLabel is the value placed in the response's "status" field.
func runActivityLifecycleOp(
	params *lifecycleOpParams,
	opFunc func(activityHandle) error,
) (*mcp.CallToolResult, error) {
	if params.activityID == "" {
		return nil, errActivityIDRequired
	}

	handle := params.cli.GetActivityHandle(client.GetActivityHandleOptions{
		ActivityID: params.activityID,
		RunID:      params.runID,
	})

	opErr := opFunc(handle)
	if opErr != nil {
		return nil, fmt.Errorf("%s: %w", params.toolName, opErr)
	}

	resolvedRunID := params.runID
	if handle != nil && resolvedRunID == "" {
		resolvedRunID = handle.GetRunID()
	}

	return marshalActivityToolResult(map[string]any{
		"status":      params.statusLabel,
		"activity_id": params.activityID,
		"run_id":      resolvedRunID,
		"reason":      params.reason,
	})
}

// lifecycleOpParams bundles the inputs to runActivityLifecycleOp so
// the helper doesn't blow past the argument-limit rule when its
// callers need every field.
type lifecycleOpParams struct {
	cli         temporalClient
	activityID  string
	runID       string
	reason      string
	toolName    string
	statusLabel string
}
