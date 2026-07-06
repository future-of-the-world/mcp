// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package temporal

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.amidman.dev/mcp/tool"
)

// --- Per-tool description suffixes (workflow tools) ---

const (
	startWorkflowDescription = "\n\nStart a new Temporal workflow execution. " +
		"The three required fields are workflow_name (the registered " +
		"workflow type), workflow_id (the user-chosen execution id; " +
		"re-using it returns WorkflowExecutionAlreadyStarted), and " +
		"task_queue (the worker queue the SDK polls). Optional args is " +
		"a JSON value passed positionally to the workflow function."

	cancelWorkflowDescription = "\n\nRequest cancellation of a running " +
		"workflow. run_id may be empty to target the latest run. " +
		"Cancel is cooperative — the workflow decides how to honor it " +
		"via workflow.GetInfo(ctx).CancelRequested. Idempotent: " +
		"canceling an already-finished workflow is a no-op success."

	terminateWorkflowDescription = "\n\nForcibly terminate a workflow run. " +
		"run_id may be empty to target the latest run. reason is " +
		"recorded in the workflow's termination event and is visible " +
		"in describe_workflow output; details is an optional JSON value " +
		"forwarded to the worker for forensics. Idempotent on the " +
		"server."

	getWorkflowResultDescription = "\n\nBlock until the workflow run " +
		"completes and return its JSON-decoded result. run_id may be " +
		"empty to target the latest run. Returns the value the workflow " +
		"function returned; for workflows that return nothing the " +
		"result field is null. Surfaces the SDK error verbatim if the " +
		"workflow failed or was canceled."

	describeWorkflowDescription = "\n\nReturn the server-side description " +
		"of a workflow execution: status, history length, pending " +
		"activity / child workflow counts, search attributes, and " +
		"parent execution info. run_id may be empty to target the " +
		"latest run."

	listWorkflowsDescription = "\n\nQuery the Temporal Visibility store " +
		"for workflow executions. query uses the Temporal Visibility " +
		"query language (e.g. 'ExecutionStatus = \"Running\" AND " +
		"WorkflowType = \"MyWorkflow\"'). page_size defaults to 100 " +
		"(server max is 1000). next_page_token from a previous call " +
		"pages through the rest."

	getWorkflowHistoryDescription = "\n\nFetch the history events of a " +
		"workflow execution, decoded for readability. run_id may be " +
		"empty to target the latest run. max_events defaults to 1000 " +
		"(the smallest cap the SDK will request). History is the " +
		"authoritative debugging surface — prefer this to logs."

	continueAsNewDescription = "\n\nSignal a workflow to continue-as-new. " +
		"The Go SDK has no client-side ContinueAsNew call; this tool " +
		"delivers a named signal and trusts the workflow to call " +
		"workflow.NewContinueAsNewError from its signal handler. " +
		"signal_args is forwarded to the handler."
)

// workflowTools builds the eight workflow tools from a manager.
// The per-tool handler factories live in workflow_handlers.go;
// this function only wires the annotations and embeds the schemas.
// The workflowLoop preamble is prepended to every tool's
// description so the agent sees the canonical list → describe →
// history sequence regardless of which tool it discovers first.
//
// Workflow annotations follow three shapes:
//   - read-only (get_workflow_result, describe_workflow,
//     list_workflows, get_workflow_history): ReadOnlyHint=true;
//     DestructiveHint=nil per the MCP spec.
//   - start (start_workflow): mutating, non-destructive,
//     non-idempotent (re-using a workflow_id returns
//     WorkflowExecutionAlreadyStarted).
//   - cancel/terminate (cancel_workflow, terminate_workflow):
//     destructive but idempotent (canceling or terminating an
//     already-finished workflow is a no-op on the server).
//   - continue_as_new: non-destructive, non-idempotent (the
//     signal may be re-delivered; the workflow decides whether to
//     honor repeats).
//
// All eight set OpenWorldHint=true because every call talks to a
// remote Temporal frontend.
func workflowTools(manager *clientManager) []tool.Tool {
	openWorld := new(true)

	readOnlyAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    true,
		DestructiveHint: (*bool)(nil),
		IdempotentHint:  false,
		OpenWorldHint:   openWorld,
	}

	startAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    false,
		DestructiveHint: new(false),
		IdempotentHint:  false,
		OpenWorldHint:   openWorld,
	}

	cancelAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    false,
		DestructiveHint: new(true),
		IdempotentHint:  true,
		OpenWorldHint:   openWorld,
	}

	continueAsNewAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    false,
		DestructiveHint: new(false),
		IdempotentHint:  false,
		OpenWorldHint:   openWorld,
	}

	return []tool.Tool{
		{
			Tool: &mcp.Tool{
				Name:         "start_workflow",
				Description:  workflowLoop + startWorkflowDescription,
				InputSchema:  startWorkflowInput,
				OutputSchema: startWorkflowOutput,
				Annotations:  startAnnotations,
			},
			Handler: handleStartWorkflow(manager),
		},
		{
			Tool: &mcp.Tool{
				Name:         "cancel_workflow",
				Description:  workflowLoop + cancelWorkflowDescription,
				InputSchema:  cancelWorkflowInput,
				OutputSchema: cancelWorkflowOutput,
				Annotations:  cancelAnnotations,
			},
			Handler: handleCancelWorkflow(manager),
		},
		{
			Tool: &mcp.Tool{
				Name:         "terminate_workflow",
				Description:  workflowLoop + terminateWorkflowDescription,
				InputSchema:  terminateWorkflowInput,
				OutputSchema: terminateWorkflowOutput,
				Annotations:  cancelAnnotations,
			},
			Handler: handleTerminateWorkflow(manager),
		},
		{
			Tool: &mcp.Tool{
				Name:         "get_workflow_result",
				Description:  workflowLoop + getWorkflowResultDescription,
				InputSchema:  getWorkflowResultInput,
				OutputSchema: getWorkflowResultOutput,
				Annotations:  readOnlyAnnotations,
			},
			Handler: handleGetWorkflowResult(manager),
		},
		{
			Tool: &mcp.Tool{
				Name:         "describe_workflow",
				Description:  workflowLoop + describeWorkflowDescription,
				InputSchema:  describeWorkflowInput,
				OutputSchema: describeWorkflowOutput,
				Annotations:  readOnlyAnnotations,
			},
			Handler: handleDescribeWorkflow(manager),
		},
		{
			Tool: &mcp.Tool{
				Name:         "list_workflows",
				Description:  workflowLoop + listWorkflowsDescription,
				InputSchema:  listWorkflowsInput,
				OutputSchema: listWorkflowsOutput,
				Annotations:  readOnlyAnnotations,
			},
			Handler: handleListWorkflows(manager),
		},
		{
			Tool: &mcp.Tool{
				Name:         "get_workflow_history",
				Description:  workflowLoop + getWorkflowHistoryDescription,
				InputSchema:  getWorkflowHistoryInput,
				OutputSchema: getWorkflowHistoryOutput,
				Annotations:  readOnlyAnnotations,
			},
			Handler: handleGetWorkflowHistory(manager),
		},
		{
			Tool: &mcp.Tool{
				Name:         "continue_as_new",
				Description:  workflowLoop + continueAsNewDescription,
				InputSchema:  continueAsNewInput,
				OutputSchema: continueAsNewOutput,
				Annotations:  continueAsNewAnnotations,
			},
			Handler: handleContinueAsNew(manager),
		},
	}
}
