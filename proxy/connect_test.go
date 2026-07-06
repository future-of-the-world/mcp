// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package proxy

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/decode"
)

// TestConnect_RequiresURLOrCommand verifies the errURLEmpty /
// errCommandEmpty sentinels (joined via errors.Join) are returned
// when neither url nor command is provided in the connect map.
// The two sentinels must both be errors.Is-able.
func TestConnect_RequiresURLOrCommand(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), make(map[string]any))
	require.Error(t, err)
	require.ErrorIs(t, err, errURLEmpty)
	require.ErrorIs(t, err, errCommandEmpty)
}

// TestConnect_RejectsNonScalarURL verifies the new decode.ErrWrongType
// path: a non-scalar value (here, a map) where a string is expected
// produces a wrapped decode.ErrWrongType, propagating the actual Go
// type in the message. Numeric values are now coerced (covered at the
// decodeConnect unit-test layer in proxy_test.go).
func TestConnect_RejectsNonScalarURL(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"url": map[string]any{"host": "example.com"},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
	require.Contains(t, err.Error(), "map[string]interface {}")
}

// TestConnect_RejectsNonScalarCommand mirrors TestConnect_RejectsNonScalarURL
// for the command field.
func TestConnect_RejectsNonScalarCommand(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"command": map[string]any{"bin": "ls"},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
	require.Contains(t, err.Error(), "map[string]interface {}")
}

// TestConnect_RejectsNonScalarHeaders verifies that the
// decodeStringMap helper surfaces a wrapped decode.ErrWrongType
// when a header value is a non-scalar type (map). Numeric values
// are still coerced (covered in proxy_test.go's per-field tests).
func TestConnect_RejectsNonScalarHeaders(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"url":     "https://example.com",
		"headers": map[string]any{"X-Foo": map[string]any{"nested": true}},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
	require.Contains(t, err.Error(), "X-Foo")
}

// TestConnect_AcceptsNumericURL is covered at the decodeConnect
// unit-test layer in proxy_test.go (TestDecodeConnect_URLNumericCoercion
// and friends). Driving the full Connect path requires a real
// upstream server just to assert the decode step didn't fail,
// which is heavy for a unit test.

// TestSessionNotReady is a tiny unit test for the
// errSessionNotReady sentinel. The makeCallHandler function uses
// this to guard against a nil session; this test exercises that
// guard directly so the coverage pipeline sees the branch.
func TestSessionNotReady(t *testing.T) {
	t.Parallel()

	handler := makeCallHandler((*mcp.ClientSession)(nil), "any_tool")
	_, err := handler(t.Context(), (*mcp.CallToolRequest)(nil))
	require.Error(t, err)
	require.ErrorIs(t, err, errSessionNotReady)
}
