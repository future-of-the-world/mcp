// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeCursor_Empty(t *testing.T) {
	t.Parallel()

	offset, err := decodeCursor("")
	require.NoError(t, err)
	assert.Zero(t, offset)
}

func TestDecodeCursor_Valid(t *testing.T) {
	t.Parallel()

	encoded := EncodeBodyCursor(42)

	offset, err := decodeCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, 42, offset)
}

func TestDecodeCursor_Invalid(t *testing.T) {
	t.Parallel()

	_, err := decodeCursor("not-a-cursor")
	require.Error(t, err)
}

func TestResolveMaxChars_Positive(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 100, resolveMaxChars(100))
}

func TestResolveMaxChars_Zero(t *testing.T) {
	t.Parallel()

	assert.Equal(t, defaultMaxChars, resolveMaxChars(0))
}

func TestResolveMaxChars_Negative(t *testing.T) {
	t.Parallel()

	assert.Equal(t, defaultMaxChars, resolveMaxChars(-10))
}

func TestResolveMaxBytes_Positive(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int64(4096), resolveMaxBytes(4096, defaultMaxFetchBytes))
}

func TestResolveMaxBytes_Zero(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int64(2097152), resolveMaxBytes(0, defaultMaxFetchBytes))
}

func TestResolveMaxBytes_Negative(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int64(1024), resolveMaxBytes(-1, 1024))
}

func TestIsHTML_TextHTML(t *testing.T) {
	t.Parallel()

	assert.True(t, isHTML("text/html; charset=utf-8", ""))
}

func TestIsHTML_ApplicationXHTML(t *testing.T) {
	t.Parallel()

	assert.True(t, isHTML("application/xhtml+xml", ""))
}

func TestIsHTML_EmptyWithHTMLURL(t *testing.T) {
	t.Parallel()

	assert.True(t, isHTML("", "https://example.com/page.html"))
}

func TestIsHTML_PlainText(t *testing.T) {
	t.Parallel()

	assert.False(t, isHTML("text/plain", "https://example.com/file.txt"))
}

func TestIsHTML_ApplicationJSON(t *testing.T) {
	t.Parallel()

	assert.False(t, isHTML("application/json", "https://api.example.com/data"))
}

func TestIsHTML_EmptyWithNonHTMLURL(t *testing.T) {
	t.Parallel()

	assert.False(t, isHTML("", "https://example.com/data.json"))
}

func TestIsRedirect_MovedPermanently(t *testing.T) {
	t.Parallel()

	assert.True(t, isRedirect(http.StatusMovedPermanently))
}

func TestIsRedirect_Found(t *testing.T) {
	t.Parallel()

	assert.True(t, isRedirect(http.StatusFound))
}

func TestIsRedirect_SeeOther(t *testing.T) {
	t.Parallel()

	assert.True(t, isRedirect(http.StatusSeeOther))
}

func TestIsRedirect_TemporaryRedirect(t *testing.T) {
	t.Parallel()

	assert.True(t, isRedirect(http.StatusTemporaryRedirect))
}

func TestIsRedirect_PermanentRedirect(t *testing.T) {
	t.Parallel()

	assert.True(t, isRedirect(http.StatusPermanentRedirect))
}

func TestIsRedirect_OK(t *testing.T) {
	t.Parallel()

	assert.False(t, isRedirect(http.StatusOK))
}

func TestIsRedirect_NotFound(t *testing.T) {
	t.Parallel()

	assert.False(t, isRedirect(http.StatusNotFound))
}

func TestLooksLikeHTMLURL_HTMLExtension(t *testing.T) {
	t.Parallel()

	assert.True(t, looksLikeHTMLURL("https://example.com/page.html"))
}

func TestLooksLikeHTMLURL_HTMExtension(t *testing.T) {
	t.Parallel()

	assert.True(t, looksLikeHTMLURL("https://example.com/page.htm"))
}

func TestLooksLikeHTMLURL_NoExtension(t *testing.T) {
	t.Parallel()

	assert.True(t, looksLikeHTMLURL("https://example.com/page"))
}

func TestLooksLikeHTMLURL_JSONExtension(t *testing.T) {
	t.Parallel()

	assert.False(t, looksLikeHTMLURL("https://example.com/data.json"))
}

func TestLooksLikeHTMLURL_TrailingSlash(t *testing.T) {
	t.Parallel()

	assert.True(t, looksLikeHTMLURL("https://example.com/"))
}

func TestBuildDocument_HTMLContent(t *testing.T) {
	t.Parallel()

	htmlBody := []byte(
		"<html><head><title>Test</title></head>" +
			"<body><p>Hello world</p></body></html>",
	)

	doc := buildDocument(&docBuildParams{
		originalURL: "https://example.com",
		finalURL:    "https://example.com",
		statusCode:  200,
		contentType: "text/html",
		body:        htmlBody,
		maxChars:    1000,
		offset:      0,
	})

	assert.Equal(t, "Test", doc.Title)
	assert.Contains(t, doc.Text, "Hello world")
	assert.Equal(t, "https://example.com", doc.URL)
}

func TestBuildDocument_PlainText(t *testing.T) {
	t.Parallel()

	textBody := []byte("Just some plain text content")

	doc := buildDocument(&docBuildParams{
		originalURL: "https://example.com/file.txt",
		finalURL:    "https://example.com/file.txt",
		statusCode:  200,
		contentType: "text/plain",
		body:        textBody,
		maxChars:    1000,
		offset:      0,
	})

	assert.Emptyf(t, doc.Title, "plain text has no title")
	assert.Equal(t, "Just some plain text content", doc.Text)
	assert.Emptyf(t, doc.Links, "plain text has no links")
}

func TestBuildDocument_Truncation(t *testing.T) {
	t.Parallel()

	longText := make([]byte, 200)
	for i := range longText {
		longText[i] = 'a'
	}

	doc := buildDocument(&docBuildParams{
		originalURL: "https://example.com",
		finalURL:    "https://example.com",
		statusCode:  200,
		contentType: "text/plain",
		body:        longText,
		maxChars:    50,
		offset:      0,
	})

	assert.NotEmptyf(t, doc.NextCursor,
		"should set cursor when text is truncated",
	)
	assert.Len(t, doc.Text, 53) // 50 chars + "…" (3-byte UTF-8 ellipsis)
}

func TestApplyTextPagination_WithinBounds(t *testing.T) {
	t.Parallel()

	var next string

	result := applyTextPagination("hello", 100, 0, &next)

	assert.Equal(t, "hello", result)
	assert.Emptyf(t, next, "no cursor when within bounds")
}

func TestApplyTextPagination_ExceedsBounds(t *testing.T) {
	t.Parallel()

	var next string

	result := applyTextPagination("hello world", 5, 0, &next)

	assert.Equal(t, "hello…", result)
	assert.NotEmptyf(t, next, "should set cursor for remaining text")
}

func TestApplyTextPagination_WithOffset(t *testing.T) {
	t.Parallel()

	var next string

	result := applyTextPagination("hello world", 100, 6, &next)

	assert.Equal(t, "world", result)
	assert.Emptyf(t, next, "remaining text fits within maxChars")
}

func TestApplyTextPagination_OffsetBeyondLength(t *testing.T) {
	t.Parallel()

	var next string

	result := applyTextPagination("hi", 100, 50, &next)

	assert.Empty(t, result)
	assert.Emptyf(t, next, "no cursor when offset exceeds text")
}

func TestApplyTextPagination_NoTruncationExact(t *testing.T) {
	t.Parallel()

	var next string

	result := applyTextPagination("hello", 5, 0, &next)

	assert.Equal(t, "hello", result)
	assert.Emptyf(t, next, "exact fit should not produce cursor")
}

func TestNewFetchService(t *testing.T) {
	t.Parallel()

	svc := NewFetchService((*slog.Logger)(nil))

	require.NotNil(t, svc)
	require.NotNil(t, svc.cache)
	require.NotNil(t, svc.client)
	require.NotNil(t, svc.hostChecker)
	assert.Equal(t, int64(defaultMaxFetchBytes), svc.maxBytes)
}

// writeText is a test helper that writes a text/plain response.
func writeText(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/plain")

	_, _ = w.Write([]byte(text)) //nolint:errcheck // test helper
}

func TestFetchURL_PlainText(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			writeText(w, "hello from server")
		},
	))
	defer srv.Close()

	svc := NewFetchService((*slog.Logger)(nil))

	svc.client = srv.Client()
	svc.hostChecker = func(_ context.Context, _ string) error { return nil }

	doc, err := svc.FetchURL(t.Context(), srv.URL, (*URLFetchOptions)(nil))
	require.NoError(t, err)

	assert.Equal(t, "hello from server", doc.Text)
	assert.Equal(t, srv.URL, doc.URL)
	assert.Equal(t, srv.URL, doc.FinalURL)
	assert.Equal(t, http.StatusOK, doc.Status)
}

func TestFetchURL_HTMLContent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")

			htmlBody := "<html><head><title>Test</title>" +
				"</head><body><p>Content</p></body></html>"

			_, _ = w.Write([]byte(htmlBody)) //nolint:errcheck // test helper
		},
	))
	defer srv.Close()

	svc := NewFetchService((*slog.Logger)(nil))

	svc.client = srv.Client()
	svc.hostChecker = func(_ context.Context, _ string) error { return nil }

	doc, err := svc.FetchURL(t.Context(), srv.URL, (*URLFetchOptions)(nil))
	require.NoError(t, err)

	assert.Equal(t, "Test", doc.Title)
	assert.Contains(t, doc.Text, "Content")
}

func TestFetchURL_FollowsRedirects(t *testing.T) {
	t.Parallel()

	var targetURL string

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/redirect" {
				w.Header().Set("Location", targetURL)
				w.WriteHeader(http.StatusFound)

				return
			}

			writeText(w, "final destination")
		},
	))
	defer srv.Close()

	targetURL = srv.URL + "/final"

	svc := NewFetchService((*slog.Logger)(nil))

	svc.client = srv.Client()
	svc.client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	svc.hostChecker = func(_ context.Context, _ string) error { return nil }

	doc, err := svc.FetchURL(
		t.Context(), srv.URL+"/redirect", (*URLFetchOptions)(nil),
	)
	require.NoError(t, err)

	assert.Equal(t, "final destination", doc.Text)
	assert.Equal(t, srv.URL+"/final", doc.FinalURL)
}

func TestFetchURL_CachesResults(t *testing.T) {
	t.Parallel()

	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			callCount++
			writeText(w, "cached content")
		},
	))
	defer srv.Close()

	svc := NewFetchService((*slog.Logger)(nil))

	svc.client = srv.Client()
	svc.hostChecker = func(_ context.Context, _ string) error { return nil }

	doc1, err := svc.FetchURL(t.Context(), srv.URL, (*URLFetchOptions)(nil))
	require.NoError(t, err)
	assert.Falsef(t, doc1.Cached, "first fetch should not be cached")

	doc2, err := svc.FetchURL(t.Context(), srv.URL, (*URLFetchOptions)(nil))
	require.NoError(t, err)
	assert.Truef(t, doc2.Cached, "second fetch should be cached")
	assert.Equalf(t, 1, callCount, "server should only be called once")
}

func TestFetchURL_Pagination(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			writeText(w, "abcdefghij")
		},
	))
	defer srv.Close()

	svc := NewFetchService((*slog.Logger)(nil))

	svc.client = srv.Client()
	svc.hostChecker = func(_ context.Context, _ string) error { return nil }

	doc1, err := svc.FetchURL(
		t.Context(), srv.URL, &URLFetchOptions{MaxChars: 5},
	)
	require.NoError(t, err)

	assert.Len(t, doc1.Text, 8) // 5 chars + "…" (3 bytes)
	assert.NotEmpty(t, doc1.NextCursor)

	doc2, err := svc.FetchURL(
		t.Context(), srv.URL,
		&URLFetchOptions{MaxChars: 10, Cursor: doc1.NextCursor},
	)
	require.NoError(t, err)

	assert.Equal(t, "fghij", doc2.Text)
}

func TestFetchURL_BadScheme(t *testing.T) {
	t.Parallel()

	svc := NewFetchService((*slog.Logger)(nil))

	_, err := svc.FetchURL(t.Context(), "ftp://example.com", (*URLFetchOptions)(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid URL")
}

func TestFetchURL_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
	))
	defer srv.Close()

	svc := NewFetchService((*slog.Logger)(nil))

	svc.client = srv.Client()
	svc.hostChecker = func(_ context.Context, _ string) error { return nil }

	_, err := svc.FetchURL(t.Context(), srv.URL, (*URLFetchOptions)(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
}

func TestFetchURL_MaxBytesCap(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")

			data := make([]byte, 1000)
			for i := range data {
				data[i] = 'x'
			}

			_, _ = w.Write(data) //nolint:errcheck // test helper
		},
	))
	defer srv.Close()

	svc := NewFetchService((*slog.Logger)(nil))

	svc.client = srv.Client()
	svc.hostChecker = func(_ context.Context, _ string) error { return nil }

	doc, err := svc.FetchURL(
		t.Context(), srv.URL, &URLFetchOptions{MaxBytes: 100},
	)
	require.NoError(t, err)

	assert.Equalf(t, 100, doc.ByteLength, "body should be capped at maxBytes")
}
