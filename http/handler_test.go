// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package http

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// TestHandler_GET_JSON covers the full handleRequest path for a
// successful GET. The httptest server returns a JSON response;
// the handler is invoked directly with a CallToolRequest. The
// test verifies Content (TextContent carrying the body) AND
// StructuredContent (a JSON object, since the body is a JSON
// object).
func TestHandler_GET_JSON(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotPath   string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, `{"id":42,"name":"sprocket"}`)
	}))
	t.Cleanup(srv.Close)

	handler := handleRequest(config{
		URL:         srv.URL + "/api/widgets",
		Method:      "GET",
		Description: "List widgets",
		Headers:     map[string]string(nil),
	})

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "http",
			Arguments: json.RawMessage(`{}`),
		},
	}

	result, err := handler(t.Context(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.Equal(t, http.MethodGet, gotMethod)
	require.Equal(t, "/api/widgets", gotPath)

	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected TextContent, got %T", result.Content[0])
	require.JSONEq(t, `{"id":42,"name":"sprocket"}`, textContent.Text)

	require.NotNil(t, result.StructuredContent)
}

// TestHandler_GET_NonJSON covers the case where the upstream
// returns a non-JSON body (HTML, plain text). StructuredContent
// must be nil (the spec requires a JSON object) but Content
// still carries the raw body.
func TestHandler_GET_NonJSON(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, "<html>hi</html>")
	}))
	t.Cleanup(srv.Close)

	handler := handleRequest(config{
		URL:         srv.URL,
		Method:      "GET",
		Description: "Raw page",
		Headers:     map[string]string(nil),
	})

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "http",
			Arguments: json.RawMessage(`{}`),
		},
	}

	result, err := handler(t.Context(), req)
	require.NoError(t, err)
	require.NotNil(t, result)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, "<html>hi</html>", textContent.Text)
	require.Nilf(t, result.StructuredContent, "non-JSON body should not produce StructuredContent")
}

// TestHandler_POST_JSONBody covers the POST path with a JSON body
// and a JSON response. Exercises prepareRequestBody (application/
// json with non-nil Body) and the full body marshaling.
func TestHandler_POST_JSONBody(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotBody   string
		readErr   error
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method

		var body []byte

		body, readErr = io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, `{"id":1,"name":"sprocket"}`)
	}))
	t.Cleanup(srv.Close)

	handler := handleRequest(config{
		URL:         srv.URL + "/api/widgets",
		Method:      "POST",
		Description: "Create widget",
		Headers:     map[string]string(nil),
	})

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "http",
			Arguments: json.RawMessage(`{"body":{"name":"sprocket"}}`),
		},
	}

	result, err := handler(t.Context(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NoError(t, readErr)
	require.Equal(t, http.MethodPost, gotMethod)
	require.JSONEq(t, `{"name":"sprocket"}`, gotBody)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, textContent.Text, `"id":1`)
}

// TestHandler_CustomHeaders verifies that headers from the
// HTTPToolRequest are forwarded to the upstream call. The http
// tool does not set a default User-Agent, so we only assert on
// the explicit Authorization header.
func TestHandler_CustomHeaders(t *testing.T) {
	t.Parallel()

	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	handler := handleRequest(config{
		URL:         srv.URL,
		Method:      "GET",
		Description: "Auth test",
		Headers: map[string]string{
			"Authorization": "Bearer secret-token",
		},
	})

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "http",
			Arguments: json.RawMessage(`{}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, "Bearer secret-token", gotAuth)
}

// TestHandler_5xxStatus covers the error path: an upstream 5xx
// must surface as a Go error (not a successful CallToolResult).
func TestHandler_5xxStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, "boom")
	}))
	t.Cleanup(srv.Close)

	handler := handleRequest(config{
		URL:         srv.URL,
		Method:      "GET",
		Description: "Failure path",
		Headers:     map[string]string(nil),
	})

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "http",
			Arguments: json.RawMessage(`{}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

// TestHandler_FormBody covers the Form-body path: when args.form
// is set, prepareRequestBody uses application/x-www-form-urlencoded.
func TestHandler_FormBody(t *testing.T) {
	t.Parallel()

	var (
		gotContentType string
		gotBody        string
		readErr        error
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")

		var body []byte

		body, readErr = io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	handler := handleRequest(config{
		URL:         srv.URL,
		Method:      "POST",
		Description: "Form post",
		Headers:     map[string]string(nil),
	})

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "http",
			Arguments: json.RawMessage(`{"form":{"key1":"value1","key2":"value2"}}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.NoError(t, err)
	require.NoError(t, readErr)
	require.Equal(t, "application/x-www-form-urlencoded", gotContentType)
	require.Equal(t, "key1=value1&key2=value2", gotBody)
}

// TestHandler_QueryString verifies that args.query is appended to
// the URL. Go's net/url sorts query keys alphabetically on encode,
// so we parse the resulting query and check the individual values
// rather than the literal RawQuery string.
func TestHandler_QueryString(t *testing.T) {
	t.Parallel()

	var gotQuery url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	handler := handleRequest(config{
		URL:         srv.URL,
		Method:      "GET",
		Description: "Query",
		Headers:     map[string]string(nil),
	})

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "http",
			Arguments: json.RawMessage(`{"query":{"q":"sprocket","page":"2"}}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, "sprocket", gotQuery.Get("q"))
	require.Equal(t, "2", gotQuery.Get("page"))
}

// TestPrepareRequestBody exercises the small pure helper that
// picks Content-Type based on which fields the request set.
// Covers Body (JSON), Form (urlencoded), neither (NoBody), and
// the Form-precedence case (when both Body and Form are set,
// Form wins — the function does not error).
func TestPrepareRequestBody(t *testing.T) {
	t.Parallel()

	t.Run("body only → JSON", func(t *testing.T) {
		t.Parallel()

		got, err := prepareRequestBody("POST", HTTPToolRequest{
			Body:    json.RawMessage(`{"a":1}`),
			Query:   map[string]string(nil),
			Headers: map[string]string(nil),
			Form:    map[string]string(nil),
		})
		require.NoError(t, err)
		require.Equal(t, "application/json", got.ContentType)

		body, err := io.ReadAll(got.Reader)
		require.NoError(t, err)
		require.JSONEq(t, `{"a":1}`, string(body))
	})

	t.Run("form only → urlencoded", func(t *testing.T) {
		t.Parallel()

		got, err := prepareRequestBody("POST", HTTPToolRequest{
			Body:    any(nil),
			Query:   map[string]string(nil),
			Headers: map[string]string(nil),
			Form:    map[string]string{"a": "1"},
		})
		require.NoError(t, err)
		require.Equal(t, "application/x-www-form-urlencoded", got.ContentType)

		body, err := io.ReadAll(got.Reader)
		require.NoError(t, err)
		require.Equal(t, "a=1", string(body))
	})

	t.Run("no body → NoBody", func(t *testing.T) {
		t.Parallel()

		got, err := prepareRequestBody("GET", HTTPToolRequest{
			Body:    any(nil),
			Query:   map[string]string(nil),
			Headers: map[string]string(nil),
			Form:    map[string]string(nil),
		})
		require.NoError(t, err)
		require.Empty(t, got.ContentType)

		body, err := io.ReadAll(got.Reader)
		require.NoError(t, err)
		require.Empty(t, body)
	})

	t.Run("body and form → form wins (precedence)", func(t *testing.T) {
		t.Parallel()

		got, err := prepareRequestBody("POST", HTTPToolRequest{
			Body:    json.RawMessage(`{"a":1}`),
			Query:   map[string]string(nil),
			Headers: map[string]string(nil),
			Form:    map[string]string{"b": "2"},
		})
		require.NoError(t, err)
		require.Equal(t, "application/x-www-form-urlencoded", got.ContentType)

		body, err := io.ReadAll(got.Reader)
		require.NoError(t, err)
		require.Equal(t, "b=2", string(body))
	})
}

// TestBuildURLWithQuery exercises the URL builder that merges the
// upstream URL with the per-call query map. The existing tests do
// not reach it directly; this is a unit test for the helper.
func TestBuildURLWithQuery(t *testing.T) {
	t.Parallel()

	t.Run("no query → unchanged", func(t *testing.T) {
		t.Parallel()

		got, err := buildURLWithQuery("https://api.example.com/foo", map[string]string(nil))
		require.NoError(t, err)
		require.Equal(t, "https://api.example.com/foo", got)
	})

	t.Run("query added", func(t *testing.T) {
		t.Parallel()

		got, err := buildURLWithQuery("https://api.example.com/foo", map[string]string{"q": "bar"})
		require.NoError(t, err)
		require.Containsf(t, got, "q=bar", "URL %q should contain q=bar", got)
	})

	t.Run("invalid URL → error", func(t *testing.T) {
		t.Parallel()

		_, err := buildURLWithQuery("://not a url", map[string]string{"q": "bar"})
		require.Error(t, err)
	})
}

// TestHandler_InvalidArgs covers the error path where the caller's
// Arguments can't be unmarshaled into HTTPToolRequest.
func TestHandler_InvalidArgs(t *testing.T) {
	t.Parallel()

	handler := handleRequest(config{
		URL:         "https://api.example.com",
		Method:      "GET",
		Description: "Bad args",
		Headers:     map[string]string(nil),
	})

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "http",
			Arguments: json.RawMessage(`not-json`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse http args")
}
