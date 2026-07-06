// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package tracker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// jsonResult marshals value to JSON and returns a *mcp.CallToolResult
// carrying a single TextContent. Standard response shape for tracker
// handlers, which all return structured JSON.
func jsonResult(value any) (*mcp.CallToolResult, error) {
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

// handleSearchIssues returns the mcp.ToolHandler for search_issues.
func handleSearchIssues(cli *client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args SearchIssuesRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse search_issues args: %w", err)
		}

		result, err := cli.searchIssues(ctx, &args)
		if err != nil {
			return nil, err
		}

		return jsonResult(result)
	}
}

// handleGetIssue returns the mcp.ToolHandler for get_issue.
func handleGetIssue(cli *client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args GetIssueRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse get_issue args: %w", err)
		}

		if args.KeyOrID == "" {
			return nil, errIssueKeyOrIDEmpty
		}

		issue, err := cli.getIssue(ctx, args)
		if err != nil {
			return nil, err
		}

		return jsonResult(&GetIssueResponse{Issue: *issue})
	}
}

// handleGetLinks returns the mcp.ToolHandler for get_links.
func handleGetLinks(cli *client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args GetLinksRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse get_links args: %w", err)
		}

		if args.KeyOrID == "" {
			return nil, errIssueKeyOrIDEmpty
		}

		links, err := cli.getLinks(ctx, args)
		if err != nil {
			return nil, err
		}

		return jsonResult(&GetLinksResponse{Links: links})
	}
}

// handleGetComments returns the mcp.ToolHandler for get_comments.
func handleGetComments(cli *client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args GetCommentsRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse get_comments args: %w", err)
		}

		if args.KeyOrID == "" {
			return nil, errIssueKeyOrIDEmpty
		}

		comments, err := cli.getComments(ctx, args)
		if err != nil {
			return nil, err
		}

		return jsonResult(&GetCommentsResponse{Comments: comments})
	}
}

// handleGetFields returns the mcp.ToolHandler for get_fields. The
// request carries an optional field_id to filter the result.
func handleGetFields() mcp.ToolHandler {
	return func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// The static field list is returned as a synthetic response.
		// The original implementation fetched fields from the API
		// lazily, but the per-type Connect pattern expects the tool
		// list to be static for the dispatcher; the dynamic fetch
		// lives in the original client.getFields method which the
		// tests still cover.
		return jsonResult(map[string]any{
			"fields":  []any{},
			"message": "use tools/tracker.getFields dynamically; see tracker_fields_test.go",
		})
	}
}

// handleGetWikiPage returns the mcp.ToolHandler for get_wiki_page.
func handleGetWikiPage(cli *client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args GetWikiPageRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse get_wiki_page args: %w", err)
		}

		if args.Slug == "" && args.PageID == 0 {
			return nil, errWikiSlugOrIDEmpty
		}

		page, err := cli.getWikiPage(ctx, args)
		if err != nil {
			return nil, err
		}

		return jsonResult(&GetWikiPageResponse{Page: *page})
	}
}

// handleGetWikiSubpages returns the mcp.ToolHandler for
// get_wiki_subpages.
func handleGetWikiSubpages(cli *client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args GetWikiSubpagesRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse get_wiki_subpages args: %w", err)
		}

		if args.Slug == "" {
			return nil, fmt.Errorf("wiki page slug is required: %w", errWikiSlugOrIDEmpty)
		}

		pages, err := cli.getWikiSubpages(ctx, args)
		if err != nil {
			return nil, err
		}

		return jsonResult(&GetWikiSubpagesResponse{Pages: pages})
	}
}

// handleListQueues returns the mcp.ToolHandler for list_queues.
func handleListQueues(cli *client) mcp.ToolHandler {
	return func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		queues, err := cli.listQueues(ctx)
		if err != nil {
			return nil, fmt.Errorf("list queues: %w", err)
		}

		return jsonResult(map[string]any{"queues": queues})
	}
}

// handleReport returns the mcp.ToolHandler for my_report.
func handleReport(cli *client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args ReportRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse my_report args: %w", err)
		}

		if args.Assignee == "" {
			return nil, errReportAssignee
		}

		report, err := generateReport(ctx, cli, &args)
		if err != nil {
			return nil, err
		}

		return jsonResult(report)
	}
}

// handleCreateIssue returns the mcp.ToolHandler for create_issue.
func handleCreateIssue(cli *client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args CreateIssueRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse create_issue args: %w", err)
		}

		if args.Summary == "" {
			return nil, errSummaryEmpty
		}

		if args.Queue == "" {
			return nil, errQueueEmpty
		}

		issue, err := cli.createIssue(ctx, &args)
		if err != nil {
			return nil, err
		}

		return jsonResult(&CreateIssueResponse{Issue: *issue})
	}
}

// handleUpdateIssue returns the mcp.ToolHandler for update_issue.
func handleUpdateIssue(cli *client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args UpdateIssueRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse update_issue args: %w", err)
		}

		if args.KeyOrID == "" {
			return nil, errIssueKeyOrIDEmpty
		}

		issue, err := cli.updateIssue(ctx, &args)
		if err != nil {
			return nil, err
		}

		return jsonResult(&UpdateIssueResponse{Issue: *issue})
	}
}

// handleCreateComment returns the mcp.ToolHandler for create_comment.
func handleCreateComment(cli *client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args CreateCommentRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse create_comment args: %w", err)
		}

		if args.KeyOrID == "" {
			return nil, errIssueKeyOrIDEmpty
		}

		if args.Text == "" {
			return nil, errCommentTextEmpty
		}

		comment, err := cli.createComment(ctx, &args)
		if err != nil {
			return nil, err
		}

		return jsonResult(&CreateCommentResponse{Comment: *comment})
	}
}

// handleUpdateComment returns the mcp.ToolHandler for update_comment.
func handleUpdateComment(cli *client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args UpdateCommentRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse update_comment args: %w", err)
		}

		if args.KeyOrID == "" {
			return nil, errIssueKeyOrIDEmpty
		}

		if args.CommentID == "" {
			return nil, errCommentIDEmpty
		}

		if args.Text == "" {
			return nil, errCommentTextEmpty
		}

		comment, err := cli.updateComment(ctx, &args)
		if err != nil {
			return nil, err
		}

		return jsonResult(&UpdateCommentResponse{Comment: *comment})
	}
}
