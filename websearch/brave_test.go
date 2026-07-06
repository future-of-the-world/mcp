// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBraveProvider_Name(t *testing.T) {
	t.Parallel()

	provider := &BraveProvider{apiKey: "test-key", logger: (*slog.Logger)(nil)}
	assert.Equal(t, braveProvider, provider.Name())
}

func TestBraveProvider_RequiresAPIKey(t *testing.T) {
	t.Parallel()

	provider := &BraveProvider{apiKey: "test-key", logger: (*slog.Logger)(nil)}
	assert.True(t, provider.RequiresAPIKey())
}

func TestBraveProvider_Supports(t *testing.T) {
	t.Parallel()

	provider := &BraveProvider{apiKey: "test-key", logger: (*slog.Logger)(nil)}

	tests := []struct {
		kind SearchKind
		want bool
	}{
		{SearchKindWeb, true},
		{SearchKindNews, true},
		{SearchKindImages, true},
		{SearchKind("video"), false},
	}

	for _, testCase := range tests {
		t.Run(string(testCase.kind), func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.want, provider.Supports(testCase.kind))
		})
	}
}

func TestBraveProvider_Search_NoAPIKey(t *testing.T) {
	t.Parallel()

	provider := &BraveProvider{}
	_, err := provider.Search(t.Context(), "test", &SearchOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key not configured")
}

func TestBraveProvider_Search_UnsupportedKind(t *testing.T) {
	t.Parallel()

	provider := &BraveProvider{apiKey: "test-key", logger: (*slog.Logger)(nil)}
	_, err := provider.Search(t.Context(), "test", &SearchOptions{
		Kind: SearchKind("video"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported kind")
}

func TestParseBraveResponse_WebResults(t *testing.T) {
	t.Parallel()

	ClearIDStore()
	t.Cleanup(ClearIDStore)

	apiResponse := map[string]any{
		"query": map[string]string{
			"original": "golang",
		},
		"web": map[string]any{
			"results": []map[string]string{
				{
					"title":       "The Go Programming Language",
					"url":         "https://go.dev",
					"description": "Go is an open source language",
				},
				{
					"title":       "Go Tour",
					"url":         "https://go.dev/tour",
					"description": "A Tour of Go",
				},
			},
		},
	}

	body := mustMarshal(t, apiResponse)
	resp, err := parseBraveResponse(
		bytes.NewReader(body),
		SearchKindWeb,
		"golang",
		&SearchOptions{Count: 20},
	)
	require.NoError(t, err)

	assert.Equal(t, braveProvider, resp.Provider)
	assert.Equal(t, "golang", resp.Query)
	require.Len(t, resp.Results, 2)
	assert.Equal(t, "The Go Programming Language", resp.Results[0].Title)
	assert.Equal(t, "https://go.dev", resp.Results[0].URL)
	assert.Equal(t, "Go is an open source language", resp.Results[0].Description)
}

func TestParseBraveResponse_PaginationCursor(t *testing.T) {
	t.Parallel()

	results := make([]map[string]string, 20)
	for idx := range 20 {
		results[idx] = map[string]string{
			"title": "Result", "url": "https://example.com",
			"description": "desc",
		}
	}

	apiResponse := map[string]any{
		"web": map[string]any{"results": results},
	}

	body := mustMarshal(t, apiResponse)
	resp, err := parseBraveResponse(
		bytes.NewReader(body),
		SearchKindWeb,
		"test",
		&SearchOptions{Count: 20, Offset: 0},
	)
	require.NoError(t, err)

	assert.NotEmptyf(t, resp.NextCursor,
		"should produce a next cursor when results fill the page",
	)

	off, ok := DecodeOffsetCursor(resp.NextCursor)
	require.True(t, ok)
	assert.Equal(t, 20, off)
}

func TestParseBraveResponse_NewsResults(t *testing.T) {
	t.Parallel()

	apiResponse := map[string]any{
		"results": []map[string]string{
			{
				"title":       "Breaking News",
				"url":         "https://news.example.com",
				"description": "Something happened",
			},
		},
	}

	body := mustMarshal(t, apiResponse)
	resp, err := parseBraveResponse(
		bytes.NewReader(body),
		SearchKindNews,
		"news",
		&SearchOptions{Count: 10},
	)
	require.NoError(t, err)

	require.Len(t, resp.Results, 1)
	assert.Equal(t, "Breaking News", resp.Results[0].Title)
}

func TestParseBraveResponse_ImageResults(t *testing.T) {
	t.Parallel()

	apiResponse := map[string]any{
		"results": []map[string]any{
			{
				"title":  "A Photo",
				"url":    "https://img.example.com/thumb",
				"source": "Example",
				"properties": map[string]string{
					"url": "https://img.example.com/full",
				},
			},
		},
	}

	body := mustMarshal(t, apiResponse)
	resp, err := parseBraveResponse(
		bytes.NewReader(body),
		SearchKindImages,
		"photos",
		&SearchOptions{Count: 10},
	)
	require.NoError(t, err)

	require.Len(t, resp.Results, 1)
	assert.Equal(t, "https://img.example.com/full", resp.Results[0].URL)
	assert.Equal(t, "https://img.example.com/thumb", resp.Results[0].RawURL)
}

// ---------------------------------------------------------------------------
// isSuccessStatus: 200-299 range check
// ---------------------------------------------------------------------------

func TestIsSuccessStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		code int
		want bool
	}{
		{200, true},
		{201, true},
		{204, true},
		{299, true},
		{300, false},
		{199, false},
		{400, false},
		{500, false},
	}

	for _, testCase := range cases {
		t.Run("status", func(t *testing.T) {
			t.Parallel()
			require.Equal(t, testCase.want, isSuccessStatus(testCase.code))
		})
	}
}

// ---------------------------------------------------------------------------
// readAPIError: parses a non-2xx response body into a descriptive error
// ---------------------------------------------------------------------------

func TestReadAPIError_Body(t *testing.T) {
	t.Parallel()

	resp := &http.Response{ //nolint:exhaustruct // only StatusCode and Body are read
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"error":"missing key"}`)),
	}

	err := readAPIError(resp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "400")
	require.Contains(t, err.Error(), `{"error":"missing key"}`)
}

// ---------------------------------------------------------------------------
// resolveKind
// ---------------------------------------------------------------------------

func TestResolveKind_DefaultIsWeb(t *testing.T) {
	t.Parallel()

	require.Equal(t, SearchKindWeb, resolveKind((*SearchOptions)(nil)))
	require.Equal(t, SearchKindWeb, resolveKind(&SearchOptions{}))
}

func TestResolveKind_RespectsOpts(t *testing.T) {
	t.Parallel()

	require.Equal(t, SearchKindNews, resolveKind(&SearchOptions{Kind: SearchKindNews}))
	require.Equal(t, SearchKindImages, resolveKind(&SearchOptions{Kind: SearchKindImages}))
}

func TestMatchesAnyDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rawURL  string
		domains []string
		want    bool
	}{
		{
			"exact match",
			"https://example.com/page",
			[]string{"example.com"},
			true,
		},
		{
			"subdomain match",
			"https://www.example.com/page",
			[]string{"example.com"},
			true,
		},
		{
			"no partial match",
			"https://notexample.com/page",
			[]string{"example.com"},
			false,
		},
		{"empty domains", "https://example.com", nil, false},
		{"invalid URL", "://invalid", []string{"example.com"}, false},
		{
			"dot prefix domain",
			"https://www.example.com",
			[]string{".example.com"},
			true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.want,
				MatchesAnyDomain(testCase.rawURL, testCase.domains),
			)
		})
	}
}

func TestApplyDomainFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		query          string
		includeDomains []string
		excludeDomains []string
		want           string
	}{
		{"no filters", "test", nil, nil, "test"},
		{
			"include only",
			"test",
			[]string{"example.com"},
			nil,
			"test (site:example.com)",
		},
		{
			"exclude only",
			"test",
			nil,
			[]string{"spam.com"},
			"test -site:spam.com",
		},
		{
			"both filters",
			"test",
			[]string{"good.com", "nice.com"},
			[]string{"bad.com"},
			"test (site:good.com OR site:nice.com) -site:bad.com",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := applyDomainFilters(
				testCase.query,
				testCase.includeDomains,
				testCase.excludeDomains,
			)
			assert.Equal(t, testCase.want, got)
		})
	}
}

func TestResolveOffset(t *testing.T) {
	t.Parallel()

	t.Run("no cursor returns offset", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, 5, resolveOffset(&SearchOptions{Offset: 5}))
	})

	t.Run("valid cursor overrides offset", func(t *testing.T) {
		t.Parallel()

		cursor := EncodeOffsetCursor(42)
		assert.Equal(t, 42,
			resolveOffset(&SearchOptions{Offset: 5, Cursor: cursor}),
		)
	})

	t.Run("invalid cursor falls back to offset", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, 5,
			resolveOffset(&SearchOptions{Offset: 5, Cursor: "invalid"}),
		)
	})

	t.Run("nil opts returns zero", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, 0, resolveOffset((*SearchOptions)(nil)))
	})
}

func TestFilterExcludedDomains(t *testing.T) {
	t.Parallel()

	results := []SearchResult{
		{Title: "Good", URL: "https://good.com", Snippet: "", RawURL: "", Description: ""},
		{Title: "Bad", URL: "https://bad.com", Snippet: "", RawURL: "", Description: ""},
		{
			Title: "Also Good", URL: "https://other.com",
			Snippet: "", RawURL: "", Description: "",
		},
	}

	filtered := filterExcludedDomains(results, []string{"bad.com"})
	require.Len(t, filtered, 2)
	assert.Equal(t, "Good", filtered[0].Title)
	assert.Equal(t, "Also Good", filtered[1].Title)
}

func TestBuildBraveURL(t *testing.T) {
	t.Parallel()

	t.Run("basic query", func(t *testing.T) {
		t.Parallel()

		gotURL, err := buildBraveURL(
			"https://api.example.com/search",
			"golang",
			SearchKindWeb,
			&SearchOptions{Count: 5},
		)
		require.NoError(t, err)
		assert.Contains(t, gotURL, "q=golang")
		assert.Contains(t, gotURL, "count=5")
	})

	t.Run("with freshness", func(t *testing.T) {
		t.Parallel()

		gotURL, err := buildBraveURL(
			"https://api.example.com/search",
			"news",
			SearchKindWeb,
			&SearchOptions{Freshness: "pd"},
		)
		require.NoError(t, err)
		assert.Contains(t, gotURL, "freshness=pd")
	})

	t.Run("web offset", func(t *testing.T) {
		t.Parallel()

		gotURL, err := buildBraveURL(
			"https://api.example.com/search",
			"test",
			SearchKindWeb,
			&SearchOptions{Offset: 10},
		)
		require.NoError(t, err)
		assert.Contains(t, gotURL, "offset=10")
	})

	t.Run("news no offset param", func(t *testing.T) {
		t.Parallel()

		gotURL, err := buildBraveURL(
			"https://api.example.com/search",
			"test",
			SearchKindNews,
			&SearchOptions{Offset: 10},
		)
		require.NoError(t, err)
		assert.NotContains(t, gotURL, "offset=")
	})

	t.Run("nil opts uses defaults", func(t *testing.T) {
		t.Parallel()

		gotURL, err := buildBraveURL(
			"https://api.example.com/search",
			"test",
			SearchKindWeb,
			(*SearchOptions)(nil),
		)
		require.NoError(t, err)
		assert.Contains(t, gotURL, "count=20")
		assert.Contains(t, gotURL, "q=test")
	})
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return data
}
