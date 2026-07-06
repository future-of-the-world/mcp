// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMintResultID(t *testing.T) {
	t.Parallel()

	ClearIDStore()
	t.Cleanup(ClearIDStore)

	resultID := MintResultID("https://example.com/page")

	assert.Truef(t, LooksLikeResultID(resultID), "minted ID should match the result-ID pattern")
	assert.Equal(t, "r_", resultID[:2])
	assert.Len(t, resultID, 14) // "r_" + 12 hex chars
}

func TestMintResultID_Deterministic(t *testing.T) {
	t.Parallel()

	ClearIDStore()
	t.Cleanup(ClearIDStore)

	id1 := MintResultID("https://example.com/page")
	id2 := MintResultID("https://example.com/page")

	assert.Equalf(t, id1, id2, "same URL should produce the same ID")
}

func TestMintResultID_DifferentURLs(t *testing.T) {
	t.Parallel()

	ClearIDStore()
	t.Cleanup(ClearIDStore)

	id1 := MintResultID("https://example.com/a")
	id2 := MintResultID("https://example.com/b")

	assert.NotEqualf(t, id1, id2, "different URLs should produce different IDs")
}

func TestLooksLikeResultID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  bool
	}{
		{"r_a1b2c3d4e5f6", true},
		{"r_0123456789ab", true},
		{"r_" + "000000000000", true},
		{"r_ABCDEF123456", false}, // uppercase not allowed
		{"r_short", false},
		{"r_", false},
		{"a1b2c3d4e5f6", false},   // missing prefix
		{"x_a1b2c3d4e5f6", false}, // wrong prefix
		{"r_a1b2c3d4e5f6extra", false},
		{"", false},
	}

	for _, testCase := range tests {
		t.Run(testCase.input, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.want, LooksLikeResultID(testCase.input))
		})
	}
}

func TestResolveResultID(t *testing.T) {
	t.Parallel()

	ClearIDStore()
	t.Cleanup(ClearIDStore)

	targetURL := "https://example.com/page"
	resultID := MintResultID(targetURL)

	resolved, ok := ResolveResultID(resultID)
	require.True(t, ok)
	assert.Equal(t, targetURL, resolved)
}

func TestResolveResultID_Unknown(t *testing.T) {
	t.Parallel()

	ClearIDStore()
	t.Cleanup(ClearIDStore)

	_, ok := ResolveResultID("r_000000000000")
	assert.False(t, ok)
}

func TestClearIDStore(t *testing.T) {
	t.Parallel()

	ClearIDStore()
	t.Cleanup(ClearIDStore)

	resultID := MintResultID("https://example.com/page")
	ClearIDStore()

	_, ok := ResolveResultID(resultID)
	assert.Falsef(t, ok, "cleared store should not resolve IDs")
}
