// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// textResult marshals value to JSON and returns a *mcp.CallToolResult
// containing a single TextContent. It is the standard response shape for
// the websearch handlers, which all return structured JSON.
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

// handleWebSearch returns the mcp.ToolHandler that drives the web_search
// tool. The request body is decoded into a webSearchRequest and
// forwarded to the search service.
func handleWebSearch(tool *Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args webSearchRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse web_search args: %w", err)
		}

		if args.SearchTerm == "" {
			return nil, errEmptySearchTerm
		}

		query, provider := args.SearchTerm, args.Provider

		cleanQuery, hint := ExtractProviderHint(query)
		if hint != "" && provider == "" {
			provider = hint
		}

		query = cleanQuery

		opts := tool.buildSearchOptions(&args, SearchKindWeb)

		resp, err := tool.search.SearchWith(ctx, query, provider, opts)
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}

		return textResult(resp)
	}
}

// handleNewsSearch returns the mcp.ToolHandler for news_search. The
// request body is decoded into a newsSearchRequest and forwarded to the
// search service.
func handleNewsSearch(tool *Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args newsSearchRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse news_search args: %w", err)
		}

		if args.SearchTerm == "" {
			return nil, errEmptySearchTerm
		}

		query, provider := args.SearchTerm, ""

		cleanQuery, hint := ExtractProviderHint(query)
		if hint != "" {
			provider = hint
		}

		query = cleanQuery

		opts := tool.buildSearchOptions(
			&webSearchRequest{ //nolint:exhaustruct // partial init is intentional
				SearchTerm: args.SearchTerm,
				Count:      args.Count,
				Freshness:  args.Freshness,
				Country:    args.Country,
			},
			SearchKindNews,
		)

		resp, err := tool.search.SearchWith(ctx, query, provider, opts)
		if err != nil {
			return nil, fmt.Errorf("news search failed: %w", err)
		}

		return textResult(resp)
	}
}

// handleImageSearch returns the mcp.ToolHandler for image_search. The
// request body is decoded into an imageSearchRequest and forwarded to the
// search service.
func handleImageSearch(tool *Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args imageSearchRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse image_search args: %w", err)
		}

		if args.SearchTerm == "" {
			return nil, errEmptySearchTerm
		}

		query, provider := args.SearchTerm, ""

		cleanQuery, hint := ExtractProviderHint(query)
		if hint != "" {
			provider = hint
		}

		query = cleanQuery

		opts := tool.buildSearchOptions(
			&webSearchRequest{ //nolint:exhaustruct // partial init is intentional
				SearchTerm: args.SearchTerm,
				Count:      args.Count,
				SafeSearch: args.SafeSearch,
			},
			SearchKindImages,
		)

		resp, err := tool.search.SearchWith(ctx, query, provider, opts)
		if err != nil {
			return nil, fmt.Errorf("image search failed: %w", err)
		}

		return textResult(resp)
	}
}

// handleFetchURL returns the mcp.ToolHandler for fetch_url. The request
// body is decoded into a fetchURLRequest and forwarded to the fetch
// service.
func handleFetchURL(tool *Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args fetchURLRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse fetch_url args: %w", err)
		}

		if args.URL == "" {
			return nil, errEmptyURL
		}

		opts := &URLFetchOptions{
			MaxChars: args.MaxChars,
			Cursor:   args.Cursor,
		}

		doc, err := tool.fetch.FetchURL(ctx, args.URL, opts)
		if err != nil {
			return nil, fmt.Errorf("fetch failed: %w", err)
		}

		return textResult(doc)
	}
}

// handleListProviders returns the mcp.ToolHandler for list_providers.
// The tool takes no parameters and returns the list of registered
// search providers along with the current default.
func handleListProviders(tool *Tool) mcp.ToolHandler {
	return func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		defaultProvider := ""

		if provider := tool.factory.GetDefault(); provider != nil {
			defaultProvider = provider.Name()
		}

		return textResult(&listProvidersResponse{
			Providers: tool.factory.Names(),
			Default:   defaultProvider,
		})
	}
}
