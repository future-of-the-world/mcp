// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package github

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/tool"
)

// ---------------------------------------------------------------------------
// decodeConnect
// ---------------------------------------------------------------------------

// TestDecodeConnect_MissingToken verifies that the connect map's required
// token field is enforced. The map may be empty or carry unrelated keys;
// both cases must surface the same "token is empty" error from
// decodeConnect.
func TestDecodeConnect_MissingToken(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		connect map[string]any
	}{
		{"empty map", make(map[string]any)},
		{"nil token", map[string]any{"token": any(nil)}},
		{"unrelated key", map[string]any{"host": "github.com"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := decodeConnect(testCase.connect)
			require.Error(t, err)
			require.ErrorIs(t, err, errTokenEmpty)
		})
	}
}

// TestDecodeConnect_RejectsNonStringToken verifies that a token value of
// a non-string type (here a bool) is rejected with a typed error rather
// than silently stringified.
func TestDecodeConnect_RejectsNonStringToken(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{"token": true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "connect.token")
}

// TestDecodeConnect_AllFields covers the happy path: every supported
// connect-map key is decoded into the right config field.
func TestDecodeConnect_AllFields(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"token":     "ghp_test",
		"host":      "github.example.com",
		"toolsets":  []any{"repos", "issues"},
		"read_only": true,
	})
	require.NoError(t, err)
	require.Equal(t, "ghp_test", cfg.Token)
	require.Equal(t, "github.example.com", cfg.Host)
	require.Equal(t, []string{"repos", "issues"}, cfg.Toolsets)
	require.True(t, cfg.ReadOnly)
}

// TestDecodeConnect_StringSliceAsString covers the ergonomics path: a
// single string in the toolsets slot is accepted as a one-element slice,
// matching comma-separated env-var-style usage.
func TestDecodeConnect_StringSliceAsString(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"token":    "ghp_test",
		"toolsets": "repos",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"repos"}, cfg.Toolsets)
}

// TestDecodeConnect_StringSliceWrongType covers the bad-element case:
// the slot exists but its element type is wrong.
func TestDecodeConnect_StringSliceWrongType(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"token":    "ghp_test",
		"toolsets": []any{"repos", 42},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "connect.toolsets[1]")
}

// TestDecodeConnect_RejectsBoolWrongType covers the bad-bool case: the
// read_only slot exists but its value is a string.
func TestDecodeConnect_RejectsBoolWrongType(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"token":     "ghp_test",
		"read_only": "yes",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "connect.read_only")
}

// ---------------------------------------------------------------------------
// validate
// ---------------------------------------------------------------------------

// TestValidate_EmptyToken is a focused unit test for the validate step.
func TestValidate_EmptyToken(t *testing.T) {
	t.Parallel()

	err := config{}.validate()
	require.Error(t, err)
	require.ErrorIs(t, err, errTokenEmpty)
}

// TestValidate_NonEmptyToken confirms that any non-empty token passes
// validation; we don't enforce shape here (the upstream's token-shape
// validation is its own concern).
func TestValidate_NonEmptyToken(t *testing.T) {
	t.Parallel()

	full := config{
		Token:    "anything",
		Host:     "github.com",
		Toolsets: []string(nil),
		ReadOnly: false,
	}
	err := full.validate()
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// decodeStringSlice
// ---------------------------------------------------------------------------

// TestDecodeStringSlice_Nil confirms that a nil input yields a nil slice
// (matching the upstream's "use defaults" convention for toolsets).
func TestDecodeStringSlice_Nil(t *testing.T) {
	t.Parallel()

	out, err := decodeToolsetsSlice(any(nil))
	require.NoError(t, err)
	require.Nil(t, out)
}

// TestDecodeStringSlice_String confirms the string-ergonomics path.
func TestDecodeStringSlice_String(t *testing.T) {
	t.Parallel()

	out, err := decodeToolsetsSlice("repos")
	require.NoError(t, err)
	require.Equal(t, []string{"repos"}, out)
}

// TestDecodeStringSlice_WrongElementType covers an element that isn't a
// string.
func TestDecodeStringSlice_WrongElementType(t *testing.T) {
	t.Parallel()

	_, err := decodeToolsetsSlice([]any{"repos", true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected string")
}

// TestDecodeStringSlice_UnsupportedShape covers a top-level shape that
// isn't nil, []any, or string.
func TestDecodeStringSlice_UnsupportedShape(t *testing.T) {
	t.Parallel()

	_, err := decodeToolsetsSlice(42)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected string or []string")
}

// ---------------------------------------------------------------------------
// Connect (integration: builds upstream, bridges over in-memory transport,
// lists tools)
// ---------------------------------------------------------------------------

// TestConnect_RequiresToken exercises Connect's first failure mode: a
// missing or empty token short-circuits before the upstream is built.
func TestConnect_RequiresToken(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), make(map[string]any))
	require.Error(t, err)
	require.Contains(t, err.Error(), "token is empty")
}

// TestConnect_RejectsBadTokenType covers the typed-error path through
// Connect. Non-string tokens must surface as "github: decode: ...".
func TestConnect_RejectsBadTokenType(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{"token": 42})
	require.Error(t, err)
	require.Contains(t, err.Error(), "github: decode:")
}

// TestConnect_HappyPath exercises the full path: build the upstream
// server, bridge it over an in-memory transport, list tools, and return
// them. The upstream must be reachable through the bridge — no network
// is touched, but the in-memory client session must successfully talk
// to the in-memory server.
//
// The token is a dummy value; the upstream does not dial GitHub during
// registration or tool listing, only when a tool is actually called.
func TestConnect_HappyPath(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"token": "ghp_dummy",
	})
	require.NoError(t, err)
	require.NotEmptyf(t, resp.Tools, "expected upstream to expose at least one tool")

	// A handful of well-known GitHub MCP tools must be present. We don't
	// pin the full list (the upstream may add tools in future releases);
	// we just check that a few representative ones are there.
	wantTools := []string{
		"get_me",
		"get_file_contents",
		"search_repositories",
		"search_issues",
	}

	names := make([]string, 0, len(resp.Tools))
	for _, entry := range resp.Tools {
		names = append(names, entry.Name)
	}

	for _, want := range wantTools {
		assert.Containsf(t, names, want, "upstream should expose tool %q", want)
	}
}

// TestConnect_ToolAnnotationsPreserved verifies that Annotations set by
// the upstream (ReadOnlyHint, etc.) flow through the bridge unchanged,
// so clients see the same hints they would see when running the upstream
// directly.
func TestConnect_ToolAnnotationsPreserved(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"token": "ghp_dummy",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Tools)

	// At least one of the well-known tools (get_me) should advertise
	// read-only semantics. The upstream's exact annotations may evolve;
	// we only assert that *some* tool has ReadOnlyHint=true.
	var readOnlyCount int

	for _, entry := range resp.Tools {
		if entry.Annotations != nil && entry.Annotations.ReadOnlyHint {
			readOnlyCount++
		}
	}

	assert.Positivef(t, readOnlyCount, "expected at least one tool with ReadOnlyHint=true")
}

// TestConnect_RejectsInvalidHost verifies that an unreachable / malformed
// host is reported as a build-upstream error rather than a panic.
func TestConnect_RejectsInvalidHost(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"token": "ghp_dummy",
		"host":  "://not a url",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "github:")
}

// TestConnect_ReadOnlyMode exercises the read_only path. The upstream
// must still build and expose tools, but those tools should not be the
// write tools. We don't pin the exact list here; we just verify the
// connect succeeds.
func TestConnect_ReadOnlyMode(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"token":     "ghp_dummy",
		"read_only": true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Tools)
}

// TestConnect_HostGitHubEnterprise covers the GitHub Enterprise path:
// the host is parsed into the right REST/GraphQL/Raw URLs.
func TestConnect_HostGitHubEnterprise(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"token": "ghp_dummy",
		"host":  "github.example.com",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Tools)
}

// TestConnect_ToolsHaveHandlers verifies that every returned tool has a
// non-nil handler. A nil handler would be a silent failure — the
// dispatcher would call AddTool(tool, nil) and the parent server would
// panic on the first request.
func TestConnect_ToolsHaveHandlers(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"token": "ghp_dummy",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Tools)

	for _, entry := range resp.Tools {
		require.NotNilf(t, entry.Handler, "tool %s must have a handler", entry.Name)
		require.NotNilf(t, entry.Tool, "tool %s must have a *mcp.Tool", entry.Name)
	}
}

// TestMakeCallHandler_NilSession confirms the nil-session guard
// surfaces a clean error rather than a nil-pointer panic.
func TestMakeCallHandler_NilSession(t *testing.T) {
	t.Parallel()

	handler := makeCallHandler((*mcp.ClientSession)(nil), "any_tool")
	require.NotNil(t, handler)

	_, err := handler(t.Context(), &mcp.CallToolRequest{})
	require.Error(t, err)
	require.ErrorIs(t, err, errSessionNotReady)
}

// TestUserAgentTransport_InjectsHeader covers the userAgentTransport's
// RoundTrip method: it must set the User-Agent header on the outgoing
// request without disturbing other headers.
func TestUserAgentTransport_InjectsHeader(t *testing.T) {
	t.Parallel()

	var capturedAgent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		capturedAgent = req.Header.Get("User-Agent")

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	transport := &userAgentTransport{
		base:  &http.Transport{},
		agent: "test-agent/1.0",
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		//nolint:errcheck // body close is best-effort in tests
		_ = resp.Body.Close()
	})

	require.Equal(t, "test-agent/1.0", capturedAgent)
}

// TestUserAgentTransport_PropagatesBaseError covers the error-wrapping
// path: when the underlying transport fails, the wrapper returns a
// wrapped error.
func TestUserAgentTransport_PropagatesBaseError(t *testing.T) {
	t.Parallel()

	// Point at a closed server to force a connection error.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()

	transport := &userAgentTransport{
		base:  &http.Transport{},
		agent: "test-agent/1.0",
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		//nolint:errcheck // body close is best-effort in tests
		_ = resp.Body.Close()
	}

	require.Error(t, err)
	require.Contains(t, err.Error(), "github REST round trip")
}

// TestBearerAuthTransport_InjectsHeader covers the bearerAuthTransport's
// RoundTrip method: it must set the Authorization header on the outgoing
// request.
func TestBearerAuthTransport_InjectsHeader(t *testing.T) {
	t.Parallel()

	var capturedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		capturedAuth = req.Header.Get("Authorization")

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	transport := &bearerAuthTransport{
		token: "ghp_test",
		base:  &http.Transport{},
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		//nolint:errcheck // body close is best-effort in tests
		_ = resp.Body.Close()
	})

	require.Equal(t, "Bearer ghp_test", capturedAuth)
}

// TestBearerAuthTransport_PropagatesBaseError covers the error-wrapping
// path for the GraphQL transport.
func TestBearerAuthTransport_PropagatesBaseError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()

	transport := &bearerAuthTransport{
		token: "ghp_test",
		base:  &http.Transport{},
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		//nolint:errcheck // body close is best-effort in tests
		_ = resp.Body.Close()
	}

	require.Error(t, err)
	require.Contains(t, err.Error(), "github GraphQL round trip")
}

// ---------------------------------------------------------------------------
// normalizeHost
// ---------------------------------------------------------------------------

// TestNormalizeHost_Empty covers the empty input case: an empty host is
// returned unchanged so the upstream's default (github.com) applies.
func TestNormalizeHost_Empty(t *testing.T) {
	t.Parallel()

	out, err := normalizeHost("")
	require.NoError(t, err)
	require.Empty(t, out)
}

// TestNormalizeHost_Whitespace covers the trim path: leading/trailing
// whitespace is stripped before the empty check.
func TestNormalizeHost_Whitespace(t *testing.T) {
	t.Parallel()

	out, err := normalizeHost("   ")
	require.NoError(t, err)
	require.Empty(t, out)
}

// TestNormalizeHost_BareHostname covers the most common case: a bare
// hostname gets https:// prepended.
func TestNormalizeHost_BareHostname(t *testing.T) {
	t.Parallel()

	out, err := normalizeHost("github.example.com")
	require.NoError(t, err)
	require.Equal(t, "https://github.example.com", out)
}

// TestNormalizeHost_AlreadyHTTPS confirms that a fully-qualified URL is
// returned unchanged.
func TestNormalizeHost_AlreadyHTTPS(t *testing.T) {
	t.Parallel()

	out, err := normalizeHost("https://github.example.com")
	require.NoError(t, err)
	require.Equal(t, "https://github.example.com", out)
}

// TestNormalizeHost_AlreadyHTTP confirms that an http:// URL is also
// returned unchanged (the upstream supports both schemes for
// on-prem GitHub Enterprise).
func TestNormalizeHost_AlreadyHTTP(t *testing.T) {
	t.Parallel()

	out, err := normalizeHost("http://github.example.com")
	require.NoError(t, err)
	require.Equal(t, "http://github.example.com", out)
}

// TestNormalizeHost_UnsupportedScheme covers the bad-scheme path: any
// scheme other than http or https is rejected.
func TestNormalizeHost_UnsupportedScheme(t *testing.T) {
	t.Parallel()

	_, err := normalizeHost("ftp://github.example.com")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported scheme")
}

// TestBuildUserAgent covers the User-Agent string builder. The expected
// shape embeds both the upstream and the local server version.
func TestBuildUserAgent(t *testing.T) {
	t.Parallel()

	userAgent := buildUserAgent()
	require.Contains(t, userAgent, "github-mcp-server/")
	require.Contains(t, userAgent, "(mcp/")
	require.Contains(t, userAgent, mcpServerVersion)
}

// TestListRemoteTools is intentionally not exercised with a nil
// session: the upstream SDK's range loop on a nil *ClientSession hangs
// rather than panicking, so a "recover" would never fire and the test
// would time out. Connect guards against nil sessions before calling
// listRemoteTools (see the errSessionNotReady check in makeCallHandler),
// so the helper's nil-safety is implicit.

// Compile-time assertion that tool.Tool is the shape the test relies on.
var _ tool.Tool
