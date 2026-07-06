// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:revive // handlers cluster related calls; SDK signatures force longer lines
package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	commonpb "go.temporal.io/api/common/v1"
)

// --- Request argument shapes ---

// queryWorkflowArgs is the JSON input to the query_workflow tool.
// workflow_id and query_name are required; run_id and args are
// optional. Args is json.RawMessage because the workflow's query
// handler argument types are user-defined.
type queryWorkflowArgs struct {
	WorkflowID string          `json:"workflow_id"`
	RunID      string          `json:"run_id,omitempty"`
	QueryName  string          `json:"query_name"`
	Args       json.RawMessage `json:"args,omitempty"`
}

// signalWorkflowArgs is the JSON input to the signal_workflow tool.
// workflow_id and signal_name are required; run_id and args are
// optional. Args is json.RawMessage because the workflow's signal
// handler argument types are user-defined.
type signalWorkflowArgs struct {
	WorkflowID string          `json:"workflow_id"`
	RunID      string          `json:"run_id,omitempty"`
	SignalName string          `json:"signal_name"`
	Args       json.RawMessage `json:"args,omitempty"`
}

// --- Sentinel errors ---

var (
	errQueryWorkflowIDRequired = errors.New(
		"temporal tool: query_workflow: workflow_id is required",
	)
	errQueryNameRequired = errors.New(
		"temporal tool: query_workflow: query_name is required",
	)
	errSignalWorkflowIDRequired = errors.New(
		"temporal tool: signal_workflow: workflow_id is required",
	)
	errSignalNameRequired = errors.New(
		"temporal tool: signal_workflow: signal_name is required",
	)
)

// --- Handler-scoped interface ---
//
// querySignalClient is the narrow slice of the Temporal SDK client
// that the two handlers in this file need. It exists so tests can
// inject a fake without depending on the full client.Client surface
// and so this issue does not extend the package-level temporalClient
// interface (the spec's "no interface changes" rule). *clientManager
// satisfies it via the forwarder methods in client.go.

type querySignalClient interface {
	QueryWorkflowWithOptions(
		ctx context.Context,
		request *client.QueryWorkflowWithOptionsRequest,
	) (*client.QueryWorkflowWithOptionsResponse, error)
	SignalWorkflow(
		ctx context.Context,
		workflowID, runID, signalName string,
		arg any,
	) error
}

// --- Shared helpers ---

// marshalQuerySignalToolResult marshals value as JSON and wraps it in a
// CallToolResult. Per the MCP spec, StructuredContent is attached
// only when the marshaled value is a JSON object (so plain arrays,
// primitives, or malformed values are conveyed through Content alone).
// The same probe-and-set shape is used in
// woodpecker/woodpecker_handlers.go.
func marshalQuerySignalToolResult(value any) (*mcp.CallToolResult, error) {
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

// decodeQuerySignalArgs unmarshals the tool request's arguments JSON
// into target. It is the per-handler decode helper that wraps the
// underlying json.Unmarshal with a tool-name prefix so the error
// message names the failing tool. A nil Params (which only happens
// in tests that construct a bare *mcp.CallToolRequest) is treated as
// "no arguments" — same as the dispatcher does in production.
func decodeQuerySignalArgs(toolName string, req *mcp.CallToolRequest, target any) error {
	if req == nil || req.Params == nil {
		return nil
	}

	err := json.Unmarshal(req.Params.Arguments, target)
	if err != nil {
		return fmt.Errorf("parse %s args: %w", toolName, err)
	}

	return nil
}

// querySignalNullLiteral is the literal "null" used to detect absent args
// in decodeArgsSlice. Declared as a var (not const) so the goconst
// linter rule doesn't trip over the string literal in the helper;
// per project convention, named sentinel values stay as vars.
var querySignalNullLiteral = "null"

// decodeArgsSlice unmarshals raw into a []any suitable for the SDK's
// variadic QueryWorkflowWithOptions.Args field. Empty/nil raw produces
// an empty slice (the SDK's "no args" shape); a non-array JSON value
// surfaces a parse error so the caller sees a clear failure rather
// than a silent empty-args call. The function is shared between the
// query and signal handlers — SignalWorkflow takes a single arg
// payload, so callers wrap the slice in a one-element payload (see
// handleSignalWorkflow below).
func decodeArgsSlice(raw json.RawMessage) ([]any, error) {
	if len(raw) == 0 || string(raw) == querySignalNullLiteral {
		return nil, nil
	}

	var out []any

	err := json.Unmarshal(raw, &out)
	if err != nil {
		return nil, fmt.Errorf("parse args: %w", err)
	}

	return out, nil
}

// decodeQueryResult pulls the *commonpb.Payloads out of an
// EncodedValue via converter.GetPayloads and decodes it through the
// default data converter into an any value. The decoded value is
// returned for json.Marshal by the caller. Any failure is wrapped
// with the tool name so the model sees a clear cause.
func decodeQueryResult(encoded converter.EncodedValue) (any, error) {
	payloads := converter.GetPayloads(encoded)
	if payloads == nil {
		return nil, errors.New("temporal tool: query_workflow: result payloads unavailable")
	}

	var result any

	err := converter.GetDefaultDataConverter().FromPayloads(payloads, &result)
	if err != nil {
		return nil, fmt.Errorf("temporal tool: query_workflow: decode result: %w", err)
	}

	return result, nil
}

// --- Tool handler factories ---

// validateQueryWorkflowArgs enforces the two required fields
// (workflow_id, query_name) for the query_workflow tool. Hoisted out
// of handleQueryWorkflow to keep the handler's cognitive complexity
// under the linter threshold (10).
func validateQueryWorkflowArgs(args queryWorkflowArgs) error {
	if args.WorkflowID == "" {
		return errQueryWorkflowIDRequired
	}

	if args.QueryName == "" {
		return errQueryNameRequired
	}

	return nil
}

// validateSignalWorkflowArgs enforces the two required fields
// (workflow_id, signal_name) for the signal_workflow tool.
func validateSignalWorkflowArgs(args signalWorkflowArgs) error {
	if args.WorkflowID == "" {
		return errSignalWorkflowIDRequired
	}

	if args.SignalName == "" {
		return errSignalNameRequired
	}

	return nil
}

// buildQueryRequest assembles the SDK's QueryWorkflowWithOptionsRequest
// from a validated args struct and the decoded query-arg slice. The
// QueryRejectCondition and Header fields are set to their zero values
// explicitly to satisfy the exhaustruct linter; the SDK defaults
// match the most common case (no rejection, no header).
func buildQueryRequest(
	args queryWorkflowArgs,
	queryArgs []any,
) *client.QueryWorkflowWithOptionsRequest {
	return &client.QueryWorkflowWithOptionsRequest{
		WorkflowID:           args.WorkflowID,
		RunID:                args.RunID,
		QueryType:            args.QueryName,
		Args:                 queryArgs,
		QueryRejectCondition: 0,
		Header:               (*commonpb.Header)(nil),
	}
}

// normalizeSignalPayload replaces a nil decodedArgs with a non-nil
// empty slice so the workflow's signal handler can always inspect
// args without a nil check. Hoisted out of handleSignalWorkflow to
// keep the handler's cognitive complexity under the linter threshold.
func normalizeSignalPayload(decodedArgs []any) []any {
	if decodedArgs == nil {
		return []any{}
	}

	return decodedArgs
}

// handleQueryWorkflow returns the mcp.ToolHandler that drives the
// query_workflow tool. It calls client.QueryWorkflowWithOptions with
// the supplied workflow_id / run_id / query_name and decodes the
// QueryResult payloads through the default data converter. The
// decoded value is JSON-marshaled into the response so the agent can
// inspect it as structured content.
func handleQueryWorkflow(client querySignalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args queryWorkflowArgs

		decodeErr := decodeQuerySignalArgs("query_workflow", req, &args)
		if decodeErr != nil {
			return nil, decodeErr
		}

		validateErr := validateQueryWorkflowArgs(args)
		if validateErr != nil {
			return nil, validateErr
		}

		queryArgs, argsErr := decodeArgsSlice(args.Args)
		if argsErr != nil {
			return nil, argsErr
		}

		result, resultErr := executeQuery(ctx, client, args, queryArgs)
		if resultErr != nil {
			return nil, resultErr
		}

		return marshalQuerySignalToolResult(map[string]any{
			"workflow_id": args.WorkflowID,
			"query_name":  args.QueryName,
			"result":      result,
		})
	}
}

// executeQuery runs the SDK call and converts the response into the
// decoded value, surfacing SDK / decode errors with the tool name
// prefix. Hoisted out of handleQueryWorkflow to keep the handler's
// cognitive complexity under the linter threshold (10).
func executeQuery(
	ctx context.Context,
	client querySignalClient,
	args queryWorkflowArgs,
	queryArgs []any,
) (any, error) {
	request := buildQueryRequest(args, queryArgs)

	response, queryErr := client.QueryWorkflowWithOptions(ctx, request)
	if queryErr != nil {
		return nil, fmt.Errorf("query_workflow: %w", queryErr)
	}

	if response == nil {
		return nil, errors.New("temporal tool: query_workflow: empty response")
	}

	if response.QueryRejected != nil {
		return nil, fmt.Errorf(
			"temporal tool: query_workflow: rejected with status %s",
			response.QueryRejected.GetStatus(),
		)
	}

	return decodeQueryResult(response.QueryResult)
}

// handleSignalWorkflow returns the mcp.ToolHandler that drives the
// signal_workflow tool. It calls client.SignalWorkflow with the
// supplied workflow_id / run_id / signal_name and the decoded args
// as a single payload (SignalWorkflow's arg is any, not variadic).
// Empty args become a non-nil empty slice so the workflow's signal
// handler can always inspect args without a nil check.
func handleSignalWorkflow(client querySignalClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args signalWorkflowArgs

		decodeErr := decodeQuerySignalArgs("signal_workflow", req, &args)
		if decodeErr != nil {
			return nil, decodeErr
		}

		validateErr := validateSignalWorkflowArgs(args)
		if validateErr != nil {
			return nil, validateErr
		}

		decodedArgs, argsErr := decodeArgsSlice(args.Args)
		if argsErr != nil {
			return nil, argsErr
		}

		signalErr := client.SignalWorkflow(
			ctx,
			args.WorkflowID,
			args.RunID,
			args.SignalName,
			normalizeSignalPayload(decodedArgs),
		)
		if signalErr != nil {
			return nil, fmt.Errorf("signal_workflow: %w", signalErr)
		}

		return marshalQuerySignalToolResult(map[string]any{
			"signaled":    true,
			"workflow_id": args.WorkflowID,
			"signal_name": args.SignalName,
		})
	}
}

// --- Per-tool description suffixes ---
//
// Kept as named consts (rather than inline string literals) so the
// lll line-length rule is happy and the per-tool phrasing is
// editable in one place. Each is prefixed by the shared
// querySignalLoop preamble when the tool is registered (see
// Connect in temporal.go).

const (
	queryWorkflowDescription = "\n\nQuery a workflow execution for " +
		"state via a named query handler. The workflow must have " +
		"registered a handler via workflow.SetQueryHandler for the " +
		"query_name to be recognized. The decoded result is returned " +
		"under result; query rejection surfaces as an error."

	signalWorkflowDescription = "\n\nDeliver an event to a workflow " +
		"execution via a named signal handler. The workflow must have " +
		"registered a handler via workflow.SetSignalHandler for the " +
		"signal_name to be recognized. Signals are NOT idempotent — " +
		"re-sending the same signal triggers a separate handler " +
		"invocation each time. The server delivers the signal " +
		"asynchronously; the response confirms delivery intent, not " +
		"that the workflow has processed it."
)
