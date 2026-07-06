// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package postgres

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// textResult: response wrapper (mirrors tracker/githab handlers)
// ---------------------------------------------------------------------------

func TestTextResult_Object(t *testing.T) {
	t.Parallel()

	result, err := textResult(map[string]any{"id": 7, "name": "sprocket"})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.JSONEq(t, `{"id":7,"name":"sprocket"}`, textContent.Text)
	require.NotNil(t, result.StructuredContent)
}

func TestTextResult_Array(t *testing.T) {
	t.Parallel()

	result, err := textResult([]int{1, 2, 3})
	require.NoError(t, err)
	require.NotNil(t, result)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, "[1,2,3]", textContent.Text)
	require.Nil(t, result.StructuredContent)
}

func TestTextResult_ChannelMarshalError(t *testing.T) {
	t.Parallel()

	result, err := textResult(make(chan int))
	require.Error(t, err)
	require.Contains(t, err.Error(), "marshal response")
	require.Nil(t, result)
}

// ---------------------------------------------------------------------------
// sanitizeValue / sanitizeBinary / sanitizeRow: row-level value handling
// ---------------------------------------------------------------------------

func TestSanitizeValue_Bytes(t *testing.T) {
	t.Parallel()

	got := sanitizeValue([]byte(`{"k":1}`))
	_, ok := got.(json.RawMessage)
	require.Truef(t, ok, "expected json.RawMessage, got %T", got)
}

func TestSanitizeValue_NonBytesPassThrough(t *testing.T) {
	t.Parallel()

	require.Equal(t, "hello", sanitizeValue("hello"))
	require.Equal(t, 42, sanitizeValue(42))
	require.Nil(t, sanitizeValue(any(nil)))
}

func TestSanitizeBinary_ValidJSON(t *testing.T) {
	t.Parallel()

	got := sanitizeBinary([]byte(`{"a":1}`))
	_, ok := got.(json.RawMessage)
	require.True(t, ok)
}

func TestSanitizeBinary_InvalidJSONPassesThrough(t *testing.T) {
	t.Parallel()

	input := []byte{0x00, 0x01, 0x02}
	got := sanitizeBinary(input)
	out, ok := got.([]byte)
	require.True(t, ok)
	require.Equal(t, input, out)
}

func TestSanitizeRow_AppliesPerElement(t *testing.T) {
	t.Parallel()

	row := []any{
		[]byte(`{"x":1}`),
		"hello",
		42,
	}

	sanitizeRow(row)

	_, ok := row[0].(json.RawMessage)
	require.True(t, ok)
	require.Equal(t, "hello", row[1])
	require.Equal(t, 42, row[2])
}

func TestSanitizeRow_Empty(t *testing.T) {
	t.Parallel()

	row := []any{}
	sanitizeRow(row)
	require.Empty(t, row)
}

// ---------------------------------------------------------------------------
// stripOneLeadingComment / stripLeadingComments / hasReadOnlyPrefix /
// validateCTEIsReadOnly / isReadOnlyQuery: SQL read-only analyzer
// ---------------------------------------------------------------------------

func TestStripOneLeadingComment_SingleLine(t *testing.T) {
	t.Parallel()

	got, changed := stripOneLeadingComment("-- this is a comment\nSELECT 1")
	require.True(t, changed)
	require.Equal(t, "SELECT 1", got)
}

func TestStripOneLeadingComment_SingleLineNoNewline(t *testing.T) {
	t.Parallel()

	got, changed := stripOneLeadingComment("-- just a comment")
	require.False(t, changed)
	require.Equal(t, "-- just a comment", got)
}

func TestStripOneLeadingComment_MultiLine(t *testing.T) {
	t.Parallel()

	got, changed := stripOneLeadingComment("/* a comment */ SELECT 1")
	require.True(t, changed)
	require.Equal(t, "SELECT 1", got)
}

func TestStripOneLeadingComment_MultiLineNoClose(t *testing.T) {
	t.Parallel()

	input := "/* unterminated"
	got, changed := stripOneLeadingComment(input)
	require.False(t, changed)
	require.Equal(t, input, got)
}

func TestStripOneLeadingComment_NonComment(t *testing.T) {
	t.Parallel()

	input := "SELECT 1"
	got, changed := stripOneLeadingComment(input)
	require.False(t, changed)
	require.Equal(t, input, got)
}

func TestStripLeadingComments_LoopsUntilDone(t *testing.T) {
	t.Parallel()

	got := stripLeadingComments("-- one\n-- two\nSELECT 1")
	require.Equal(t, "SELECT 1", got)
}

func TestStripLeadingComments_NoComments(t *testing.T) {
	t.Parallel()

	got := stripLeadingComments("SELECT 1")
	require.Equal(t, "SELECT 1", got)
}

func TestHasReadOnlyPrefix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		query string
		want  bool
	}{
		{"SELECT 1", true},
		{"SELECT", true},
		{"SHOW TABLES", true},
		{"EXPLAIN SELECT 1", true},
		{"DESCRIBE foo", true},
		{"DESC foo", true},
		{"WITH x AS (SELECT 1) SELECT * FROM x", true},
		{"TABLE foo", true},
		{"VALUES (1)", true},

		{"INSERT INTO foo VALUES (1)", false},
		{"UPDATE foo SET x = 1", false},
		{"DELETE FROM foo", false},
		{"", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.query, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, testCase.want, hasReadOnlyPrefix(testCase.query))
		})
	}
}

func TestValidateCTEIsReadOnly(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateCTEIsReadOnly("WITH x AS (SELECT 1) SELECT * FROM x"))
	require.NoError(t, validateCTEIsReadOnly("WITH RECURSIVE x AS (SELECT 1) SELECT * FROM x"))

	err := validateCTEIsReadOnly("WITH x AS (SELECT 1) INSERT INTO foo SELECT * FROM x")
	require.Error(t, err)
	require.Contains(t, err.Error(), "modifying statement")
}

func TestIsReadOnlyQuery(t *testing.T) {
	t.Parallel()

	require.NoError(t, isReadOnlyQuery("SELECT 1"))
	require.NoError(t, isReadOnlyQuery("  select 1  "))
	require.NoError(t, isReadOnlyQuery("-- comment\nSELECT 1"))
	require.NoError(t, isReadOnlyQuery("WITH x AS (SELECT 1) SELECT * FROM x"))

	err := isReadOnlyQuery("INSERT INTO foo VALUES (1)")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrQueryNotReadOnly)

	err = isReadOnlyQuery("WITH x AS (SELECT 1) DELETE FROM foo")
	require.Error(t, err)
	require.Contains(t, err.Error(), "modifying statement")

	err = isReadOnlyQuery("foo bar")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrQueryNotReadOnly)
}
