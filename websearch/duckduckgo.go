// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
)

const (
	ddgEndpoint     = "https://html.duckduckgo.com/html/"
	ddgMaxCount     = 25
	ddgProviderName = "DuckDuckGo"
	ddgMatchGroups  = 4 // full match + 3 submatches
)

// ddgBlockRe matches a single DDG result block: title link + snippet.
var ddgBlockRe = regexp.MustCompile(
	`(?i)<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>` +
		`([\s\S]*?)</a>[\s\S]*?` +
		`<a[^>]*class="[^"]*result__snippet[^"]*"[^>]*>([\s\S]*?)</a>`,
)

// ddgUddgRe matches the uddg parameter in a DDG redirect URL.
var ddgUddgRe = regexp.MustCompile(`[?&]uddg=([^&]+)`)

// DuckDuckGoProvider implements SearchProvider by scraping the DDG HTML endpoint.
type DuckDuckGoProvider struct {
	logger *slog.Logger
}

// Name returns the display name of this provider.
func (*DuckDuckGoProvider) Name() string { return ddgProviderName }

// Supports reports whether the provider handles the given search kind.
func (*DuckDuckGoProvider) Supports(kind SearchKind) bool {
	return kind == SearchKindWeb
}

// RequiresAPIKey returns false because DuckDuckGo does not need an API key.
func (*DuckDuckGoProvider) RequiresAPIKey() bool { return false }

// Search performs a web search by scraping the DuckDuckGo HTML endpoint.
func (p *DuckDuckGoProvider) Search(
	ctx context.Context,
	query string,
	opts *SearchOptions,
) (*SearchResponse, error) {
	kind := resolveKind(opts)

	if kind != SearchKindWeb {
		return nil, fmt.Errorf("duckduckgo: unsupported kind %q", kind)
	}

	enhancedQuery := applyDomainFilters(
		query,
		opts.GetIncludeDomains(),
		opts.GetExcludeDomains(),
	)
	reqURL := buildDDGURL(enhancedQuery, opts)

	if p.logger != nil {
		p.logger.DebugContext(ctx,
			"duckduckgo search request",
			"url", reqURL,
		)
	}

	resp, fetchErr := Fetch(ctx, reqURL, FetchOptions{
		Headers: map[string]string{
			"Accept":          "text/html",
			"Accept-Language": getSearchLang(opts),
		},
	})
	if fetchErr != nil {
		return nil, fmt.Errorf("duckduckgo: fetch: %w", fetchErr)
	}

	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // cleanup

	if !isSuccessStatus(resp.StatusCode) {
		return nil, fmt.Errorf("duckduckgo: HTTP %d", resp.StatusCode)
	}

	return parseDDGResponse(resp.Body, query, opts)
}

// buildDDGURL constructs the DuckDuckGo search URL.
func buildDDGURL(query string, opts *SearchOptions) string {
	parsedURL, _ := url.Parse(ddgEndpoint) //nolint:errcheck // constant URL is always valid
	params := parsedURL.Query()
	params.Set("q", query)

	if opts != nil && opts.Country != "" {
		params.Set("kl", opts.Country)
	}

	parsedURL.RawQuery = params.Encode()

	return parsedURL.String()
}

// parseDDGResponse parses the DDG HTML response into a SearchResponse.
func parseDDGResponse(
	body io.Reader,
	query string,
	opts *SearchOptions,
) (*SearchResponse, error) {
	htmlBytes, readErr := io.ReadAll(body)
	if readErr != nil {
		return nil, fmt.Errorf("duckduckgo: read body: %w", readErr)
	}

	count := getDDGCount(opts)

	results := parseDDGResults(string(htmlBytes), count)

	if opts != nil && len(opts.ExcludeDomains) > 0 {
		results = filterExcludedDomains(results, opts.ExcludeDomains)
	}

	return &SearchResponse{
		Results:    results,
		Cached:     false,
		Provider:   ddgProviderName,
		Query:      query,
		Count:      count,
		Offset:     0,
		NextCursor: "",
	}, nil
}

// getDDGCount returns the count from opts, clamped to ddgMaxCount.
func getDDGCount(opts *SearchOptions) int {
	if opts == nil || opts.Count <= 0 || opts.Count > ddgMaxCount {
		return ddgMaxCount
	}

	return opts.Count
}

// getSearchLang returns the search language from opts.
func getSearchLang(opts *SearchOptions) string {
	if opts == nil || opts.SearchLang == "" {
		return "en-US,en;q=0.9"
	}

	return opts.SearchLang
}

// parseDDGResults extracts search results from DDG HTML.
func parseDDGResults(html string, limit int) []SearchResult {
	matches := ddgBlockRe.FindAllStringSubmatch(html, -1)
	results := make([]SearchResult, 0, len(matches))

	for _, match := range matches {
		if len(match) < ddgMatchGroups {
			continue
		}

		rawURL := decodeEntities(match[1])
		resultURL := unwrapDDG(rawURL)
		title := strings.TrimSpace(decodeEntities(stripTags(match[2])))
		description := strings.TrimSpace(decodeEntities(stripTags(match[3])))

		if resultURL == "" || title == "" {
			continue
		}

		_ = MintResultID(resultURL)

		results = append(results, SearchResult{
			Title:       title,
			URL:         resultURL,
			RawURL:      rawURL,
			Description: description,
			Snippet:     description,
		})

		if len(results) >= limit {
			break
		}
	}

	return results
}

// unwrapDDG extracts the real URL from a DDG redirect link.
func unwrapDDG(href string) string {
	match := ddgUddgRe.FindStringSubmatch(href)
	if len(match) > 1 {
		decoded, unescapeErr := url.QueryUnescape(match[1])
		if unescapeErr == nil {
			return decoded
		}
	}

	if strings.HasPrefix(href, "//") {
		return "https:" + href
	}

	return href
}
