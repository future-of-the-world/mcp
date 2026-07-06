// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:wsl_v5 // handlers cluster validation calls
package woodpecker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- Request argument shapes ---

// listReposArgs is the JSON input to the list_repos tool. all and name
// are optional; the omit-empty tags on the struct let the handler
// distinguish absent from zero.
type listReposArgs struct {
	All  bool   `json:"all,omitempty"`
	Name string `json:"name,omitempty"`
}

// listPipelinesArgs is the JSON input to the list_pipelines tool. Only
// repo_id is required; the rest are optional filters.
type listPipelinesArgs struct {
	RepoID  int    `json:"repo_id"`
	Page    int    `json:"page,omitempty"`
	PerPage int    `json:"per_page,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Event   string `json:"event,omitempty"`
	Status  string `json:"status,omitempty"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
}

// pipelineByNumberArgs is the shared input for get_pipeline,
// restart_pipeline, and cancel_pipeline. repo_id and pipeline_number
// are required.
type pipelineByNumberArgs struct {
	RepoID         int    `json:"repo_id"`
	PipelineNumber int    `json:"pipeline_number"`
	Event          string `json:"event,omitempty"`
	DeployTo       string `json:"deploy_to,omitempty"`
}

// getStepLogsArgs is the JSON input to the get_step_logs tool.
type getStepLogsArgs struct {
	RepoID         int `json:"repo_id"`
	PipelineNumber int `json:"pipeline_number"`
	StepID         int `json:"step_id"`
}

// launchPipelineArgs is the JSON input to the launch_pipeline tool.
// repo_id is required; branch and variables are optional.
type launchPipelineArgs struct {
	RepoID    int               `json:"repo_id"`
	Branch    string            `json:"branch,omitempty"`
	Variables map[string]string `json:"variables,omitempty"`
}

// --- Shared helpers ---

// decodeArgs unmarshals the request's arguments JSON into target. It
// is the one place every tool handler does JSON-decode so the per-tool
// handlers stay focused on per-tool argument validation.
//
//nolint:wrapcheck // external json.Unmarshal error returned verbatim for handler parsing failures
func decodeArgs(req *mcp.CallToolRequest, target any) error {
	return json.Unmarshal(req.Params.Arguments, target)
}

// optionalStringPtr returns &value when value is non-empty, nil otherwise.
// Used to pass the right pointer (or "absent") to the client wrapper.
func optionalStringPtr(value string) *string {
	if value == "" {
		return nil
	}

	return &value
}

// optionalIntPtr returns &v when v > 0, nil otherwise. Used for
// pagination fields where 0 means "use the server default".
func optionalIntPtr(value int) *int {
	if value <= 0 {
		return nil
	}

	return &value
}

// marshalToolResult marshals value as JSON and wraps it in a
// CallToolResult. Per the MCP spec, StructuredContent is attached
// only when the marshaled value is a JSON object (so plain arrays,
// primitives, or malformed values are conveyed through Content alone).
// The same probe-and-set shape is used in mcp/english/english.go.
func marshalToolResult(value any) (*mcp.CallToolResult, error) {
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

// --- Tool handler factories ---

// handleListRepos returns the mcp.ToolHandler that drives the
// list_repos tool.
func handleListRepos(client *woodpeckerClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args listReposArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse list_repos args: %w", decodeErr)
		}

		var allPtr *bool
		if req.Params.Arguments != nil && containsKey(req.Params.Arguments, "all") {
			value := args.All
			allPtr = &value
		}

		repos, reposErr := client.listRepos(ctx, allPtr, optionalStringPtr(args.Name))
		if reposErr != nil {
			return nil, reposErr
		}

		return marshalToolResult(map[string]any{"repos": repos})
	}
}

// handleListPipelines returns the mcp.ToolHandler that drives the
// list_pipelines tool.
func handleListPipelines(client *woodpeckerClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args listPipelinesArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse list_pipelines args: %w", decodeErr)
		}

		if args.RepoID <= 0 {
			return nil, errRepoIDRequired
		}

		opts := listPipelinesOpts{
			Page:    optionalIntPtr(args.Page),
			PerPage: optionalIntPtr(args.PerPage),
			Before:  optionalStringPtr(args.Before),
			After:   optionalStringPtr(args.After),
			Branch:  optionalStringPtr(args.Branch),
			Event:   optionalStringPtr(args.Event),
			Status:  optionalStringPtr(args.Status),
		}

		pipelines, pipelinesErr := client.listPipelines(ctx, args.RepoID, opts)
		if pipelinesErr != nil {
			return nil, pipelinesErr
		}

		return marshalToolResult(map[string]any{"pipelines": pipelines})
	}
}

// handleGetPipeline returns the mcp.ToolHandler that drives the
// get_pipeline tool.
func handleGetPipeline(client *woodpeckerClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args pipelineByNumberArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse get_pipeline args: %w", decodeErr)
		}

		if args.RepoID <= 0 {
			return nil, errRepoIDRequired
		}

		if args.PipelineNumber <= 0 {
			return nil, errPipelineNumberRequired
		}

		detail, detailErr := client.getPipeline(ctx, args.RepoID, args.PipelineNumber)
		if detailErr != nil {
			return nil, detailErr
		}

		return marshalToolResult(map[string]any{"pipeline": detail})
	}
}

// handleGetStepLogs returns the mcp.ToolHandler that drives the
// get_step_logs tool. The decoding test (UTF-8 string per entry,
// kind per LogEntryType) is exercised at the client layer; this
// handler only marshals the result.
func handleGetStepLogs(client *woodpeckerClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args getStepLogsArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse get_step_logs args: %w", decodeErr)
		}

		if args.RepoID <= 0 {
			return nil, errRepoIDRequired
		}

		if args.PipelineNumber <= 0 {
			return nil, errPipelineNumberRequired
		}

		if args.StepID <= 0 {
			return nil, errStepIDRequired
		}

		logs, logsErr := client.getStepLogs(ctx, args.RepoID, args.PipelineNumber, args.StepID)
		if logsErr != nil {
			return nil, logsErr
		}

		return marshalToolResult(map[string]any{"logs": logs})
	}
}

// handleRestartPipeline returns the mcp.ToolHandler that drives the
// restart_pipeline tool. Event and deploy_to are optional overrides.
func handleRestartPipeline(client *woodpeckerClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args pipelineByNumberArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse restart_pipeline args: %w", decodeErr)
		}

		if args.RepoID <= 0 {
			return nil, errRepoIDRequired
		}

		if args.PipelineNumber <= 0 {
			return nil, errPipelineNumberRequired
		}

		overrides := restartOverrides{
			Event:    optionalStringPtr(args.Event),
			DeployTo: optionalStringPtr(args.DeployTo),
		}

		detail, detailErr := client.restartPipeline(
			ctx,
			args.RepoID,
			args.PipelineNumber,
			overrides,
		)
		if detailErr != nil {
			return nil, detailErr
		}

		return marshalToolResult(map[string]any{"pipeline": detail})
	}
}

// handleLaunchPipeline returns the mcp.ToolHandler that drives the
// launch_pipeline tool.
func handleLaunchPipeline(client *woodpeckerClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args launchPipelineArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse launch_pipeline args: %w", decodeErr)
		}

		if args.RepoID <= 0 {
			return nil, errRepoIDRequired
		}

		detail, detailErr := client.launchPipeline(ctx, args.RepoID,
			optionalStringPtr(args.Branch), args.Variables)
		if detailErr != nil {
			return nil, detailErr
		}

		return marshalToolResult(map[string]any{"pipeline": detail})
	}
}

// handleCancelPipeline returns the mcp.ToolHandler that drives the
// cancel_pipeline tool. The Woodpecker endpoint has no response body;
// we wrap the success in a {"canceled": true, "repo_id": …,
// "pipeline_number": …} envelope so the model has an observable
// success signal.
func handleCancelPipeline(client *woodpeckerClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args pipelineByNumberArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse cancel_pipeline args: %w", decodeErr)
		}

		if args.RepoID <= 0 {
			return nil, errRepoIDRequired
		}

		if args.PipelineNumber <= 0 {
			return nil, errPipelineNumberRequired
		}

		cancelErr := client.cancelPipeline(ctx, args.RepoID, args.PipelineNumber)
		if cancelErr != nil {
			return nil, cancelErr
		}

		return marshalToolResult(map[string]any{
			"canceled":        true,
			"repo_id":         args.RepoID,
			"pipeline_number": args.PipelineNumber,
		})
	}
}
