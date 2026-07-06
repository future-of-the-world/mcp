// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package sequentialthinking

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConnect_RegistersBaseNameTool verifies the Connect entry point
// returns a single tool named "sequentialthinking" (no prefix) when no
// tools.prefix override is supplied. The tool description, input
// schema, and output schema are wired through end-to-end.
func TestConnect_RegistersBaseNameTool(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), make(map[string]any))

	require.NoError(t, err)

	require.Len(t, resp.Tools, 1)

	tool := resp.Tools[0]
	assert.Equal(t, "sequentialthinking", tool.Name)
	assert.NotEmpty(t, tool.Description)

	require.NotNil(t, tool.InputSchema)
	require.NotNil(t, tool.OutputSchema)
}

// TestConnect_AnnotationsMatchUpstream verifies the tool advertises
// ReadOnlyHint=true, DestructiveHint=false, IdempotentHint=true,
// OpenWorldHint=false — matching upstream's TypeScript annotations
// 1:1.
func TestConnect_AnnotationsMatchUpstream(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), make(map[string]any))

	require.NoError(t, err)

	require.Len(t, resp.Tools, 1)

	annotations := resp.Tools[0].Annotations
	require.NotNil(t, annotations)

	assert.True(t, annotations.ReadOnlyHint)
	assert.False(t, *annotations.DestructiveHint)
	assert.True(t, annotations.IdempotentHint)
	assert.False(t, *annotations.OpenWorldHint)
}

// TestConnect_InputSchemaHasAllFields verifies the embedded input
// schema is a JSON object with the four required fields and the five
// optional fields from the upstream Zod schema.
func TestConnect_InputSchemaHasAllFields(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), make(map[string]any))

	require.NoError(t, err)

	raw, ok := resp.Tools[0].InputSchema.(json.RawMessage)
	require.Truef(t, ok, "InputSchema should be json.RawMessage, got %T", resp.Tools[0].InputSchema)

	var schema struct {
		Type     string         `json:"type"`
		Required []string       `json:"required"`
		Props    map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))

	assert.Equal(t, "object", schema.Type)
	assert.ElementsMatch(t,
		[]string{"thought", "nextThoughtNeeded", "thoughtNumber", "totalThoughts"},
		schema.Required,
	)
	assert.Contains(t, schema.Props, "thought")
	assert.Contains(t, schema.Props, "nextThoughtNeeded")
	assert.Contains(t, schema.Props, "thoughtNumber")
	assert.Contains(t, schema.Props, "totalThoughts")
	assert.Contains(t, schema.Props, "isRevision")
	assert.Contains(t, schema.Props, "revisesThought")
	assert.Contains(t, schema.Props, "branchFromThought")
	assert.Contains(t, schema.Props, "branchId")
	assert.Contains(t, schema.Props, "needsMoreThoughts")
}

// TestConnect_OutputSchemaHasAllFields verifies the embedded output
// schema is a JSON object with the five required response fields.
func TestConnect_OutputSchemaHasAllFields(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), make(map[string]any))

	require.NoError(t, err)

	raw, ok := resp.Tools[0].OutputSchema.(json.RawMessage)
	require.Truef(
		t,
		ok,
		"OutputSchema should be json.RawMessage, got %T",
		resp.Tools[0].OutputSchema,
	)

	var schema struct {
		Type     string         `json:"type"`
		Required []string       `json:"required"`
		Props    map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))

	assert.Equal(t, "object", schema.Type)
	assert.ElementsMatch(
		t,
		[]string{
			"thoughtNumber",
			"totalThoughts",
			"nextThoughtNeeded",
			"branches",
			"thoughtHistoryLength",
		},
		schema.Required,
	)
	assert.Contains(t, schema.Props, "thoughtNumber")
	assert.Contains(t, schema.Props, "totalThoughts")
	assert.Contains(t, schema.Props, "nextThoughtNeeded")
	assert.Contains(t, schema.Props, "branches")
	assert.Contains(t, schema.Props, "thoughtHistoryLength")
}

// TestConnect_PassesDisableThrough verifies the disable_thought_logging
// config flag is wired into the per-source server instance.
func TestConnect_PassesDisableThrough(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		connectKeyDisableThoughtLogging: true,
	})
	require.NoError(t, err)
	require.Len(t, resp.Tools, 1)

	// The handler is non-nil; the disable flag is observable via the
	// structured log path (covered in sequentialthinking_test.go).
	require.NotNil(t, resp.Tools[0].Handler)
}

// TestConnect_TwoSourcesHaveIndependentState verifies that calling
// Connect twice produces two server instances with disjoint state —
// this is the contract that lets operators run multiple
// sequentialthinking sources (e.g. one per workflow) side-by-side.
func TestConnect_TwoSourcesHaveIndependentState(t *testing.T) {
	t.Parallel()

	respA, err := Connect(t.Context(), make(map[string]any))

	require.NoError(t, err)
	require.Len(t, respA.Tools, 1)

	respB, err := Connect(t.Context(), make(map[string]any))

	require.NoError(t, err)
	require.Len(t, respB.Tools, 1)

	data, _ := json.Marshal(thoughtArgs{ //nolint:exhaustruct // defaults are intentional
		Thought:           "shared payload",
		ThoughtNumber:     1,
		TotalThoughts:     1,
		NextThoughtNeeded: false,
	})

	resultA, err := respA.Tools[0].Handler(
		t.Context(),
		&mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
			Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
				Arguments: data,
			},
		},
	)
	require.NoError(t, err)

	resultB, err := respB.Tools[0].Handler(
		t.Context(),
		&mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
			Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
				Arguments: data,
			},
		},
	)
	require.NoError(t, err)

	// After one call each, both servers should report history length 1.
	// If the closures shared state, the second call would report 2.
	textA, ok := resultA.Content[0].(*mcp.TextContent)
	require.True(t, ok)

	textB, ok := resultB.Content[0].(*mcp.TextContent)
	require.True(t, ok)

	assert.Contains(t, textA.Text, `"thought_history_length":1`)
	assert.Contains(t, textB.Text, `"thought_history_length":1`)
}
