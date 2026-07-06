// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package fs

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// binaryPrefix marks file content as base64-encoded binary data instead of
// UTF-8 text. Same convention as mcp/shell and mcp/woodpecker (see the
// .issues/woodpecker-log-data-base64 resolution note).
const binaryPrefix = "b64:"

// base64Encoding is the standard encoder used for binary content. The
// padded StdEncoding is chosen so a human reading the response can decode
// it directly; the volume is bounded by connect.max_read_bytes so the
// padding overhead is negligible.
var base64Encoding = base64.StdEncoding

// encodeBytes converts captured file content into the wire form. UTF-8
// text passes through verbatim; invalid UTF-8 sequences are base64-
// encoded and prefixed with "b64:" so the JSON envelope stays valid
// regardless of what the file contained. isBinary is true when the data
// was non-UTF-8 and base64-encoded.
func encodeBytes(data []byte) (encoded string, isBinary bool) {
	if isValidUTF8(data) {
		return string(data), false
	}

	return binaryPrefix + base64Encoding.EncodeToString(data), true
}

// isValidUTF8 returns true when data is valid UTF-8 text. The check
// matches the one encoding/json applies when serializing strings, so
// the "valid UTF-8 → pass through" branch stays in lockstep with the
// JSON envelope.
func isValidUTF8(data []byte) bool {
	return utf8.Valid(data)
}

// textResult marshals value to JSON and returns a *mcp.CallToolResult
// containing a single TextContent. The helper mirrors the
// mcp/shell/handler.textResult pattern: StructuredContent is set only
// when the marshaled payload is a valid JSON object (per the MCP spec),
// so JSON arrays and primitives safely degrade to Content-only.
func textResult(value any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}

	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}

	var probe map[string]any

	if json.Unmarshal(data, &probe) == nil {
		result.StructuredContent = json.RawMessage(data)
	}

	return result, nil
}
