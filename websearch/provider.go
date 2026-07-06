// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import "context"

// SearchKind represents the type of search to perform.
type SearchKind string

const (
	SearchKindWeb    SearchKind = "web"
	SearchKindNews   SearchKind = "news"
	SearchKindImages SearchKind = "images"
)

// SearchOptions configures a search request.
type SearchOptions struct {
	Count          int        `json:"count"           yaml:"count"`
	Offset         int        `json:"offset"          yaml:"offset"`
	Cursor         string     `json:"cursor"          yaml:"cursor"`
	Freshness      string     `json:"freshness"       yaml:"freshness"`
	Country        string     `json:"country"         yaml:"country"`
	SearchLang     string     `json:"search_lang"     yaml:"search_lang"`
	SafeSearch     string     `json:"safesearch"      yaml:"safesearch"`
	IncludeDomains []string   `json:"include_domains" yaml:"include_domains"`
	ExcludeDomains []string   `json:"exclude_domains" yaml:"exclude_domains"`
	Kind           SearchKind `json:"kind"            yaml:"kind"`
}

// SearchResult represents a single search result.
type SearchResult struct {
	Title       string `json:"title"       yaml:"title"`
	URL         string `json:"url"         yaml:"url"`
	Snippet     string `json:"snippet"     yaml:"snippet"`
	RawURL      string `json:"raw_url"     yaml:"raw_url"`
	Description string `json:"description" yaml:"description"`
}

// SearchResponse contains the results and metadata from a search query.
type SearchResponse struct {
	Results    []SearchResult `json:"results"     yaml:"results"`
	Cached     bool           `json:"cached"      yaml:"cached"`
	Provider   string         `json:"provider"    yaml:"provider"`
	Query      string         `json:"query"       yaml:"query"`
	Count      int            `json:"count"       yaml:"count"`
	Offset     int            `json:"offset"      yaml:"offset"`
	NextCursor string         `json:"next_cursor" yaml:"next_cursor"`
}

// SearchProvider is the interface that all search providers must implement.
type SearchProvider interface {
	// Name returns the human-readable name of the provider.
	Name() string
	// Supports reports whether this provider handles the given search kind.
	Supports(kind SearchKind) bool
	// RequiresAPIKey reports whether this provider needs an API key.
	RequiresAPIKey() bool
	// Search executes a search query and returns results.
	Search(ctx context.Context, query string, opts *SearchOptions) (*SearchResponse, error)
}
