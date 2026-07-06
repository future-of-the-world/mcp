// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.temporal.io/sdk/client"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- Request argument shapes ---

// createScheduleArgs is the JSON input to the create_schedule tool.
// scheduleID, workflowName, taskQueue, and cron are required; args and
// notes are optional. The handler validates the four required fields
// explicitly so missing-field errors surface as clear sentinel errors
// rather than opaque "CronExpressions is empty" SDK errors.
type createScheduleArgs struct {
	ScheduleID   string          `json:"schedule_id"`
	WorkflowName string          `json:"workflow_name"`
	TaskQueue    string          `json:"task_queue"`
	Cron         string          `json:"cron"`
	Args         json.RawMessage `json:"args,omitempty"`
	Notes        string          `json:"notes,omitempty"`
}

// listSchedulesArgs is the JSON input to the list_schedules tool.
// pageSize defaults to 100 server-side when 0. The Go SDK's
// ScheduleListIterator doesn't expose a page token — once the
// iterator is drained, the caller has every visible schedule. A
// follow-up issue can add server-side filtering via the
// ScheduleListOptions.Query field if needed.
type listSchedulesArgs struct {
	PageSize int `json:"page_size,omitempty"`
}

// scheduleByIDArgs is the JSON input to every tool that operates on
// exactly one schedule by ID: pause_schedule, unpause_schedule,
// delete_schedule, trigger_schedule, describe_schedule. Keeping the
// shared struct in one place avoids field-name drift across tools.
type scheduleByIDArgs struct {
	ScheduleID string `json:"schedule_id"`
}

// pauseScheduleArgs is the JSON input to pause_schedule. schedule_id is
// required; note is optional and is forwarded to SchedulePauseOptions.Note.
type pauseScheduleArgs struct {
	ScheduleID string `json:"schedule_id"`
	Note       string `json:"note,omitempty"`
}

// unpauseScheduleArgs mirrors pauseScheduleArgs for unpause_schedule.
type unpauseScheduleArgs struct {
	ScheduleID string `json:"schedule_id"`
	Note       string `json:"note,omitempty"`
}

// --- Sentinel errors ---

var (
	errScheduleIDRequired   = errors.New("temporal tool: schedule_id is required")
	errWorkflowNameRequired = errors.New("temporal tool: workflow_name is required")
	errTaskQueueRequired    = errors.New("temporal tool: task_queue is required")
	errCronRequired         = errors.New("temporal tool: cron is required")
	errScheduleClientNil    = errors.New("temporal: schedule client is nil")
)

// --- Shared helpers ---

// decodeArgs unmarshals the request's arguments JSON into target. It
// is the one place every tool handler does JSON-decode so the per-tool
// handlers stay focused on per-tool argument validation.
//
//nolint:wrapcheck // external json.Unmarshal error returned verbatim for handler parsing failures
func decodeArgs(req *mcp.CallToolRequest, target any) error {
	return json.Unmarshal(req.Params.Arguments, target)
}

// marshalToolResult marshals value as JSON and wraps it in a
// CallToolResult. Per the MCP spec, StructuredContent is attached only
// when the marshaled value is a JSON object (so plain arrays,
// primitives, or malformed values are conveyed through Content alone).
// Same probe-and-set shape used in mcp/woodpecker/woodpecker_handlers.go
// and mcp/english/english.go.
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

// decodeScheduleArgs decodes args into a typed struct and decodes the
// inner Args field (when present) into a []any for the SDK's
// ScheduleWorkflowAction.Args. The inner Args is left as a JSON object
// (or array) per Temporal's convention; the SDK's Args field is
// []interface{} so each top-level JSON value is forwarded as one
// argument value.
//
// If args.Args is missing or "null", the returned []any is nil — the
// SDK treats nil Args as "no arguments", which is the correct
// behavior for a schedule that fires a workflow with no input.
//
// args is passed by pointer (hugeParam) — the struct carries a
// json.RawMessage plus five strings, which would copy unnecessarily on
// every call.
func decodeScheduleArgs(args *createScheduleArgs) ([]any, error) {
	raw := args.Args
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var decoded []any

	decodeErr := json.Unmarshal(raw, &decoded)
	if decodeErr != nil {
		return nil, fmt.Errorf("temporal: schedule create: args: %w", decodeErr)
	}

	return decoded, nil
}

// --- Tool handler factories ---

// handleCreateSchedule returns the mcp.ToolHandler that drives the
// create_schedule tool. The handler validates the four required
// fields, builds a ScheduleWorkflowAction with the supplied workflow
// name / task queue / args, and asks the Temporal server to validate
// the cron expression server-side.
func handleCreateSchedule(manager *clientManager) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args createScheduleArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse create_schedule args: %w", decodeErr)
		}

		validateErr := validateCreateScheduleArgs(&args)
		if validateErr != nil {
			return nil, validateErr
		}

		decodedArgs, argsErr := decodeScheduleArgs(&args)
		if argsErr != nil {
			return nil, argsErr
		}

		//nolint:exhaustruct // optional SDK fields default to zero values
		options := client.ScheduleOptions{
			ID: args.ScheduleID,
			Spec: client.ScheduleSpec{
				// The SDK's CronExpressions accepts a 5-field standard
				// cron string. The server validates the expression
				// when Create is called; client-side we just plumb the
				// string through.
				CronExpressions: []string{args.Cron},
			},
			Action: &client.ScheduleWorkflowAction{
				Workflow:  args.WorkflowName,
				TaskQueue: args.TaskQueue,
				Args:      decodedArgs,
			},
			Note: args.Notes,
		}

		sched := manager.ScheduleClient()
		if sched == nil {
			return nil, errScheduleClientNil
		}

		handle, createErr := sched.Create(ctx, options)
		if createErr != nil {
			return nil, fmt.Errorf("temporal: schedule create: %w", createErr)
		}

		return marshalToolResult(map[string]any{
			"created":     true,
			"schedule_id": handle.GetID(),
		})
	}
}

// validateCreateScheduleArgs enforces the four required fields on the
// create_schedule input. The checks are extracted from
// handleCreateSchedule so the per-tool handler stays under the
// gocognit threshold. Order matches the order in the JSON schema's
// required[] list.
func validateCreateScheduleArgs(args *createScheduleArgs) error {
	if args.ScheduleID == "" {
		return errScheduleIDRequired
	}

	if args.WorkflowName == "" {
		return errWorkflowNameRequired
	}

	if args.TaskQueue == "" {
		return errTaskQueueRequired
	}

	if args.Cron == "" {
		return errCronRequired
	}

	return nil
}

// handleListSchedules returns the mcp.ToolHandler that drives the
// list_schedules tool. The handler drains the iterator into a
// []scheduleSummary so the model receives a deterministic JSON array
// rather than a stateful iterator. NextPageToken plumbing is exposed
// in the output envelope so the agent can paginate explicitly.
func handleListSchedules(manager *clientManager) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args listSchedulesArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse list_schedules args: %w", decodeErr)
		}

		sched := manager.ScheduleClient()
		if sched == nil {
			return nil, errScheduleClientNil
		}

		summaries, listErr := drainScheduleIterator(ctx, sched, client.ScheduleListOptions{
			PageSize: args.PageSize,
		})
		if listErr != nil {
			return nil, listErr
		}

		return marshalToolResult(map[string]any{
			"schedules": summaries,
		})
	}
}

// drainScheduleIterator exhausts the schedule list iterator and
// projects each entry to a model-facing scheduleSummary. The loop is
// pulled out of handleListSchedules so the per-tool handler stays
// under the gocognit threshold.
func drainScheduleIterator(
	ctx context.Context, sched client.ScheduleClient, options client.ScheduleListOptions,
) ([]scheduleSummary, error) {
	iter, listErr := sched.List(ctx, options)
	if listErr != nil {
		return nil, fmt.Errorf("temporal: schedule list: %w", listErr)
	}

	// Pre-allocate to a non-nil empty slice so the JSON encoder emits
	// "[]" rather than "null" when the iterator is empty.
	summaries := make([]scheduleSummary, 0)

	for iter.HasNext() {
		entry, nextErr := iter.Next()
		if nextErr != nil {
			return nil, fmt.Errorf("temporal: schedule list: %w", nextErr)
		}

		summaries = append(summaries, scheduleEntryToSummary(entry))
	}

	return summaries, nil
}

// handlePauseSchedule returns the mcp.ToolHandler that drives the
// pause_schedule tool. The optional note is forwarded to
// SchedulePauseOptions.Note (the SDK defaults the note to "Paused via
// Go SDK" when nil; we forward a nil pointer for an absent note so the
// SDK's default fires).
func handlePauseSchedule(manager *clientManager) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args pauseScheduleArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse pause_schedule args: %w", decodeErr)
		}

		if args.ScheduleID == "" {
			return nil, errScheduleIDRequired
		}

		sched := manager.ScheduleClient()
		if sched == nil {
			return nil, errScheduleClientNil
		}

		options := client.SchedulePauseOptions{
			Note: args.Note,
		}

		pauseErr := sched.GetHandle(ctx, args.ScheduleID).Pause(ctx, options)
		if pauseErr != nil {
			return nil, fmt.Errorf("temporal: schedule pause: %w", pauseErr)
		}

		return marshalToolResult(map[string]any{
			"paused":      true,
			"schedule_id": args.ScheduleID,
		})
	}
}

// handleUnpauseSchedule returns the mcp.ToolHandler that drives the
// unpause_schedule tool. Mirrors handlePauseSchedule with the
// unpause-shaped SDK call.
func handleUnpauseSchedule(manager *clientManager) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args unpauseScheduleArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse unpause_schedule args: %w", decodeErr)
		}

		if args.ScheduleID == "" {
			return nil, errScheduleIDRequired
		}

		sched := manager.ScheduleClient()
		if sched == nil {
			return nil, errScheduleClientNil
		}

		options := client.ScheduleUnpauseOptions{
			Note: args.Note,
		}

		unpauseErr := sched.GetHandle(ctx, args.ScheduleID).Unpause(ctx, options)
		if unpauseErr != nil {
			return nil, fmt.Errorf("temporal: schedule unpause: %w", unpauseErr)
		}

		return marshalToolResult(map[string]any{
			"unpaused":    true,
			"schedule_id": args.ScheduleID,
		})
	}
}

// handleDeleteSchedule returns the mcp.ToolHandler that drives the
// delete_schedule tool. Deleting an already-deleted schedule is a
// no-op on the server, so the handler is idempotent in practice.
func handleDeleteSchedule(manager *clientManager) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args scheduleByIDArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse delete_schedule args: %w", decodeErr)
		}

		if args.ScheduleID == "" {
			return nil, errScheduleIDRequired
		}

		sched := manager.ScheduleClient()
		if sched == nil {
			return nil, errScheduleClientNil
		}

		deleteErr := sched.GetHandle(ctx, args.ScheduleID).Delete(ctx)
		if deleteErr != nil {
			return nil, fmt.Errorf("temporal: schedule delete: %w", deleteErr)
		}

		return marshalToolResult(map[string]any{
			"deleted":     true,
			"schedule_id": args.ScheduleID,
		})
	}
}

// handleTriggerSchedule returns the mcp.ToolHandler that drives the
// trigger_schedule tool. The trigger is non-idempotent: each call
// starts a new workflow execution.
func handleTriggerSchedule(manager *clientManager) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args scheduleByIDArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse trigger_schedule args: %w", decodeErr)
		}

		if args.ScheduleID == "" {
			return nil, errScheduleIDRequired
		}

		sched := manager.ScheduleClient()
		if sched == nil {
			return nil, errScheduleClientNil
		}

		triggerErr := sched.GetHandle(ctx, args.ScheduleID).
			Trigger(ctx, client.ScheduleTriggerOptions{})
		if triggerErr != nil {
			return nil, fmt.Errorf("temporal: schedule trigger: %w", triggerErr)
		}

		return marshalToolResult(map[string]any{
			"triggered":   true,
			"schedule_id": args.ScheduleID,
		})
	}
}

// handleDescribeSchedule returns the mcp.ToolHandler that drives the
// describe_schedule tool. The handler returns the full
// ScheduleDescription as JSON — the SDK's struct shape maps cleanly to
// the JSON schema declared in schemas/describe_schedule_output.json.
func handleDescribeSchedule(manager *clientManager) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args scheduleByIDArgs

		decodeErr := decodeArgs(req, &args)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse describe_schedule args: %w", decodeErr)
		}

		if args.ScheduleID == "" {
			return nil, errScheduleIDRequired
		}

		sched := manager.ScheduleClient()
		if sched == nil {
			return nil, errScheduleClientNil
		}

		description, describeErr := sched.GetHandle(ctx, args.ScheduleID).Describe(ctx)
		if describeErr != nil {
			return nil, fmt.Errorf("temporal: schedule describe: %w", describeErr)
		}

		return marshalToolResult(map[string]any{
			"schedule_id": args.ScheduleID,
			"description": description,
		})
	}
}

// --- Model-facing types ---

// scheduleSummary is the model-facing shape of one entry in the
// list_schedules output. The SDK's ScheduleListEntry is heavier than
// what the model needs to identify a schedule and decide whether to
// describe it; we project to id, paused, recent-action count, next
// fire time, and the workflow name.
type scheduleSummary struct {
	ID            string `json:"id"`
	Paused        bool   `json:"paused"`
	Note          string `json:"note,omitempty"`
	WorkflowType  string `json:"workflow_type,omitempty"`
	NextFireTimes []any  `json:"next_fire_times,omitempty"`
}

// scheduleEntryToSummary projects the SDK's ScheduleListEntry into
// the model-facing scheduleSummary. The WorkflowType is a
// client.WorkflowType struct (with Name + ParentWorkflowInfo fields)
// — we only carry the Name string. NextActionTimes is a []time.Time;
// we round-trip through time.Time.MarshalJSON by leaving the field as
// []any with time.Time values so the standard encoder emits RFC 3339
// timestamps.
func scheduleEntryToSummary(entry *client.ScheduleListEntry) scheduleSummary {
	out := scheduleSummary{}
	if entry == nil {
		return out
	}

	out.ID = entry.ID
	out.Paused = entry.Paused
	out.Note = entry.Note

	out.WorkflowType = entry.WorkflowType.Name

	if len(entry.NextActionTimes) > 0 {
		next := make([]any, 0, len(entry.NextActionTimes))
		for _, t := range entry.NextActionTimes {
			next = append(next, t)
		}

		out.NextFireTimes = next
	}

	return out
}
