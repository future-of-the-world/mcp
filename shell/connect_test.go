// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package shell

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConnect_RegistersRunCommand is the end-to-end smoke test for the
// shell source. It confirms Connect returns exactly one tool, named
// "run_command", with the run_command description, embedded input/
// output schemas, and the expected Annotations block.
func TestConnect_RegistersRunCommand(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"working_dir": "/tmp",
	})
	require.NoError(t, err)
	require.Len(t, resp.Tools, 1)

	entry := resp.Tools[0]

	assert.Equal(t, "run_command", entry.Name)
	assert.Equal(t, runCommandDescription, entry.Description)

	inSchema, marshalErr := json.Marshal(entry.InputSchema)
	require.NoError(t, marshalErr)
	assert.JSONEq(t, string(shellRunInput), string(inSchema))

	outSchema, marshalErr := json.Marshal(entry.OutputSchema)
	require.NoError(t, marshalErr)
	assert.JSONEq(t, string(shellOutput), string(outSchema))

	require.NotNil(t, entry.Annotations)
	assert.Falsef(t, entry.Annotations.ReadOnlyHint,
		"shell tools are not read-only")
	require.NotNil(t, entry.Annotations.DestructiveHint)
	assert.Truef(t, *entry.Annotations.DestructiveHint,
		"shell tools must advertise DestructiveHint=true so host-level "+
			"gating on destructive hints treats them correctly")
	assert.False(t, entry.Annotations.IdempotentHint)
	require.NotNil(t, entry.Annotations.OpenWorldHint)
	assert.Falsef(t, *entry.Annotations.OpenWorldHint,
		"shell is local, not network")
}

// TestConnect_RequiresWorkingDir pins the required-field contract: an
// empty connect map produces an error, not a silently-defaulted tool.
func TestConnect_RequiresWorkingDir(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), make(map[string]any))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "working_dir")
}

// TestConnect_NegativeTimeout verifies that a negative connect.timeout
// is rejected at validate time. Numbers are stringified by
// decode.AsString and then parsed by time.ParseDuration, so a leading
// minus produces a valid Duration we can compare against zero.
func TestConnect_NegativeTimeout(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"working_dir": "/tmp",
		"timeout":     "-1s",
	})
	require.Error(t, err)
}

// TestConnect_DefaultAnnotationsDoNotAdvertiseReadOnly is a focused
// guard: a future refactor must not silently flip ReadOnlyHint back to
// true. The shell source is the only MCP source currently allowed to
// set ReadOnlyHint=false; this test pins that contract.
func TestConnect_DefaultAnnotationsDoNotAdvertiseReadOnly(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"working_dir": "/tmp",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Tools)

	for _, entry := range resp.Tools {
		assert.Falsef(t, entry.Annotations.ReadOnlyHint,
			"tool %q must not advertise ReadOnlyHint=true", entry.Name)
	}
}

// TestConnect_HandlerIsInvocable is a structural test: the handler must
// be non-nil so the dispatcher can register it with mcp.Server.AddTool.
// This catches "returned the tool struct but forgot to set the handler"
// regressions.
func TestConnect_HandlerIsInvocable(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"working_dir": "/tmp",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Tools)

	for _, entry := range resp.Tools {
		assert.NotNilf(t, entry.Handler,
			"tool %q must carry a non-nil Handler", entry.Name)
	}
}

// TestSchemas_EmbeddedRawMessagesAreValidJSON verifies that the
// //go:embed'd JSON schemas parse cleanly. A regression here would
// cause silent runtime decode failures when the dispatcher tries to
// pass the schema to mcp.Server.AddTool.
func TestSchemas_EmbeddedRawMessagesAreValidJSON(t *testing.T) {
	t.Parallel()

	var probe map[string]any

	require.NoErrorf(t, json.Unmarshal(shellRunInput, &probe),
		"shellRunInput must be valid JSON object: %s", shellRunInput)
	require.NoErrorf(t, json.Unmarshal(shellOutput, &probe),
		"shellOutput must be valid JSON object: %s", shellOutput)
}
