// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/tool"
)

// env helpers — Go's os.LookupEnv / Setenv / Unsetenv are simple enough
// to wrap once here so the tests read cleanly.
func osLookupEnv(key string) (string, bool) { return os.LookupEnv(key) }

func osSetenv(key, val string) {
	_ = os.Setenv(key, val) //nolint:errcheck // test env var writes are best-effort
}

func osUnsetenv(key string) {
	_ = os.Unsetenv(key) //nolint:errcheck // test env var writes are best-effort
}

const testAPIKey = "fake-key"

// ---------------------------------------------------------------------------
// Connect-level tests (no env, can run in parallel)
// ---------------------------------------------------------------------------

func TestConnect_NoAPIKey(t *testing.T) {
	// Ensure no env var is set, so Connect sees no API key and skips
	// the news_search and image_search tools. Unsetenv is not t.Setenv
	// (which requires a value); use os to drop the var, then Setenv to
	// restore it. The t.Setenv semantics require t to NOT be parallel.
	oldVal, oldSet := osLookupEnv("BRAVE_API_KEY")
	osUnsetenv("BRAVE_API_KEY")
	t.Cleanup(func() {
		if oldSet {
			osSetenv("BRAVE_API_KEY", oldVal)
		} else {
			osUnsetenv("BRAVE_API_KEY")
		}
	})

	resp, err := Connect(t.Context(), make(map[string]any))
	require.NoError(t, err)

	names := toolResponseNames(resp)
	require.ElementsMatch(t,
		[]string{"web_search", "fetch_url", "list_providers"},
		names,
	)
}

func TestConnect_InvalidTimeout(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"timeout": "garbage",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout")
}

func TestConnect_NegativeTimeout(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"timeout": "-1s",
	})
	require.Error(t, err)
}

func TestConnect_WithAPIKey(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", testAPIKey)

	resp, err := Connect(t.Context(), make(map[string]any))
	require.NoError(t, err)

	names := toolResponseNames(resp)
	require.ElementsMatch(t,
		[]string{"web_search", "news_search", "image_search", "fetch_url", "list_providers"},
		names,
	)
}

func TestConnect_AllReadOnlyHints(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", testAPIKey)

	resp, err := Connect(t.Context(), make(map[string]any))
	require.NoError(t, err)

	// All 5 websearch tools (3 unconditional + 2 with a Brave API key)
	// must set Annotations: ReadOnlyHint=true because they never
	// mutate upstream state.
	for _, entry := range resp.Tools {
		require.NotNilf(t, entry.Annotations, "tool %s must have Annotations", entry.Name)
		assert.Truef(t, entry.Annotations.ReadOnlyHint,
			"tool %s should have ReadOnlyHint=true", entry.Name)
	}
}

// ---------------------------------------------------------------------------
// Test helpers (use t.Setenv, so consumer tests cannot use t.Parallel)
// ---------------------------------------------------------------------------

type testMockProvider struct {
	name      string
	kind      SearchKind
	hasKey    bool
	results   []SearchResult
	searchErr error
}

func (m *testMockProvider) Name() string { return m.name }

func (m *testMockProvider) Supports(kind SearchKind) bool { return m.kind == kind }

func (m *testMockProvider) RequiresAPIKey() bool { return m.hasKey }

func (m *testMockProvider) Search(
	_ context.Context,
	query string,
	_ *SearchOptions,
) (*SearchResponse, error) {
	if m.searchErr != nil {
		return nil, m.searchErr
	}

	return &SearchResponse{
		Results:    m.results,
		Query:      query,
		Provider:   m.name,
		Count:      len(m.results),
		Cached:     false,
		Offset:     0,
		NextCursor: "",
	}, nil
}

// newLiveTool constructs a *Tool with the same init path as Connect and
// installs a mock provider in the search factory so handlers can be
// driven without hitting any external network.
func newLiveTool(t *testing.T, kind SearchKind, results []SearchResult) *Tool {
	t.Helper()

	t.Setenv("BRAVE_API_KEY", testAPIKey)

	searchTool := &Tool{
		BraveAPIKeyEnv: "BRAVE_API_KEY",
		MaxResults:     defaultMaxResults,
	}
	require.NoError(t, searchTool.init(t.Context()))

	searchTool.factory = NewProviderFactory((*slog.Logger)(nil))
	searchTool.factory.Add(t.Context(), &testMockProvider{
		name:      "Mock",
		kind:      kind,
		hasKey:    false,
		results:   results,
		searchErr: error(nil),
	}, true)

	searchTool.search = NewSearchService(searchTool.factory)

	return searchTool
}

// newLiveFetchTool constructs a *Tool ready for fetch_url tests with
// the SSRF check disabled (so the test can hit a local httptest server).
func newLiveFetchTool(t *testing.T) *Tool {
	t.Helper()

	t.Setenv("BRAVE_API_KEY", testAPIKey)

	searchTool := &Tool{
		BraveAPIKeyEnv: "BRAVE_API_KEY",
		MaxResults:     defaultMaxResults,
	}
	require.NoError(t, searchTool.init(t.Context()))

	searchTool.fetch.hostChecker = func(_ context.Context, _ string) error { return nil }

	return searchTool
}

func newLiveNoSearchTool(t *testing.T) *Tool {
	t.Helper()

	t.Setenv("BRAVE_API_KEY", testAPIKey)

	searchTool := &Tool{
		BraveAPIKeyEnv: "BRAVE_API_KEY",
		MaxResults:     defaultMaxResults,
	}
	require.NoError(t, searchTool.init(t.Context()))

	return searchTool
}

// ---------------------------------------------------------------------------
// Handler-level tests (drive the tool handlers directly)
// These cannot use t.Parallel because their helpers call t.Setenv.
// ---------------------------------------------------------------------------

func TestHandler_WebSearch_Success(t *testing.T) {
	searchTool := newLiveTool(t, SearchKindWeb, []SearchResult{
		{
			Title:       "Result 1",
			URL:         "https://example.com",
			Snippet:     "snippet",
			RawURL:      "",
			Description: "",
		},
	})

	handler := handleWebSearch(searchTool)

	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "",
			Arguments: json.RawMessage(`{"search_term":"golang","count":5}`),
		},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, textContent.Text, "Result 1")
	require.Contains(t, textContent.Text, "golang")
}

func TestHandler_WebSearch_MissingTerm(t *testing.T) {
	searchTool := newLiveTool(t, SearchKindWeb, []SearchResult(nil))

	handler := handleWebSearch(searchTool)

	_, err := handler(t.Context(), &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "",
			Arguments: json.RawMessage(`{}`),
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "search_term")
}

func TestHandler_NewsSearch(t *testing.T) {
	searchTool := newLiveTool(t, SearchKindNews, []SearchResult{
		{Title: "News 1", URL: "https://example.com", Snippet: "", RawURL: "", Description: ""},
	})

	handler := handleNewsSearch(searchTool)

	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "",
			Arguments: json.RawMessage(`{"search_term":"golang"}`),
		},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, textContent.Text, "News 1")
}

func TestHandler_ImageSearch(t *testing.T) {
	searchTool := newLiveTool(t, SearchKindImages, []SearchResult{
		{Title: "Image 1", URL: "https://example.com", Snippet: "", RawURL: "", Description: ""},
	})

	handler := handleImageSearch(searchTool)

	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "",
			Arguments: json.RawMessage(`{"search_term":"golang"}`),
		},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, textContent.Text, "Image 1")
}

func TestHandler_FetchURL_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // test handler: write failure is not critical
		_, _ = w.Write(
			[]byte(
				`<html><head><title>Test Page</title></head><body><p>Hello World</p></body></html>`,
			),
		)
	}))
	defer srv.Close()

	searchTool := newLiveFetchTool(t)

	handler := handleFetchURL(searchTool)

	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "",
			Arguments: json.RawMessage(`{"url":"` + srv.URL + `"}`),
		},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, textContent.Text, "Test Page")
	require.Contains(t, textContent.Text, "Hello World")
}

func TestHandler_FetchURL_MissingURL(t *testing.T) {
	searchTool := newLiveFetchTool(t)

	handler := handleFetchURL(searchTool)

	_, err := handler(t.Context(), &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "",
			Arguments: json.RawMessage(`{}`),
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "url is required")
}

func TestHandler_ListProviders(t *testing.T) {
	searchTool := newLiveNoSearchTool(t)

	handler := handleListProviders(searchTool)

	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "",
			Arguments: json.RawMessage(`{}`),
		},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, textContent.Text, "duckduckgo")
	require.Contains(t, textContent.Text, "providers")
}

func TestHandler_ProviderHint(t *testing.T) {
	searchTool := newLiveTool(t, SearchKindWeb, []SearchResult{
		{Title: "Web Result", URL: "https://example.com", Snippet: "", RawURL: "", Description: ""},
	})

	handler := handleWebSearch(searchTool)

	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "",
			Arguments: json.RawMessage(`{"search_term":"provider:mock golang"}`),
		},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, textContent.Text, "Web Result")
}

func TestHandler_SearchError(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", testAPIKey)

	searchTool := &Tool{
		BraveAPIKeyEnv: "BRAVE_API_KEY",
		MaxResults:     defaultMaxResults,
	}
	require.NoError(t, searchTool.init(t.Context()))

	searchTool.factory = NewProviderFactory((*slog.Logger)(nil))
	searchTool.factory.Add(t.Context(), &testMockProvider{
		name:      "Mock",
		kind:      SearchKindWeb,
		hasKey:    false,
		results:   []SearchResult(nil),
		searchErr: errors.New("provider down"),
	}, true)

	searchTool.search = NewSearchService(searchTool.factory)

	handler := handleWebSearch(searchTool)

	_, err := handler(t.Context(), &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "",
			Arguments: json.RawMessage(`{"search_term":"golang"}`),
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider down")
}

// ---------------------------------------------------------------------------
// Response struct marshaling tests
// ---------------------------------------------------------------------------

func TestSearchResponse_JSON(t *testing.T) {
	t.Parallel()

	resp := &SearchResponse{
		Provider: "Mock",
		Query:    "golang",
		Cached:   true,
		Count:    2,
		Offset:   0,
		Results: []SearchResult{
			{
				Title:       "Result 1",
				URL:         "https://example.com/1",
				Snippet:     "Snippet 1",
				RawURL:      "",
				Description: "",
			},
			{
				Title:       "Result 2",
				URL:         "https://example.com/2",
				Snippet:     "",
				RawURL:      "",
				Description: "",
			},
		},
		NextCursor: "next-page",
	}

	data := mustMarshal(t, resp)

	var parsed map[string]any

	err := json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	require.Equal(t, "Mock", parsed["provider"])
	require.Equal(t, "golang", parsed["query"])
	require.Equal(t, true, parsed["cached"])
	require.Equal(t, "next-page", parsed["next_cursor"])

	results, ok := parsed["results"].([]any)
	require.True(t, ok)
	require.Len(t, results, 2)
}

func TestFetchedDocument_JSON(t *testing.T) {
	t.Parallel()

	doc := &FetchedDocument{
		Title:       "Test Title",
		Text:        "Hello world",
		Links:       []ExtractedLink(nil),
		URL:         "https://example.com",
		FinalURL:    "https://example.com/final",
		Status:      200,
		ContentType: "text/html",
		ByteLength:  100,
		NextCursor:  "cursor-1",
		Cached:      true,
	}

	data := mustMarshal(t, doc)

	var parsed map[string]any

	err := json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	require.Equal(t, "Test Title", parsed["title"])
	require.Equal(t, "Hello world", parsed["text"])
	require.Equal(t, "https://example.com", parsed["url"])
	require.Equal(t, "https://example.com/final", parsed["final_url"])
	require.Equal(t, "cursor-1", parsed["next_cursor"])
}

func TestListProvidersResponse_JSON(t *testing.T) {
	t.Parallel()

	resp := &listProvidersResponse{
		Providers: []string{"Brave Search", "DuckDuckGo"},
		Default:   "DuckDuckGo",
	}

	data := mustMarshal(t, resp)

	var parsed map[string]any

	err := json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	providers, ok := parsed["providers"].([]any)
	require.True(t, ok)
	require.Len(t, providers, 2)
	require.Equal(t, "DuckDuckGo", parsed["default"])
}

// toolResponseNames returns the names of the tools in resp.Tools,
// decoupling the test from the tool package's struct shape.
func toolResponseNames(resp tool.Response) []string {
	names := make([]string, len(resp.Tools))
	for i, e := range resp.Tools {
		names[i] = e.Name
	}

	return names
}
