// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package temporal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	sdkclient "go.temporal.io/sdk/client"
)

// MaxListWorkflowsPageSize caps the page_size the list_workflows tool
// will accept; the Temporal server itself rejects values > 1000.
const maxListWorkflowsPageSize = 1000

// Response key constants. Hoisted so the linter's add-constant check
// stays satisfied and the JSON shape is documented in one place.
const (
	keyWorkflowID   = "workflow_id"
	keyRunID        = "run_id"
	keyStatus       = "status"
	keyStartTime    = "start_time"
	keyCloseTime    = "close_time"
	keyWorkflowType = "workflow_type"
)

// --- Request argument shapes ---

// startWorkflowArgs is the JSON input to the start_workflow tool. The
// three required fields (workflow_name, workflow_id, task_queue) mirror
// the SDK's StartWorkflowOptions + workflow type name. Args is
// json.RawMessage because the workflow's argument types are user-defined.
type startWorkflowArgs struct {
	WorkflowName string          `json:"workflow_name"`
	WorkflowID   string          `json:"workflow_id"`
	TaskQueue    string          `json:"task_queue"`
	Args         json.RawMessage `json:"args,omitempty"`
}

// cancelWorkflowArgs is the JSON input to the cancel_workflow tool.
// RunID is optional; the empty string targets the latest run.
type cancelWorkflowArgs struct {
	WorkflowID string `json:"workflow_id"`
	RunID      string `json:"run_id,omitempty"`
}

// terminateWorkflowArgs is the JSON input to the terminate_workflow
// tool. Reason and Details are optional; the SDK defaults Reason to
// "" and Details to nil.
type terminateWorkflowArgs struct {
	WorkflowID string          `json:"workflow_id"`
	RunID      string          `json:"run_id,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Details    json.RawMessage `json:"details,omitempty"`
}

// getWorkflowResultArgs is the JSON input to the get_workflow_result
// tool. RunID is optional; the empty string targets the latest run.
type getWorkflowResultArgs struct {
	WorkflowID string `json:"workflow_id"`
	RunID      string `json:"run_id,omitempty"`
}

// describeWorkflowArgs is the JSON input to the describe_workflow
// tool. RunID is optional; the empty string targets the latest run.
type describeWorkflowArgs struct {
	WorkflowID string `json:"workflow_id"`
	RunID      string `json:"run_id,omitempty"`
}

// listWorkflowsArgs is the JSON input to the list_workflows tool.
// Query is the Temporal Visibility query language expression;
// PageSize defaults to defaultListWorkflowsPageSize when zero;
// NextPageToken is the opaque token returned by a previous call
// (base64-encoded to keep the JSON shape copy-paste friendly).
type listWorkflowsArgs struct {
	Query         string `json:"query,omitempty"`
	PageSize      int    `json:"page_size,omitempty"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

// getWorkflowHistoryArgs is the JSON input to the
// get_workflow_history tool. RunID is optional; MaxEvents defaults to
// defaultHistoryMaxEvents when zero.
type getWorkflowHistoryArgs struct {
	WorkflowID string `json:"workflow_id"`
	RunID      string `json:"run_id,omitempty"`
	MaxEvents  int    `json:"max_events,omitempty"`
}

// continueAsNewArgs is the JSON input to the continue_as_new tool.
// SignalName is the signal the workflow listens for; SignalArgs is
// passed through to the workflow's signal handler.
type continueAsNewArgs struct {
	WorkflowID string          `json:"workflow_id"`
	SignalName string          `json:"signal_name"`
	SignalArgs json.RawMessage `json:"signal_args,omitempty"`
}

// --- Sentinel errors ---

var (
	// errWorkflowIDRequired is returned by every workflow handler
	// when the request omits a non-empty workflow_id. Sharing the
	// sentinel across handlers keeps the model-facing error
	// message consistent.
	errWorkflowIDRequired = errors.New("temporal tool: workflow_id is required")

	// errWorkflowSignalNameRequired is the workflow-specific
	// rename of errSignalNameRequired (the query-signal branch
	// already declared the same message text under that name).
	// Lives here so the continue_as_new handler has a
	// workflow-scoped lookup without colliding with the
	// query_signal_handlers.go sentinel.
	errWorkflowSignalNameRequired = errors.New("temporal tool: signal_name is required")

	// defaultTerminateReason is used by the terminate_workflow handler
	// when the caller omits the reason field. Matches the upstream
	// Python temporal-mcp behavior so users get a familiar message
	// in the server-side WorkflowExecutionTerminated event.
	defaultTerminateReason = "Terminated via MCP"
)

// --- Shared helpers ---

// decodeWorkflowArgs unmarshals the request's arguments JSON into target. It
// is the one place every tool handler does JSON-decode so the per-tool
// handlers stay focused on per-tool argument validation.
//
//nolint:wrapcheck // external json.Unmarshal error returned verbatim for handler parsing failures
func decodeWorkflowArgs(req *mcp.CallToolRequest, target any) error {
	return json.Unmarshal(req.Params.Arguments, target)
}

// marshalWorkflowToolResult marshals value as JSON and wraps it in a
// CallToolResult. Per the MCP spec, StructuredContent is attached
// only when the marshaled value is a JSON object (so plain arrays,
// primitives, or malformed values are conveyed through Content alone).
// The same probe-and-set shape is used in woodpecker/woodpecker_handlers.go.
func marshalWorkflowToolResult(value any) (*mcp.CallToolResult, error) {
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

// decodeWorkflowArgsSlice unmarshals raw into a []any suitable for the SDK's
// variadic StartWorkflow / SignalWorkflow / TerminateWorkflow args.
// Empty/nil raw produces an empty slice; a non-array JSON value
// surfaces a parse error so the caller sees a clear failure rather than
// a silent empty-args call.
//
// The signature matches the query_signal_handlers.go helper of the
// same name (single positional arg). Splitting them per-feature is
// intentional — adding a "field" parameter here was workspace-local
// dead weight.
func decodeWorkflowArgsSlice(raw json.RawMessage) ([]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var out []any

	err := json.Unmarshal(raw, &out)
	if err != nil {
		return nil, fmt.Errorf("parse args: %w", err)
	}

	return out, nil
}

// decodeArgsRequest unmarshals the tool request's arguments JSON into
// the supplied target and wraps any decode error with the tool name.
// Returning a single error message keeps the per-handler error paths
// under the gocognit threshold.
func decodeArgsRequest(toolName string, req *mcp.CallToolRequest, target any) error {
	decodeErr := decodeWorkflowArgs(req, target)
	if decodeErr != nil {
		return fmt.Errorf("parse %s args: %w", toolName, decodeErr)
	}

	return nil
}

// requireWorkflowID returns errWorkflowIDRequired when id is empty.
// Hoisted so each handler's cognitive complexity stays under the linter
// threshold (10).
func requireWorkflowID(id string) error {
	if id == "" {
		return errWorkflowIDRequired
	}

	return nil
}

// historyEventSummary flattens a *history.HistoryEvent into the JSON
// shape the get_workflow_history output schema documents. We extract
// only event_id, event_type, and event_time to keep the response
// payload small — the raw event carries dozens of fields whose JSON
// shape is unstable across Temporal versions.
func historyEventSummary(event *history.HistoryEvent) map[string]any {
	if event == nil {
		return nil
	}

	return map[string]any{
		"event_id":   event.EventId,
		"event_type": event.EventType.String(),
		"event_time": event.EventTime.String(),
	}
}

// executionSummary flattens a *workflow.WorkflowExecutionInfo into the
// JSON shape the list_workflows output schema documents. The type lives
// in go.temporal.io/api/workflow/v1 (the visibility proto) — it is the
// per-row payload the server returns inside
// workflowservice.ListWorkflowExecutionsResponse.Executions.
func executionSummary(info *workflow.WorkflowExecutionInfo) map[string]any {
	if info == nil {
		return nil
	}

	summary := map[string]any{
		keyWorkflowID:   info.GetExecution().GetWorkflowId(),
		keyRunID:        info.GetExecution().GetRunId(),
		keyWorkflowType: info.GetType().GetName(),
		keyStatus:       info.GetStatus().String(),
	}

	if start := info.GetStartTime(); start != nil {
		summary[keyStartTime] = start.String()
	}

	if closedAt := info.GetCloseTime(); closedAt != nil {
		summary[keyCloseTime] = closedAt.String()
	}

	return summary
}

// describeSummary flattens a *sdkclient.WorkflowExecutionDescription
// into the JSON shape the describe_workflow output schema documents.
func describeSummary(desc *sdkclient.WorkflowExecutionDescription) map[string]any {
	summary := map[string]any{
		keyWorkflowID:   desc.WorkflowExecution.ID,
		keyRunID:        desc.WorkflowExecution.RunID,
		keyWorkflowType: desc.WorkflowType.Name,
		keyStatus:       desc.Status.String(),
		"status_code":   int32(desc.Status),
	}

	if !desc.WorkflowStartTime.IsZero() {
		summary[keyStartTime] = desc.WorkflowStartTime.String()
	}

	if exec := desc.ExecutionTime; exec != nil {
		summary["execution_time"] = exec.String()
	}

	if closedAt := desc.WorkflowCloseTime; closedAt != nil {
		summary[keyCloseTime] = closedAt.String()
	}

	return summary
}

// decodeListRequest assembles the SDK's ListWorkflowExecutionsRequest
// from the parsed args, applying defaults and bounds. The Temporal
// server caps page_size at 1000; we clamp on the way down so the
// conversion to int32 stays in range.
func decodeListRequest(
	args listWorkflowsArgs,
) (*workflowservice.ListWorkflowExecutionsRequest, error) {
	pageSize := args.PageSize
	if pageSize <= 0 {
		pageSize = defaultListWorkflowsPageSize
	}

	if pageSize > maxListWorkflowsPageSize {
		pageSize = maxListWorkflowsPageSize
	}

	var nextPageToken []byte

	if args.NextPageToken != "" {
		decoded, decodeErr := base64.StdEncoding.DecodeString(args.NextPageToken)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse next_page_token: %w", decodeErr)
		}

		nextPageToken = decoded
	}

	//nolint:gosec // pageSize is bounded above to maxListWorkflowsPageSize (1000).
	return &workflowservice.ListWorkflowExecutionsRequest{
		Namespace:     "",
		PageSize:      int32(pageSize),
		NextPageToken: nextPageToken,
		Query:         args.Query,
	}, nil
}

// drainHistoryIterator reads up to maxEvents events from iter and
// returns the slice of summaries plus whether the iterator still had
// more events after the cap (truncated). The iterator's Next errors
// are returned verbatim so the caller can wrap them with the tool
// name.
//
//nolint:wrapcheck,revive // SDK signature; Next() error returned verbatim for handler wrapping.
func drainHistoryIterator(
	iter sdkclient.HistoryEventIterator,
	maxEvents int,
) (events []map[string]any, truncated bool, err error) {
	events = make([]map[string]any, 0, maxEvents)

	for range maxEvents {
		if !iter.HasNext() {
			break
		}

		event, nextErr := iter.Next()
		if nextErr != nil {
			return nil, false, nextErr
		}

		events = append(events, historyEventSummary(event))
	}

	if iter.HasNext() {
		truncated = true
	}

	return events, truncated, nil
}

// --- Tool handler factories ---

// handleStartWorkflow returns the mcp.ToolHandler that drives the
// start_workflow tool. It decodes the three required fields, converts
// the args array, and calls client.StartWorkflow.
func handleStartWorkflow(client temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args startWorkflowArgs

		decodeErr := decodeArgsRequest("start_workflow", req, &args)
		if decodeErr != nil {
			return nil, decodeErr
		}

		validateErr := validateStartWorkflowArgs(args)
		if validateErr != nil {
			return nil, validateErr
		}

		decodedArgs, argsErr := decodeWorkflowArgsSlice(args.Args)
		if argsErr != nil {
			return nil, argsErr
		}

		opts := sdkclient.StartWorkflowOptions{
			ID:        args.WorkflowID,
			TaskQueue: args.TaskQueue,
		}

		run, runErr := client.StartWorkflow(ctx, opts, args.WorkflowName, decodedArgs...)
		if runErr != nil {
			return nil, fmt.Errorf("start_workflow: %w", runErr)
		}

		return marshalWorkflowToolResult(map[string]any{
			keyWorkflowID: run.GetID(),
			keyRunID:      run.GetRunID(),
			keyStatus:     "started",
		})
	}
}

// validateStartWorkflowArgs enforces the three required fields
// (workflow_name, workflow_id, task_queue) for the start_workflow
// tool. Hoisted out of handleStartWorkflow to keep the handler's
// cognitive complexity under the linter threshold.
func validateStartWorkflowArgs(args startWorkflowArgs) error {
	if args.WorkflowName == "" {
		// Declared in schedule_handlers.go and reused here so the
		// model-facing error message is consistent across the
		// schedule + workflow tool groups.
		return errWorkflowNameRequired
	}

	idErr := requireWorkflowID(args.WorkflowID)
	if idErr != nil {
		return idErr
	}

	if args.TaskQueue == "" {
		// Declared in schedule_handlers.go and reused here.
		return errTaskQueueRequired
	}

	return nil
}

// handleCancelWorkflow returns the mcp.ToolHandler that drives the
// cancel_workflow tool.
func handleCancelWorkflow(client temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args cancelWorkflowArgs

		decodeErr := decodeArgsRequest("cancel_workflow", req, &args)
		if decodeErr != nil {
			return nil, decodeErr
		}

		err := requireWorkflowID(args.WorkflowID)
		if err != nil {
			return nil, err
		}

		cancelErr := client.CancelWorkflow(ctx, args.WorkflowID, args.RunID)
		if cancelErr != nil {
			return nil, fmt.Errorf("cancel_workflow: %w", cancelErr)
		}

		return marshalWorkflowToolResult(map[string]any{
			"canceled":    true,
			keyWorkflowID: args.WorkflowID,
			keyRunID:      args.RunID,
		})
	}
}

// handleTerminateWorkflow returns the mcp.ToolHandler that drives the
// terminate_workflow tool. Reason defaults to defaultTerminateReason
// when the caller omits it.
func handleTerminateWorkflow(client temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args terminateWorkflowArgs

		decodeErr := decodeArgsRequest("terminate_workflow", req, &args)
		if decodeErr != nil {
			return nil, decodeErr
		}

		err := requireWorkflowID(args.WorkflowID)
		if err != nil {
			return nil, err
		}

		reason := args.Reason
		if reason == "" {
			reason = defaultTerminateReason
		}

		decodedDetails, detailsErr := decodeWorkflowArgsSlice(args.Details)
		if detailsErr != nil {
			return nil, detailsErr
		}

		termErr := client.TerminateWorkflow(
			ctx, args.WorkflowID, args.RunID, reason, decodedDetails...,
		)
		if termErr != nil {
			return nil, fmt.Errorf("terminate_workflow: %w", termErr)
		}

		return marshalWorkflowToolResult(map[string]any{
			"terminated":  true,
			keyWorkflowID: args.WorkflowID,
			keyRunID:      args.RunID,
			"reason":      reason,
		})
	}
}

// handleGetWorkflowResult returns the mcp.ToolHandler that drives the
// get_workflow_result tool. The SDK's WorkflowRun.Get blocks until
// the workflow completes; caller-side timeouts come from the request
// context.
func handleGetWorkflowResult(client temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args getWorkflowResultArgs

		decodeErr := decodeArgsRequest("get_workflow_result", req, &args)
		if decodeErr != nil {
			return nil, decodeErr
		}

		err := requireWorkflowID(args.WorkflowID)
		if err != nil {
			return nil, err
		}

		run := client.GetWorkflow(ctx, args.WorkflowID, args.RunID)

		var result any

		getErr := run.Get(ctx, &result)
		if getErr != nil {
			return nil, fmt.Errorf("get_workflow_result: %w", getErr)
		}

		return marshalWorkflowToolResult(map[string]any{
			keyWorkflowID: args.WorkflowID,
			keyRunID:      run.GetRunID(),
			"result":      result,
		})
	}
}

// handleDescribeWorkflow returns the mcp.ToolHandler that drives the
// describe_workflow tool. The SDK returns a
// *WorkflowExecutionDescription with all the WorkflowExecutionInfo +
// close event; we flatten the commonly-consulted fields into a stable
// JSON shape via describeSummary.
func handleDescribeWorkflow(client temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args describeWorkflowArgs

		decodeErr := decodeArgsRequest("describe_workflow", req, &args)
		if decodeErr != nil {
			return nil, decodeErr
		}

		err := requireWorkflowID(args.WorkflowID)
		if err != nil {
			return nil, err
		}

		desc, descErr := client.DescribeWorkflow(ctx, args.WorkflowID, args.RunID)
		if descErr != nil {
			return nil, fmt.Errorf("describe_workflow: %w", descErr)
		}

		return marshalWorkflowToolResult(describeSummary(desc))
	}
}

// handleListWorkflows returns the mcp.ToolHandler that drives the
// list_workflows tool. PageSize defaults to defaultListWorkflowsPageSize
// when zero. The next_page_token is base64-encoded in the JSON shape so
// the user can copy-paste it back verbatim into a follow-up call.
func handleListWorkflows(client temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args listWorkflowsArgs

		decodeErr := decodeArgsRequest("list_workflows", req, &args)
		if decodeErr != nil {
			return nil, decodeErr
		}

		request, reqErr := decodeListRequest(args)
		if reqErr != nil {
			return nil, reqErr
		}

		resp, respErr := client.ListWorkflow(ctx, request)
		if respErr != nil {
			return nil, fmt.Errorf("list_workflows: %w", respErr)
		}

		executions := make([]map[string]any, 0, len(resp.GetExecutions()))
		for _, info := range resp.GetExecutions() {
			executions = append(executions, executionSummary(info))
		}

		payload := map[string]any{"executions": executions}

		if token := resp.GetNextPageToken(); len(token) > 0 {
			payload["next_page_token"] = base64.StdEncoding.EncodeToString(token)
		}

		return marshalWorkflowToolResult(payload)
	}
}

// handleGetWorkflowHistory returns the mcp.ToolHandler that drives the
// get_workflow_history tool. The SDK returns a HistoryEventIterator;
// we drain up to maxEvents (default defaultHistoryMaxEvents) and stop.
// truncated is true when the iterator still had more events when the
// cap was hit so the caller knows to ask for more.
func handleGetWorkflowHistory(client temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args getWorkflowHistoryArgs

		decodeErr := decodeArgsRequest("get_workflow_history", req, &args)
		if decodeErr != nil {
			return nil, decodeErr
		}

		err := requireWorkflowID(args.WorkflowID)
		if err != nil {
			return nil, err
		}

		maxEvents := args.MaxEvents
		if maxEvents <= 0 {
			maxEvents = defaultHistoryMaxEvents
		}

		iter := client.GetWorkflowHistory(
			ctx, args.WorkflowID, args.RunID,
			false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT,
		)

		events, truncated, drainErr := drainHistoryIterator(iter, maxEvents)
		if drainErr != nil {
			return nil, fmt.Errorf("get_workflow_history: %w", drainErr)
		}

		return marshalWorkflowToolResult(map[string]any{
			keyWorkflowID: args.WorkflowID,
			keyRunID:      args.RunID,
			"events":      events,
			"truncated":   truncated,
		})
	}
}

// handleContinueAsNew returns the mcp.ToolHandler that drives the
// continue_as_new tool. The Go SDK has no dedicated client API for
// continue-as-new, so this handler delivers a signal and trusts the
// workflow's signal handler to call workflow.NewContinueAsNewError.
func handleContinueAsNew(client temporalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args continueAsNewArgs

		decodeErr := decodeArgsRequest("continue_as_new", req, &args)
		if decodeErr != nil {
			return nil, decodeErr
		}

		validateErr := validateContinueAsNewArgs(args)
		if validateErr != nil {
			return nil, validateErr
		}

		decodedArgs, sigErr := decodeWorkflowArgsSlice(args.SignalArgs)
		if sigErr != nil {
			return nil, sigErr
		}

		// SignalWorkflow takes a single arg interface{}. For
		// variadic-style signal args we send the decoded slice as
		// one payload (the SDK marshals it via the default JSON
		// codec); the workflow's signal handler unpacks it. An
		// empty slice becomes a non-nil empty payload so the
		// workflow's handler can always inspect args.
		payload := decodedArgs
		if payload == nil {
			payload = []any{}
		}

		signalErr := client.SignalWorkflow(
			ctx, args.WorkflowID, "", args.SignalName, payload,
		)
		if signalErr != nil {
			return nil, fmt.Errorf("continue_as_new: %w", signalErr)
		}

		return marshalWorkflowToolResult(map[string]any{
			"signaled":    true,
			keyWorkflowID: args.WorkflowID,
			"signal_name": args.SignalName,
		})
	}
}

// validateContinueAsNewArgs enforces the two required fields
// (workflow_id, signal_name) for the continue_as_new tool. Hoisted
// out of handleContinueAsNew to keep the handler's cognitive
// complexity under the linter threshold.
func validateContinueAsNewArgs(args continueAsNewArgs) error {
	err := requireWorkflowID(args.WorkflowID)
	if err != nil {
		return err
	}

	if args.SignalName == "" {
		return errWorkflowSignalNameRequired
	}

	return nil
}
