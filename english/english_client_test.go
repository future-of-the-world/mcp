// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package english

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/english/langtoolapi"
)

// ---------------------------------------------------------------------------
// Text processing helpers
// ---------------------------------------------------------------------------

func TestStripIgnoredSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain text passes through trimmed",
			in:   "  hello world  ",
			want: "hello world",
		},
		{
			name: "fenced code block is removed",
			in:   "before ```\ncode block\n``` after",
			want: "before after",
		},
		{
			name: "inline code is removed",
			in:   "use `foo` here",
			want: "use here",
		},
		{
			name: "URL is removed",
			in:   "see https://example.com/path?q=1 for details",
			want: "see for details",
		},
		{
			name: "multiple whitespaces collapse",
			in:   "a   b\t\tc\n\nd",
			want: "a b c d",
		},
		{
			name: "empty string is empty",
			in:   "",
			want: "",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, testCase.want, stripIgnoredSegments(testCase.in))
		})
	}
}

func TestStripCyrillic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "Cyrillic is removed",
			in:   "hello мир world",
			want: "hello world",
		},
		{
			name: "pure English passes through",
			in:   "hello world",
			want: "hello world",
		},
		{
			name: "pure Cyrillic becomes empty",
			in:   "привет",
			want: "",
		},
		{
			name: "whitespace collapses after stripping",
			in:   "a бб c",
			want: "a c",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, testCase.want, stripCyrillic(testCase.in))
		})
	}
}

// ---------------------------------------------------------------------------
// Learner-explanation enrichment + hint generation
// ---------------------------------------------------------------------------

func TestEnrichExplanation_Known(t *testing.T) {
	t.Parallel()

	got := enrichExplanation("EFFECT_AFFECT", "raw message")
	require.NotEqual(t, "raw message", got)
	require.Contains(t, got, "Affect")
}

func TestEnrichExplanation_UnknownFallsBackToOriginal(t *testing.T) {
	t.Parallel()

	got := enrichExplanation("SOMETHING_NOT_IN_MAP", "original message")
	require.Equal(t, "original message", got)
}

func TestGenerateHint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		category string
		contains string
	}{
		{"Grammar", "grammar"},
		{"Spelling", "misspelled"},
		{"Punctuation", "punctuation"},
		{"Capitalization", "capital"},
		{"Casing", "capital"},
		{"Mystery", "Review this part of the sentence for correctness."},
	}

	for _, testCase := range tests {
		t.Run(testCase.category, func(t *testing.T) {
			t.Parallel()

			match := matchWithCategory(t, testCase.category)
			require.Contains(t, generateHint(&match), testCase.contains)
		})
	}

	t.Run("nil rule", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, "Review this part of the sentence for correctness.",
			generateHint(&langtoolapi.Match{
				Context: struct {
					Length int    `json:"length"`
					Offset int    `json:"offset"`
					Text   string `json:"text"`
				}{},
				Length:  0,
				Message: "",
				Offset:  0,
				Replacements: []struct {
					Value *string `json:"value,omitempty"`
				}(nil),
				Rule:         ruleTypedNil(),
				Sentence:     "",
				ShortMessage: (*string)(nil),
			}))
	})

	t.Run("nil category name", func(t *testing.T) {
		t.Parallel()

		match := matchWithCategory(t, "")
		require.Equal(t, "Review this part of the sentence for correctness.",
			generateHint(&match))
	})
}

// ---------------------------------------------------------------------------
// Error filtering and formatting
// ---------------------------------------------------------------------------

func TestFilterErrors_KeepsNonSkipped(t *testing.T) {
	t.Parallel()

	matches := []langtoolapi.Match{
		matchWithCategory(t, "Grammar"),
		matchWithCategory(t, "Style"),
		matchWithCategory(t, "Grammar"),
	}

	got := filterErrors(matches)
	require.Len(t, got, 2)
}

func TestFilterErrors_SkipsAllSkippedCategories(t *testing.T) {
	t.Parallel()

	matches := []langtoolapi.Match{
		matchWithCategory(t, "Style"),
		matchWithCategory(t, "Typography"),
		matchWithCategory(t, "Whitespace"),
		matchWithCategory(t, "Redundancy"),
	}

	got := filterErrors(matches)
	require.Empty(t, got)
}

func TestFilterErrors_NilRuleAndNilNameAreKept(t *testing.T) {
	t.Parallel()

	matches := []langtoolapi.Match{
		{
			Context: struct {
				Length int    `json:"length"`
				Offset int    `json:"offset"`
				Text   string `json:"text"`
			}{},
			Length:  0,
			Message: "",
			Offset:  0,
			Replacements: []struct {
				Value *string `json:"value,omitempty"`
			}(nil),
			Rule:         ruleTypedNil(),
			Sentence:     "",
			ShortMessage: (*string)(nil),
		},
		matchWithCategory(t, ""),
	}

	got := filterErrors(matches)
	require.Len(t, got, 2)
}

func TestFormatErrors_BasicMatch(t *testing.T) {
	t.Parallel()

	matches := []langtoolapi.Match{
		matchWithIDAndCategory(t, "EFFECT_AFFECT", "Some grammar rule", "Grammar"),
	}

	// override context fields via JSON to keep the struct-literal pain out
	raw := []byte(`{"context":{"text":"this is a typo","offset":10,"length":4}}`)
	mergeContext(&matches[0], raw)

	matches[0].Message = "raw message"

	got := formatErrors(matches)
	require.Len(t, got, 1)

	err := got[0]
	require.Equal(t, 1, err.Index)
	require.Equal(t, "typo", err.Mistake)
	require.Equal(t, "Grammar", err.Category)
	require.Equal(t, "Some grammar rule", err.Rule)
	require.Equal(t, "this is a typo", err.Context)
	require.Contains(t, err.Hint, "grammar")
	require.NotEqual(t, "raw message", err.Explanation)
}

func TestFormatErrors_ContextOutOfRange(t *testing.T) {
	t.Parallel()

	matches := []langtoolapi.Match{matchWithIDAndCategory(t, "", "Desc", "Grammar")}

	raw := []byte(`{"context":{"text":"short","offset":100,"length":5}}`)
	mergeContext(&matches[0], raw)

	matches[0].Message = "msg"

	got := formatErrors(matches)
	require.Len(t, got, 1)
	require.Empty(t, got[0].Mistake)
}

func TestFormatErrors_NilRule(t *testing.T) {
	t.Parallel()

	match := &langtoolapi.Match{
		Context: struct {
			Length int    `json:"length"`
			Offset int    `json:"offset"`
			Text   string `json:"text"`
		}{},
		Rule:    ruleTypedNil(),
		Message: "raw",
		Length:  0,
		Offset:  0,
		Replacements: []struct {
			Value *string `json:"value,omitempty"`
		}(nil),
		Sentence:     "",
		ShortMessage: (*string)(nil),
	}
	raw := []byte(`{"context":{"text":"abc","offset":0,"length":3}}`)
	mergeContext(match, raw)

	got := formatErrors([]langtoolapi.Match{*match})
	require.Len(t, got, 1)
	require.Equal(t, "abc", got[0].Mistake)
	require.Equal(t, "raw", got[0].Explanation)
	require.Empty(t, got[0].Rule)
	require.Empty(t, got[0].Category)
}

// ruleTypedNil returns a typed nil for the langtoolapi.Match.Rule
// anonymous struct, satisfying the ruleguard typed-nil rule.
//
//nolint:revive,staticcheck,tagliatelle // mirrors upstream langtoolapi.Match.Rule
func ruleTypedNil() *struct {
	Category struct {
		Id   *string `json:"id,omitempty"`
		Name *string `json:"name,omitempty"`
	} `json:"category"`
	Description string  `json:"description"`
	Id          string  `json:"id"`
	IssueType   *string `json:"issueType,omitempty"`
	SubId       *string `json:"subId,omitempty"`
	Urls        *[]struct {
		Value *string `json:"value,omitempty"`
	} `json:"urls,omitempty"`
} {
	return nil
}

// matchWithCategory constructs a Match whose Rule.Category.Name is
// the given string. Use an empty string for an unset name.
func matchWithCategory(t *testing.T, name string) langtoolapi.Match {
	return matchFromPayload(t, map[string]any{
		"rule": map[string]any{
			"category": map[string]any{"name": name},
		},
	})
}

// matchWithIDAndCategory is like matchWithCategory but also sets the
// rule's Id and Description fields.
func matchWithIDAndCategory(t *testing.T, ruleID, desc, name string) langtoolapi.Match {
	return matchFromPayload(t, map[string]any{
		"rule": map[string]any{
			"id":          ruleID,
			"description": desc,
			"category":    map[string]any{"name": name},
		},
	})
}

// matchFromPayload decodes the given JSON-shaped payload into a
// langtoolapi.Match. Used by the helpers above to keep the test
// bodies free of error-checking clutter.
func matchFromPayload(t *testing.T, payload map[string]any) langtoolapi.Match {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var match langtoolapi.Match

	err = json.Unmarshal(data, &match)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	return match
}

// mergeContext decodes a JSON fragment into a Match's inline context
// struct (the langtoolapi.Match.Context field is an anonymous struct).
func mergeContext(match *langtoolapi.Match, raw []byte) {
	var wrapper struct {
		Context json.RawMessage `json:"context"`
	}

	err := json.Unmarshal(raw, &wrapper)
	if err != nil {
		return
	}

	err = json.Unmarshal(wrapper.Context, &match.Context)
	if err != nil {
		return
	}
}

// mustJSON marshals value or fails the test. Used by the matchWith* helpers
// to keep the test code free of error checks.
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return data
}

var _ = mustJSON // keep the helper available for future inline use
