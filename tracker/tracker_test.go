// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package tracker

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Connect-level tests (no OAuth, no network)
// ---------------------------------------------------------------------------

func TestConnect_RequiresOrgID(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"token": "test-token",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "org_id is empty")
}

func TestConnect_RequiresToken(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"org_id": "test-org",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "token is empty")
}

func TestConnect_AnnotationsMatchPolicy(t *testing.T) {
	t.Parallel()

	// Call Connect with a valid config (token + org_id). The client is
	// built but no HTTP call is made at this point — handlers are
	// returned as values. We can therefore verify the per-tool
	// Annotations policy without hitting the Tracker API.
	resp, err := Connect(t.Context(), map[string]any{
		"token":  "test-token",
		"org_id": "test-org",
	})
	require.NoError(t, err)

	actualAnnotations := make(map[string]*mcp.ToolAnnotations, len(resp.Tools))
	for _, entry := range resp.Tools {
		actualAnnotations[entry.Name] = entry.Annotations
	}

	queryTools := []string{
		"search_issues", "get_issue", "get_links", "get_comments",
		"get_fields", "get_wiki_page", "get_wiki_subpages",
		"list_queues", "my_report",
	}

	for _, name := range queryTools {
		annotations, ok := actualAnnotations[name]
		require.Truef(t, ok, "expected tool %q missing from Connect response", name)
		require.NotNilf(t, annotations,
			"tool %q should have Annotations set per the per-type Annotations policy", name)
		assert.Truef(t, annotations.ReadOnlyHint,
			"tool %q should be read-only per the per-type Annotations policy", name)
	}

	createTools := []string{"create_issue", "create_comment"}

	for _, name := range createTools {
		annotations, ok := actualAnnotations[name]
		require.Truef(t, ok, "expected tool %q missing from Connect response", name)
		require.NotNilf(t, annotations,
			"tool %q should have Annotations set per the per-type Annotations policy", name)
		assert.Falsef(t, annotations.ReadOnlyHint,
			"tool %q should mutate state per the per-type Annotations policy", name)
		require.NotNilf(t, annotations.DestructiveHint,
			"tool %q should declare DestructiveHint per the per-type Annotations policy", name)
		assert.Falsef(t, *annotations.DestructiveHint,
			"tool %q should be additive per the per-type Annotations policy", name)
		assert.Falsef(t, annotations.IdempotentHint,
			"tool %q should not be idempotent per the per-type Annotations policy", name)
	}

	updateTools := []string{"update_issue", "update_comment"}

	for _, name := range updateTools {
		annotations, ok := actualAnnotations[name]
		require.Truef(t, ok, "expected tool %q missing from Connect response", name)
		require.NotNilf(t, annotations,
			"tool %q should have Annotations set per the per-type Annotations policy", name)
		assert.Falsef(t, annotations.ReadOnlyHint,
			"tool %q should mutate state per the per-type Annotations policy", name)
		require.NotNilf(t, annotations.DestructiveHint,
			"tool %q should declare DestructiveHint per the per-type Annotations policy", name)
		assert.Truef(t, *annotations.DestructiveHint,
			"tool %q should be destructive per the per-type Annotations policy", name)
		assert.Truef(t, annotations.IdempotentHint,
			"tool %q should be idempotent per the per-type Annotations policy", name)
	}
}

// ---------------------------------------------------------------------------
// jsonResult helper (the standard response wrapper for tracker handlers)
// ---------------------------------------------------------------------------

func TestJSONResult_Object(t *testing.T) {
	t.Parallel()

	result, err := jsonResult(map[string]any{"id": 7, "name": "sprocket"})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.JSONEq(t, `{"id":7,"name":"sprocket"}`, textContent.Text)
	require.NotNil(t, result.StructuredContent)
}

func TestJSONResult_Array(t *testing.T) {
	t.Parallel()

	// Arrays are valid JSON but not valid MCP StructuredContent, so
	// StructuredContent must be nil — only Content carries the body.
	result, err := jsonResult([]int{1, 2, 3})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, "[1,2,3]", textContent.Text)
	require.Nil(t, result.StructuredContent)
}

func TestJSONResult_ChannelMarshalError(t *testing.T) {
	t.Parallel()

	result, err := jsonResult(make(chan int))
	require.Error(t, err)
	require.Contains(t, err.Error(), "marshal response")
	require.Nil(t, result)
}
