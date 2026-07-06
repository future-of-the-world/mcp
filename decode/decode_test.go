// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package decode

import (
	"errors"
	"testing"
)

// TestAsString_String covers the string passthrough path: a value
// that is already a Go string comes back unchanged with no error.
// This is the most common case in practice and the most important
// to keep regression-free — every per-type decodeConnect path
// relies on it.
func TestAsString_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"plain", "hello"},
		{"numeric-looking", "12345"},
		{"with-spaces", "  leading and trailing  "},
		{"yaml-special", "true: false ~ null"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := AsString(testCase.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got != testCase.input {
				t.Errorf("expected %q, got %q", testCase.input, got)
			}
		})
	}
}

// TestAsString_Integers covers every integer width accepted by
// AsString. Each must stringify via fmt.Sprint (so 12345 → "12345",
// not "12.345" or some other formatting).
func TestAsString_Integers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input any
		want  string
	}{
		{"int", int(12345), "12345"},
		{"int8", int8(-128), "-128"},
		{"int16", int16(32767), "32767"},
		{"int32", int32(-2147483648), "-2147483648"},
		{"int64", int64(9223372036854775807), "9223372036854775807"},
		{"uint", uint(0), "0"},
		{"uint8", uint8(255), "255"},
		{"uint16", uint16(65535), "65535"},
		{"uint32", uint32(4294967295), "4294967295"},
		{"uint64", uint64(18446744073709551615), "18446744073709551615"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := AsString(testCase.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got != testCase.want {
				t.Errorf("expected %q, got %q", testCase.want, got)
			}
		})
	}
}

// TestAsString_Floats covers float32 and float64. fmt.Sprint uses
// the natural %v formatting, which for whole-valued floats like
// 1.0 produces "1" (no decimal point) — verify the helper matches
// that behavior so callers get predictable strings.
func TestAsString_Floats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input any
		want  string
	}{
		{"float32-whole", float32(1.0), "1"},
		{"float32-fractional", float32(1.5), "1.5"},
		{"float64-whole", float64(42.0), "42"},
		{"float64-fractional", float64(3.14), "3.14"},
		{"float64-zero", float64(0), "0"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := AsString(testCase.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got != testCase.want {
				t.Errorf("expected %q, got %q", testCase.want, got)
			}
		})
	}
}

// TestAsString_Bools covers the two boolean values. fmt.Sprint
// produces "true" and "false" — the same strings a user would type
// in YAML.
func TestAsString_Bools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input bool
		want  string
	}{
		{"true", true, "true"},
		{"false", false, "false"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := AsString(testCase.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got != testCase.want {
				t.Errorf("expected %q, got %q", testCase.want, got)
			}
		})
	}
}

// TestAsString_Nil covers the not-set case: a nil raw value
// (absent key, explicit YAML null, or unmapped "connect: { }")
// must return ("", ErrNotSet) so callers can branch on it.
func TestAsString_Nil(t *testing.T) {
	t.Parallel()

	got, err := AsString(any(nil))

	if !errors.Is(err, ErrNotSet) {
		t.Errorf("expected ErrNotSet, got %v", err)
	}

	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// TestAsString_WrongType covers the three non-scalar shapes that
// YAML can produce: maps, sequences, and structs (the latter
// only via custom unmarshalers, but we still reject it). Each
// must return a wrapped ErrWrongType with the actual Go type
// in the message — that's the bug-surface signal a per-type
// decodeConnect propagates upward.
func TestAsString_WrongType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    any
		wantType string
	}{
		{"map", map[string]any{"foo": "bar"}, "map[string]interface {}"},
		{"slice", []any{"a", "b"}, "[]interface {}"},
		{"struct", struct{ X int }{X: 1}, "struct { X int }"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			assertWrongTypeError(t, testCase.input, testCase.wantType)
		})
	}
}

// assertWrongTypeError is the per-case body of TestAsString_WrongType,
// extracted to keep the test's cognitive complexity under the linter's
// threshold. The helper asserts that AsString returns ErrWrongType,
// yields an empty string, and mentions the expected Go type in the
// error message.
func assertWrongTypeError(t *testing.T, input any, wantType string) {
	t.Helper()

	got, err := AsString(input)

	if !errors.Is(err, ErrWrongType) {
		t.Errorf("expected wrapped ErrWrongType, got %v", err)
	}

	if got != "" {
		t.Errorf("expected empty string on error, got %q", got)
	}

	if err != nil && !contains(err.Error(), wantType) {
		t.Errorf("expected error to mention %q, got %q", wantType, err.Error())
	}
}

// contains is a tiny string-search helper to avoid pulling strings
// into the test file just for one Contains call.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}

	return false
}
