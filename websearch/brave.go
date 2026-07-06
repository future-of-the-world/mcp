// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	braveBaseURL    = "https://api.search.brave.com/res/v1"
	braveMaxCount   = 20
	braveProvider   = "Brave Search"
	braveResultsKey = "results"
	httpStatusOK    = 200
	httpStatusBreak = 300
)

var errBraveNoAPIKey = errors.New("brave search: API key not configured")

// braveImageProperties holds the nested properties for an image result.
type braveImageProperties struct {
	ImageURL string `json:"url"`
}

// braveWebItem is a single web search result from the Brave API.
type braveWebItem struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// braveWebResponse wraps the web section of a Brave API response.
type braveWebResponse struct {
	Results []braveWebItem `json:"results"`
}

// braveNewsItem is a single news search result from the Brave API.
type braveNewsItem struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// braveImageItem is a single image search result from the Brave API.
type braveImageItem struct {
	Title      string               `json:"title"`
	URL        string               `json:"url"`
	Source     string               `json:"source"`
	Properties braveImageProperties `json:"properties"`
}

// BraveProvider implements SearchProvider using the Brave Search API.
type BraveProvider struct {
	apiKey string
	logger *slog.Logger
}

// Name returns the display name of this provider.
func (*BraveProvider) Name() string { return braveProvider }

// Supports reports whether the provider handles the given search kind.
func (*BraveProvider) Supports(kind SearchKind) bool {
	return kind == SearchKindWeb || kind == SearchKindNews || kind == SearchKindImages
}

// RequiresAPIKey returns true because Brave requires an API key.
func (*BraveProvider) RequiresAPIKey() bool { return true }

// Search performs a search using the Brave Search API.
func (provider *BraveProvider) Search(
	ctx context.Context,
	query string,
	opts *SearchOptions,
) (*SearchResponse, error) {
	if provider.apiKey == "" {
		return nil, errBraveNoAPIKey
	}

	kind := resolveKind(opts)

	if !provider.Supports(kind) {
		return nil, fmt.Errorf("brave search: unsupported kind %q", kind)
	}

	endpoint := braveBaseURL + "/" + string(kind) + "/search"

	reqURL, reqErr := buildBraveURL(endpoint, query, kind, opts)
	if reqErr != nil {
		return nil, reqErr
	}

	if provider.logger != nil {
		provider.logger.DebugContext(ctx,
			"brave search request",
			"url", reqURL,
			"kind", string(kind),
		)
	}

	resp, fetchErr := Fetch(ctx, reqURL, FetchOptions{
		Headers: map[string]string{
			"Accept":               "application/json",
			"X-Subscription-Token": provider.apiKey,
		},
	})
	if fetchErr != nil {
		return nil, fmt.Errorf("brave search: fetch: %w", fetchErr)
	}

	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // cleanup

	if !isSuccessStatus(resp.StatusCode) {
		return nil, readAPIError(resp)
	}

	return parseBraveResponse(resp.Body, kind, query, opts)
}

// MatchesAnyDomain reports whether the hostname of rawURL matches any domain
// using hostname-suffix matching (so "example.com" matches "www.example.com"
// but not "notexample.com").
func MatchesAnyDomain(rawURL string, domains []string) bool {
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil {
		return false
	}

	host := strings.ToLower(parsed.Hostname())

	for _, domain := range domains {
		needle := strings.TrimPrefix(strings.ToLower(domain), ".")
		if needle == "" {
			continue
		}

		if host == needle || strings.HasSuffix(host, "."+needle) {
			return true
		}
	}

	return false
}

// isSuccessStatus reports whether the HTTP status code indicates success.
func isSuccessStatus(code int) bool {
	return code >= httpStatusOK && code < httpStatusBreak
}

// readAPIError reads the error response body and returns a descriptive error.
func readAPIError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("brave search: read error body: %w", err)
	}

	return fmt.Errorf("brave search: API error %d: %s", resp.StatusCode, string(body))
}

// resolveKind returns the search kind from opts, defaulting to web.
func resolveKind(opts *SearchOptions) SearchKind {
	if opts != nil && opts.Kind != "" {
		return opts.Kind
	}

	return SearchKindWeb
}

// buildBraveURL constructs the full request URL with query parameters.
func buildBraveURL(endpoint, query string, kind SearchKind, opts *SearchOptions) (string, error) {
	parsedURL, parseErr := url.Parse(endpoint)
	if parseErr != nil {
		return "", fmt.Errorf("brave search: parse endpoint: %w", parseErr)
	}

	queryUsed := applyDomainFilters(query, opts.GetIncludeDomains(), opts.GetExcludeDomains())
	params := parsedURL.Query()
	params.Set("q", queryUsed)

	count := getCount(opts)
	params.Set("count", strconv.Itoa(count))

	offset := resolveOffset(opts)
	if offset > 0 && kind == SearchKindWeb {
		params.Set("offset", strconv.Itoa(offset))
	}

	applyBraveOptionalParams(params, opts)

	parsedURL.RawQuery = params.Encode()

	return parsedURL.String(), nil
}

// applyBraveOptionalParams sets optional query parameters from opts.
func applyBraveOptionalParams(params url.Values, opts *SearchOptions) {
	if opts == nil {
		return
	}

	if opts.Freshness != "" {
		params.Set("freshness", opts.Freshness)
	}

	if opts.Country != "" {
		params.Set("country", opts.Country)
	}

	if opts.SearchLang != "" {
		params.Set("search_lang", opts.SearchLang)
	}

	if opts.SafeSearch != "" {
		params.Set("safesearch", opts.SafeSearch)
	}
}

// resolveOffset decodes the cursor or falls back to the explicit offset.
func resolveOffset(opts *SearchOptions) int {
	if opts == nil {
		return 0
	}

	if opts.Cursor != "" {
		off, ok := DecodeOffsetCursor(opts.Cursor)
		if ok {
			return off
		}
	}

	return opts.Offset
}

// applyDomainFilters appends site: and -site: operators to the query.
func applyDomainFilters(query string, includeDomains, excludeDomains []string) string {
	parts := []string{query}

	if len(includeDomains) > 0 {
		siteParts := make([]string, len(includeDomains))
		for idx, domain := range includeDomains {
			siteParts[idx] = "site:" + domain
		}

		parts = append(parts, "("+strings.Join(siteParts, " OR ")+")")
	}

	for _, domain := range excludeDomains {
		parts = append(parts, "-site:"+domain)
	}

	return strings.Join(parts, " ")
}

// getCount returns the count from opts, clamped to braveMaxCount.
func getCount(opts *SearchOptions) int {
	if opts == nil || opts.Count <= 0 || opts.Count > braveMaxCount {
		return braveMaxCount
	}

	return opts.Count
}

// parseBraveResponse decodes the Brave API JSON body into a SearchResponse.
func parseBraveResponse(
	body io.Reader,
	kind SearchKind,
	query string,
	opts *SearchOptions,
) (*SearchResponse, error) {
	var raw map[string]json.RawMessage

	decoder := json.NewDecoder(body)

	decodeErr := decoder.Decode(&raw)
	if decodeErr != nil {
		return nil, fmt.Errorf("brave search: decode response: %w", decodeErr)
	}

	count := getCount(opts)
	offset := resolveOffset(opts)

	out := &SearchResponse{
		Results:    []SearchResult{},
		Cached:     false,
		Provider:   braveProvider,
		Query:      extractBraveQuery(raw, query),
		Count:      count,
		Offset:     offset,
		NextCursor: "",
	}

	results, extractErr := extractBraveResults(raw, kind, count, opts)
	if extractErr != nil {
		return nil, extractErr
	}

	out.Results = results

	if kind == SearchKindWeb && len(results) == count {
		out.NextCursor = EncodeOffsetCursor(offset + count)
	}

	return out, nil
}

// extractBraveQuery extracts the original query from the API response.
func extractBraveQuery(raw map[string]json.RawMessage, fallback string) string {
	queryRaw, ok := raw["query"]
	if !ok {
		return fallback
	}

	var queryObj struct {
		Original string `json:"original"`
	}

	unmarshalErr := json.Unmarshal(queryRaw, &queryObj)
	if unmarshalErr != nil || queryObj.Original == "" {
		return fallback
	}

	return queryObj.Original
}

// extractBraveResults dispatches to the appropriate result parser by kind.
func extractBraveResults(
	raw map[string]json.RawMessage,
	kind SearchKind,
	count int,
	opts *SearchOptions,
) ([]SearchResult, error) {
	var results []SearchResult

	var extractErr error

	switch kind {
	case SearchKindWeb:
		results, extractErr = extractBraveWebResults(raw, count)

	case SearchKindNews:
		results, extractErr = extractBraveNewsResults(raw, count)

	case SearchKindImages:
		results, extractErr = extractBraveImageResults(raw, count)

	default:
		return nil, fmt.Errorf("brave search: unsupported kind %q", kind)
	}

	if extractErr != nil {
		return nil, extractErr
	}

	excludeDomains := getExcludeDomains(opts)

	return filterExcludedDomains(results, excludeDomains), nil
}

// getExcludeDomains returns the exclude domains from opts.
func getExcludeDomains(opts *SearchOptions) []string {
	if opts == nil {
		return nil
	}

	return opts.ExcludeDomains
}

// extractBraveWebResults parses web search results.
func extractBraveWebResults(raw map[string]json.RawMessage, count int) ([]SearchResult, error) {
	webRaw, ok := raw["web"]
	if !ok {
		return []SearchResult{}, nil
	}

	var webResp braveWebResponse

	unmarshalErr := json.Unmarshal(webRaw, &webResp)
	if unmarshalErr != nil {
		return nil, fmt.Errorf("brave search: decode web results: %w", unmarshalErr)
	}

	return mapWebItems(webResp.Results, count), nil
}

// extractBraveNewsResults parses news search results.
func extractBraveNewsResults(raw map[string]json.RawMessage, count int) ([]SearchResult, error) {
	newsRaw, ok := raw[braveResultsKey]
	if !ok {
		return []SearchResult{}, nil
	}

	var items []braveNewsItem

	unmarshalErr := json.Unmarshal(newsRaw, &items)
	if unmarshalErr != nil {
		return nil, fmt.Errorf("brave search: decode news results: %w", unmarshalErr)
	}

	return mapNewsItems(items, count), nil
}

// extractBraveImageResults parses image search results.
func extractBraveImageResults(raw map[string]json.RawMessage, count int) ([]SearchResult, error) {
	imgRaw, ok := raw[braveResultsKey]
	if !ok {
		return []SearchResult{}, nil
	}

	var items []braveImageItem

	unmarshalErr := json.Unmarshal(imgRaw, &items)
	if unmarshalErr != nil {
		return nil, fmt.Errorf("brave search: decode image results: %w", unmarshalErr)
	}

	return mapImageItems(items, count), nil
}

// mapWebItems converts Brave web items to SearchResults.
func mapWebItems(items []braveWebItem, count int) []SearchResult {
	limit := min(len(items), count)
	out := make([]SearchResult, limit)

	for idx := range limit {
		out[idx] = SearchResult{
			Title:       items[idx].Title,
			URL:         items[idx].URL,
			RawURL:      items[idx].URL,
			Description: items[idx].Description,
			Snippet:     items[idx].Description,
		}
	}

	return out
}

// mapNewsItems converts Brave news items to SearchResults.
func mapNewsItems(items []braveNewsItem, count int) []SearchResult {
	limit := min(len(items), count)
	out := make([]SearchResult, limit)

	for idx := range limit {
		out[idx] = SearchResult{
			Title:       items[idx].Title,
			URL:         items[idx].URL,
			RawURL:      items[idx].URL,
			Description: items[idx].Description,
			Snippet:     items[idx].Description,
		}
	}

	return out
}

// mapImageItems converts Brave image items to SearchResults.
func mapImageItems(items []braveImageItem, count int) []SearchResult {
	limit := min(len(items), count)
	out := make([]SearchResult, limit)

	for idx := range limit {
		resultURL := items[idx].URL
		if items[idx].Properties.ImageURL != "" {
			resultURL = items[idx].Properties.ImageURL
		}

		out[idx] = SearchResult{
			Title:       items[idx].Title,
			URL:         resultURL,
			RawURL:      items[idx].URL,
			Description: items[idx].Source,
			Snippet:     items[idx].Source,
		}
	}

	return out
}

// filterExcludedDomains removes results whose URLs match excluded domains.
func filterExcludedDomains(results []SearchResult, excludeDomains []string) []SearchResult {
	if len(excludeDomains) == 0 {
		return results
	}

	filtered := make([]SearchResult, 0, len(results))
	for _, result := range results {
		if !MatchesAnyDomain(result.URL, excludeDomains) {
			filtered = append(filtered, result)
		}
	}

	return filtered
}

// GetIncludeDomains returns the include domains, nil-safe.
func (opts *SearchOptions) GetIncludeDomains() []string {
	if opts == nil {
		return nil
	}

	return opts.IncludeDomains
}

// GetExcludeDomains returns the exclude domains, nil-safe.
func (opts *SearchOptions) GetExcludeDomains() []string {
	if opts == nil {
		return nil
	}

	return opts.ExcludeDomains
}
