// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/decode"
)

// TestConnect_Defaults exercises the full websearch.Connect path with
// an empty config. No Brave API key means only the 3 unconditional
// tools (web_search, fetch_url, list_providers) are returned. All 3
// set Annotations: ReadOnlyHint=true per the per-type Annotations
// policy.
func TestConnect_Defaults(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), make(map[string]any))
	require.NoError(t, err)
	require.Len(t, resp.Tools, 3)

	actualAnnotations := make(map[string]bool, len(resp.Tools))

	actualNames := make(map[string]bool, len(resp.Tools))
	for _, entry := range resp.Tools {
		actualNames[entry.Name] = true
		if entry.Annotations != nil {
			actualAnnotations[entry.Name] = entry.Annotations.ReadOnlyHint
		}
	}

	// All 3 unconditional tools must be present and tagged
	// ReadOnlyHint=true.
	for _, name := range []string{"web_search", "fetch_url", "list_providers"} {
		require.Truef(t, actualNames[name], "expected tool %q in Connect response", name)
		assert.Truef(t, actualAnnotations[name],
			"tool %q should have ReadOnlyHint=true per the per-type Annotations policy", name)
	}
}

// TestConnect_WithTimeout verifies that a duration string is
// correctly parsed and applied to the config.
func TestConnect_WithTimeout(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"timeout": "5s",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Tools)
}

// TestConnect_InvalidTimeoutFormat verifies that a non-parseable
// duration string is rejected at decode time. The per-package
// Duration type wraps time.ParseDuration.
func TestConnect_InvalidTimeoutFormat(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"timeout": "not-a-duration",
	})
	require.Error(t, err)
}

// TestConnect_AcceptsNumericTimeout verifies the new
// decode.AsString coercion: a numeric timeout value is
// stringified via fmt.Sprint (→ "5") and then handed to
// time.ParseDuration, which rejects it as "missing unit".
// The error message is the parse error, not the old
// "must be a string" — that's the headline behavior change.
func TestConnect_AcceptsNumericTimeout(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"timeout": 5, // coerced to "5", ParseDuration rejects "5"
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing unit")
}

// TestConnect_RejectsNonScalarTimeout verifies the new strict
// path: a non-scalar value (a map) where a string is expected
// produces a wrapped decode.ErrWrongType.
func TestConnect_RejectsNonScalarTimeout(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"timeout": map[string]any{"seconds": 5},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
}
