// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package fs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errNoStructuredContent is the sentinel returned by callTool when
// the handler produced a result but no parseable structured content
// nor any text block could be surfaced. Callers use errors.Is to
// distinguish "no parseable output" from "handler errored".
var errNoStructuredContent = errors.New("callTool: no structured content and no text block")

// callTool runs a tool handler with the given JSON arguments and
// returns the parsed structured result. It returns the Go error
// alongside so test cases can assert on both shapes.
func callTool(t *testing.T, tool toolForTest, args string) (map[string]any, error) {
	t.Helper()

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      tool.Name,
			Arguments: json.RawMessage(args),
		},
	}

	result, err := tool.Handler(t.Context(), req)
	if err != nil {
		return nil, err
	}

	if result == nil {
		return nil, errNoStructuredContent
	}

	var parsed map[string]any

	if result.StructuredContent == nil {
		// No structured content; surface the first text block as
		// a fallback so the test can still inspect it.
		if len(result.Content) > 0 {
			if textContent, ok := result.Content[0].(*mcp.TextContent); ok {
				return map[string]any{"_text": textContent.Text}, nil
			}
		}

		return nil, errNoStructuredContent
	}

	raw, marshalErr := json.Marshal(result.StructuredContent)
	if marshalErr != nil {
		return nil, fmt.Errorf("callTool: marshal structured content: %w", marshalErr)
	}

	unmarshalErr := json.Unmarshal(raw, &parsed)
	if unmarshalErr != nil {
		return nil, fmt.Errorf("callTool: unmarshal structured content: %w", unmarshalErr)
	}

	return parsed, nil
}

// toolForTest pairs a tool name with its handler so callTool can
// dispatch through the same wiring Connect uses.
type toolForTest struct {
	Name    string
	Handler mcp.ToolHandler
}

// toolsFor builds a name → handler map from a Connect response so
// each handler test can grab the tool it needs.
func toolsFor(t *testing.T, connect map[string]any) map[string]toolForTest {
	t.Helper()

	resp, err := Connect(t.Context(), connect)
	require.NoError(t, err)

	out := make(map[string]toolForTest, len(resp.Tools))

	for _, item := range resp.Tools {
		out[item.Name] = toolForTest{Name: item.Name, Handler: item.Handler}
	}

	return out
}

// --- read_file ---

func TestHandler_ReadFile_OK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "hello.txt")

	require.NoError(t, os.WriteFile(path, []byte("hello world"), 0o600))

	tools := toolsFor(t, map[string]any{
		"allowed_paths": []string{root},
	})

	got, err := callTool(t, tools["read_file"], `{"path": "`+path+`"}`)
	require.NoError(t, err)

	assert.Equal(t, "hello world", got["content"])
	assert.EqualValues(t, len("hello world"), got["size_bytes"])
	assert.Equal(t, false, got["is_binary"])
}

// TestHandler_ReadFile_BinaryEncoded verifies the b64: prefix path
// for non-UTF-8 content.
func TestHandler_ReadFile_BinaryEncoded(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "blob.bin")

	require.NoError(t, os.WriteFile(path, []byte{0xFF, 0xFE, 0xFD}, 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["read_file"], `{"path": "`+path+`"}`)
	require.NoError(t, err)

	assert.Equal(t, true, got["is_binary"])
	assert.True(t, strings.HasPrefix(got["content"].(string), binaryPrefix))
}

// readFileFiveLineFile writes a canonical 5-line file used by the
// line-selection tests. The content is "line1\nline2\nline3\nline4
// \nline5" with no trailing newline, so total_lines == 5 and the
// slice boundaries are unambiguous. Centralized so every
// line-selection test exercises the same shape.
func readFileFiveLineFile(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	path := filepath.Join(root, "lines.txt")

	const content = "line1\nline2\nline3\nline4\nline5"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

// TestHandler_ReadFile_StartLineOnly pins the "start_line set,
// end_line absent" branch: the slice must be lines N..EOF.
func TestHandler_ReadFile_StartLineOnly(t *testing.T) {
	t.Parallel()

	path := readFileFiveLineFile(t)

	tools := toolsFor(
		t,
		map[string]any{"allowed_paths": []string{strings.TrimSuffix(path, "/lines.txt")}},
	)

	got, err := callTool(t, tools["read_file"],
		`{"path": "`+path+`", "start_line": 3}`)
	require.NoError(t, err)

	assert.Equal(t, "line3\nline4\nline5", got["content"])
	assert.Equal(t, false, got["is_binary"])
	assert.EqualValues(t, len("line1\nline2\nline3\nline4\nline5"), got["size_bytes"])
	assert.EqualValues(t, len("line3\nline4\nline5"), got["returned_bytes"])
	assert.EqualValues(t, 5, got["total_lines"])
	assert.EqualValues(t, 3, got["returned_lines"])
}

// TestHandler_ReadFile_EndLineOnly pins the "end_line set,
// start_line absent" branch: the slice must be lines 1..M.
func TestHandler_ReadFile_EndLineOnly(t *testing.T) {
	t.Parallel()

	path := readFileFiveLineFile(t)

	tools := toolsFor(
		t,
		map[string]any{"allowed_paths": []string{strings.TrimSuffix(path, "/lines.txt")}},
	)

	got, err := callTool(t, tools["read_file"],
		`{"path": "`+path+`", "end_line": 2}`)
	require.NoError(t, err)

	assert.Equal(t, "line1\nline2", got["content"])
	assert.EqualValues(t, len("line1\nline2\nline3\nline4\nline5"), got["size_bytes"])
	assert.EqualValues(t, len("line1\nline2"), got["returned_bytes"])
	assert.EqualValues(t, 5, got["total_lines"])
	assert.EqualValues(t, 2, got["returned_lines"])
}

// TestHandler_ReadFile_BothLines pins the "start_line AND end_line"
// branch: the slice must be lines [start_line, end_line].
func TestHandler_ReadFile_BothLines(t *testing.T) {
	t.Parallel()

	path := readFileFiveLineFile(t)

	tools := toolsFor(
		t,
		map[string]any{"allowed_paths": []string{strings.TrimSuffix(path, "/lines.txt")}},
	)

	got, err := callTool(t, tools["read_file"],
		`{"path": "`+path+`", "start_line": 2, "end_line": 4}`)
	require.NoError(t, err)

	assert.Equal(t, "line2\nline3\nline4", got["content"])
	assert.EqualValues(t, len("line1\nline2\nline3\nline4\nline5"), got["size_bytes"])
	assert.EqualValues(t, len("line2\nline3\nline4"), got["returned_bytes"])
	assert.EqualValues(t, 5, got["total_lines"])
	assert.EqualValues(t, 3, got["returned_lines"])
}

// TestHandler_ReadFile_SingleLineSlice pins the start_line == end_line
// case: the slice is exactly one line.
func TestHandler_ReadFile_SingleLineSlice(t *testing.T) {
	t.Parallel()

	path := readFileFiveLineFile(t)

	tools := toolsFor(
		t,
		map[string]any{"allowed_paths": []string{strings.TrimSuffix(path, "/lines.txt")}},
	)

	got, err := callTool(t, tools["read_file"],
		`{"path": "`+path+`", "start_line": 3, "end_line": 3}`)
	require.NoError(t, err)

	assert.Equal(t, "line3", got["content"])
	assert.EqualValues(t, 1, got["returned_lines"])
}

// TestHandler_ReadFile_EndLinePastTotal_Clamped pins the
// "end_line > total_lines" silent-clamp contract: the slice runs
// to EOF, no error is raised, and returned_lines reflects the
// actual slice size.
func TestHandler_ReadFile_EndLinePastTotal_Clamped(t *testing.T) {
	t.Parallel()

	path := readFileFiveLineFile(t)

	tools := toolsFor(
		t,
		map[string]any{"allowed_paths": []string{strings.TrimSuffix(path, "/lines.txt")}},
	)

	got, err := callTool(t, tools["read_file"],
		`{"path": "`+path+`", "start_line": 3, "end_line": 9999}`)
	require.NoError(t, err)

	assert.Equal(t, "line3\nline4\nline5", got["content"])
	assert.EqualValues(t, 5, got["total_lines"])
	assert.EqualValues(t, 3, got["returned_lines"])
}

// TestHandler_ReadFile_LastLineDropsTrailingNewline pins the
// "end_line == total_lines drops terminal newline" behavior:
// the file ends with "\n", and the slice covers the full file,
// so the returned content must be the file's content minus the
// trailing newline.
func TestHandler_ReadFile_LastLineDropsTrailingNewline(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "lines.txt")

	const content = "line1\nline2\nline3\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["read_file"],
		`{"path": "`+path+`", "start_line": 1, "end_line": 3}`)
	require.NoError(t, err)

	assert.Equal(t, "line1\nline2\nline3", got["content"])
	assert.EqualValues(t, len(content), got["size_bytes"])
	assert.EqualValues(t, len("line1\nline2\nline3"), got["returned_bytes"])
	assert.EqualValues(t, 3, got["total_lines"])
	assert.EqualValues(t, 3, got["returned_lines"])
}

// TestHandler_ReadFile_NoArgsPreservesTrailingNewline pins the
// "no line args => whole file returned unchanged" contract: a
// file ending in "\n" must come back with the trailing newline
// intact (the bytes.Split trailing-newline-drop applies only
// when the caller asks for a slice that runs to the last line).
func TestHandler_ReadFile_NoArgsPreservesTrailingNewline(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "trailing.txt")

	const content = "line1\nline2\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["read_file"], `{"path": "`+path+`"}`)
	require.NoError(t, err)

	assert.Equal(t, content, got["content"])
	assert.EqualValues(t, len(content), got["size_bytes"])
	assert.EqualValues(t, len(content), got["returned_bytes"])
	assert.EqualValues(t, 2, got["total_lines"])
	assert.EqualValues(t, 2, got["returned_lines"])
}

// TestHandler_ReadFile_EmptyFile pins the "no line args + empty
// file" branch: total_lines is 0, returned_lines is 0, content
// is empty, and the response carries no error. The "no args"
// early return exists so this case does not regress into the
// "start_line > total_lines" error path.
func TestHandler_ReadFile_EmptyFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "empty.txt")

	require.NoError(t, os.WriteFile(path, []byte{}, 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["read_file"], `{"path": "`+path+`"}`)
	require.NoError(t, err)

	assert.Empty(t, got["content"])
	assert.EqualValues(t, 0, got["size_bytes"])
	assert.EqualValues(t, 0, got["returned_bytes"])
	assert.EqualValues(t, 0, got["total_lines"])
	assert.EqualValues(t, 0, got["returned_lines"])
}

// TestHandler_ReadFile_StartLinePastEOF pins the "start_line > total
// _lines" error path: a clear message that names both numbers.
func TestHandler_ReadFile_StartLinePastEOF(t *testing.T) {
	t.Parallel()

	path := readFileFiveLineFile(t)

	tools := toolsFor(
		t,
		map[string]any{"allowed_paths": []string{strings.TrimSuffix(path, "/lines.txt")}},
	)

	_, err := callTool(t, tools["read_file"],
		`{"path": "`+path+`", "start_line": 10}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds file length")
	assert.Contains(t, err.Error(), "10")
	assert.Contains(t, err.Error(), "5")
}

// TestHandler_ReadFile_EndLineBeforeStartLine pins the
// "end_line < start_line" error path. Validation runs before
// any I/O, so a malformed call surfaces its error cheaply.
func TestHandler_ReadFile_EndLineBeforeStartLine(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "f.txt")

	require.NoError(t, os.WriteFile(path, []byte("a\nb\nc"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["read_file"],
		`{"path": "`+path+`", "start_line": 3, "end_line": 1}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "end_line (1) must be >= start_line (3)")
}

// TestHandler_ReadFile_NegativeStartLine pins the pre-I/O
// validation for an out-of-range value that the JSON schema
// might or might not enforce depending on the runtime: the Go
// layer must still reject it with a clear message.
func TestHandler_ReadFile_NegativeStartLine(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "f.txt")

	require.NoError(t, os.WriteFile(path, []byte("a\nb"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["read_file"],
		`{"path": "`+path+`", "start_line": -1}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start_line must be >= 1")
}

// TestHandler_ReadFile_NegativeEndLine pins the pre-I/O
// validation for a negative end_line value.
func TestHandler_ReadFile_NegativeEndLine(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "f.txt")

	require.NoError(t, os.WriteFile(path, []byte("a\nb"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["read_file"],
		`{"path": "`+path+`", "end_line": -5}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "end_line must be >= 1")
}

// TestHandler_ReadFile_BinaryWithLineArgsRejected pins the
// "binary + line args" rejection: the error names the conflict
// and tells the caller to drop the line fields. No b64 payload
// leaks into the error message.
func TestHandler_ReadFile_BinaryWithLineArgsRejected(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "blob.bin")

	require.NoError(t, os.WriteFile(path, []byte{0xFF, 0xFE, 0xFD}, 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["read_file"],
		`{"path": "`+path+`", "start_line": 1}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UTF-8 text files only")
	assert.Contains(t, err.Error(), "drop start_line/end_line")
}

// TestHandler_ReadFile_BinaryShapePreserved pins the contract
// that binary files without line arguments keep their existing
// output shape byte-for-byte: no `returned_bytes`, no
// `total_lines`, no `returned_lines` keys appear.
func TestHandler_ReadFile_BinaryShapePreserved(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "blob.bin")

	require.NoError(t, os.WriteFile(path, []byte{0xFF, 0xFE, 0xFD}, 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["read_file"], `{"path": "`+path+`"}`)
	require.NoError(t, err)

	assert.Equal(t, true, got["is_binary"])
	assert.True(t, strings.HasPrefix(got["content"].(string), binaryPrefix))
	assert.EqualValues(t, 3, got["size_bytes"])

	// Text-file-only fields must NOT appear on the binary
	// response. The handler omits them entirely rather than
	// emitting a zero value, so the keys must be absent.
	_, hasReturnedBytes := got["returned_bytes"]
	_, hasTotalLines := got["total_lines"]
	_, hasReturnedLines := got["returned_lines"]

	assert.Falsef(t, hasReturnedBytes, "binary response must not include returned_bytes")
	assert.Falsef(t, hasTotalLines, "binary response must not include total_lines")
	assert.Falsef(t, hasReturnedLines, "binary response must not include returned_lines")
}

// TestHandler_ReadFile_TextShapeAddsFields pins the contract
// that text files always include `returned_bytes`,
// `total_lines`, and `returned_lines`, even when no line
// arguments are supplied (where they mirror the whole-file
// shape: returned_bytes == size_bytes,
// returned_lines == total_lines).
func TestHandler_ReadFile_TextShapeAddsFields(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "hello.txt")

	require.NoError(t, os.WriteFile(path, []byte("hello world"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["read_file"], `{"path": "`+path+`"}`)
	require.NoError(t, err)

	assert.Equal(t, "hello world", got["content"])
	assert.Equal(t, false, got["is_binary"])
	assert.EqualValues(t, len("hello world"), got["size_bytes"])
	assert.EqualValues(t, len("hello world"), got["returned_bytes"])
	assert.EqualValues(t, 1, got["total_lines"])
	assert.EqualValues(t, 1, got["returned_lines"])
}

// TestHandler_ReadFile_OutsideAllowed pins the path-guard chokepoint:
// a path outside the allowlist returns a tool error.
func TestHandler_ReadFile_OutsideAllowed(t *testing.T) {
	t.Parallel()

	allowed := t.TempDir()
	outside := t.TempDir()
	path := filepath.Join(outside, "secret.txt")

	require.NoError(t, os.WriteFile(path, []byte("nope"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{allowed}})

	_, err := callTool(t, tools["read_file"], `{"path": "`+path+`"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the configured allowed paths")
}

// --- write_file ---

func TestHandler_WriteFile_OK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dest := filepath.Join(root, "subdir", "out.txt")

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["write_file"], `{"path": "`+dest+`", "content": "hi"}`)
	require.NoError(t, err)

	realRoot, realRootErr := canonicalRoot(root)
	require.NoError(t, realRootErr)

	assert.Equal(t, filepath.Join(realRoot, "subdir", "out.txt"), got["path"])
	assert.EqualValues(t, 2, got["bytes_written"])

	onDisk, readErr := os.ReadFile(dest)
	require.NoError(t, readErr)
	assert.Equal(t, "hi", string(onDisk))
}

// TestHandler_WriteFile_Base64 verifies the base64 encoding path.
func TestHandler_WriteFile_Base64(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dest := filepath.Join(root, "blob.bin")

	// "\x01\x02" base64-encoded = "AQI="
	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["write_file"],
		`{"path": "`+dest+`", "content": "AQI=", "encoding": "base64"}`)
	require.NoError(t, err)

	onDisk, readErr := os.ReadFile(dest)
	require.NoError(t, readErr)
	assert.Equal(t, []byte{0x01, 0x02}, onDisk)
}

// TestHandler_WriteFile_ExceedsMax verifies the size cap.
func TestHandler_WriteFile_ExceedsMax(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dest := filepath.Join(root, "big.txt")

	tools := toolsFor(t, map[string]any{
		"allowed_paths":   []string{root},
		"max_write_bytes": 4,
	})

	_, err := callTool(t, tools["write_file"],
		`{"path": "`+dest+`", "content": "this is longer than four bytes"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_write_bytes")
}

// TestHandler_WriteFile_BadEncoding verifies that an unknown
// encoding surfaces a clear error.
func TestHandler_WriteFile_BadEncoding(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dest := filepath.Join(root, "x.bin")

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["write_file"],
		`{"path": "`+dest+`", "content": "hello", "encoding": "rot13"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encoding")
}

// TestHandler_WriteFile_BadBase64 verifies that a non-base64
// payload surfaces the decoding error.
func TestHandler_WriteFile_BadBase64(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dest := filepath.Join(root, "x.bin")

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["write_file"],
		`{"path": "`+dest+`", "content": "not valid base64!!!", "encoding": "base64"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base64")
}

// TestDecodeWriteContent_BadEncoding is a unit test for the
// decoder helper that drives the write_file encoding branch.
func TestDecodeWriteContent_BadEncoding(t *testing.T) {
	t.Parallel()

	_, err := decodeWriteContent("hi", "rot13")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encoding")
}

// TestDecodeWriteContent_BadBase64 covers the bad-base64 branch.
func TestDecodeWriteContent_BadBase64(t *testing.T) {
	t.Parallel()

	_, err := decodeWriteContent("not-base64!@#$", "base64")
	require.Error(t, err)
}

// --- edit_file ---

func TestHandler_EditFile_OK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "f.txt")

	require.NoError(t, os.WriteFile(path, []byte("hello world"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["edit_file"],
		`{"path": "`+path+`", "old_text": "world", "new_text": "earth"}`)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got["replacements"])

	onDisk, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "hello earth", string(onDisk))
}

// TestHandler_EditFile_NotFound verifies the strict-once contract:
// old_text that is not present at all is rejected.
func TestHandler_EditFile_NotFound(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "f.txt")

	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["edit_file"],
		`{"path": "`+path+`", "old_text": "missing", "new_text": "x"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestHandler_EditFile_MultipleMatches verifies the strict-once
// contract: old_text that occurs more than once is rejected.
func TestHandler_EditFile_MultipleMatches(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "f.txt")

	require.NoError(t, os.WriteFile(path, []byte("foo foo foo"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["edit_file"],
		`{"path": "`+path+`", "old_text": "foo", "new_text": "bar"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "3 times")
}

// --- create_directory ---

func TestHandler_CreateDirectory_OK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "a", "b", "c")

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["create_directory"], `{"path": "`+target+`"}`)
	require.NoError(t, err)

	info, statErr := os.Stat(target)
	require.NoError(t, statErr)
	assert.True(t, info.IsDir())
}

// TestHandler_CreateDirectory_ExistingIsNoOp verifies that
// creating a directory that already exists is a no-op (mkdir -p
// semantics).
func TestHandler_CreateDirectory_ExistingIsNoOp(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "preexisting")

	require.NoError(t, os.MkdirAll(target, 0o750))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["create_directory"], `{"path": "`+target+`"}`)
	require.NoError(t, err)
}

// TestHandler_CreateDirectory_RefusesFileTarget verifies that the
// handler refuses to clobber an existing regular file.
func TestHandler_CreateDirectory_RefusesFileTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "iamafile")

	require.NoError(t, os.WriteFile(target, []byte("hi"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["create_directory"], `{"path": "`+target+`"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

// --- list_directory ---

func TestHandler_ListDirectory_OK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o750))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["list_directory"], `{"path": "`+root+`"}`)
	require.NoError(t, err)

	entries, ok := got["entries"].([]any)
	require.True(t, ok)
	assert.Len(t, entries, 2)
}

// TestHandler_ListDirectory_NotDirectory verifies the kind check.
func TestHandler_ListDirectory_NotDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "x.txt")

	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["list_directory"], `{"path": "`+file+`"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

// --- directory_tree ---

func TestHandler_DirectoryTree_OK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sub := filepath.Join(root, "sub")

	require.NoError(t, os.MkdirAll(sub, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "b.txt"), []byte("y"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["directory_tree"], `{"path": "`+root+`"}`)
	require.NoError(t, err)

	assert.Equal(t, "directory", got["type"])

	children, ok := got["children"].([]any)
	require.True(t, ok)
	assert.NotEmpty(t, children)
}

// --- move_file ---

func TestHandler_MoveFile_OK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	src := filepath.Join(root, "from.txt")
	dst := filepath.Join(root, "to.txt")

	require.NoError(t, os.WriteFile(src, []byte("hello"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["move_file"],
		`{"source": "`+src+`", "destination": "`+dst+`"}`)
	require.NoError(t, err)

	realRoot, realRootErr := canonicalRoot(root)
	require.NoError(t, realRootErr)

	assert.Equal(t, filepath.Join(realRoot, "to.txt"), got["destination"])

	_, statErr := os.Stat(src)
	assert.True(t, os.IsNotExist(statErr))

	onDisk, readErr := os.ReadFile(dst)
	require.NoError(t, readErr)
	assert.Equal(t, "hello", string(onDisk))
}

// --- copy_file ---

func TestHandler_CopyFile_OK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	src := filepath.Join(root, "from.txt")
	dst := filepath.Join(root, "to.txt")

	require.NoError(t, os.WriteFile(src, []byte("hello"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["copy_file"],
		`{"source": "`+src+`", "destination": "`+dst+`"}`)
	require.NoError(t, err)

	assert.EqualValues(t, 5, got["bytes_copied"])

	srcData, srcReadErr := os.ReadFile(src)
	require.NoError(t, srcReadErr)

	dstData, dstReadErr := os.ReadFile(dst)
	require.NoError(t, dstReadErr)
	assert.Equal(t, string(srcData), string(dstData))
}

// TestHandler_CopyFile_RefusesDirectory verifies that copy_file
// refuses directory sources (the contract is regular files only).
func TestHandler_CopyFile_RefusesDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o750))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["copy_file"],
		`{"source": "`+filepath.Join(root, "sub")+`",`+
			` "destination": "`+filepath.Join(root, "sub2")+`"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "regular files only")
}

// --- delete_file ---

func TestHandler_DeleteFile_OK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "doomed.txt")

	require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["delete_file"], `{"path": "`+target+`"}`)
	require.NoError(t, err)

	_, statErr := os.Stat(target)
	assert.True(t, os.IsNotExist(statErr))
}

// TestHandler_DeleteFile_RefusesNonEmptyDir pins the recursive-delete
// refusal: the LLM must walk the tree first.
func TestHandler_DeleteFile_RefusesNonEmptyDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sub := filepath.Join(root, "sub")

	require.NoError(t, os.MkdirAll(sub, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "inside.txt"), []byte("x"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["delete_file"], `{"path": "`+sub+`"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-empty")
}

// TestHandler_DeleteFile_NotFound verifies that deleting a missing
// path surfaces ENOENT cleanly.
func TestHandler_DeleteFile_NotFound(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["delete_file"],
		`{"path": "`+filepath.Join(root, "nope.txt")+`"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope.txt")
}

// TestHandler_CopyFile_OutsideAllowed verifies the path guard
// rejects cross-allowlist copies.
func TestHandler_CopyFile_OutsideAllowed(t *testing.T) {
	t.Parallel()

	allowed := t.TempDir()
	outside := t.TempDir()

	src := filepath.Join(outside, "src.txt")
	dst := filepath.Join(allowed, "dst.txt")

	require.NoError(t, os.WriteFile(src, []byte("x"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{allowed}})

	_, err := callTool(t, tools["copy_file"],
		`{"source": "`+src+`", "destination": "`+dst+`"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the configured allowed paths")
}

// TestHandler_MoveFile_OutsideAllowed verifies the path guard
// rejects cross-allowlist moves.
func TestHandler_MoveFile_OutsideAllowed(t *testing.T) {
	t.Parallel()

	allowed := t.TempDir()
	outside := t.TempDir()

	src := filepath.Join(outside, "src.txt")
	dst := filepath.Join(allowed, "dst.txt")

	require.NoError(t, os.WriteFile(src, []byte("x"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{allowed}})

	_, err := callTool(t, tools["move_file"],
		`{"source": "`+src+`", "destination": "`+dst+`"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the configured allowed paths")
}

// TestHandler_DeleteFile_EmptyDirOK verifies that empty directories
// can be removed.
func TestHandler_DeleteFile_EmptyDirOK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sub := filepath.Join(root, "empty")

	require.NoError(t, os.MkdirAll(sub, 0o750))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["delete_file"], `{"path": "`+sub+`"}`)
	require.NoError(t, err)
}

// TestHandler_DeleteFile_ParallelBatch_EmptiesAndRemovesDirs is the
// regression test for forgejo issue #116: a parallel batch of
// delete_file calls — interleaved file and directory deletes where
// every directory's children are also deleted in the same batch —
// must complete without any spurious "non-empty directory" errors.
//
// The contract this test pins:
//
//   - deleteDirectoryIfEmpty calls os.ReadDir(resolved) on every
//     invocation. No call relies on a snapshot, a session-level
//     cache, or any prior read; the FS state at the instant of
//     invocation is what decides whether the directory can be
//     removed.
//
//   - Multiple delete_file calls within the same Go process see
//     each other's effects on disk, regardless of goroutine
//     ordering, because each call's "is it empty?" check is live.
//
// The bug report (fs-mcp-bug-report.md, Bug 3) describes an
// observed behavior in a real parallel-batch scenario where some
// directory-deletion calls reported "non-empty" even after sibling
// file deletes in the same batch had emptied the directory. The
// investigation found no stale-state layer in the current Go code
// (the dispatcher's middlewares only mutate tool names, and the
// only in-process caches are in the unrelated websearch package),
// so the symptom is treated as outside this codebase. This test
// pins the contract in case a future change accidentally introduces
// a snapshot or in-memory directory listing.
func TestHandler_DeleteFile_ParallelBatch_EmptiesAndRemovesDirs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	plans := deleteParallelBatchPlans()

	for _, plan := range plans {
		dir := filepath.Join(root, plan.name)
		require.NoError(t, os.MkdirAll(dir, 0o750))

		for _, file := range plan.files {
			require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte("x"), 0o600))
		}
	}

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	results := deleteEachDirInParallel(t, tools["delete_file"], root, plans)

	var failures []string

	for _, res := range results {
		if res.err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", res.dir, res.err))
		}
	}

	require.Emptyf(t, failures,
		"delete_file parallel batch produced errors: %s",
		strings.Join(failures, "; "))

	for _, plan := range plans {
		dir := filepath.Join(root, plan.name)
		_, statErr := os.Stat(dir)

		assert.Truef(t, os.IsNotExist(statErr),
			"directory %s should be deleted after parallel batch, stat err: %v",
			plan.name, statErr)
	}
}

// deleteDirPlan is one directory's file layout for the parallel-batch
// regression test.
type deleteDirPlan struct {
	name  string
	files []string
}

// deleteParallelBatchPlans returns the canonical "three directories,
// mixed file counts" layout for the parallel-batch regression test.
// One directory has two files so the batch has at least as many
// file deletes as directory deletes — exactly one directory's
// children get emptied by *two* concurrent file deletes, exercising
// the parallel-batch contract.
func deleteParallelBatchPlans() []deleteDirPlan {
	return []deleteDirPlan{
		{name: "docs", files: []string{"r.md"}},
		{name: "src", files: []string{"a.py", "b.py"}},
		{name: "tests", files: []string{"t.py"}},
	}
}

// deleteBatchResult pairs an error with the directory it came from
// so a calling test can attribute per-directory failures clearly.
type deleteBatchResult struct {
	dir string
	err error
}

// planDeleteState carries the per-goroutine context for the
// parallel-batch regression test. The struct is the parameter
// bundle that drives the per-plan goroutine; using a struct keeps
// runPlanDelete below revive's argument-limit threshold.
type planDeleteState struct {
	deleteFile toolForTest
	root       string
	plan       deleteDirPlan
	results    chan<- deleteBatchResult
}

// deleteEachDirInParallel fans out one goroutine per plan; each
// goroutine deletes every file in its directory and then deletes
// the directory once the contents are gone. The function returns
// after every goroutine has either errored or completed a dir
// removal. Goroutines cover the case where sibling file deletes
// are still in flight when a directory-delete call evaluates
// emptiness.
func deleteEachDirInParallel(
	t *testing.T,
	deleteFile toolForTest,
	root string,
	plans []deleteDirPlan,
) []deleteBatchResult {
	t.Helper()

	results := make(chan deleteBatchResult, len(plans))

	var wg sync.WaitGroup

	for _, plan := range plans {
		wg.Add(1)

		state := planDeleteState{
			deleteFile: deleteFile,
			root:       root,
			plan:       plan,
			results:    results,
		}

		go func(state *planDeleteState) {
			defer wg.Done()

			state.run(t)
		}(&state)
	}

	wg.Wait()

	close(results)

	out := make([]deleteBatchResult, 0, len(plans))

	for res := range results {
		out = append(out, res)
	}

	return out
}

// run executes the per-plan delete sequence: every file in the
// directory is deleted first; the directory itself is deleted
// afterwards. Each failure is reported on results so the calling
// test can attribute the per-directory cause. Pulled out of
// deleteEachDirInParallel's goroutine to keep that orchestrator
// below gocognit's complexity threshold.
func (s *planDeleteState) run(t *testing.T) {
	t.Helper()

	dir := filepath.Join(s.root, s.plan.name)

	for _, file := range s.plan.files {
		path := filepath.Join(dir, file)

		_, fileErr := callTool(t, s.deleteFile, `{"path": "`+path+`"}`)
		if fileErr != nil {
			s.results <- deleteBatchResult{dir: s.plan.name, err: fileErr}

			return
		}
	}

	_, dirErr := callTool(t, s.deleteFile, `{"path": "`+dir+`"}`)
	s.results <- deleteBatchResult{dir: s.plan.name, err: dirErr}
}

// TestHandler_DeleteFile_FreshReadDirPerCall pins the per-call
// fresh-ReadDir contract for deleteDirectoryIfEmpty. Two sequential
// delete_file calls against the same directory — one blocked while
// its children are still on disk, then a second after the children
// are gone — must each evaluate emptiness from the live filesystem.
// If anyone wraps deleteDirectoryIfEmpty in a session-level cache
// or memoised listing in the future, this test will catch it
// because the second call must observe the by-then-empty directory
// and succeed.
func TestHandler_DeleteFile_FreshReadDirPerCall(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sub := filepath.Join(root, "dir")

	require.NoError(t, os.MkdirAll(sub, 0o750))

	target := filepath.Join(sub, "child.txt")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, firstErr := callTool(t, tools["delete_file"], `{"path": "`+sub+`"}`)
	require.Error(t, firstErr)
	assert.Contains(t, firstErr.Error(), "non-empty")

	_, deleteChildErr := callTool(t, tools["delete_file"], `{"path": "`+target+`"}`)
	require.NoError(t, deleteChildErr)

	_, secondErr := callTool(t, tools["delete_file"], `{"path": "`+sub+`"}`)
	require.NoErrorf(t, secondErr,
		"second delete_file on %s after children removed must succeed; "+
			"if it reports 'non-empty' the handler is reading stale "+
			"directory state", sub)
}

// --- search_files ---

func TestHandler_SearchFiles_OK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(root, "foo.txt"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "bar.txt"), []byte("y"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "foo.md"), []byte("z"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["search_files"],
		`{"root": "`+root+`", "pattern": "*.txt"}`)
	require.NoError(t, err)

	matches, ok := got["matches"].([]any)
	require.True(t, ok)
	assert.Len(t, matches, 2)
}

// --- get_file_info ---

// TestHandler_GetFileInfo_OK verifies the stat happy path.
func TestHandler_GetFileInfo_OK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "info.txt")

	require.NoError(t, os.WriteFile(target, []byte("hello"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["get_file_info"], `{"path": "`+target+`"}`)
	require.NoError(t, err)

	assert.EqualValues(t, 5, got["size_bytes"])
	assert.Equal(t, false, got["is_dir"])
	// os.WriteFile honors the exact mode passed (0o600), so the
	// info call reports "0600" rather than the default umask.
	assert.Equal(t, "0600", got["mode"])
}

// TestHandler_GetFileInfo_NotFound verifies that stat on a missing
// path surfaces ENOENT.
func TestHandler_GetFileInfo_NotFound(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["get_file_info"],
		`{"path": "`+filepath.Join(root, "missing.txt")+`"}`)
	require.Error(t, err)
}

// TestHandler_DirectoryTree_NotDirectory verifies the kind check.
func TestHandler_DirectoryTree_NotDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "file.txt")

	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["directory_tree"], `{"path": "`+file+`"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

// TestHandler_DirectoryTree_NestedShape pins the recursive
// {name, type, children} contract end-to-end. A three-level layout
// must produce a nested tree (not a flat file stat), every node
// must carry exactly the documented keys, and stat keys
// (path, mode, modified_at, is_dir, size_bytes) — which belong to
// get_file_info — must never appear at any depth. This is the
// regression test for the bug originally reported as "directory_tree
// returns flat stat instead of nested tree" (issue #117).
func TestHandler_DirectoryTree_NestedShape(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "top.txt"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "leaf.txt"), []byte("y"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "b", "deep.txt"), []byte("z"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["directory_tree"],
		`{"path": "`+root+`", "max_depth": 5}`)
	require.NoError(t, err)

	// Root node: name matches the requested path's basename,
	// type is "directory", and the node carries exactly
	// {name, type, children} — no stat keys.
	assert.Equal(t, filepath.Base(root), got["name"])
	assert.Equal(t, "directory", got["type"])
	requireNodeKeys(t, "root", got, "name", "type", "children")

	rootChildren := childrenSlice(t, "root", got)
	require.Lenf(t, rootChildren, 2, "root must have exactly two top-level entries")

	// Root children: "a" (directory) and "top.txt" (file).
	aNode := findChild(t, rootChildren, "a")
	topNode := findChild(t, rootChildren, "top.txt")

	assert.Equal(t, "directory", aNode["type"])
	requireNodeKeys(t, "a", aNode, "name", "type", "children")

	assert.Equal(t, "file", topNode["type"])
	requireNodeKeys(t, "top.txt", topNode, "name", "type", "children")
	// File leaves must serialize to an empty array (not null, not
	// absent) — this matches the directory_tree_output schema's
	// `type: "array"` and keeps LLM consumers free of null checks.
	assert.Emptyf(t, childrenSlice(t, "top.txt", topNode), "file leaf must have empty children")

	// Descend one level: a/ contains b (directory) and leaf.txt (file).
	aChildren := childrenSlice(t, "a", aNode)
	require.Lenf(t, aChildren, 2, "a must have exactly two entries")

	bNode := findChild(t, aChildren, "b")
	leafNode := findChild(t, aChildren, "leaf.txt")

	assert.Equal(t, "directory", bNode["type"])
	assert.Equal(t, "file", leafNode["type"])
	assert.Empty(t, childrenSlice(t, "leaf.txt", leafNode))

	// Descend two more levels: a/b/deep.txt (file at depth three).
	bChildren := childrenSlice(t, "b", bNode)
	require.Lenf(t, bChildren, 1, "b must have exactly one entry")

	deepNode := findChild(t, bChildren, "deep.txt")
	assert.Equal(t, "file", deepNode["type"])
	assert.Empty(t, childrenSlice(t, "deep.txt", deepNode))

	// Walk the entire tree and assert no node carries stat keys.
	// This is the chokepoint regression: a future refactor that
	// accidentally returns a flat file-info shape (path / mode /
	// modified_at / is_dir / size_bytes) at any depth will trip
	// these assertions even if the root still looks correct.
	forbidden := []string{"path", "mode", "modified_at", "is_dir", "size_bytes"}
	walkTree(t, got, func(node map[string]any) {
		for _, key := range forbidden {
			_, present := node[key]
			assert.Falsef(t, present,
				"directory_tree node %q must not carry %q key (that belongs to get_file_info)",
				node["name"], key)
		}
	})
}

// TestHandler_DirectoryTree_MaxDepthTruncates pins the max_depth
// truncation contract: directories at the depth cap must still be
// reported with type "directory" but with an empty children array.
// This protects against a refactor that drops the directory leaf
// when it can't recurse any further.
func TestHandler_DirectoryTree_MaxDepthTruncates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub", "deeper"), 0o750))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	// max_depth=1 means root's direct children are listed but the
	// "sub" directory leaf at depth 1 must not be expanded into
	// "sub/deeper".
	got, err := callTool(t, tools["directory_tree"],
		`{"path": "`+root+`", "max_depth": 1}`)
	require.NoError(t, err)

	rootChildren := childrenSlice(t, "root", got)
	require.Lenf(t, rootChildren, 1, "root must have exactly one direct child")

	sub := findChild(t, rootChildren, "sub")
	assert.Equalf(t, "directory", sub["type"], "sub at max_depth must still be type directory")
	requireNodeKeys(t, "sub", sub, "name", "type", "children")

	// Truncated directory: children must be an empty array
	// (not absent, not null) so the schema's `type: "array"`
	// holds uniformly.
	subChildren := childrenSlice(t, "sub", sub)
	assert.Emptyf(t, subChildren, "directory at max_depth must have empty children array")
}

// requireNodeKeys asserts node has exactly the listed keys (no
// more, no less). It is the workhorse for the "every node carries
// exactly the documented shape and nothing else" assertion in the
// directory_tree regression tests.
func requireNodeKeys(t *testing.T, label string, node map[string]any, expected ...string) {
	t.Helper()

	expectedSet := make(map[string]struct{}, len(expected))
	for _, key := range expected {
		expectedSet[key] = struct{}{}
	}

	for _, key := range expected {
		_, ok := node[key]
		assert.Truef(t, ok, "%s must carry %q key", label, key)
	}

	for key := range node {
		if _, want := expectedSet[key]; !want {
			t.Errorf("%s must not carry %q key (documented shape is exactly %v)",
				label, key, expected)
		}
	}
}

// childrenSlice returns node["children"] as []map[string]any. It
// fails the test if the key is missing, has the wrong type, or
// any element isn't an object. Used by the directory_tree
// regression tests to walk the recursive tree uniformly.
func childrenSlice(t *testing.T, label string, node map[string]any) []map[string]any {
	t.Helper()

	raw, ok := node["children"].([]any)
	require.Truef(t, ok,
		"%s.children must be a JSON array (got %T)", label, node["children"])

	out := make([]map[string]any, 0, len(raw))
	for i, item := range raw {
		asMap, ok := item.(map[string]any)
		require.Truef(t, ok, "%s.children[%d] must be a JSON object", label, i)

		out = append(out, asMap)
	}

	return out
}

// findChild returns the child whose name matches name. It fails
// the test if no such child exists, so callers can rely on a
// non-nil result.
func findChild(t *testing.T, children []map[string]any, name string) map[string]any {
	t.Helper()

	for _, child := range children {
		if child["name"] == name {
			return child
		}
	}

	t.Fatalf("no child with name %q (have %d children)", name, len(children))

	return nil
}

// walkTree visits every node in the recursive tree rooted at root
// (root included) and calls visit on each one. It is used to
// assert invariants that must hold at every depth — e.g. that no
// stat key from get_file_info leaks into a directory_tree node.
func walkTree(t *testing.T, root map[string]any, visit func(map[string]any)) {
	t.Helper()
	visit(root)

	for _, child := range childrenSlice(t, "walk", root) {
		walkTree(t, child, visit)
	}
}

// TestHandler_SearchFiles_NotDirectory verifies the kind check.
func TestHandler_SearchFiles_NotDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "file.txt")

	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["search_files"],
		`{"root": "`+file+`", "pattern": "*"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

// TestHandler_SearchFiles_NestedMatchesBasename pins the fix for
// the basename-glob contract: "*.py" must match files in
// subdirectories, not just root-level files. TestHandler_SearchFiles_OK
// only exercises a flat root, so this is the regression test for
// filepath.Match's '*'-does-not-cross-separators semantics.
func TestHandler_SearchFiles_NestedMatchesBasename(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	srcDir := filepath.Join(root, "src")
	deepDir := filepath.Join(srcDir, "subdir")

	// Nested layout: python files scattered across src and a
	// deeper subdirectory, plus one non-python file to confirm
	// the pattern is not over-broad.
	require.NoError(t, os.MkdirAll(srcDir, 0o750))
	require.NoError(t, os.MkdirAll(deepDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "calculator.py"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "parser.py"), []byte("y"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(deepDir, "deep.py"), []byte("z"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("w"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["search_files"],
		`{"root": "`+root+`", "pattern": "*.py"}`)
	require.NoError(t, err)

	matches, ok := got["matches"].([]any)
	require.True(t, ok)
	assert.Len(t, matches, 3)
}

// TestHandler_SearchFiles_FlatMatchesBasename confirms the
// basename-based match does not regress the simple flat-root
// case: "*.py" must still match root-level files when there are
// no nested matches to confuse the picture.
func TestHandler_SearchFiles_FlatMatchesBasename(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(root, "foo.py"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "bar.py"), []byte("y"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("z"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["search_files"],
		`{"root": "`+root+`", "pattern": "*.py"}`)
	require.NoError(t, err)

	matches, ok := got["matches"].([]any)
	require.True(t, ok)
	assert.Len(t, matches, 2)
}

// TestHandler_SearchFiles_EmptyMatchesArray pins the contract
// that "no matches" serializes as an empty array, not null. The
// old typed-nil initialization marshaled to null, which the LLM
// could not distinguish from an internal error.
func TestHandler_SearchFiles_EmptyMatchesArray(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(root, "foo.txt"), []byte("x"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["search_files"],
		`{"root": "`+root+`", "pattern": "*.nonexistent"}`)
	require.NoError(t, err)

	// got["matches"] must be a JSON array, not null. With the
	// old typed-nil initialization, json.Unmarshal produced nil
	// here; with the non-nil empty slice, it is []any{}.
	matches, ok := got["matches"].([]any)
	require.Truef(t, ok,
		"matches must be a JSON array, got %T (%v)", got["matches"], got["matches"])
	assert.Empty(t, matches)
}

// TestHandler_SearchFiles_InvalidPattern verifies that an invalid
// glob pattern (unbalanced bracket) propagates as a tool error
// rather than silently surfacing as matches: null. The pattern is
// validated by filepath.Match inside matchSearchPattern; the
// error travels back through walkState.visit → filepath.WalkDir →
// walkAndMatch → searchFiles.
func TestHandler_SearchFiles_InvalidPattern(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(root, "foo.py"), []byte("x"), 0o600))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["search_files"],
		`{"root": "`+root+`", "pattern": "["}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "match pattern")
}

// --- list_allowed_directories ---

func TestHandler_ListAllowedDirectories_OK(t *testing.T) {
	t.Parallel()

	allowed := t.TempDir()

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{allowed}})

	got, err := callTool(t, tools["list_allowed_directories"], `{}`)
	require.NoError(t, err)

	paths, ok := got["allowed_paths"].([]any)
	require.True(t, ok)
	require.Len(t, paths, 1)

	// The returned path is canonical (realRoot). Both sides are
	// run through canonicalRoot so the assertion holds regardless
	// of which macOS-style mount-point symlink the OS introduces
	// (/tmp → /private/tmp on this machine, /var/folders →
	// /private/var/folders on the default macOS temp dir, etc.) —
	// and is also correct on Linux, where EvalSymlinks is a no-op
	// for t.TempDir().
	gotPath := paths[0].(string)

	expectedCanonical, expectedErr := canonicalRoot(allowed)
	require.NoError(t, expectedErr)

	gotCanonical, gotErr := canonicalRoot(gotPath)
	require.NoError(t, gotErr)

	assert.Equalf(t, expectedCanonical, gotCanonical,
		"returned allowed path must canonicalize to the same path as the input; "+
			"got %q (canonical %q), expected %q (canonical %q)",
		gotPath, gotCanonical, allowed, expectedCanonical)
}

// --- per-path locking regression tests ---
//
// The tests below pin the contract introduced by fs-source-
// concurrent-safety: every mutating handler wraps its I/O in
// withPathLock (or withPathTwoLocks for two-path operations), and
// read_file takes withPathRLock so concurrent reads serialize
// against any in-flight writer on the same resolved path. Each
// test exercises a single invariant from that contract end-to-end
// via the public callTool dispatch path, so a future regression
// that drops a withPathLock call or introduces a torn-write path
// will trip one or more of these assertions.

// parallelNReaders is the number of concurrent read goroutines
// fan-outs in Test 3 and Test 10. Eight keeps the test fast while
// giving the race-detector enough scheduling variety to surface a
// torn read; with --race both writers and readers run on
// independent goroutines so the per-path RWMutex contention is
// exercised under realistic scheduling pressure.
const parallelNReaders = 8

// parallelNWriters is the fan-out count for the writer-only
// parallel tests (WriteFile, CopyFile, MoveFile, DeleteFile,
// CreateDirectory). Eight writers give the atomic-write +
// per-path-lock combination enough contention to surface a
// regression on -race.
const parallelNWriters = 8

// parallelNMoveCallers is the smaller fan-out for
// TestHandler_MoveFile_ParallelSameSourceLoudFails. Four is
// enough to assert the "exactly one wins" contract; more callers
// would create so many failing renames that the per-destination
// ambiguity becomes harder to reason about.
const parallelNMoveCallers = 4

// parallelNDeleteCallers mirrors parallelNMoveCallers for the
// delete-same-path contract.
const parallelNDeleteCallers = 4

// fanOutN runs workfn on n goroutines behind a barrier: each
// goroutine blocks on `<-start`, and the caller closes `start`
// after every goroutine has been registered with `wg`. The
// returned slice has length n and preserves goroutine launch
// order. Pulled out of the per-test fixtures so each regression
// test stays focused on the invariant it pins rather than on
// channel-and-waitgroup boilerplate.
func fanOutN(
	t *testing.T,
	count int,
	workFn func(idx int),
) {
	t.Helper()

	start := make(chan struct{})

	var wg sync.WaitGroup

	for idx := range count {
		wg.Go(func() {
			<-start
			workFn(idx)
		})
	}

	close(start)
	wg.Wait()
}

// collectErrors drains errs into a slice, in order, dropping
// nil entries. Used by the fan-out tests so "every call must
// succeed" assertions check the failure count rather than the
// total count: a channel of `chan error` carries both nil
// (success) and non-nil (failure) entries, and a test that
// receives from a caller goroutine wants only the failures.
func collectErrors(errs <-chan error) []error {
	out := make([]error, 0)

	for err := range errs {
		if err == nil {
			continue
		}

		out = append(out, err)
	}

	return out
}

// tornReadOutcome pairs a read_file content string with any
// error the call returned, so torn-read and read-during-write
// fixtures capture both successful reads and read failures
// uniformly for later assertion.
type tornReadOutcome struct {
	content string
	err     error
}

// runRacingReaders fans out `nReaders` read_file goroutines
// that hammer the supplied path in a tight loop. The returned
// teardown function closes the done channel, joins every
// reader, drains the in-flight outcomes into a slice, and
// returns it. The slice is only populated inside teardown
// (after all readers have exited and the result channel is
// closed), so callers can iterate it without locking. The
// pattern mirrors deleteEachDirInParallel: goroutines send to
// a channel; the orchestrator drains the channel sequentially
// once the WaitGroup has joined.
func runRacingReaders(
	t *testing.T,
	readFile toolForTest,
	path string,
	nReaders int,
) (teardown func() []tornReadOutcome) {
	t.Helper()

	readResults := make(chan tornReadOutcome, nReaders*1024)

	done := make(chan struct{})

	var readersWG sync.WaitGroup

	for range nReaders {
		readersWG.Go(func() {
			cfg := racingReadConfig{
				test:     t,
				readFile: readFile,
				path:     path,
				done:     done,
				results:  readResults,
			}
			runRacingReadLoop(cfg)
		})
	}

	teardown = func() []tornReadOutcome {
		close(done)
		readersWG.Wait()
		close(readResults)

		collected := make([]tornReadOutcome, 0)

		for outcome := range readResults {
			collected = append(collected, outcome)
		}

		return collected
	}

	return teardown
}

// racingReadConfig bundles the per-goroutine inputs of a
// torn-read fan-out into one parameter, keeping
// runRacingReadLoop below revive:argument-limit's 4-arg cap
// without losing readability at the call site.
type racingReadConfig struct {
	test     *testing.T
	readFile toolForTest
	path     string
	done     <-chan struct{}
	results  chan<- tornReadOutcome
}

// runRacingReadLoop is the per-goroutine body of a torn-read
// fan-out: loop forever reading, push each result, and exit
// the moment `done` is closed. The send into `results` is
// inside a select with `done` so a stuck collector cannot
// wedge a reader past the teardown signal.
func runRacingReadLoop(cfg racingReadConfig) {
	cfg.test.Helper()

	for {
		select {
		case <-cfg.done:
			return

		default:
		}

		got, readErr := callTool(cfg.test, cfg.readFile, `{"path": "`+cfg.path+`"}`)

		content := ""
		if readErr == nil {
			content = got["content"].(string)
		}

		select {
		case cfg.results <- tornReadOutcome{content: content, err: readErr}:
		case <-cfg.done:
			return
		}
	}
}

// countMoveWinners walks the candidate destination paths and
// counts those that exist as a regular file whose contents
// equal expectedContent. The function exists to keep the
// move_file parallel regression test below gocognit's
// complexity threshold by extracting the per-destination
// stat+read+compare step from the assertion block.
func countMoveWinners(
	t *testing.T,
	destPaths []string,
	expectedContent string,
) int {
	t.Helper()

	winners := 0

	for _, destPath := range destPaths {
		info, infoErr := os.Stat(destPath)
		if infoErr != nil {
			continue
		}

		if info.IsDir() {
			continue
		}

		data, readErr := os.ReadFile(destPath)
		require.NoError(t, readErr)

		if string(data) == expectedContent {
			winners++
		}
	}

	return winners
}

// --- TestHandler_EditFile_ParallelDisjointEditsBothPersist ---

// TestHandler_EditFile_ParallelDisjointEditsBothPersist pins the
// per-path lock contract for edit_file: two goroutines issuing
// edit_file calls against disjoint old_text blocks on the same
// path must both succeed and the final on-disk file must contain
// both replacements. Without the per-path write lock the read-
// modify-write inside each call can interleave so the second
// edit lands on top of the first's pre-edit view and silently
// clobbers the first replacement. The bug this guards against
// is "stale-read lost write": one goroutine reads the file,
// reads "MARKER_ONE", schedules the rename; a sibling goroutine
// reads the same file, reads "MARKER_ONE" too, schedules its
// rename; whichever rename runs second wins, and one of the two
// edits is silently lost. With the per-path lock serialized, the
// second goroutine observes the first edit's commit before it
// reads.
func TestHandler_EditFile_ParallelDisjointEditsBothPersist(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "f.txt")

	initial := "prefix MARKER_ONE middle MARKER_TWO suffix"

	require.NoError(t, os.WriteFile(path, []byte(initial), fileCreateMode))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	type editOp struct {
		label   string
		oldText string
		newText string
	}

	type editOutcome struct {
		label string
		err   error
	}

	edits := []editOp{
		{label: "one", oldText: "MARKER_ONE", newText: "REPLACED_ONE"},
		{label: "two", oldText: "MARKER_TWO", newText: "REPLACED_TWO"},
	}

	results := make(chan editOutcome, len(edits))
	fanOutN(t, len(edits), func(idx int) {
		entry := edits[idx]

		_, callErr := callTool(t, tools["edit_file"],
			`{"path": "`+path+`", "old_text": "`+entry.oldText+
				`", "new_text": "`+entry.newText+`"}`)

		results <- editOutcome{label: entry.label, err: callErr}
	})

	close(results)

	var failures []string

	for res := range results {
		if res.err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", res.label, res.err))
		}
	}

	require.Emptyf(t, failures,
		"parallel disjoint edit_file calls must both succeed; without the per-path "+
			"lock the read-modify-write races and one replacement is silently lost; "+
			"failures: %s",
		strings.Join(failures, "; "))

	onDisk, readErr := os.ReadFile(path)
	require.NoError(t, readErr)

	assert.Equal(t,
		"prefix REPLACED_ONE middle REPLACED_TWO suffix",
		string(onDisk))
}

// --- TestHandler_EditFile_ParallelOverlappingEditsOneSurvives ---

// TestHandler_EditFile_ParallelOverlappingEditsOneSurvives pins
// the strict-once contract under concurrent edit_file: when two
// goroutines attempt to replace the SAME old_text with DIFFERENT
// new_text on the same path, exactly one must succeed (its
// new_text lands in the file) and the other must surface a
// "not found" tool error because the surviving edit already
// removed the old_text. Without the per-path lock both
// goroutines could read the file, both could observe the
// old_text exactly once, both could commit their replacement,
// and the file would silently contain the last writer's new_text
// with no signal that the second edit's success was actually a
// race-induced mistake.
func TestHandler_EditFile_ParallelOverlappingEditsOneSurvives(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "f.txt")

	require.NoError(t, os.WriteFile(path, []byte("foo bar baz"), fileCreateMode))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	type editOutcome struct {
		newText string
		err     error
	}

	outcomes := make(chan editOutcome, 2)

	fanOutN(t, 2, func(idx int) {
		// Two distinct new_text values so the test can identify
		// the surviving call by inspecting the on-disk content.
		newText := "BAR_A"
		if idx == 1 {
			newText = "BAR_B"
		}

		_, callErr := callTool(t, tools["edit_file"],
			`{"path": "`+path+`", "old_text": "foo", "new_text": "`+newText+`"}`)

		outcomes <- editOutcome{newText: newText, err: callErr}
	})

	close(outcomes)

	var successes, failures []editOutcome

	for outcome := range outcomes {
		if outcome.err != nil {
			failures = append(failures, outcome)
		} else {
			successes = append(successes, outcome)
		}
	}

	require.Lenf(t, successes, 1,
		"exactly one of two parallel edit_file calls on the same old_text must "+
			"succeed; got %d successes and %d failures",
		len(successes), len(failures))

	require.Lenf(t, failures, 1,
		"the second parallel edit_file on the same old_text must surface a "+
			"'not found' error rather than silently winning the race; "+
			"got %d failures", len(failures))

	assert.Containsf(t, failures[0].err.Error(), "not found",
		"losing edit_file must report 'not found', got: %v", failures[0].err)

	onDisk, readErr := os.ReadFile(path)
	require.NoError(t, readErr)

	survivorSuffix := string(successes[0].newText[len(successes[0].newText)-1])

	assert.Equalf(t, "BAR_"+survivorSuffix+" bar baz", string(onDisk),
		"on-disk content must match the surviving edit_file's new_text verbatim")
}

// --- TestHandler_EditFile_NoTornReadsDuringWrite ---

// TestHandler_EditFile_NoTornReadsDuringWrite pins the atomicity
// contract for edit_file: while one goroutine replaces a small
// chunk of content with a same-length replacement, parallel
// readers running read_file in a tight loop must NEVER observe a
// partial mix of the pre-edit and post-edit strings. The
// invariant is "every concurrent read sees either fully pre-edit
// or fully post-edit content", enforced by withPathRLock
// serializing readers against the writer and by atomicWriteFile
// making the rename at the kernel level a single visible state
// transition. A regression that drops either protection would
// surface here as a read returning both alpha and BRAVO in the
// same payload — that torn-read assertion is the chokepoint.
func TestHandler_EditFile_NoTornReadsDuringWrite(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "f.txt")

	const preEdit = "alpha"

	const postEdit = "BRAVO"

	require.NoError(t, os.WriteFile(path, []byte(preEdit), fileCreateMode))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	finish := runRacingReaders(t, tools["read_file"], path, parallelNReaders)

	// Head start so at least some reads observe the pre-edit
	// state; without it a very fast run could leave the test
	// with only post-edit reads and weaken the "no torn
	// reads" assertion (a torn read is detectable pre-edit,
	// but the symmetry is what protects against skew).
	time.Sleep(50 * time.Millisecond)

	_, writeErr := callTool(t, tools["edit_file"],
		`{"path": "`+path+`", "old_text": "`+preEdit+`", "new_text": "`+postEdit+`"}`)
	require.NoError(t, writeErr)

	collected := finish()

	var readErrors []error

	for _, outcome := range collected {
		if outcome.err != nil {
			readErrors = append(readErrors, outcome.err)

			continue
		}

		// The chokepoint: a torn read would contain BOTH
		// alpha and BRAVO in its payload. A valid pre-edit
		// read contains alpha (and no BRAVO); a valid
		// post-edit read contains BRAVO (and no alpha); a
		// torn read contains both. The Contains +
		// NotContains pair makes the exclusive-or
		// explicit: at most one of the two strings
		// appears in any single read.
		containsOld := strings.Contains(outcome.content, preEdit)
		containsNew := strings.Contains(outcome.content, postEdit)

		if containsOld {
			assert.NotContainsf(t, outcome.content, postEdit,
				"torn read: contains old chunk %q AND new chunk %q; "+
					"content=%q",
				preEdit, postEdit, outcome.content)
		}

		if containsNew {
			assert.NotContainsf(t, outcome.content, preEdit,
				"torn read: contains new chunk %q AND old chunk %q; "+
					"content=%q",
				postEdit, preEdit, outcome.content)
		}
	}

	assert.Emptyf(t, readErrors,
		"every read_file call during the edit must succeed; "+
			"errors: %v", readErrors)
}

// --- TestHandler_EditFile_SequentialSameOldTextFails ---

// TestHandler_EditFile_SequentialSameOldTextFails pins the
// strict-once contract for edit_file in the sequential case: once
// an edit_file call replaces "foo" with "bar", a second
// edit_file call using old_text="foo" must fail with "not found"
// because the old text no longer appears in the file. The test
// exists separately from the parallel variant to pin both the
// "strict-once" property of the replacement and the fact that
// the failure mode is a clean tool error rather than a silent
// zero-replacement no-op.
func TestHandler_EditFile_SequentialSameOldTextFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "f.txt")

	require.NoError(t, os.WriteFile(path, []byte("foo"), fileCreateMode))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, firstErr := callTool(t, tools["edit_file"],
		`{"path": "`+path+`", "old_text": "foo", "new_text": "bar"}`)
	require.NoError(t, firstErr)

	// Second call uses the same old_text; with "foo" already
	// replaced, the file no longer contains it, so the
	// strict-once check must report "not found" rather than
	// silently no-op.
	_, secondErr := callTool(t, tools["edit_file"],
		`{"path": "`+path+`", "old_text": "foo", "new_text": "qux"}`)
	require.Error(t, secondErr)
	assert.Contains(t, secondErr.Error(), "not found")

	onDisk, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "bar", string(onDisk))
}

// --- TestHandler_WriteFile_ParallelLastWriterWins ---

// TestHandler_WriteFile_ParallelLastWriterWins pins the
// "concurrent writes serialize, last writer wins" contract for
// write_file: when N goroutines issue write_file calls with
// distinct sentinel payloads against the same path, every call
// must succeed (the per-path lock makes the operation safe to
// issue in parallel) and the final on-disk content must match
// EXACTLY one of the sentinels (no prefix mixing, no truncation,
// no interleaving). The atomicity is guaranteed by
// atomicWriteFile: each write goes through a temp file +
// os.Rename, so a reader mid-write sees either the pre-write or
// post-write bytes — not the half-written tmp buffer. A
// regression that drops withPathLock or skips the temp-file
// dance would surface as either an opaque error or a final
// file whose content does not equal any of the sentinels.
func TestHandler_WriteFile_ParallelLastWriterWins(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "f.txt")

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	sentinels := make([]string, parallelNWriters)

	for idx := range parallelNWriters {
		sentinels[idx] = fmt.Sprintf("sentinel_%d", idx)
	}

	writeErrs := make(chan error, parallelNWriters)

	fanOutN(t, parallelNWriters, func(idx int) {
		_, callErr := callTool(t, tools["write_file"],
			`{"path": "`+path+`", "content": "`+sentinels[idx]+`"}`)

		writeErrs <- callErr
	})

	close(writeErrs)

	writeErrors := collectErrors(writeErrs)
	require.Emptyf(t, writeErrors,
		"every parallel write_file call on the same path must succeed; "+
			"errors: %v", writeErrors)

	onDisk, readErr := os.ReadFile(path)
	require.NoError(t, readErr)

	assert.Truef(t, slices.Contains(sentinels, string(onDisk)),
		"final on-disk content must exactly equal one of the parallel-write "+
			"sentinels; got %q, sentinels=%v",
		string(onDisk), sentinels)
}

// --- TestHandler_CopyFile_ParallelLastWriterWins ---

// TestHandler_CopyFile_ParallelLastWriterWins pins the
// "destination-side lock prevents silent last-writer-wins
// corruption" contract for copy_file: when N goroutines copy the
// same source onto the same destination, every call must succeed
// and the final on-disk content must equal the source content
// EXACTLY (no truncation, no mixing, no prefix from one copy
// glued onto suffix from another). The destination is created
// empty before the fan-out so every copy_file observes a
// pre-existing destination — this is the realistic shape of the
// concurrent-copy bug the lock guards against: two writes to
// the same fd racing on byte-stream offsets.
func TestHandler_CopyFile_ParallelLastWriterWins(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	src := filepath.Join(root, "src.txt")
	dst := filepath.Join(root, "dst.txt")

	const payload = "the quick brown fox jumps over the lazy dog"

	require.NoError(t, os.WriteFile(src, []byte(payload), fileCreateMode))

	// Pre-create an empty destination. The pre-existence
	// guarantees O_CREAT does not race with O_TRUNC, so every
	// copy_file observes the same starting state.
	require.NoError(t, os.WriteFile(dst, []byte{}, fileCreateMode))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	copyErrs := make(chan error, parallelNWriters)

	fanOutN(t, parallelNWriters, func(_ int) {
		_, callErr := callTool(t, tools["copy_file"],
			`{"source": "`+src+`", "destination": "`+dst+`"}`)

		copyErrs <- callErr
	})

	close(copyErrs)

	copyErrors := collectErrors(copyErrs)
	require.Emptyf(t, copyErrors,
		"every parallel copy_file call to the same destination must succeed; "+
			"errors: %v", copyErrors)

	onDisk, readErr := os.ReadFile(dst)
	require.NoError(t, readErr)

	assert.Equalf(t, payload, string(onDisk),
		"final destination must equal source exactly; got %q",
		string(onDisk))
}

// --- TestHandler_MoveFile_ParallelSameSourceLoudFails ---

// TestHandler_MoveFile_ParallelSameSourceLoudFails pins the
// "source-side lock prevents silent duplicate moves" contract
// for move_file: when N goroutines issue move_file with the SAME
// source and DIFFERENT destinations, exactly one call must
// succeed (the file ends up at its chosen destination with the
// source's content) and the other N-1 calls must surface a
// tool error matching "no such file" or the source path — they
// must not silently no-op or move an empty file. Without the
// per-path lock on the source, all N callers could read the
// source's existence, all N could rename, and the file would
// land at one destination chosen non-deterministically while
// the others appeared to succeed.
func TestHandler_MoveFile_ParallelSameSourceLoudFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	src := filepath.Join(root, "src.txt")

	const payload = "movable payload"

	require.NoError(t, os.WriteFile(src, []byte(payload), fileCreateMode))

	destinations := make([]string, parallelNMoveCallers)

	for idx := range parallelNMoveCallers {
		destinations[idx] = filepath.Join(root, fmt.Sprintf("dst_%d.txt", idx))
	}

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	type moveOutcome struct {
		destination string
		err         error
	}

	outcomes := make(chan moveOutcome, parallelNMoveCallers)

	fanOutN(t, parallelNMoveCallers, func(idx int) {
		_, callErr := callTool(t, tools["move_file"],
			`{"source": "`+src+`", "destination": "`+destinations[idx]+`"}`)

		outcomes <- moveOutcome{destination: destinations[idx], err: callErr}
	})

	close(outcomes)

	var successes, failures []moveOutcome

	for outcome := range outcomes {
		if outcome.err != nil {
			failures = append(failures, outcome)
		} else {
			successes = append(successes, outcome)
		}
	}

	require.Lenf(t, successes, 1,
		"exactly one of %d parallel move_file calls on the same source must "+
			"succeed; got %d successes and %d failures",
		parallelNMoveCallers, len(successes), len(failures))

	require.Lenf(t, failures, parallelNMoveCallers-1,
		"the other %d move_file calls must surface a tool error rather than "+
			"silently succeeding on an empty source",
		parallelNMoveCallers-1)

	for _, fail := range failures {
		assert.Truef(t,
			strings.Contains(fail.err.Error(), "no such file") ||
				strings.Contains(fail.err.Error(), src),
			"failed move_file must surface either 'no such file' or the "+
				"source path %q in its error; got: %v",
			src, fail.err)
	}

	// Source must be gone.
	_, statErr := os.Stat(src)
	assert.Truef(t, os.IsNotExist(statErr),
		"source %q must be gone after the surviving move; stat err: %v",
		src, statErr)

	// countMoveWinners asserts per-destination that the file
	// exists as a regular file and its content equals the
	// source payload; the test then asserts exactly one of
	// the four destinations won the rename race.
	winners := countMoveWinners(t, destinations, payload)

	assert.Equalf(t, 1, winners,
		"exactly one destination must exist on disk after the parallel move "+
			"fan-out; got %d", winners)
}

// --- TestHandler_DeleteFile_ParallelSamePathLoudFails ---

// TestHandler_DeleteFile_ParallelSamePathLoudFails pins the
// "concurrent deletes on the same path do not silently no-op"
// contract for delete_file: when N goroutines issue delete_file
// for the SAME path, exactly one call must succeed and the
// other N-1 must surface a tool error matching "no such file"
// (the path was removed by the winning caller). Without the
// per-path lock, all N callers could stat-then-remove and all
// could return success — the LLM would observe no error and
// have no way to tell that the operation was already completed
// by a sibling. The on-disk check confirms the file is gone.
func TestHandler_DeleteFile_ParallelSamePathLoudFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "victim.txt")

	require.NoError(t, os.WriteFile(target, []byte("x"), fileCreateMode))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	deleteErrs := make(chan error, parallelNDeleteCallers)

	fanOutN(t, parallelNDeleteCallers, func(_ int) {
		_, callErr := callTool(t, tools["delete_file"],
			`{"path": "`+target+`"}`)

		deleteErrs <- callErr
	})

	close(deleteErrs)

	collected := collectErrors(deleteErrs)

	require.Lenf(t, collected, parallelNDeleteCallers-1,
		"exactly %d of %d parallel delete_file calls on the same path must "+
			"surface a tool error; got %d errors",
		parallelNDeleteCallers-1, parallelNDeleteCallers, len(collected))

	for idx, errVal := range collected {
		assert.Truef(t,
			strings.Contains(errVal.Error(), "no such file") ||
				strings.Contains(errVal.Error(), target),
			"failed delete_file[%d] must surface either 'no such file' or "+
				"the target path %q in its error; got: %v",
			idx, target, errVal)
	}

	_, statErr := os.Stat(target)
	assert.Truef(t, os.IsNotExist(statErr),
		"target %q must be gone after the surviving delete", target)
}

// --- TestHandler_CreateDirectory_ParallelNoError ---

// TestHandler_CreateDirectory_ParallelNoError pins the
// "mkdir -p semantics under concurrent repeat invocations"
// contract for create_directory: when N goroutines issue
// create_directory on the SAME path, every call must succeed
// (the lock prevents EEXIST from racing with EEXIST and surfacing
// as a tool error). The on-disk check confirms the directory
// exists and is a directory. A regression that drops
// withPathLock around createDirectoryLocked would allow two
// callers to race between the stat and the mkdir, surfacing a
// spurious "exists and is not a directory" or "file exists"
// error to one of the callers.
func TestHandler_CreateDirectory_ParallelNoError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "made", "by", "parallel", "callers")

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	mkdirErrs := make(chan error, parallelNWriters)

	fanOutN(t, parallelNWriters, func(_ int) {
		_, callErr := callTool(t, tools["create_directory"],
			`{"path": "`+target+`"}`)

		mkdirErrs <- callErr
	})

	close(mkdirErrs)

	mkdirErrors := collectErrors(mkdirErrs)

	require.Emptyf(t, mkdirErrors,
		"every parallel create_directory call on the same path must succeed "+
			"(mkdir -p + per-path lock prevents EEXIST races); errors: %v",
		mkdirErrors)

	info, statErr := os.Stat(target)
	require.NoError(t, statErr)
	assert.Truef(t, info.IsDir(),
		"target %q must exist and be a directory after parallel mkdir", target)
}

// --- TestHandler_ReadFile_DuringEditNoTornRead ---

// TestHandler_ReadFile_DuringEditNoTornRead pins the read-during-
// edit torn-read contract end-to-end. One goroutine replaces the
// entire file content with a single line; `parallelNReaders`
// goroutines hammer read_file in a tight loop. Each successful
// read's content must equal EXACTLY one of two valid states —
// the pre-edit three-line content or the post-edit one-line
// content — never a partial mix. The atomicity is enforced by
// atomicWriteFile (rename at the kernel level is a single state
// transition) and the per-path RWMutex (readers serialize
// against the writer). A regression in either guarantee would
// surface as a read returning e.g. "line1\nline2\nAFTER\n" —
// a partial mix that the equality assertion below catches.
func TestHandler_ReadFile_DuringEditNoTornRead(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "f.txt")

	preEdit := "line1\nline2\nline3\n"
	postEdit := "AFTER\n"

	// File on disk gets the unescaped bytes; JSON argument
	// gets the escaped form so the JSON parser doesn't
	// choke on real newlines in a string literal.
	require.NoError(t, os.WriteFile(path, []byte(preEdit), fileCreateMode))

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	finish := runRacingReaders(t, tools["read_file"], path, parallelNReaders)

	// Head start: enough for at least some reads to observe
	// the pre-edit state. Without the sleep a very fast run
	// could leave the test with only post-edit reads and miss
	// the "no torn pre-edit reads" half of the invariant.
	time.Sleep(50 * time.Millisecond)

	// Build the edit_file argument via fmt.Sprintf with %q so
	// the newlines in preEdit / postEdit are escaped as \n in
	// the JSON literal (raw string concatenation would
	// otherwise inject real newlines into the string literal
	// and trip the JSON parser).
	editArgs := fmt.Sprintf(
		`{"path": %q, "old_text": %q, "new_text": %q}`,
		path, preEdit, postEdit,
	)

	_, writeErr := callTool(t, tools["edit_file"], editArgs)
	require.NoError(t, writeErr)

	collected := finish()

	var torn []string

	var readErrors []error

	var preCount, postCount int

	for _, outcome := range collected {
		if outcome.err != nil {
			readErrors = append(readErrors, outcome.err)

			continue
		}

		// The strict chokepoint: a read's content must be one
		// of the two known-good states, never a partial mix
		// that would imply a torn read between the pre-edit
		// buffer and the post-edit rename.
		if outcome.content == preEdit {
			preCount++

			continue
		}

		if outcome.content == postEdit {
			postCount++

			continue
		}

		torn = append(torn, outcome.content)
	}

	assert.Emptyf(t, torn,
		"every read must return exactly %q or exactly %q; observed torn "+
			"reads: %v", preEdit, postEdit, torn)
	assert.Emptyf(t, readErrors,
		"every read_file call during the edit must succeed; "+
			"errors: %v", readErrors)
	assert.Positivef(t, preCount+postCount,
		"test setup error: readers never observed any state")
}

// --- grep ---

func TestHandler_Grep_OK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	writeText(t, filepath.Join(root, "a.txt"), "alpha\nfoo\nbeta\n")
	writeText(t, filepath.Join(root, "b.txt"), "gamma\nfoo bar\n")

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	got, err := callTool(t, tools["grep"], fmt.Sprintf(`{"root": %q, "pattern": "foo"}`, root))
	require.NoError(t, err)

	matches, ok := got["matches"].([]any)
	require.True(t, ok)
	assert.Len(t, matches, 2)
	assert.InDelta(t, 2, got["total_matches"], 0.001)
	assert.False(t, got["truncated"].(bool))
}

func TestHandler_Grep_NotDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "x.txt")
	writeText(t, file, "hi\n")

	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["grep"], fmt.Sprintf(`{"root": %q, "pattern": "hi"}`, file))
	assert.Error(t, err)
}

func TestHandler_Grep_InvalidPattern(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tools := toolsFor(t, map[string]any{"allowed_paths": []string{root}})

	_, err := callTool(t, tools["grep"], fmt.Sprintf(`{"root": %q, "pattern": "("}`, root))
	assert.Error(t, err)
}

func TestHandler_Grep_OutsideAllowed(t *testing.T) {
	t.Parallel()

	allowed := t.TempDir()
	tools := toolsFor(t, map[string]any{"allowed_paths": []string{allowed}})

	_, err := callTool(t, tools["grep"], `{"root": "/etc", "pattern": "x"}`)
	assert.Error(t, err)
}
