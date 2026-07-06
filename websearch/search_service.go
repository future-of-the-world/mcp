// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	searchCacheCapacity = 256
	searchCacheTTL      = 5 * time.Minute
	cacheKeySeparator   = "|"
	defaultSearchKind   = "web"
)

var errNoDefaultProvider = errors.New("no default search provider configured")

// SearchService dispatches search queries to providers with LRU caching.
type SearchService struct {
	factory *ProviderFactory
	cache   *LRUCache[*SearchResponse]
}

// NewSearchService creates a search service with the given provider factory.
func NewSearchService(factory *ProviderFactory) *SearchService {
	return &SearchService{
		factory: factory,
		cache: NewLRUCache[*SearchResponse](
			searchCacheCapacity,
			searchCacheTTL,
		),
	}
}

// Search performs a search using the default provider.
func (s *SearchService) Search(
	ctx context.Context,
	query string,
	opts *SearchOptions,
) (*SearchResponse, error) {
	return s.SearchWith(ctx, query, "", opts)
}

// SearchWith performs a search using the named provider (or the default if empty).
func (s *SearchService) SearchWith(
	ctx context.Context,
	query, providerName string,
	opts *SearchOptions,
) (*SearchResponse, error) {
	provider, err := s.resolveProvider(providerName)
	if err != nil {
		return nil, err
	}

	if opts == nil {
		opts = &SearchOptions{}
	}

	key := buildSearchCacheKey(provider.Name(), query, opts)

	cached, ok := s.cache.Get(key)

	if ok {
		cached.Cached = true

		return cached, nil
	}

	resp, searchErr := provider.Search(ctx, query, opts)
	if searchErr != nil {
		return nil, fmt.Errorf("search failed: %w", searchErr)
	}

	resp.Provider = provider.Name()
	resp.Query = query
	s.cache.Set(key, resp)

	return resp, nil
}

// GetProviders returns the names of all registered providers.
func (s *SearchService) GetProviders() []string {
	return s.factory.Names()
}

// resolveProvider returns the named provider or the default.
func (s *SearchService) resolveProvider(
	providerName string,
) (SearchProvider, error) {
	if providerName != "" {
		provider, ok := s.factory.Get(providerName)
		if !ok {
			return nil, fmt.Errorf("unknown provider: %s", providerName)
		}

		return provider, nil
	}

	provider := s.factory.GetDefault()
	if provider == nil {
		return nil, errNoDefaultProvider
	}

	return provider, nil
}

// buildSearchCacheKey creates a deterministic cache key from provider, query, and options.
func buildSearchCacheKey(
	provider, query string,
	opts *SearchOptions,
) string {
	normalized := normalizeSearchOptions(opts)

	// json.Marshal on a struct with only primitive fields never fails.
	optsJSON, _ := json.Marshal(normalized)

	return provider + cacheKeySeparator + query + cacheKeySeparator + string(optsJSON)
}

// searchOptsJSON is a JSON-serializable representation of search options for cache keys.
type searchOptsJSON struct {
	Kind           SearchKind `json:"kind,omitempty"`
	Count          int        `json:"count,omitempty"`
	Offset         int        `json:"offset,omitempty"`
	Cursor         string     `json:"cursor,omitempty"`
	Freshness      string     `json:"freshness,omitempty"`
	Country        string     `json:"country,omitempty"`
	SearchLang     string     `json:"search_lang,omitempty"`
	SafeSearch     string     `json:"safe_search,omitempty"`
	IncludeDomains []string   `json:"include_domains,omitempty"`
	ExcludeDomains []string   `json:"exclude_domains,omitempty"`
}

func normalizeSearchOptions(opts *SearchOptions) searchOptsJSON {
	result := searchOptsJSON{
		Kind:           opts.Kind,
		Count:          opts.Count,
		Offset:         opts.Offset,
		Cursor:         opts.Cursor,
		Freshness:      opts.Freshness,
		Country:        opts.Country,
		SearchLang:     opts.SearchLang,
		SafeSearch:     opts.SafeSearch,
		IncludeDomains: []string(nil),
		ExcludeDomains: []string(nil),
	}

	if result.Kind == "" {
		result.Kind = defaultSearchKind
	}

	if len(opts.IncludeDomains) > 0 {
		result.IncludeDomains = make([]string, len(opts.IncludeDomains))
		copy(result.IncludeDomains, opts.IncludeDomains)
		sort.Strings(result.IncludeDomains)
	}

	if len(opts.ExcludeDomains) > 0 {
		result.ExcludeDomains = make([]string, len(opts.ExcludeDomains))
		copy(result.ExcludeDomains, opts.ExcludeDomains)
		sort.Strings(result.ExcludeDomains)
	}

	return result
}

// ResolveResultIDQuery checks if a query looks like a result ID and resolves it.
func ResolveResultIDQuery(query string) (resolved string, ok bool) {
	if !LooksLikeResultID(query) {
		return "", false
	}

	return ResolveResultID(query)
}

// ExtractProviderHint parses a query for provider hints like "provider:brave search term"
// and returns the cleaned query and provider name.
func ExtractProviderHint(query string) (cleanQuery, provider string) {
	const providerPrefix = "provider:"

	lower := strings.ToLower(query)
	if !strings.HasPrefix(lower, providerPrefix) {
		return query, ""
	}

	rest := query[len(providerPrefix):]

	provider, cleanQuery, _ = strings.Cut(rest, " ")

	return strings.TrimSpace(cleanQuery), strings.TrimSpace(provider)
}
