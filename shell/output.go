// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package shell

import (
	"encoding/base64"
	"unicode/utf8"
)

// binaryPrefix marks output as base64-encoded binary data instead of
// UTF-8 text. Same convention as mcp/woodpecker/ step logs (see the
// .issues/woodpecker-log-data-base64 resolution note).
const binaryPrefix = "b64:"

// base64Encoding is the standard encoder used for binary output. The
// padded StdEncoding is chosen so a human reading the response can
// decode it directly; the volume of binary output is bounded by
// connect.max_output_bytes so the padding overhead is negligible.
var base64Encoding = base64.StdEncoding

// encodeOutput converts captured child-process output into the wire form.
// UTF-8 text passes through verbatim; invalid UTF-8 sequences are
// base64-encoded and prefixed with "b64:" so the JSON envelope stays
// valid regardless of what the child wrote.
func encodeOutput(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}

	return binaryPrefix + base64Encoding.EncodeToString(data)
}
