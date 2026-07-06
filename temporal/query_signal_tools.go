// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package temporal

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.amidman.dev/mcp/tool"
)

// querySignalTools returns the two query/signal tools in the
// registration order documented in the integration spec. The
// returned slice is consumed by the Connect entry point.
//
// query_workflow is read-only; signal_workflow is mutating. Both
// set OpenWorldHint=true because every call talks to a remote
// Temporal frontend.
func querySignalTools(cli querySignalClient) []tool.Tool {
	return []tool.Tool{
		queryWorkflowTool(cli),
		signalWorkflowTool(cli),
	}
}

// queryWorkflowTool returns the query_workflow tool.Tool wired with
// the matching Handler and annotations.
func queryWorkflowTool(cli querySignalClient) tool.Tool {
	return tool.Tool{
		Tool: &mcp.Tool{
			Name:         "query_workflow",
			Description:  querySignalLoop + queryWorkflowDescription,
			InputSchema:  queryWorkflowInput,
			OutputSchema: queryWorkflowOutput,
			Annotations: &mcp.ToolAnnotations{
				Title:           "",
				ReadOnlyHint:    true,
				DestructiveHint: (*bool)(nil),
				IdempotentHint:  false,
				OpenWorldHint:   new(true),
			},
		},
		Handler: handleQueryWorkflow(cli),
	}
}

// signalWorkflowTool returns the signal_workflow tool.Tool wired
// with the matching Handler and annotations.
func signalWorkflowTool(cli querySignalClient) tool.Tool {
	return tool.Tool{
		Tool: &mcp.Tool{
			Name:         "signal_workflow",
			Description:  querySignalLoop + signalWorkflowDescription,
			InputSchema:  signalWorkflowInput,
			OutputSchema: signalWorkflowOutput,
			Annotations: &mcp.ToolAnnotations{
				Title:           "",
				ReadOnlyHint:    false,
				DestructiveHint: new(false),
				IdempotentHint:  false,
				OpenWorldHint:   new(true),
			},
		},
		Handler: handleSignalWorkflow(cli),
	}
}
