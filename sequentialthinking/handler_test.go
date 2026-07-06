// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package sequentialthinking

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- handler: end-to-end via mcp.CallToolRequest ---

// invokeHandler serializes args to JSON, calls the handler with a real
// mcp.CallToolRequest, and returns the result. It is the standard
// testing surface for handler-level tests.
func invokeHandler(
	t *testing.T,
	server *sequentialThinkingServer,
	args *thoughtArgs,
) (*mcp.CallToolResult, error) {
	t.Helper()

	handler := handleSequentialThinking(server)

	data, err := json.Marshal(args) //nolint:errchkjson // thoughtArgs is a safe, fixed-shape struct
	require.NoError(t, err)

	req := &mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
		Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
			Arguments: data,
		},
	}

	return handler(t.Context(), req)
}

func newTestServer() *sequentialThinkingServer {
	return newSequentialThinkingServer(config{}, slog.New(slog.DiscardHandler))
}

func TestHandler_AcceptsRegularThought(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	result, err := invokeHandler(
		t,
		server,
		&thoughtArgs{ //nolint:exhaustruct // defaults are intentional
			Thought:           "first",
			ThoughtNumber:     1,
			TotalThoughts:     3,
			NextThoughtNeeded: true,
		},
	)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)

	var resp ThoughtResponse
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &resp))

	assert.Equal(t, 1, resp.ThoughtNumber)
	assert.Equal(t, 3, resp.TotalThoughts)
	assert.True(t, resp.NextThoughtNeeded)
	assert.Equal(t, []string{}, resp.Branches)
	assert.Equal(t, 1, resp.ThoughtHistoryLength)
}

func TestHandler_PopulatesStructuredContent(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	result, err := invokeHandler(
		t,
		server,
		&thoughtArgs{ //nolint:exhaustruct // defaults are intentional
			Thought:           "structured",
			ThoughtNumber:     1,
			TotalThoughts:     1,
			NextThoughtNeeded: false,
		},
	)
	require.NoError(t, err)
	require.NotNil(t, result.StructuredContent)

	raw, ok := result.StructuredContent.(json.RawMessage)
	require.Truef(
		t,
		ok,
		"StructuredContent should be json.RawMessage, got %T",
		result.StructuredContent,
	)

	var resp ThoughtResponse
	require.NoError(t, json.Unmarshal(raw, &resp))

	assert.Equal(t, 1, resp.ThoughtNumber)
}

func TestHandler_RejectsEmptyThought(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	_, err := invokeHandler(
		t,
		server,
		&thoughtArgs{ //nolint:exhaustruct // defaults are intentional
			Thought:           "",
			ThoughtNumber:     1,
			TotalThoughts:     1,
			NextThoughtNeeded: false,
		},
	)
	require.ErrorIs(t, err, errThoughtEmpty)
}

func TestHandler_RejectsZeroThoughtNumber(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	_, err := invokeHandler(
		t,
		server,
		&thoughtArgs{ //nolint:exhaustruct // defaults are intentional
			Thought:           "x",
			ThoughtNumber:     0,
			TotalThoughts:     1,
			NextThoughtNeeded: false,
		},
	)
	require.ErrorIs(t, err, errThoughtNumberInvalid)
}

func TestHandler_RejectsZeroTotalThoughts(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	_, err := invokeHandler(
		t,
		server,
		&thoughtArgs{ //nolint:exhaustruct // defaults are intentional
			Thought:           "x",
			ThoughtNumber:     1,
			TotalThoughts:     0,
			NextThoughtNeeded: false,
		},
	)
	require.ErrorIs(t, err, errTotalThoughtsInvalid)
}

func TestHandler_RejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	handler := handleSequentialThinking(server)

	req := &mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
		Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
			Arguments: json.RawMessage(`{"not": "valid for the schema but parseable json`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Errorf(t, err, "malformed JSON should surface as a parse error")
}

// --- argsToThoughtData ---

func TestArgsToThoughtData_CopiesAllFields(t *testing.T) {
	t.Parallel()

	args := thoughtArgs{
		Thought:           "thought",
		NextThoughtNeeded: true,
		ThoughtNumber:     5,
		TotalThoughts:     10,
		IsRevision:        true,
		RevisesThought:    3,
		BranchFromThought: 2,
		BranchID:          "alt",
		NeedsMoreThoughts: true,
	}

	got := argsToThoughtData(&args)

	assert.Equal(t, args.Thought, got.Thought)
	assert.Equal(t, args.NextThoughtNeeded, got.NextThoughtNeeded)
	assert.Equal(t, args.ThoughtNumber, got.ThoughtNumber)
	assert.Equal(t, args.TotalThoughts, got.TotalThoughts)
	assert.Equal(t, args.IsRevision, got.IsRevision)
	assert.Equal(t, args.RevisesThought, got.RevisesThought)
	assert.Equal(t, args.BranchFromThought, got.BranchFromThought)
	assert.Equal(t, args.BranchID, got.BranchID)
	assert.Equal(t, args.NeedsMoreThoughts, got.NeedsMoreThoughts)
}

// --- textResult ---

func TestTextResult_ProducesStructuredContent(t *testing.T) {
	t.Parallel()

	result, err := textResult(ThoughtResponse{
		ThoughtNumber:        1,
		TotalThoughts:        1,
		NextThoughtNeeded:    false,
		Branches:             []string{},
		ThoughtHistoryLength: 1,
	})
	require.NoError(t, err)
	require.NotNil(t, result.StructuredContent)

	raw, ok := result.StructuredContent.(json.RawMessage)
	require.Truef(
		t,
		ok,
		"StructuredContent should be json.RawMessage, got %T",
		result.StructuredContent,
	)

	var resp ThoughtResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	assert.Equal(t, 1, resp.ThoughtNumber)
}

func TestTextResult_DropsStructuredContentForNonObject(t *testing.T) {
	t.Parallel()

	result, err := textResult([]string{"a", "b"})
	require.NoError(t, err)
	assert.Nil(t, result.StructuredContent)
	require.Len(t, result.Content, 1)
}
