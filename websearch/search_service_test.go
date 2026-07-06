// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSearchCacheKey(t *testing.T) {
	t.Parallel()

	opts := &SearchOptions{
		Count:  10,
		Kind:   SearchKindWeb,
		Cursor: "abc",
	}

	key1 := buildSearchCacheKey("brave", "golang", opts)
	key2 := buildSearchCacheKey("brave", "golang", opts)

	assert.Equalf(t, key1, key2,
		"same inputs should produce the same cache key",
	)

	key3 := buildSearchCacheKey("duckduckgo", "golang", opts)
	assert.NotEqualf(t, key1, key3,
		"different provider should produce different keys",
	)

	key4 := buildSearchCacheKey("brave", "rust", opts)
	assert.NotEqualf(t, key1, key4,
		"different query should produce different keys",
	)
}

func TestBuildSearchCacheKey_DifferentOptions(t *testing.T) {
	t.Parallel()

	optsA := &SearchOptions{Count: 10}
	optsB := &SearchOptions{Count: 20}

	keyA := buildSearchCacheKey("brave", "go", optsA)
	keyB := buildSearchCacheKey("brave", "go", optsB)

	assert.NotEqualf(t, keyA, keyB,
		"different option counts should produce different keys",
	)
}

func TestNormalizeSearchOptions_DefaultKind(t *testing.T) {
	t.Parallel()

	opts := &SearchOptions{}

	normalized := normalizeSearchOptions(opts)

	assert.Equalf(t, SearchKind(defaultSearchKind), normalized.Kind,
		"empty kind should default to web",
	)
}

func TestNormalizeSearchOptions_ExplicitKind(t *testing.T) {
	t.Parallel()

	opts := &SearchOptions{Kind: SearchKindNews}

	normalized := normalizeSearchOptions(opts)

	assert.Equalf(t, SearchKindNews, normalized.Kind,
		"explicit kind should be preserved",
	)
}

func TestNormalizeSearchOptions_SortedDomains(t *testing.T) {
	t.Parallel()

	opts := &SearchOptions{
		IncludeDomains: []string{"z.com", "a.com", "m.com"},
		ExcludeDomains: []string{"y.com", "b.com"},
	}

	normalized := normalizeSearchOptions(opts)

	assert.Equal(t,
		[]string{"a.com", "m.com", "z.com"},
		normalized.IncludeDomains,
	)
	assert.Equal(t,
		[]string{"b.com", "y.com"},
		normalized.ExcludeDomains,
	)
}

func TestNormalizeSearchOptions_NilDomains(t *testing.T) {
	t.Parallel()

	opts := &SearchOptions{}

	normalized := normalizeSearchOptions(opts)

	assert.Nil(t, normalized.IncludeDomains)
	assert.Nil(t, normalized.ExcludeDomains)
}

func TestExtractProviderHint_WithHint(t *testing.T) {
	t.Parallel()

	clean, provider := ExtractProviderHint("provider:brave search term")

	assert.Equal(t, "search term", clean)
	assert.Equal(t, "brave", provider)
}

func TestExtractProviderHint_NoHint(t *testing.T) {
	t.Parallel()

	clean, provider := ExtractProviderHint("just a query")

	assert.Equal(t, "just a query", clean)
	assert.Empty(t, provider)
}

func TestExtractProviderHint_ProviderOnly(t *testing.T) {
	t.Parallel()

	clean, provider := ExtractProviderHint("provider:brave")

	assert.Empty(t, clean)
	assert.Equal(t, "brave", provider)
}

func TestExtractProviderHint_CaseInsensitivePrefix(t *testing.T) {
	t.Parallel()

	clean, provider := ExtractProviderHint("Provider:Brave query")

	assert.Equal(t, "query", clean)
	assert.Equal(t, "Brave", provider)
}

func TestResolveResultIDQuery_ValidID(t *testing.T) {
	ClearIDStore()

	resultID := MintResultID("https://example.com")

	resolved, ok := ResolveResultIDQuery(resultID)
	require.True(t, ok)
	assert.Equal(t, "https://example.com", resolved)
}

func TestResolveResultIDQuery_NonIDString(t *testing.T) {
	t.Parallel()

	resolved, ok := ResolveResultIDQuery("not-an-id")

	assert.False(t, ok)
	assert.Empty(t, resolved)
}

func TestSearchService_Search(t *testing.T) {
	t.Parallel()

	mock := newMockProvider("default-search", newTestSearchResponse(), error(nil))

	factory := NewProviderFactory((*slog.Logger)(nil))
	factory.Add(t.Context(), mock, true)

	svc := NewSearchService(factory)

	resp, err := svc.Search(t.Context(), "golang", &SearchOptions{})
	require.NoError(t, err)

	assert.Equal(t, "default-search", resp.Provider)
	assert.Equal(t, "golang", resp.Query)
}

func TestSearchService_SearchWith(t *testing.T) {
	t.Parallel()

	mock := newMockProvider("test-provider", newTestSearchResponse(), error(nil))

	factory := NewProviderFactory((*slog.Logger)(nil))
	factory.Add(t.Context(), mock, true)

	svc := NewSearchService(factory)

	resp, err := svc.SearchWith(
		t.Context(), "golang", "test-provider", &SearchOptions{},
	)
	require.NoError(t, err)

	assert.Equal(t, "test-provider", resp.Provider)
	assert.Equal(t, "golang", resp.Query)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "Test Result", resp.Results[0].Title)
}

func TestSearchService_SearchWith_Cached(t *testing.T) {
	t.Parallel()

	mock := newMockProvider("cache-test", newTestSearchResponse(), error(nil))

	factory := NewProviderFactory((*slog.Logger)(nil))
	factory.Add(t.Context(), mock, true)

	svc := NewSearchService(factory)

	resp, err := svc.SearchWith(
		t.Context(), "test", "cache-test", &SearchOptions{},
	)
	require.NoError(t, err)

	assert.Falsef(t, resp.Cached,
		"fresh fetch should not be marked cached",
	)
	assert.Equal(t, "cache-test", resp.Provider)
	assert.Equal(t, "test", resp.Query)

	// Verify the cache entry exists (TTL is 5 minutes so it won't expire).
	assert.Equal(t, 1, svc.cache.Size())
}

func TestSearchService_SearchWith_CacheHit(t *testing.T) {
	t.Parallel()

	mock := newMockProvider("cache-hit", newTestSearchResponse(), error(nil))

	factory := NewProviderFactory((*slog.Logger)(nil))
	factory.Add(t.Context(), mock, true)

	svc := NewSearchService(factory)

	// First call to populate cache.
	_, err := svc.SearchWith(
		t.Context(), "test", "cache-hit", &SearchOptions{},
	)
	require.NoError(t, err)

	// Second call should be a cache hit.
	resp, err := svc.SearchWith(
		t.Context(), "test", "cache-hit", &SearchOptions{},
	)
	require.NoError(t, err)

	assert.Truef(t, resp.Cached, "second call should be cached")
}

func TestSearchService_SearchWith_ProviderError(t *testing.T) {
	t.Parallel()

	mock := newMockProvider(
		"error-provider",
		(*SearchResponse)(nil),
		errors.New("provider failure"),
	)

	factory := NewProviderFactory((*slog.Logger)(nil))
	factory.Add(t.Context(), mock, true)

	svc := NewSearchService(factory)

	_, err := svc.SearchWith(
		t.Context(), "fail", "error-provider", &SearchOptions{},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "search failed")
}

func TestSearchService_SearchWith_UnknownProvider(t *testing.T) {
	t.Parallel()

	factory := NewProviderFactory((*slog.Logger)(nil))
	svc := NewSearchService(factory)

	_, err := svc.SearchWith(
		t.Context(), "test", "nonexistent", &SearchOptions{},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}

func TestSearchService_SearchWith_DefaultProvider(t *testing.T) {
	t.Parallel()

	mock := newMockProvider("default-mock", emptySearchResponse(), error(nil))

	factory := NewProviderFactory((*slog.Logger)(nil))
	factory.Add(t.Context(), mock, true)

	svc := NewSearchService(factory)

	resp, err := svc.SearchWith(
		t.Context(), "query", "", &SearchOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, "default-mock", resp.Provider)
}

func TestSearchService_GetProviders(t *testing.T) {
	t.Parallel()

	factory := NewProviderFactory((*slog.Logger)(nil))
	factory.SetupDefaults(t.Context(), "test-key")

	svc := NewSearchService(factory)

	names := svc.GetProviders()
	assert.Contains(t, names, "brave search")
	assert.Contains(t, names, "duckduckgo")
}

// mockProvider is a test double for SearchProvider.
type mockProvider struct {
	nameVal string
	respVal *SearchResponse
	errVal  error
}

func newMockProvider(
	name string,
	resp *SearchResponse,
	err error,
) *mockProvider {
	return &mockProvider{nameVal: name, respVal: resp, errVal: err}
}

//nolint:staticcheck // ST1006
func (_ *mockProvider) Supports(
	SearchKind,
) bool {
	return true
}

//nolint:staticcheck // ST1006
func (_ *mockProvider) RequiresAPIKey() bool {
	return false
}

func (m *mockProvider) Name() string { return m.nameVal }

func (m *mockProvider) Search(
	_ context.Context,
	_ string,
	_ *SearchOptions,
) (*SearchResponse, error) {
	if m.errVal != nil {
		return nil, m.errVal
	}

	return m.respVal, nil
}

func newTestSearchResponse() *SearchResponse {
	return &SearchResponse{
		Results: []SearchResult{
			{
				Title:       "Test Result",
				URL:         "https://example.com",
				Snippet:     "snippet",
				RawURL:      "",
				Description: "desc",
			},
		},
		Cached:     false,
		Provider:   "",
		Query:      "",
		Count:      1,
		Offset:     0,
		NextCursor: "",
	}
}

func emptySearchResponse() *SearchResponse {
	return &SearchResponse{
		Results:    []SearchResult(nil),
		Cached:     false,
		Provider:   "",
		Query:      "",
		Count:      0,
		Offset:     0,
		NextCursor: "",
	}
}
