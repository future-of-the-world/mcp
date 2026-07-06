// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"encoding/base64"
	"fmt"
	"strconv"
)

var b64 = base64.URLEncoding.WithPadding(base64.NoPadding)

const (
	cursorPrefixLen = 3 // "x:" + at least 1 digit
	colonIndex      = 1
	numberStart     = 2
)

// EncodeOffsetCursor encodes an offset integer into an opaque base64url
// string of the form "o:<n>".
func EncodeOffsetCursor(offset int) string {
	raw := fmt.Sprintf("o:%d", offset)

	return b64.EncodeToString([]byte(raw))
}

// DecodeOffsetCursor decodes an offset cursor produced by EncodeOffsetCursor.
// Returns the offset and true on success, or 0 and false if the input is not a
// valid offset cursor.
func DecodeOffsetCursor(s string) (int, bool) {
	decoded, err := b64.DecodeString(s)
	if err != nil {
		return 0, false
	}

	return parseCursorPrefix(decoded, 'o')
}

// EncodeBodyCursor encodes a body-offset integer into an opaque base64url
// string of the form "b:<n>".
func EncodeBodyCursor(offset int) string {
	raw := fmt.Sprintf("b:%d", offset)

	return b64.EncodeToString([]byte(raw))
}

// DecodeBodyCursor decodes a body cursor produced by EncodeBodyCursor.
// Returns the offset and true on success, or 0 and false if the input is not a
// valid body cursor.
func DecodeBodyCursor(s string) (int, bool) {
	decoded, err := b64.DecodeString(s)
	if err != nil {
		return 0, false
	}

	return parseCursorPrefix(decoded, 'b')
}

// parseCursorPrefix parses "x:<digits>" where x is the expected prefix byte.
func parseCursorPrefix(decoded []byte, prefix byte) (int, bool) {
	str := string(decoded)

	if len(str) < cursorPrefixLen || str[0] != prefix || str[colonIndex] != ':' {
		return 0, false
	}

	num, err := strconv.Atoi(str[numberStart:])
	if err != nil {
		return 0, false
	}

	return num, true
}
