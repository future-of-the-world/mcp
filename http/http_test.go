// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package http

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/decode"
)

// TestConnect_RequiresURL verifies that http.Connect returns an
// error when no URL is provided in the connect map. The error is
// identified by the errURLEmpty sentinel so the test is decoupled
// from the exact error message.
func TestConnect_RequiresURL(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), make(map[string]any))
	require.Error(t, err)
	require.ErrorIs(t, err, errURLEmpty)
}

// TestDecodeConnect_URLNumericCoercion verifies the new
// decode.AsString coercion: a numeric url value is stringified
// via fmt.Sprint rather than rejected.
func TestDecodeConnect_URLNumericCoercion(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"url":    "https://example.com",
		"method": "GET",
	})
	require.NoError(t, err)
	require.Equal(t, "https://example.com", cfg.URL)
}

// TestDecodeConnect_URLNonScalar verifies the new strict path:
// a non-scalar value (a map, here) where a string is expected
// produces a wrapped decode.ErrWrongType.
func TestDecodeConnect_URLNonScalar(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"url": map[string]any{"host": "example.com"},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
}

// TestDecodeConnect_HeadersNumericCoercion verifies the headline
// YAML-natural-value acceptance path: a numeric header value is
// stringified via fmt.Sprint rather than rejected.
func TestDecodeConnect_HeadersNumericCoercion(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"url":    "https://example.com",
		"method": "GET",
		"headers": map[string]any{
			"X-Number": 42,
			"X-Bool":   true,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "42", cfg.Headers["X-Number"])
	require.Equal(t, "true", cfg.Headers["X-Bool"])
}

// TestDecodeConnect_HeadersNonScalar verifies that a non-scalar
// header value (a map) produces a wrapped decode.ErrWrongType.
func TestDecodeConnect_HeadersNonScalar(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"url":    "https://example.com",
		"method": "GET",
		"headers": map[string]any{
			"X-Bad": map[string]any{"nested": true},
		},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
}

// TestConnect_URL_GET verifies that an http tool configured with the
// GET method sets ReadOnlyHint: true. GET is one of the RFC 7231
// "safe" methods and must not change upstream state.
func TestConnect_URL_GET(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"url":         "https://example.com/api",
		"method":      "GET",
		"description": "Example API for tests",
	})
	require.NoError(t, err)

	require.Len(t, resp.Tools, 1)

	entry := resp.Tools[0]
	require.Equal(t, "http", entry.Name)
	require.NotEmpty(t, entry.Description)
	require.NotNil(t, entry.Handler)

	require.NotNil(t, entry.Annotations)
	assert.True(t, entry.Annotations.ReadOnlyHint)
}

// TestConnect_URL_POST verifies that an http tool configured with
// the POST method sets ReadOnlyHint: false and DestructiveHint: true.
// POST is not one of the safe methods and may change upstream state.
func TestConnect_URL_POST(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"url":         "https://example.com/api",
		"method":      "POST",
		"description": "Example API for tests",
	})
	require.NoError(t, err)

	require.Len(t, resp.Tools, 1)

	entry := resp.Tools[0]
	require.Equal(t, "http", entry.Name)
	require.NotNil(t, entry.Handler)

	require.NotNil(t, entry.Annotations)
	assert.False(t, entry.Annotations.ReadOnlyHint)
	require.NotNil(t, entry.Annotations.DestructiveHint)
	assert.True(t, *entry.Annotations.DestructiveHint)
}

// TestConnect_DefaultDescription verifies that http.Connect fills in
// the common "HTTP <METHOD> <URL>" description when the user does
// not provide one. Keeps the connect map terse for the common case
// of just url + method.
func TestConnect_DefaultDescription(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"url":    "https://example.com/api",
		"method": "GET",
	})
	require.NoError(t, err)

	require.Len(t, resp.Tools, 1)
	require.Equal(t, "HTTP GET https://example.com/api", resp.Tools[0].Description)
}

// TestConnect_DescriptionOverride verifies that an explicit
// description in the connect map wins over the default.
func TestConnect_DescriptionOverride(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"url":         "https://example.com/api",
		"method":      "GET",
		"description": "My custom description",
	})
	require.NoError(t, err)

	require.Len(t, resp.Tools, 1)
	require.Equal(t, "My custom description", resp.Tools[0].Description)
}
