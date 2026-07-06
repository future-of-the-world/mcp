// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeOffsetCursor(t *testing.T) {
	t.Parallel()

	encoded := EncodeOffsetCursor(0)
	assert.NotEmpty(t, encoded)

	// Verify it is valid base64url (no +/ or = padding).
	assert.NotContains(t, encoded, "+")
	assert.NotContains(t, encoded, "/")
	assert.NotContains(t, encoded, "=")
}

func TestEncodeDecodeOffsetCursor(t *testing.T) {
	t.Parallel()

	tests := []int{0, 1, 10, 100, 999999}

	for _, offset := range tests {
		t.Run("offset", func(t *testing.T) {
			t.Parallel()

			encoded := EncodeOffsetCursor(offset)
			decoded, ok := DecodeOffsetCursor(encoded)

			require.True(t, ok)
			assert.Equal(t, offset, decoded)
		})
	}
}

func TestDecodeOffsetCursor_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"not_base64", "!!!not-base64!!!"},
		{"wrong_prefix", "x:5"},
		{"missing_colon", "o5"},
		{"not_number", "o:abc"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, ok := DecodeOffsetCursor(testCase.input)
			assert.False(t, ok)
		})
	}
}

func TestEncodeDecodeBodyCursor(t *testing.T) {
	t.Parallel()

	tests := []int{0, 100, 8000, 50000}

	for _, offset := range tests {
		t.Run("offset", func(t *testing.T) {
			t.Parallel()

			encoded := EncodeBodyCursor(offset)
			decoded, ok := DecodeBodyCursor(encoded)

			require.True(t, ok)
			assert.Equal(t, offset, decoded)
		})
	}
}

func TestDecodeBodyCursor_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"wrong_prefix", "o:5"},
		{"missing_colon", "b5"},
		{"not_number", "b:xyz"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, ok := DecodeBodyCursor(testCase.input)
			assert.False(t, ok)
		})
	}
}

func TestOffsetCursor_IsNotBodyCursor(t *testing.T) {
	t.Parallel()

	encoded := EncodeOffsetCursor(42)

	_, ok := DecodeBodyCursor(encoded)
	assert.Falsef(t, ok, "offset cursor should not decode as body cursor")
}

func TestBodyCursor_IsNotOffsetCursor(t *testing.T) {
	t.Parallel()

	encoded := EncodeBodyCursor(42)

	_, ok := DecodeOffsetCursor(encoded)
	assert.Falsef(t, ok, "body cursor should not decode as offset cursor")
}
