// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package temporal

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.amidman.dev/mcp/tool"
)

// --- Per-tool descriptions ---

const (
	startActivityDescription = "\n\nStart a new standalone activity " +
		"execution. The activity is scheduled but NOT awaited — use " +
		"temporal_get_activity_result to block on its completion, or " +
		"temporal_describe_activity to inspect its progress. Pair this " +
		"with temporal_execute_activity by intent: start_activity is " +
		"fire-and-forget; execute_activity awaits the result."

	executeActivityDescription = "\n\nSchedule a standalone activity " +
		"execution AND wait for its result. Equivalent to " +
		"start_activity followed by a blocking poll on the handle. " +
		"Use this when the caller needs the return value immediately."

	getActivityResultDescription = "\n\nBlock on the result of an " +
		"existing standalone activity. Pair with start_activity: " +
		"start_activity returns the handle synchronously; this tool " +
		"returns the decoded return value once the activity finishes."

	describeActivityDescription = "\n\nReturn detailed state for a " +
		"standalone activity execution: status, attempt count, last " +
		"failure (if any), and the worker that picked it up. Use this " +
		"to triage a stuck or failed activity before deciding to " +
		"cancel or terminate it."

	listActivitiesDescription = "\n\nList standalone activity " +
		"executions matching a Visibility query. Pagination via " +
		"page_size and next_page_token (returned opaque). Use this to " +
		"discover activity_id values to feed into " +
		"temporal_describe_activity or temporal_get_activity_result."

	countActivitiesDescription = "\n\nCount standalone activity " +
		"executions matching a Visibility query. Cheaper than " +
		"list_activities when only the aggregate number matters; the " +
		"server may also return aggregation groups when configured."

	cancelActivityDescription = "\n\nRequest cancellation of a " +
		"running standalone activity. Idempotent — re-canceling an " +
		"already-canceled or already-finished activity is a no-op " +
		"success. Use when the activity should stop voluntarily; for " +
		"forceful termination use temporal_terminate_activity."

	terminateActivityDescription = "\n\nForcefully terminate a " +
		"standalone activity execution. Idempotent. Prefer " +
		"temporal_cancel_activity for cooperative shutdown; use " +
		"terminate only when cancel is insufficient."
)

// --- Tool list ---

// activityTools returns the eight activity tool.Tool values in the
// registration order documented in the integration spec. The
// returned slice is consumed by the Connect entry point.
//
// Each tool's Description is the standaloneActivityLoop preamble
// followed by the per-tool suffix. Annotations match the spec:
//
//   - start_activity, execute_activity: mutating, non-destructive,
//     non-idempotent (each call schedules a new execution).
//   - get_activity_result, describe_activity, list_activities,
//     count_activities: read-only.
//   - cancel_activity, terminate_activity: mutating, destructive,
//     but idempotent on the server.
//
// All eight set OpenWorldHint=true because every call talks to a
// remote Temporal server.
func activityTools(cli temporalClient) []tool.Tool {
	return []tool.Tool{
		startActivityTool(cli),
		executeActivityTool(cli),
		getActivityResultTool(cli),
		describeActivityTool(cli),
		listActivitiesTool(cli),
		countActivitiesTool(cli),
		cancelActivityTool(cli),
		terminateActivityTool(cli),
	}
}

// startActivityTool returns the start_activity tool.Tool wired with
// the matching Handler and annotations.
func startActivityTool(cli temporalClient) tool.Tool {
	return tool.Tool{
		Tool: &mcp.Tool{
			Name:         "start_activity",
			Description:  standaloneActivityLoop + startActivityDescription,
			InputSchema:  startActivityInput,
			OutputSchema: startActivityOutput,
			Annotations: &mcp.ToolAnnotations{
				Title:           "",
				ReadOnlyHint:    false,
				DestructiveHint: new(false),
				IdempotentHint:  false,
				OpenWorldHint:   new(true),
			},
		},
		Handler: handleStartActivity(cli),
	}
}

// executeActivityTool returns the execute_activity tool.Tool.
func executeActivityTool(cli temporalClient) tool.Tool {
	return tool.Tool{
		Tool: &mcp.Tool{
			Name:         "execute_activity",
			Description:  standaloneActivityLoop + executeActivityDescription,
			InputSchema:  executeActivityInput,
			OutputSchema: executeActivityOutput,
			Annotations: &mcp.ToolAnnotations{
				Title:           "",
				ReadOnlyHint:    false,
				DestructiveHint: new(false),
				IdempotentHint:  false,
				OpenWorldHint:   new(true),
			},
		},
		Handler: handleExecuteActivity(cli),
	}
}

// getActivityResultTool returns the get_activity_result tool.Tool.
func getActivityResultTool(cli temporalClient) tool.Tool {
	return tool.Tool{
		Tool: &mcp.Tool{
			Name:         "get_activity_result",
			Description:  standaloneActivityLoop + getActivityResultDescription,
			InputSchema:  getActivityResultInput,
			OutputSchema: getActivityResultOutput,
			Annotations: &mcp.ToolAnnotations{
				Title:           "",
				ReadOnlyHint:    true,
				DestructiveHint: (*bool)(nil),
				IdempotentHint:  false,
				OpenWorldHint:   new(true),
			},
		},
		Handler: handleGetActivityResult(cli),
	}
}

// describeActivityTool returns the describe_activity tool.Tool.
func describeActivityTool(cli temporalClient) tool.Tool {
	return tool.Tool{
		Tool: &mcp.Tool{
			Name:         "describe_activity",
			Description:  standaloneActivityLoop + describeActivityDescription,
			InputSchema:  describeActivityInput,
			OutputSchema: describeActivityOutput,
			Annotations: &mcp.ToolAnnotations{
				Title:           "",
				ReadOnlyHint:    true,
				DestructiveHint: (*bool)(nil),
				IdempotentHint:  false,
				OpenWorldHint:   new(true),
			},
		},
		Handler: handleDescribeActivity(cli),
	}
}

// listActivitiesTool returns the list_activities tool.Tool.
func listActivitiesTool(cli temporalClient) tool.Tool {
	return tool.Tool{
		Tool: &mcp.Tool{
			Name:         "list_activities",
			Description:  standaloneActivityLoop + listActivitiesDescription,
			InputSchema:  listActivitiesInput,
			OutputSchema: listActivitiesOutput,
			Annotations: &mcp.ToolAnnotations{
				Title:           "",
				ReadOnlyHint:    true,
				DestructiveHint: (*bool)(nil),
				IdempotentHint:  false,
				OpenWorldHint:   new(true),
			},
		},
		Handler: handleListActivities(cli),
	}
}

// countActivitiesTool returns the count_activities tool.Tool.
func countActivitiesTool(cli temporalClient) tool.Tool {
	return tool.Tool{
		Tool: &mcp.Tool{
			Name:         "count_activities",
			Description:  standaloneActivityLoop + countActivitiesDescription,
			InputSchema:  countActivitiesInput,
			OutputSchema: countActivitiesOutput,
			Annotations: &mcp.ToolAnnotations{
				Title:           "",
				ReadOnlyHint:    true,
				DestructiveHint: (*bool)(nil),
				IdempotentHint:  false,
				OpenWorldHint:   new(true),
			},
		},
		Handler: handleCountActivities(cli),
	}
}

// cancelActivityTool returns the cancel_activity tool.Tool.
func cancelActivityTool(cli temporalClient) tool.Tool {
	return tool.Tool{
		Tool: &mcp.Tool{
			Name:         "cancel_activity",
			Description:  standaloneActivityLoop + cancelActivityDescription,
			InputSchema:  cancelActivityInput,
			OutputSchema: cancelActivityOutput,
			Annotations: &mcp.ToolAnnotations{
				Title:           "",
				ReadOnlyHint:    false,
				DestructiveHint: new(true),
				IdempotentHint:  true,
				OpenWorldHint:   new(true),
			},
		},
		Handler: handleCancelActivity(cli),
	}
}

// terminateActivityTool returns the terminate_activity tool.Tool.
func terminateActivityTool(cli temporalClient) tool.Tool {
	return tool.Tool{
		Tool: &mcp.Tool{
			Name:         "terminate_activity",
			Description:  standaloneActivityLoop + terminateActivityDescription,
			InputSchema:  terminateActivityInput,
			OutputSchema: terminateActivityOutput,
			Annotations: &mcp.ToolAnnotations{
				Title:           "",
				ReadOnlyHint:    false,
				DestructiveHint: new(true),
				IdempotentHint:  true,
				OpenWorldHint:   new(true),
			},
		},
		Handler: handleTerminateActivity(cli),
	}
}
