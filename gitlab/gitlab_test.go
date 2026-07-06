// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package gitlab

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"go.amidman.dev/mcp/decode"
)

// ---------------------------------------------------------------------------
// Connect-level tests
// ---------------------------------------------------------------------------

func TestConnect_RequiresToken(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), make(map[string]any))
	require.Error(t, err)
	require.Contains(t, err.Error(), "token is empty")
}

func TestConnect_AllReadOnlyHints(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"token": "test-token",
	})
	require.NoError(t, err)
	require.Len(t, resp.Tools, 2)

	// Both gitlab tools (get_mr_discussions, get_mr_commits) must set
	// Annotations: ReadOnlyHint=true because they only ever query the
	// GitLab API.
	for _, entry := range resp.Tools {
		require.NotNilf(t, entry.Annotations, "tool %s must have Annotations", entry.Name)
		assert.Truef(t, entry.Annotations.ReadOnlyHint,
			"tool %s should have ReadOnlyHint=true", entry.Name)
	}
}

func TestConnect_ToolNames(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"token": "test-token",
	})
	require.NoError(t, err)

	names := make([]string, len(resp.Tools))
	for i, e := range resp.Tools {
		names[i] = e.Name
	}

	require.ElementsMatch(t,
		[]string{"get_mr_discussions", "get_mr_commits"},
		names,
	)
}

// ---------------------------------------------------------------------------
// URL parsing
// ---------------------------------------------------------------------------

func TestParseMRURL_Valid(t *testing.T) {
	t.Parallel()

	parts, err := parseMRURL("https://gitlab.example.com/group/project/-/merge_requests/42")
	require.NoError(t, err)
	require.Equal(t, "group/project", parts.ProjectPath)
	require.Equal(t, int64(42), parts.MRIID)
}

func TestParseMRURL_WithSubgroup(t *testing.T) {
	t.Parallel()

	parts, err := parseMRURL("https://gitlab.example.com/group/sub/project/-/merge_requests/99")
	require.NoError(t, err)
	require.Equal(t, "group/sub/project", parts.ProjectPath)
	require.Equal(t, int64(99), parts.MRIID)
}

func TestParseMRURL_Invalid(t *testing.T) {
	t.Parallel()

	_, err := parseMRURL("https://gitlab.example.com/group/project")
	require.Error(t, err)
}

func TestParseAndValidateURL_Empty(t *testing.T) {
	t.Parallel()

	_, err := parseAndValidateURL("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "merge request URL is required")
}

// ---------------------------------------------------------------------------
// getURLField (type-switch helper for the two MR arg types)
// ---------------------------------------------------------------------------

func TestGetURLField_GetMRDiscussionsRequest(t *testing.T) {
	t.Parallel()

	arg := &GetMRDiscussionsRequest{
		URL:     "https://gitlab.example.com/group/proj/-/merge_requests/1",
		Page:    0,
		PerPage: 0,
	}
	got := getURLField(arg)
	require.Equal(t, "https://gitlab.example.com/group/proj/-/merge_requests/1", got)
}

func TestGetURLField_GetMRCommitsRequest(t *testing.T) {
	t.Parallel()

	arg := &GetMRCommitsRequest{
		URL:     "https://gitlab.example.com/group/proj/-/merge_requests/2",
		Page:    0,
		PerPage: 0,
	}
	got := getURLField(arg)
	require.Equal(t, "https://gitlab.example.com/group/proj/-/merge_requests/2", got)
}

func TestGetURLField_UnknownType(t *testing.T) {
	t.Parallel()

	got := getURLField(struct{ URL string }{URL: "should-not-be-read"})
	require.Empty(t, got)
}

func TestGetURLField_Nil(t *testing.T) {
	t.Parallel()

	got := getURLField(any(nil))
	require.Empty(t, got)
}

// ---------------------------------------------------------------------------
// jsonResult (response wrapper that sets Content + StructuredContent)
// ---------------------------------------------------------------------------

func TestJSONResult_Map(t *testing.T) {
	t.Parallel()

	result, err := jsonResult(map[string]any{"id": 42, "name": "sprocket"})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.JSONEq(t, `{"id":42,"name":"sprocket"}`, textContent.Text)
	require.NotNil(t, result.StructuredContent)
}

func TestJSONResult_Struct(t *testing.T) {
	t.Parallel()

	type payload struct {
		Count int      `json:"count"`
		Items []string `json:"items"`
	}

	result, err := jsonResult(payload{Count: 2, Items: []string{"a", "b"}})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.JSONEq(t, `{"count":2,"items":["a","b"]}`, textContent.Text)
}

func TestJSONResult_ChannelMarshalError(t *testing.T) {
	t.Parallel()

	// channels cannot be JSON-marshaled, so the function must return
	// an error rather than a successful result.
	result, err := jsonResult(make(chan int))
	require.Error(t, err)
	require.Contains(t, err.Error(), "marshal response")
	require.Nil(t, result)
}

// ---------------------------------------------------------------------------
// decodeConnect/validate negative paths (gated by type-asserted inputs)
// ---------------------------------------------------------------------------

// TestDecodeConnect_NumericTokenCoercion verifies the new
// decode.AsString path: a numeric token value is stringified via
// fmt.Sprint rather than rejected. This is the YAML-natural-value
// acceptance path the issue asked for.
func TestDecodeConnect_NumericTokenCoercion(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{"token": 12345})
	require.NoError(t, err)
	require.Equal(t, "12345", cfg.Token)
}

// TestDecodeConnect_NonScalarToken verifies the new strict path:
// a non-scalar value (a map, here) where a string is expected
// produces a wrapped decode.ErrWrongType.
func TestDecodeConnect_NonScalarToken(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{"token": map[string]any{"a": "b"}})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
}

// TestDecodeConnect_NonScalarBaseURL mirrors the token test for
// the optional base_url field.
func TestDecodeConnect_NonScalarBaseURL(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{"token": "t", "base_url": map[string]any{"h": "x"}})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
}

// TestDecodeConnect_NumericBaseURLCoercion mirrors the token test.
func TestDecodeConnect_NumericBaseURLCoercion(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{"token": "t", "base_url": 443})
	require.NoError(t, err)
	require.Equal(t, "443", cfg.BaseURL)
}

func TestValidate_InvalidBaseURL(t *testing.T) {
	t.Parallel()

	err := config{Token: "t", BaseURL: "not a url with ://"}.validate()
	require.Error(t, err)
}

func TestValidate_BaseURLNoScheme(t *testing.T) {
	t.Parallel()

	err := config{Token: "t", BaseURL: "gitlab.example.com"}.validate()
	require.ErrorIs(t, err, errBaseURLParse)
}

func TestValidate_BaseURLNoHost(t *testing.T) {
	t.Parallel()

	err := config{Token: "t", BaseURL: "https://"}.validate()
	require.ErrorIs(t, err, errBaseURLParse)
}

func TestValidate_EmptyBaseURL(t *testing.T) {
	t.Parallel()

	// Empty BaseURL is allowed: the client falls back to the default.
	require.NoError(t, config{Token: "t", BaseURL: ""}.validate())
}

func TestValidate_ValidBaseURL(t *testing.T) {
	t.Parallel()

	require.NoError(t, config{Token: "t", BaseURL: "https://gitlab.example.com"}.validate())
}

// ---------------------------------------------------------------------------
// Handler integration tests (httptest-based mock GitLab API)
// ---------------------------------------------------------------------------

// mockGitLabServer starts an httptest server that mimics the GitLab
// API. The handler is called for every request; it is expected to
// write the response. If handler is nil, the server returns 404.
func mockGitLabServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()

	mux := http.NewServeMux()
	if handler != nil {
		mux.HandleFunc("/", handler)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			//nolint:errcheck // hard-coded response; write error is not actionable
			_, _ = io.WriteString(w, "not found")
		})
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv.URL
}

// callTool invokes a tool handler from Connect with the given
// arguments payload. The baseURL is forwarded to the underlying
// GitLab client.
func callTool(
	t *testing.T,
	baseURL, toolName string,
	args json.RawMessage,
) (*mcp.CallToolResult, error) {
	t.Helper()

	resp, err := Connect(t.Context(), map[string]any{
		"token":    "test-token",
		"base_url": baseURL,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Tools)

	var handler mcp.ToolHandler

	for _, entry := range resp.Tools {
		if entry.Name == toolName {
			handler = entry.Handler
			break
		}
	}

	require.NotNilf(t, handler, "tool %q not found in Connect response", toolName)

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta{},
			Name:      toolName,
			Arguments: args,
		},
		Extra: (*mcp.RequestExtra)(nil),
	}

	return handler(t.Context(), req)
}

// TestHandleGetMRDiscussions_Success exercises handleGetMRDiscussions
// against a mock GitLab API server. The mock returns a single
// discussion; the handler must marshal it into a CallToolResult.
func TestHandleGetMRDiscussions_Success(t *testing.T) {
	t.Parallel()

	baseURL := mockGitLabServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Use t.Errorf directly: the go-require linter disallows
		// require.* inside http.HandlerFunc because the assertions
		// can run in a goroutine that outlives the test.
		const wantPath = "/api/v4/projects/group/project/merge_requests/42/discussions"
		if got := r.URL.Path; got != wantPath {
			t.Errorf("URL.Path = %q, want %q", got, wantPath)
		}

		if got, want := r.Header.Get("Private-Token"), "test-token"; got != want {
			t.Errorf("Private-Token header = %q, want %q", got, want)
		}

		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, `[{
			"id":"abc123",
			"individual_note":true,
			"notes":[{"id":1,"body":"looks good",
				"author":{"id":1,"username":"alice","name":"Alice"}}]
		}]`)
	})

	result, err := callTool(t, baseURL, "get_mr_discussions",
		json.RawMessage(`{"url":"https://gitlab.example.com/group/project/-/merge_requests/42"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected *mcp.TextContent, got %T", result.Content[0])
	require.Contains(t, textContent.Text, "abc123")
	require.NotNil(t, result.StructuredContent)
}

// TestHandleGetMRCommits_Success exercises handleGetMRCommits
// against a mock GitLab API server.
func TestHandleGetMRCommits_Success(t *testing.T) {
	t.Parallel()

	baseURL := mockGitLabServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Use t.Errorf directly: the go-require linter disallows
		// require.* inside http.HandlerFunc because the assertions
		// can run in a goroutine that outlives the test.
		const wantPath = "/api/v4/projects/group/project/merge_requests/7/commits"
		if got := r.URL.Path; got != wantPath {
			t.Errorf("URL.Path = %q, want %q", got, wantPath)
		}

		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, `[{
			"id":"deadbeef",
			"short_id":"deadbee",
			"title":"Fix bug",
			"author_name":"Alice",
			"author_email":"alice@example.com"
		}]`)
	})

	result, err := callTool(t, baseURL, "get_mr_commits",
		json.RawMessage(`{"url":"https://gitlab.example.com/group/project/-/merge_requests/7"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected *mcp.TextContent, got %T", result.Content[0])
	require.Contains(t, textContent.Text, "deadbeef")
}

// TestHandleGetMRDiscussions_APIError covers the path where GitLab
// returns a 5xx status; the handler must wrap the error.
func TestHandleGetMRDiscussions_APIError(t *testing.T) {
	t.Parallel()

	baseURL := mockGitLabServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, "boom")
	})

	result, err := callTool(t, baseURL, "get_mr_discussions",
		json.RawMessage(`{"url":"https://gitlab.example.com/group/project/-/merge_requests/42"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "list merge request discussions")
	require.Nil(t, result)
}

// TestHandleGetMRCommits_APIError covers the path where GitLab
// returns a 5xx status; the handler must wrap the error.
func TestHandleGetMRCommits_APIError(t *testing.T) {
	t.Parallel()

	baseURL := mockGitLabServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, "boom")
	})

	result, err := callTool(t, baseURL, "get_mr_commits",
		json.RawMessage(`{"url":"https://gitlab.example.com/group/project/-/merge_requests/7"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "get merge request commits")
	require.Nil(t, result)
}

// TestHandleGetMRDiscussions_InvalidArgs covers the JSON-parse
// error path of makeMRHandler.
func TestHandleGetMRDiscussions_InvalidArgs(t *testing.T) {
	t.Parallel()

	baseURL := mockGitLabServer(t, func(_ http.ResponseWriter, _ *http.Request) {})

	result, err := callTool(t, baseURL, "get_mr_discussions",
		json.RawMessage(`{not valid json`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse get_mr_discussions args")
	require.Nil(t, result)
}

// TestHandleGetMRCommits_InvalidArgs covers the JSON-parse error path
// of makeMRHandler.
func TestHandleGetMRCommits_InvalidArgs(t *testing.T) {
	t.Parallel()

	baseURL := mockGitLabServer(t, func(_ http.ResponseWriter, _ *http.Request) {})

	result, err := callTool(t, baseURL, "get_mr_commits",
		json.RawMessage(`{not valid json`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse get_mr_commits args")
	require.Nil(t, result)
}

// TestHandleGetMRDiscussions_EmptyURL covers the parseAndValidateURL
// error path (empty URL) inside makeMRHandler.
func TestHandleGetMRDiscussions_EmptyURL(t *testing.T) {
	t.Parallel()

	result, err := callTool(t, mockGitLabServer(t, http.HandlerFunc(nil)), "get_mr_discussions",
		json.RawMessage(`{"url":""}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "merge request URL is required")
	require.Nil(t, result)
}

// TestHandleGetMRDiscussions_InvalidURL covers the parseMRURL error
// path (URL doesn't match the expected pattern) inside makeMRHandler.
func TestHandleGetMRDiscussions_InvalidURL(t *testing.T) {
	t.Parallel()

	result, err := callTool(t, mockGitLabServer(t, http.HandlerFunc(nil)), "get_mr_discussions",
		json.RawMessage(`{"url":"https://gitlab.example.com/group/project"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to parse merge request URL")
	require.Nil(t, result)
}

// TestParseAndValidateURL_Valid covers the happy path of
// parseAndValidateURL.
func TestParseAndValidateURL_Valid(t *testing.T) {
	t.Parallel()

	parts, err := parseAndValidateURL(
		"https://gitlab.example.com/group/project/-/merge_requests/42",
	)
	require.NoError(t, err)
	require.Equal(t, "group/project", parts.ProjectPath)
	require.Equal(t, int64(42), parts.MRIID)
}

// TestParseAndValidateURL_InvalidURL covers the parse error path of
// parseAndValidateURL.
func TestParseAndValidateURL_InvalidURL(t *testing.T) {
	t.Parallel()

	_, err := parseAndValidateURL("https://gitlab.example.com/group/project")
	require.Error(t, err)
}

// TestParseMRURL_InvalidIID covers the path where the URL has the
// merge_requests segment but no numeric IID after it (the regex
// fails to match, returning the parse-error sentinel).
func TestParseMRURL_InvalidIID(t *testing.T) {
	t.Parallel()

	_, err := parseMRURL("https://gitlab.example.com/group/project/-/merge_requests/notanumber")
	require.Error(t, err)
	require.ErrorIs(t, err, errMRURLParse)
}

// TestConnect_InvalidTokenError covers the wrap-error path when the
// token is empty.
func TestConnect_InvalidTokenError(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{"base_url": "https://gitlab.example.com"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "gitlab: validate:")
	require.ErrorIs(t, err, errTokenEmpty)
}

// TestConnect_InvalidBaseURL covers the wrap-error path when the
// base_url doesn't parse.
func TestConnect_InvalidBaseURL(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"token":    "test-token",
		"base_url": "not a url with ://",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "gitlab: validate:")
}

// TestConnect_BaseURLNoScheme covers the parse error path with a
// base URL that has no scheme.
func TestConnect_BaseURLNoScheme(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"token":    "test-token",
		"base_url": "gitlab.example.com",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, errBaseURLParse)
}

// silenceUnused keeps the gitlab import referenced for downstream
// additions without affecting the test surface.
var _ = gitlab.NewClient
