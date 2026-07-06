// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/transform"
)

const (
	defaultMaxFetchBytes = 2 * 1024 * 1024 // 2 MB
	maxRedirectHops      = 5
	fetchCacheCapacity   = 128
	fetchCacheTTL        = 10 * time.Minute
	extractMaxChars      = 1_000_000
	defaultMaxChars      = 8000
	defaultFetchTimeout  = 15 * time.Second
	fetchUserAgent       = "Mozilla/5.0 (compatible; MCPWebSearch/1.0)"
	fetchAcceptHeader    = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
)

// URLFetchOptions controls URL fetching behavior for FetchService.
type URLFetchOptions struct {
	MaxChars int
	MaxBytes int64
	Cursor   string
}

// FetchedDocument represents a fetched and extracted web page.
type FetchedDocument struct {
	Title       string          `json:"title"`
	Text        string          `json:"text"`
	Links       []ExtractedLink `json:"links,omitempty"`
	URL         string          `json:"url"`
	FinalURL    string          `json:"final_url"`
	Status      int             `json:"status"`
	ContentType string          `json:"content_type"`
	ByteLength  int             `json:"byte_length"`
	NextCursor  string          `json:"next_cursor,omitempty"`
	Cached      bool            `json:"cached,omitempty"`
}

// FetchService fetches URLs with redirect following, byte capping, and HTML extraction.
type FetchService struct {
	cache       *LRUCache[*FetchedDocument]
	client      *http.Client
	logger      *slog.Logger
	maxBytes    int64
	hostChecker func(context.Context, string) error
}

// NewFetchService creates a fetch service with default settings.
// If logger is nil, logging is disabled.
func NewFetchService(logger *slog.Logger) *FetchService {
	return &FetchService{
		cache:       NewLRUCache[*FetchedDocument](fetchCacheCapacity, fetchCacheTTL),
		logger:      logger,
		maxBytes:    defaultMaxFetchBytes,
		client:      &http.Client{Timeout: defaultFetchTimeout},
		hostChecker: AssertPublicHost,
	}
}

// FetchURL fetches a URL, follows redirects safely, and extracts readable content.
func (svc *FetchService) FetchURL(
	ctx context.Context,
	rawURL string,
	opts *URLFetchOptions,
) (*FetchedDocument, error) {
	if opts == nil {
		opts = &URLFetchOptions{}
	}

	resolvedURL := resolveResultIDs(rawURL)

	offset, err := decodeCursor(opts.Cursor)
	if err != nil {
		return nil, err
	}

	maxChars := resolveMaxChars(opts.MaxChars)
	maxBytes := resolveMaxBytes(opts.MaxBytes, svc.maxBytes)

	cacheKey := fmt.Sprintf("%s|%d|%s", resolvedURL, maxChars, opts.Cursor)

	cached, ok := svc.cache.Get(cacheKey)

	if ok {
		cached.Cached = true

		return cached, nil
	}

	parsed, schemeErr := ParseAndCheckScheme(resolvedURL)
	if schemeErr != nil {
		return nil, fmt.Errorf("invalid URL: %w", schemeErr)
	}

	result, redirectErr := svc.followRedirects(ctx, parsed.String())
	if redirectErr != nil {
		return nil, redirectErr
	}

	defer result.resp.Body.Close() //nolint:errcheck // deferred body close is acceptable

	if result.resp.StatusCode < httpStatusOK || result.resp.StatusCode >= httpStatusBreak {
		return nil, fmt.Errorf("HTTP %d: %s", result.resp.StatusCode, result.resp.Status)
	}

	body, readErr := io.ReadAll(io.LimitReader(result.resp.Body, maxBytes))
	if readErr != nil {
		return nil, fmt.Errorf("reading body: %w", readErr)
	}

	contentType := result.resp.Header.Get("Content-Type")

	body = decodeBody(body, contentType)

	doc := buildDocument(&docBuildParams{
		originalURL: resolvedURL,
		finalURL:    result.finalURL,
		statusCode:  result.resp.StatusCode,
		contentType: contentType,
		body:        body,
		maxChars:    maxChars,
		offset:      offset,
	})

	svc.cache.Set(cacheKey, doc)

	return doc, nil
}

// redirectResult holds the outcome of following redirects.
type redirectResult struct {
	finalURL string
	resp     *http.Response
}

// followRedirects follows HTTP redirects manually, checking each hop with SSRF guard.
func (svc *FetchService) followRedirects(
	ctx context.Context,
	startURL string,
) (*redirectResult, error) {
	currentURL := startURL

	for hop := range maxRedirectHops + 1 {
		hopResult, err := svc.fetchSingleHop(ctx, currentURL, hop)
		if err != nil {
			return nil, err
		}

		if !isRedirect(hopResult.resp.StatusCode) {
			return hopResult, nil
		}

		closeErr := hopResult.resp.Body.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("closing redirect body: %w", closeErr)
		}

		nextURL, resolveErr := resolveRedirect(
			currentURL, hopResult.resp.Header.Get("Location"),
		)
		if resolveErr != nil {
			return nil, resolveErr
		}

		currentURL = nextURL
	}

	return nil, fmt.Errorf("too many redirects (>%d)", maxRedirectHops)
}

// fetchSingleHop performs a single fetch, validating scheme and host.
func (svc *FetchService) fetchSingleHop(
	ctx context.Context,
	currentURL string,
	hop int,
) (*redirectResult, error) {
	parsed, parseErr := ParseAndCheckScheme(currentURL)
	if parseErr != nil {
		return nil, fmt.Errorf("invalid redirect URL: %w", parseErr)
	}

	hostErr := svc.hostChecker(ctx, parsed.Hostname())
	if hostErr != nil {
		return nil, fmt.Errorf("SSRF blocked: %w", hostErr)
	}

	if svc.logger != nil {
		svc.logger.DebugContext(
			ctx,
			"fetching URL",
			"hop",
			hop,
			"url",
			currentURL,
		)
	}

	req, reqErr := http.NewRequestWithContext(
		ctx, http.MethodGet, currentURL, http.NoBody,
	)
	if reqErr != nil {
		return nil, fmt.Errorf("creating request: %w", reqErr)
	}

	req.Header.Set("User-Agent", fetchUserAgent)
	req.Header.Set("Accept", fetchAcceptHeader)

	resp, fetchErr := svc.client.Do(req) //nolint:bodyclose // closed by caller
	if fetchErr != nil {
		return nil, fmt.Errorf("fetching %s: %w", currentURL, fetchErr)
	}

	return &redirectResult{finalURL: currentURL, resp: resp}, nil
}

// resolveRedirect validates and resolves a redirect Location header.
func resolveRedirect(baseURL, location string) (string, error) {
	if location == "" {
		return "", fmt.Errorf("redirect with no Location header from %s", baseURL)
	}

	parsed, parseErr := ParseAndCheckScheme(baseURL)
	if parseErr != nil {
		return "", fmt.Errorf("parsing base URL: %w", parseErr)
	}

	locParsed, locErr := parsed.Parse(location)
	if locErr != nil {
		return "", fmt.Errorf("parsing redirect Location: %w", locErr)
	}

	if locParsed.Scheme != "http" && locParsed.Scheme != "https" {
		return "", fmt.Errorf("redirect to non-HTTP scheme: %s", locParsed.Scheme)
	}

	return locParsed.String(), nil
}

// docBuildParams holds parameters for building a FetchedDocument.
type docBuildParams struct {
	originalURL string
	finalURL    string
	statusCode  int
	contentType string
	body        []byte
	maxChars    int
	offset      int
}

// buildDocument constructs a FetchedDocument from raw response data.
func buildDocument(params *docBuildParams) *FetchedDocument {
	doc := &FetchedDocument{
		Title:       "",
		Text:        "",
		Links:       []ExtractedLink(nil),
		URL:         params.originalURL,
		FinalURL:    params.finalURL,
		Status:      params.statusCode,
		ContentType: params.contentType,
		ByteLength:  len(params.body),
		NextCursor:  "",
		Cached:      false,
	}

	if isHTML(params.contentType, params.finalURL) {
		page := ExtractReadable(string(params.body), extractMaxChars)

		doc.Title = page.Title
		doc.Text = page.Text
		doc.Links = page.Links
	} else {
		doc.Text = string(params.body)
	}

	doc.Text = applyTextPagination(
		doc.Text, params.maxChars, params.offset, &doc.NextCursor,
	)

	return doc
}

// applyTextPagination slices text at offset, caps at maxChars, and sets nextCursor.
func applyTextPagination(
	text string,
	maxChars, offset int,
	nextCursor *string,
) string {
	if offset > 0 {
		if offset > len(text) {
			return ""
		}

		text = text[offset:]
	}

	if len(text) > maxChars {
		*nextCursor = EncodeBodyCursor(offset + maxChars)

		return text[:maxChars] + "…"
	}

	return text
}

// resolveResultIDs resolves result IDs to actual URLs.
func resolveResultIDs(rawURL string) string {
	if LooksLikeResultID(rawURL) {
		if resolved, ok := ResolveResultID(rawURL); ok {
			return resolved
		}
	}

	return rawURL
}

// decodeCursor decodes a body cursor and returns the offset.
func decodeCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}

	decoded, ok := DecodeBodyCursor(cursor)
	if !ok {
		return 0, fmt.Errorf("invalid body cursor: %s", cursor)
	}

	return decoded, nil
}

// resolveMaxChars returns the effective maxChars value.
func resolveMaxChars(maxChars int) int {
	if maxChars <= 0 {
		return defaultMaxChars
	}

	return maxChars
}

// resolveMaxBytes returns the effective maxBytes value.
func resolveMaxBytes(maxBytes, defaultBytes int64) int64 {
	if maxBytes <= 0 {
		return defaultBytes
	}

	return maxBytes
}

// decodeBody detects charset from Content-Type and decodes the body to UTF-8.
func decodeBody(body []byte, contentType string) []byte {
	_, name, _ := charset.DetermineEncoding(body, contentType)
	if name == "" || strings.EqualFold(name, "utf-8") || strings.EqualFold(name, "utf8") {
		return body
	}

	enc, _ := charset.Lookup(name)
	if enc == nil {
		return body
	}

	reader := transform.NewReader(
		strings.NewReader(string(body)), enc.NewDecoder(),
	)

	decoded, err := io.ReadAll(reader)
	if err != nil {
		return body
	}

	return decoded
}

// isHTML checks if the content type or URL suggests HTML content.
func isHTML(contentType, urlStr string) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml") {
		return true
	}

	if contentType == "" {
		return looksLikeHTMLURL(urlStr)
	}

	return false
}

// looksLikeHTMLURL checks if a URL path suggests HTML content.
func looksLikeHTMLURL(urlStr string) bool {
	lower := strings.ToLower(urlStr)
	pathPart := lower[strings.LastIndex(lower, "/"):]

	return strings.HasSuffix(pathPart, ".html") ||
		strings.HasSuffix(pathPart, ".htm") ||
		!strings.Contains(pathPart, ".")
}

// isRedirect returns true for HTTP redirect status codes.
func isRedirect(statusCode int) bool {
	return statusCode == http.StatusMovedPermanently ||
		statusCode == http.StatusFound ||
		statusCode == http.StatusSeeOther ||
		statusCode == http.StatusTemporaryRedirect ||
		statusCode == http.StatusPermanentRedirect
}
