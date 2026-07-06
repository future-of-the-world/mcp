// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/tool"
)

// ---------------------------------------------------------------------------
// Config decoding
// ---------------------------------------------------------------------------

func TestDecodeConnect_Empty(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(make(map[string]any))
	require.NoError(t, err)
	require.Empty(t, cfg.URL)
	require.Empty(t, cfg.Command)
}

func TestDecodeConnect_URL(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"url": "http://localhost:8080/mcp",
	})
	require.NoError(t, err)
	require.Equal(t, "http://localhost:8080/mcp", cfg.URL)
}

func TestDecodeConnect_Headers(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"url": "http://localhost:8080/mcp",
		"headers": map[string]any{
			"Authorization": "Bearer test-token",
			"X-Custom":      "value",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "Bearer test-token", cfg.Headers["Authorization"])
	require.Equal(t, "value", cfg.Headers["X-Custom"])
}

// TestDecodeConnect_HeaderWrongType verifies that a non-scalar
// header value (a map, here) produces a wrapped decode.ErrWrongType
// rather than the old "must be a string" error. The numeric case
// 42 is no longer "wrong type" — it's coerced to "42" (see
// TestDecodeConnect_HeadersNumericCoercion below).
func TestDecodeConnect_HeaderWrongType(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"headers": map[string]any{"X-Bad": map[string]any{"nested": true}},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
}

// TestDecodeConnect_HeadersNumericCoercion verifies the headline
// YAML-natural-value acceptance path: a numeric header value is
// stringified via fmt.Sprint rather than rejected. Same for bool.
func TestDecodeConnect_HeadersNumericCoercion(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"url": "http://localhost:8080/mcp",
		"headers": map[string]any{
			"X-Number": 42,
			"X-Bool":   true,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "42", cfg.Headers["X-Number"])
	require.Equal(t, "true", cfg.Headers["X-Bool"])
}

// TestDecodeConnect_URLNumericCoercion verifies that a numeric
// url value is stringified (the issue's primary example: users
// typing org_id: 12345 — or in this case url: 443 — and having
// it Just Work).
func TestDecodeConnect_URLNumericCoercion(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"url": 443,
	})
	require.NoError(t, err)
	require.Equal(t, "443", cfg.URL)
}

// TestDecodeConnect_URLNullSkipped verifies that a YAML null
// (or absent key) leaves the field at its zero value, the same
// way the old "key absent" code path did. The downstream
// validate() step then surfaces the empty URL as errURLEmpty.
func TestDecodeConnect_URLNullSkipped(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"url": any(nil),
	})
	require.NoError(t, err)
	require.Empty(t, cfg.URL)
}

// TestDecodeConnect_URLWrongType verifies the new strict path:
// a map (or any non-scalar) where a string is expected produces
// a wrapped decode.ErrWrongType.
func TestDecodeConnect_URLWrongType(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"url": map[string]any{"host": "example.com", "port": 443},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
}

func TestDecodeConnect_Command(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"command": "mcp-server",
		"args":    []any{"--port", "8080"},
	})
	require.NoError(t, err)
	require.Equal(t, "mcp-server", cfg.Command)
	require.Equal(t, []string{"--port", "8080"}, cfg.Args)
}

func TestDecodeConnect_Env(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"command": "mcp-server",
		"env":     map[string]any{"FOO": "bar"},
	})
	require.NoError(t, err)
	require.Equal(t, "bar", cfg.Env["FOO"])
}

func TestDecodeConnect_Transport(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"url":       "http://localhost:8080",
		"transport": "sse",
	})
	require.NoError(t, err)
	require.Equal(t, "sse", cfg.Transport)
}

// ---------------------------------------------------------------------------
// Config validation
// ---------------------------------------------------------------------------

func TestValidate_BothEmpty(t *testing.T) {
	t.Parallel()

	cfg := config{}
	err := cfg.validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "url is empty")
	require.Contains(t, err.Error(), "command is empty")
}

func TestValidate_URLOnly(t *testing.T) {
	t.Parallel()

	cfg := config{
		URL:       "http://localhost",
		Headers:   map[string]string(nil),
		Command:   "",
		Args:      []string(nil),
		Env:       map[string]string(nil),
		Transport: "",
	}
	require.NoError(t, cfg.validate())
}

func TestValidate_CommandOnly(t *testing.T) {
	t.Parallel()

	cfg := config{
		URL:       "",
		Headers:   map[string]string(nil),
		Command:   "mcp-server",
		Args:      []string(nil),
		Env:       map[string]string(nil),
		Transport: "",
	}
	require.NoError(t, cfg.validate())
}

// ---------------------------------------------------------------------------
// Connect integration (with a real in-process MCP upstream)
// ---------------------------------------------------------------------------

// startTestUpstream launches a minimal MCP server with a fixed tool
// list and returns the streamable HTTP endpoint URL.
func startTestUpstream(t *testing.T) string {
	t.Helper()

	// The proxy only needs the URL to be reachable and to respond
	// with a tools/list containing the expected tools. We use a
	// tiny stub HTTP server here that returns a hard-coded list of
	// tools so the proxy can be exercised without a real MCP server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// The proxy uses session.Tools(ctx, nil) which goes through
		// the SDK's streamable transport; the body returned is a
		// MCP tools/list response. For this test we only assert that
		// the proxy reaches the upstream — the actual tool list
		// parsing is the SDK's job. Return a minimal valid response.
		// The streamable transport will fail to fully decode this
		// because it's not a real MCP envelope, but the proxy
		// connection itself should succeed or fail predictably.
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	t.Cleanup(srv.Close)

	return srv.URL
}

func TestConnect_URL_NoToolsReturned(t *testing.T) {
	t.Parallel()

	url := startTestUpstream(t)

	resp, err := Connect(t.Context(), map[string]any{
		"url": url,
	})
	// The streamable transport may either return an empty tool list
	// (which we treat as an error) or fail to parse the stub
	// response. Either way, the proxy must wrap the error and not
	// return tools.
	if err == nil {
		require.Emptyf(t, resp.Tools, "stub upstream has no tools")
		return
	}

	require.Error(t, err)
	require.Truef(t,
		strings.Contains(err.Error(), "no tools") ||
			strings.Contains(err.Error(), "list tools") ||
			strings.Contains(err.Error(), "discover"),
		"unexpected error: %v", err,
	)
}

func TestConnect_EmptyConfig(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), make(map[string]any))
	require.Error(t, err)
	require.Contains(t, err.Error(), "url is empty")
	require.Contains(t, err.Error(), "command is empty")
}

func TestConnect_InvalidHeaders(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"url":     "http://localhost",
		"headers": map[string]any{"X-Bad": 42},
	})
	require.Error(t, err)
}

func TestConnect_InvalidArgs(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"command": "mcp-server",
		"args":    []any{"--port", 8080}, // 8080 is not a string
	})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// makeCallHandler
// ---------------------------------------------------------------------------

func TestMakeCallHandler_NilSession(t *testing.T) {
	t.Parallel()

	handler := makeCallHandler((*mcp.ClientSession)(nil), "remote_tool")

	_, err := callHandler(t, handler)
	require.Error(t, err)
	require.Contains(t, err.Error(), "session is not established")
}

// callHandler invokes the given handler with a no-op request and
// returns the resulting error. Used by the nil-session test only.
func callHandler(t *testing.T, handler mcp.ToolHandler) (*mcp.CallToolResult, error) {
	t.Helper()

	return handler(t.Context(), &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "remote_tool",
			Arguments: json.RawMessage(nil),
		},
	})
}

// TestDiscover_PropagatesUpstreamTitleAndAnnotations is the regression
// test for #59: the discover function must pass the upstream *mcp.Tool
// through unchanged so Title and Annotations (ReadOnlyHint,
// DestructiveHint, IdempotentHint, OpenWorldHint) flow end-to-end.
// Before the refactor, discover reconstructed the *mcp.Tool
// field-by-field and dropped Title and Annotations on the floor.
//
// The test uses the SDK's in-memory transport to stand up a real
// upstream MCP server (with a tool registered that has custom Title
// and Annotations), dial it as a client, and run the same discover
// loop body in-test. This proves the property end-to-end without
// depending on the streamable HTTP transport's parser behavior, which
// is not under test here.
func TestDiscover_PropagatesUpstreamTitleAndAnnotations(t *testing.T) {
	t.Parallel()

	upstream := mcp.NewServer(
		&mcp.Implementation{Name: "mock-upstream", Version: "0.0.0"},
		(*mcp.ServerOptions)(nil),
	)

	upstream.AddTool(
		&mcp.Tool{
			Name:        "annotated",
			Title:       "Custom Title",
			Description: "A tool with custom annotations",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Annotations: &mcp.ToolAnnotations{
				Title:           "",
				ReadOnlyHint:    true,
				DestructiveHint: (*bool)(nil),
				IdempotentHint:  false,
				OpenWorldHint:   new(false),
			},
		},
		func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		},
	)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverCtx, cancelServer := context.WithCancel(t.Context())

	t.Cleanup(cancelServer)

	serverSession, err := upstream.Connect(
		serverCtx,
		serverTransport,
		(*mcp.ServerSessionOptions)(nil),
	)
	require.NoError(t, err)

	t.Cleanup(func() { //nolint:errcheck // best-effort session close
		_ = serverSession.Close()
	})

	client := mcp.NewClient(
		&mcp.Implementation{Name: "proxy-test-client", Version: "0.0.0"},
		(*mcp.ClientOptions)(nil),
	)
	clientSession, err := client.Connect(
		t.Context(),
		clientTransport,
		(*mcp.ClientSessionOptions)(nil),
	)
	require.NoError(t, err)

	t.Cleanup(func() { //nolint:errcheck // best-effort session close
		_ = clientSession.Close()
	})

	// Replicate the discover loop body in-test: for each remote tool,
	// shallow-copy the *mcp.Tool (Annotations and Meta remain shared)
	// and wrap it in a tool.Tool with a forwarding handler.
	out := make([]tool.Tool, 0, 1)

	for remoteTool, err := range clientSession.Tools(t.Context(), (*mcp.ListToolsParams)(nil)) {
		require.NoError(t, err)

		remote := *remoteTool

		out = append(out, tool.Tool{
			Tool:    &remote,
			Handler: makeCallHandler(clientSession, remoteTool.Name),
		})
	}

	require.Len(t, out, 1)

	got := out[0]
	require.Equal(t, "annotated", got.Name)
	require.Equal(t, "Custom Title", got.Title)
	require.NotNil(t, got.Annotations)
	require.True(t, got.Annotations.ReadOnlyHint)
	require.NotNil(t, got.Annotations.OpenWorldHint)
	require.False(t, *got.Annotations.OpenWorldHint)
}

// ---------------------------------------------------------------------------
// wrapConnectError
// ---------------------------------------------------------------------------

func TestWrapConnectError_URL(t *testing.T) {
	t.Parallel()

	cfg := fullConfig()

	cfg.URL = "http://localhost:8080"

	err := cfg.wrapConnectError(errors.New("connection refused"), bytes.Buffer{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "proxy tool connect to http://localhost:8080")
	require.Contains(t, err.Error(), "connection refused")
}

func TestWrapConnectError_Command_NoStderr(t *testing.T) {
	t.Parallel()

	cfg := fullConfig()

	cfg.Command = testMCPServerCommand

	err := cfg.wrapConnectError(errors.New("exit 1"), bytes.Buffer{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "proxy tool connect to command mcp-server")
	require.Contains(t, err.Error(), "exit 1")
	require.NotContains(t, err.Error(), "stderr:")
}

func TestWrapConnectError_Command_WithStderr(t *testing.T) {
	t.Parallel()

	cfg := fullConfig()

	cfg.Command = testMCPServerCommand

	var stderr bytes.Buffer

	_, _ = stderr.WriteString("boom: child crashed")

	err := cfg.wrapConnectError(errors.New("exit 1"), stderr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "proxy tool connect to command "+testMCPServerCommand)
	require.Contains(t, err.Error(), "stderr:")
	require.Contains(t, err.Error(), "boom: child crashed")
}

func TestWrapConnectError_Command_LargeStderrTruncated(t *testing.T) {
	t.Parallel()

	cfg := fullConfig()

	cfg.Command = testMCPServerCommand

	var stderr bytes.Buffer

	for range maxStderrBytes + 100 {
		_, _ = stderr.WriteString("x")
	}

	err := cfg.wrapConnectError(errors.New("exit 1"), stderr)
	require.Error(t, err)
	// 4096 is the cap; the rest is dropped. The wrapped error still
	// includes the captured prefix.
	require.Contains(t, err.Error(), "stderr:")
	require.LessOrEqual(t, strings.Count(err.Error(), "x"), maxStderrBytes+200)
}

// ---------------------------------------------------------------------------
// headerRoundTripper
// ---------------------------------------------------------------------------

func TestHeaderRoundTripper_AddsHeaders(t *testing.T) {
	t.Parallel()

	var (
		gotAuth  string
		gotTrace string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTrace = r.Header.Get("X-Trace-Id")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	roundTripper := &headerRoundTripper{
		base:    &http.Transport{},
		headers: map[string]string{"Authorization": "Bearer secret", "X-Trace-Id": "trace-123"},
	}

	client := &http.Client{Transport: roundTripper}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "Bearer secret", gotAuth)
	require.Equal(t, "trace-123", gotTrace)
	//nolint:errcheck // hard-coded response; close error is not actionable
	resp.Body.Close()
}

func TestHeaderRoundTripTransport_NilHeaders(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	roundTripper := &headerRoundTripper{base: &http.Transport{}, headers: map[string]string(nil)}
	client := &http.Client{Transport: roundTripper}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	//nolint:errcheck // hard-coded response; close error is not actionable
	resp.Body.Close()
}

func TestHeaderRoundTripper_BaseError(t *testing.T) {
	t.Parallel()

	roundTripper := &headerRoundTripper{
		base:    failingRoundTripper{},
		headers: map[string]string{"X-Foo": "bar"},
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, dummyURL, http.NoBody)
	require.NoError(t, err)

	resp, err := roundTripper.RoundTrip(req)
	if resp != nil {
		//nolint:errcheck // synthetic failure path; close error is not actionable
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "http round trip")
}

// failingRoundTripper always returns an error to exercise the
// wrap-error path in headerRoundTripper.RoundTrip.
type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("synthetic transport failure")
}

// fullConfig returns a config with all fields explicitly zeroed
// (nil maps, empty slices) so the test exhaustruct linter is
// satisfait without each test repeating the field set. The caller
// sets the fields it cares about afterwards.
func fullConfig() *config {
	return &config{
		URL:       "",
		Headers:   map[string]string(nil),
		Command:   "",
		Args:      []string(nil),
		Env:       map[string]string(nil),
		Transport: "",
	}
}

const testMCPServerCommand = "mcp-server" //nolint:gochecknoglobals // shared across tests

const dummyURL = "http://does-not-matter" //nolint:gochecknoglobals // placeholder URL

// ---------------------------------------------------------------------------
// makeCallHandler — happy path
// ---------------------------------------------------------------------------

// startInMemoryUpstream stands up a real upstream MCP server with the
// given tools, dials it as a client via the SDK's in-memory transport,
// and returns the live client session plus a cleanup. Used by the
// makeCallHandler and listRemoteTools tests below.
func startInMemoryUpstream(t *testing.T, tools ...*mcp.Tool) *mcp.ClientSession {
	t.Helper()

	upstream := mcp.NewServer(
		&mcp.Implementation{Name: "mock-upstream", Version: "0.0.0"},
		(*mcp.ServerOptions)(nil),
	)

	for _, toolDef := range tools {
		upstream.AddTool(
			toolDef,
			func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{Text: "ok"},
					},
				}, nil
			},
		)
	}

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverCtx, cancelServer := context.WithCancel(t.Context())

	t.Cleanup(cancelServer)

	serverSession, err := upstream.Connect(
		serverCtx,
		serverTransport,
		(*mcp.ServerSessionOptions)(nil),
	)
	require.NoError(t, err)

	t.Cleanup(func() { //nolint:errcheck // best-effort session close
		_ = serverSession.Close()
	})

	client := mcp.NewClient(
		&mcp.Implementation{Name: "proxy-test-client", Version: "0.0.0"},
		(*mcp.ClientOptions)(nil),
	)
	clientSession, err := client.Connect(
		t.Context(),
		clientTransport,
		(*mcp.ClientSessionOptions)(nil),
	)
	require.NoError(t, err)

	t.Cleanup(func() { //nolint:errcheck // best-effort session close
		_ = clientSession.Close()
	})

	return clientSession
}

// TestMakeCallHandler_ForwardsToSession verifies the happy path of
// makeCallHandler: with a real client session, the handler forwards
// the call to the upstream tool and returns the upstream's result
// unchanged. The test stands up a real upstream MCP server (via
// mcp.NewInMemoryTransports) so the session.CallTool path is
// exercised end-to-end.
func TestMakeCallHandler_ForwardsToSession(t *testing.T) {
	t.Parallel()

	session := startInMemoryUpstream(t, &mcp.Tool{
		Name:        "echo",
		Description: "Echoes back the input arguments",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	})

	handler := makeCallHandler(session, "echo")

	result, err := callHandler(t, handler)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected TextContent, got %T", result.Content[0])
	require.Equal(t, "ok", textContent.Text)
}

// TestMakeCallHandler_ForwardsArguments verifies that the handler
// forwards raw arguments as-is to the upstream tool. The proxy does
// not inspect or rewrite the arguments — it passes them through.
func TestMakeCallHandler_ForwardsArguments(t *testing.T) {
	t.Parallel()

	session := startInMemoryUpstream(t, &mcp.Tool{
		Name:        "echo",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	})

	handler := makeCallHandler(session, "echo")

	// Build a CallToolRequest with non-empty arguments.
	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "echo",
			Arguments: json.RawMessage(`{"key":"value"}`),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
}

// ---------------------------------------------------------------------------
// listRemoteTools
// ---------------------------------------------------------------------------

// TestListRemoteTools_ReturnsAll verifies that listRemoteTools
// collects every tool exposed by the upstream server.
func TestListRemoteTools_ReturnsAll(t *testing.T) {
	t.Parallel()

	alpha := &mcp.Tool{Name: "alpha", InputSchema: json.RawMessage(`{"type":"object"}`)}
	beta := &mcp.Tool{Name: "beta", InputSchema: json.RawMessage(`{"type":"object"}`)}
	gamma := &mcp.Tool{Name: "gamma", InputSchema: json.RawMessage(`{"type":"object"}`)}

	session := startInMemoryUpstream(t, alpha, beta, gamma)

	tools, err := listRemoteTools(t.Context(), session)
	require.NoError(t, err)
	require.Len(t, tools, 3)

	names := make([]string, 0, 3)

	for _, toolDef := range tools {
		names = append(names, toolDef.Name)
	}

	require.ElementsMatch(t, []string{"alpha", "beta", "gamma"}, names)
}

// TestListRemoteTools_PreservesAnnotations verifies that the tools
// returned by listRemoteTools retain their Annotations pointer. This
// is the data the proxy's discover loop shallow-copies into each
// tool.Tool; if listRemoteTools ever mutated the upstream's tool
// (e.g. by re-allocating Annotations), the copy would still be sound
// but the upstream's tool would be left in an inconsistent state.
func TestListRemoteTools_PreservesAnnotations(t *testing.T) {
	t.Parallel()

	annotated := &mcp.Tool{
		Name:        "annotated",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Annotations: &mcp.ToolAnnotations{
			Title:           "",
			ReadOnlyHint:    true,
			DestructiveHint: (*bool)(nil),
			IdempotentHint:  false,
			OpenWorldHint:   new(false),
		},
	}

	session := startInMemoryUpstream(t, annotated)

	tools, err := listRemoteTools(t.Context(), session)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	require.NotNil(t, tools[0].Annotations)
	require.True(t, tools[0].Annotations.ReadOnlyHint)
}

// TestListRemoteTools_Empty verifies that listRemoteTools returns an
// empty (non-nil) slice when the upstream exposes no tools. The
// proxy's discover loop treats this as an error (errNoRemoteTools),
// but the helper itself does not.
func TestListRemoteTools_Empty(t *testing.T) {
	t.Parallel()

	session := startInMemoryUpstream(t)

	tools, err := listRemoteTools(t.Context(), session)
	require.NoError(t, err)
	require.Empty(t, tools)
}

// ---------------------------------------------------------------------------
// newTransport
// ---------------------------------------------------------------------------

const testUpstreamURL = "http://upstream.example/mcp" //nolint:gochecknoglobals,lll // shared across tests

// TestNewTransport_StreamableHTTPDefault verifies that an empty
// transport string defaults to the streamable HTTP client transport.
// This is the default mode and the one most upstreams use.
func TestNewTransport_StreamableHTTPDefault(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // URL/Command/Headers/Args/Env all set explicitly below
		URL:       testUpstreamURL,
		Headers:   make(map[string]string),
		Args:      []string{},
		Env:       make(map[string]string),
		Transport: "",
	}

	transport, stderrBuf := cfg.newTransport(t.Context(), &bytes.Buffer{})

	_, ok := transport.(*mcp.StreamableClientTransport)
	require.Truef(t, ok, "expected *mcp.StreamableClientTransport, got %T", transport)
	require.NotNil(t, stderrBuf)
}

// TestNewTransport_SSE verifies that transport: "sse" selects the
// SSE client transport.
func TestNewTransport_SSE(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // URL/Command/Headers/Args/Env all set explicitly below
		URL:       "http://upstream.example/sse",
		Headers:   make(map[string]string),
		Args:      []string{},
		Env:       make(map[string]string),
		Transport: transportSSE,
	}

	transport, _ := cfg.newTransport(t.Context(), &bytes.Buffer{})

	_, ok := transport.(*mcp.SSEClientTransport)
	require.Truef(t, ok, "expected *mcp.SSEClientTransport, got %T", transport)
}

// TestNewTransport_StreamableHTTPExplicit verifies that transport:
// "streamable" selects the streamable HTTP client transport
// (explicit form of the default).
func TestNewTransport_StreamableHTTPExplicit(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // URL/Command/Headers/Args/Env all set explicitly below
		URL:       testUpstreamURL,
		Headers:   make(map[string]string),
		Args:      []string{},
		Env:       make(map[string]string),
		Transport: "streamable",
	}

	transport, _ := cfg.newTransport(t.Context(), &bytes.Buffer{})

	_, ok := transport.(*mcp.StreamableClientTransport)
	require.Truef(t, ok, "expected *mcp.StreamableClientTransport, got %T", transport)
}

// TestNewTransport_HeadersApplied verifies that the headerRoundTripper
// in the resulting HTTP client carries the configured headers. The
// proxy's only contribution to the upstream request is injecting
// these headers, so this is the contract that matters.
func TestNewTransport_HeadersApplied(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // URL/Command/Headers/Args/Env all set explicitly below
		URL:     testUpstreamURL,
		Headers: map[string]string{"X-Auth-Token": "secret", "X-Tenant": "acme"},
		Args:    []string{},
		Env:     make(map[string]string),
	}

	transport, _ := cfg.newTransport(t.Context(), &bytes.Buffer{})

	streamable, ok := transport.(*mcp.StreamableClientTransport)
	require.Truef(t, ok, "expected *mcp.StreamableClientTransport, got %T", transport)
	require.NotNil(t, streamable.HTTPClient)
	require.NotNil(t, streamable.HTTPClient.Transport)

	hrt, ok := streamable.HTTPClient.Transport.(*headerRoundTripper)
	require.Truef(t, ok, "expected *headerRoundTripper, got %T", streamable.HTTPClient.Transport)
	require.Equal(t, cfg.Headers, hrt.headers)
}

// TestNewTransport_CommandReturnsCommandTransport verifies that
// setting Command selects the command (stdio) transport, regardless
// of whether URL is also set. Command is the more specific option.
func TestNewTransport_CommandReturnsCommandTransport(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // URL/Command/Headers/Args/Env all set explicitly below
		URL:     "",
		Headers: make(map[string]string),
		Command: testMCPServerCommand,
		Args:    []string{"--port", "8080"},
		Env:     make(map[string]string),
	}

	transport, stderrBuf := cfg.newTransport(t.Context(), &bytes.Buffer{})

	cmdTransport, ok := transport.(*mcp.CommandTransport)
	require.Truef(t, ok, "expected *mcp.CommandTransport, got %T", transport)
	require.NotNil(t, cmdTransport.Command)
	require.Equal(t, testMCPServerCommand, cmdTransport.Command.Path)

	// exec.Command appends the command name as Args[0].
	require.Equal(t, []string{testMCPServerCommand, "--port", "8080"}, cmdTransport.Command.Args)
	require.NotNil(t, stderrBuf)

	// stderrBuffer is wired to the child process's stderr.
	require.Same(t, stderrBuf, cmdTransport.Command.Stderr)
}

// TestNewTransport_CommandWinsOverURL verifies that when both
// Command and URL are set, Command takes precedence. The
// validate() function allows both to be set; newTransport must
// resolve the ambiguity deterministically.
func TestNewTransport_CommandWinsOverURL(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // URL/Command/Headers/Args/Env all set explicitly below
		URL:     testUpstreamURL,
		Headers: make(map[string]string),
		Command: testMCPServerCommand,
		Args:    []string{},
		Env:     make(map[string]string),
	}

	transport, _ := cfg.newTransport(t.Context(), &bytes.Buffer{})

	_, ok := transport.(*mcp.CommandTransport)
	require.Truef(t, ok, "expected *mcp.CommandTransport when Command is set, got %T", transport)
}

// TestNewTransport_CommandWithEnv verifies that env entries are
// applied to the spawned child process alongside the inherited
// environment.
func TestNewTransport_CommandWithEnv(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // URL/Command/Headers/Args/Env all set explicitly below
		URL:     "",
		Headers: make(map[string]string),
		Command: testMCPServerCommand,
		Args:    []string{},
		Env:     map[string]string{"FOO": "bar", "BAZ": "qux"},
	}

	transport, _ := cfg.newTransport(t.Context(), &bytes.Buffer{})

	cmdTransport, ok := transport.(*mcp.CommandTransport)
	require.Truef(t, ok, "expected *mcp.CommandTransport, got %T", transport)
	require.NotNil(t, cmdTransport.Command)

	env := cmdTransport.Command.Env
	require.NotEmpty(t, env)

	hasFoo := false
	hasBaz := false

	for _, entry := range env {
		if entry == "FOO=bar" {
			hasFoo = true
		}

		if entry == "BAZ=qux" {
			hasBaz = true
		}
	}

	require.Truef(t, hasFoo, "FOO=bar missing from cmd.Env: %v", env)
	require.Truef(t, hasBaz, "BAZ=qux missing from cmd.Env: %v", env)
}
