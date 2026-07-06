// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package decode provides shared helpers for decoding the per-source
// `connect:` map (the free-form map each per-type Connect function
// receives as its second argument). The package is intentionally
// tiny and dependency-free so both the per-type packages
// (tracker, proxy, gitlab, …) and the source package can import it
// without forming an import cycle.
package decode

import (
	"errors"
	"fmt"
)

// ErrNotSet is returned by AsString when the input value is nil —
// the key was absent from the connect map or explicitly set to null
// in YAML/JSON. Callers can check errors.Is(err, ErrNotSet) and
// leave the destination field untouched; downstream validation
// (e.g. errOrgIDEmpty, errTokenEmpty) will surface the missing
// field with a familiar "field is empty" message.
var ErrNotSet = errors.New("decode: value not set")

// ErrWrongType is returned by AsString when the input is a non-scalar
// type (map, slice, struct, …) where a scalar was expected. The
// returned error wraps ErrWrongType (via %w) and carries the actual
// Go type in its message. Callers can either propagate it (strict)
// or treat it like ErrNotSet (permissive); the per-type decodeConnect
// functions in this repo propagate it so a config bug surfaces as
// a clear error rather than a silent "field is empty".
var ErrWrongType = errors.New("decode: expected scalar")

// AsString converts a YAML/JSON scalar to its string representation.
// Strings are returned as-is; bools and numbers are stringified via
// fmt.Sprint. nil yields ("", ErrNotSet); non-scalar types yield
// ("", wrapped ErrWrongType) carrying the actual Go type.
//
// The helper makes the per-type decodeConnect functions permissive
// about YAML-natural values (e.g. org_id: 12345 instead of the
// quoted "12345") while still surfacing genuine config bugs (e.g.
// org_id: { foo: bar }) as clear errors.
func AsString(raw any) (string, error) {
	if raw == nil {
		return "", ErrNotSet
	}

	switch val := raw.(type) {
	case string:
		return val, nil

	case bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return fmt.Sprint(val), nil

	default:
		return "", fmt.Errorf("%w: got %T", ErrWrongType, raw)
	}
}
