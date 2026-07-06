// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package tool

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staticHandler returns a no-op mcp.ToolHandler that always produces an
// empty result. It is used only as a stored value in Tool.Handler; tests
// never invoke it.
func staticHandler() mcp.ToolHandler {
	return func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return new(mcp.CallToolResult), nil
	}
}

func TestTool_ZeroValue(t *testing.T) {
	t.Parallel()

	var tool Tool
	require.Nilf(t, tool.Tool, "embedded *mcp.Tool should be nil in zero value")
	assert.Nilf(t, tool.Handler, "Handler should be nil in zero value")
}

func TestTool_EmbeddedFieldsAccessible(t *testing.T) {
	t.Parallel()

	tool := Tool{
		Tool: &mcp.Tool{
			Name:        "demo",
			Description: "demo tool",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Demo",
				ReadOnlyHint:    true,
				DestructiveHint: (*bool)(nil),
				IdempotentHint:  false,
				OpenWorldHint:   (*bool)(nil),
			},
		},
		Handler: staticHandler(),
	}

	// Promoted access through the embedded *mcp.Tool works.
	assert.Equal(t, "demo", tool.Name)
	assert.Equal(t, "demo tool", tool.Description)
	require.NotNil(t, tool.Annotations)
	assert.True(t, tool.Annotations.ReadOnlyHint)
}

func TestTool_EmbeddedPointerPreserved(t *testing.T) {
	t.Parallel()

	inner := &mcp.Tool{
		Name: "preserved",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Original",
			ReadOnlyHint:    true,
			DestructiveHint: (*bool)(nil),
			IdempotentHint:  false,
			OpenWorldHint:   (*bool)(nil),
		},
	}
	tool := Tool{Tool: inner, Handler: staticHandler()}

	// The Tool field is the same pointer — no copy.
	assert.Same(t, inner, tool.Tool)

	// Mutations to the inner *mcp.Tool are visible through the embed
	// because the pointer is shared, not a deep copy.
	inner.Annotations.Title = "Mutated"
	assert.Equal(t, "Mutated", tool.Annotations.Title)
}

func TestTool_AnnotatedFieldsRoundTrip(t *testing.T) {
	t.Parallel()

	// Verify that all five mcp.ToolAnnotations fields survive through
	// the Tool struct, including the *bool pointers (DestructiveHint,
	// OpenWorldHint) which the dispatcher will later set per the
	// per-type policy.
	destructive := true
	openWorld := false
	tool := Tool{
		Tool: &mcp.Tool{
			Name: "fully-annotated",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Fully Annotated",
				ReadOnlyHint:    true,
				DestructiveHint: &destructive,
				IdempotentHint:  true,
				OpenWorldHint:   &openWorld,
			},
		},
		Handler: staticHandler(),
	}

	require.NotNil(t, tool.Annotations)
	assert.Equal(t, "Fully Annotated", tool.Annotations.Title)
	assert.True(t, tool.Annotations.ReadOnlyHint)
	require.NotNil(t, tool.Annotations.DestructiveHint)
	assert.True(t, *tool.Annotations.DestructiveHint)
	assert.True(t, tool.Annotations.IdempotentHint)
	require.NotNil(t, tool.Annotations.OpenWorldHint)
	assert.False(t, *tool.Annotations.OpenWorldHint)
}

func TestResponse_ZeroValueIsIterable(t *testing.T) {
	t.Parallel()

	var resp Response

	// A nil slice is safe to range over; middlewares rely on this.
	assert.NotPanics(t, func() {
		for range resp.Tools {
			t.Error("unexpected tool in zero-value response")
		}
	})
	assert.Empty(t, resp.Tools)
}

func TestResponse_HoldsToolsInOrder(t *testing.T) {
	t.Parallel()

	resp := Response{
		Tools: []Tool{
			{
				Tool: &mcp.Tool{
					Name:        "alpha",
					Description: "first tool",
				},
				Handler: staticHandler(),
			},
			{
				Tool: &mcp.Tool{
					Name:        "beta",
					Description: "second tool",
				},
				Handler: staticHandler(),
			},
		},
	}

	require.Len(t, resp.Tools, 2)
	assert.Equal(t, "alpha", resp.Tools[0].Name)
	assert.Equal(t, "beta", resp.Tools[1].Name)
}
