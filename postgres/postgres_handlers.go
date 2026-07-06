// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// handleListSchemas returns the mcp.ToolHandler that drives the
// list_schemas tool. The handler has no request body, so it just invokes
// the underlying *Tool.getSchemas method and marshals the result.
func handleListSchemas(tool *Tool) mcp.ToolHandler {
	return func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		resp, err := tool.getSchemas(ctx)
		if err != nil {
			return nil, fmt.Errorf("list schemas: %w", err)
		}

		return textResult(resp)
	}
}

// handleListTables returns the mcp.ToolHandler for list_tables. The
// PostgresTablesRequest is decoded from the call's raw Arguments.
func handleListTables(tool *Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args PostgresTablesRequest

		err := json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse list_tables args: %w", err)
		}

		resp, err := tool.getTables(ctx, args)
		if err != nil {
			return nil, fmt.Errorf("list tables: %w", err)
		}

		return textResult(resp)
	}
}

// handleExecuteQuery returns the mcp.ToolHandler for execute_query. The
// PostgresExecuteRequest carries the SQL and its bound parameters; the
// underlying *Tool.executeQuery enforces the read-only check.
func handleExecuteQuery(tool *Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args PostgresExecuteRequest

		err := json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse execute_query args: %w", err)
		}

		resp, err := tool.executeQuery(ctx, args)
		if err != nil {
			return nil, fmt.Errorf("execute query: %w", err)
		}

		return textResult(resp)
	}
}

// handleGetTableInfo returns the mcp.ToolHandler for get_table_info. The
// PostgresTableInfoRequest carries the schema and table name; the
// underlying *Tool.getTableInfo recursively follows foreign keys.
func handleGetTableInfo(tool *Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args PostgresTableInfoRequest

		err := json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse get_table_info args: %w", err)
		}

		resp, err := tool.getTableInfo(ctx, args)
		if err != nil {
			return nil, fmt.Errorf("get table info: %w", err)
		}

		return textResult(resp)
	}
}

// textResult marshals value to JSON and returns a *mcp.CallToolResult
// containing a single TextContent. It is the standard response shape
// for the postgres handlers, which all return structured JSON.
func textResult(value any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}

	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}

	// Per the MCP spec, StructuredContent must marshal to a JSON object.
	// The unmarshal to map[string]any succeeds only when data is a valid
	// JSON object (a JSON array, primitive, or malformed value returns
	// an error). Those non-object cases should be conveyed via Content only.
	var probe map[string]any
	if json.Unmarshal(data, &probe) == nil {
		result.StructuredContent = json.RawMessage(data)
	}

	return result, nil
}
