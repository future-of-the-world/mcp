// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package proxy implements an MCP tool source that proxies tools from a
// remote or local MCP server. Connect connects to the upstream server,
// discovers the available tools, and returns a tool.Response carrying
// one tool.Tool per upstream tool. Each tool forwards its call to the
// upstream server transparently.
//
// Both HTTP (streamable / SSE) and stdio (command) transports are
// supported. The proxy passes the upstream *mcp.Tool through unchanged
// — Title and every Annotations field (ReadOnlyHint, DestructiveHint,
// IdempotentHint, OpenWorldHint) flow end-to-end, so the registered
// tool advertises exactly the same hints as the upstream.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/tool"
)

const (
	// transportSSE is the MCP transport protocol name for SSE HTTP mode.
	transportSSE = "sse"

	// maxStderrBytes caps the amount of child-process stderr included
	// in a wrapped connect error.
	maxStderrBytes = 4096

	// keyURL is the connect-map key for the upstream URL.
	keyURL = "url"

	// keyCommand is the connect-map key for the upstream subprocess command.
	keyCommand = "command"

	// keyHeaders is the connect-map key and label for the HTTP headers map.
	keyHeaders = "headers"

	// keyEnv is the connect-map key and label for the command env map.
	keyEnv = "env"
)

var (
	errURLEmpty        = errors.New("proxy tool: url is empty")
	errCommandEmpty    = errors.New("proxy tool: command is empty")
	errNoRemoteTools   = errors.New("proxy tool found no tools on remote server")
	errSessionNotReady = errors.New("proxy tool: session is not established")
)

// config holds the decoded `connect:` map for a proxy source. Either
// URL (HTTP mode) or Command (stdio mode) must be set.
type config struct {
	URL       string
	Headers   map[string]string
	Command   string
	Args      []string
	Env       map[string]string
	Transport string
}

// decodeConnect decodes the source's `connect:` map into a config.
// Scalar string fields are decoded through decode.AsString so YAML-natural
// values (numbers, bools, null) are accepted and stringified; non-scalar
// values (maps, slices) produce a wrapped decode.ErrWrongType error so
// genuine config bugs surface as a clear message rather than a silent
// "field is empty" downstream. Map values (headers, env) are coerced
// element-by-element through the same helper.
func decodeConnect(connect map[string]any) (config, error) {
	var (
		cfg config
		err error
	)

	str, err := decode.AsString(connect[keyURL])

	switch {
	case err == nil:
		cfg.URL = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — url is optional; validate() catches the "both empty" case

	default:
		return cfg, fmt.Errorf("connect.url: %w", err)
	}

	headers, hErr := decodeStringMap(connect, keyHeaders, keyHeaders)
	if hErr != nil {
		return cfg, hErr
	}

	cfg.Headers = headers

	str, err = decode.AsString(connect[keyCommand])

	switch {
	case err == nil:
		cfg.Command = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — command is optional; validate() catches the "both empty" case

	default:
		return cfg, fmt.Errorf("connect.command: %w", err)
	}

	cfg.Args, err = decodeStringSlice(connect, "args")
	if err != nil {
		return cfg, err
	}

	env, eErr := decodeStringMap(connect, keyEnv, keyEnv)
	if eErr != nil {
		return cfg, eErr
	}

	cfg.Env = env

	str, err = decode.AsString(connect["transport"])

	switch {
	case err == nil:
		cfg.Transport = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — transport is optional; newTransport picks a default

	default:
		return cfg, fmt.Errorf("connect.transport: %w", err)
	}

	return cfg, nil
}

// decodeStringMap decodes a string→string map field from connect, where
// key is the connect-map key and label is the dotted prefix used in
// error messages (e.g. "headers" or "env"). Each value is coerced
// through decode.AsString; non-scalar values produce a wrapped
// decode.ErrWrongType so the caller sees the actual Go type in the
// error message.
func decodeStringMap(connect map[string]any, key, label string) (map[string]string, error) {
	raw, ok := connect[key].(map[string]any)
	if !ok {
		return make(map[string]string), nil
	}

	out := make(map[string]string, len(raw))

	for mapKey, v := range raw {
		str, err := decode.AsString(v)

		switch {
		case err == nil:
			out[mapKey] = str

		case errors.Is(err, decode.ErrNotSet):
			// skip — null header/env value is treated as not set

		default:
			return nil, fmt.Errorf("connect.%s[%q]: %w", label, mapKey, err)
		}
	}

	return out, nil
}

// decodeStringSlice decodes a string-slice field from connect. The
// element-type check is enforced; nil or missing key yields empty slice.
func decodeStringSlice(connect map[string]any, key string) ([]string, error) {
	raw, ok := connect[key].([]any)
	if !ok {
		return []string{}, nil
	}

	out := make([]string, 0, len(raw))

	for _, v := range raw {
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("proxy: connect.%s elements must be strings", key)
		}

		out = append(out, str)
	}

	return out, nil
}

// validate checks that the decoded config is usable: at least one of
// URL or Command must be set.
func (c *config) validate() error {
	if c.Command == "" && c.URL == "" {
		return errors.Join(errURLEmpty, errCommandEmpty)
	}

	return nil
}

// Connect decodes the source's `connect:` map, connects to the
// configured upstream MCP server, discovers its tools, and returns a
// tool.Response carrying one tool.Tool per upstream tool. The upstream
// *mcp.Tool (including Title and Annotations) is passed through
// unchanged because the proxy has no way to reason about the upstream
// tool's semantics.
func Connect(
	ctx context.Context,
	connect map[string]any,
	opts ...tool.Option,
) (tool.Response, error) {
	_ = tool.NewOptions(opts...)

	cfg, err := decodeConnect(connect)
	if err != nil {
		return tool.Response{}, fmt.Errorf("proxy: decode: %w", err)
	}

	validateErr := cfg.validate()
	if validateErr != nil {
		return tool.Response{}, fmt.Errorf("proxy: validate: %w", validateErr)
	}

	tools, err := cfg.discover(ctx)
	if err != nil {
		return tool.Response{}, fmt.Errorf("proxy: discover: %w", err)
	}

	return tool.Response{Tools: tools}, nil
}

// discover connects to the upstream MCP server and copies each remote
// *mcp.Tool into a tool.Tool with a forwarding handler. The copy is
// shallow so Annotations and Meta (which are pointers) remain shared
// with the upstream value.
func (c *config) discover(ctx context.Context) ([]tool.Tool, error) {
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, c.wrapConnectError(err, conn.stderr)
	}

	session := conn.session

	remoteTools, err := listRemoteTools(ctx, session)
	if err != nil {
		//nolint:errcheck // close error is not critical when listing tools fails
		session.Close()

		return nil, fmt.Errorf("proxy tool list tools: %w", err)
	}

	if len(remoteTools) == 0 {
		//nolint:errcheck // close error is not critical when no tools found
		session.Close()

		return nil, errNoRemoteTools
	}

	out := make([]tool.Tool, 0, len(remoteTools))

	for _, remoteTool := range remoteTools {
		// Copy: the dispatcher holds the *mcp.Tool pointer past the
		// loop body and past the session's lifetime, so the upstream
		// pointer must not be aliased. The copy is shallow (struct
		// value of *mcp.Tool) — Annotations and Meta are themselves
		// pointers, so they remain shared with the upstream value.
		remote := *remoteTool

		out = append(out, tool.Tool{
			Tool:    &remote,
			Handler: makeCallHandler(session, remoteTool.Name),
		})
	}

	return out, nil
}

// connectResult bundles the dialed client session with any captured
// stderr from the upstream transport. stderr is only populated for
// command transports.
type connectResult struct {
	session *mcp.ClientSession
	stderr  bytes.Buffer
}

// connect dials the upstream MCP server.
func (c *config) connect(ctx context.Context) (connectResult, error) {
	var stderr bytes.Buffer

	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "mcp-proxy",
			Version: "1.0.0",
		},
		(*mcp.ClientOptions)(nil),
	)

	transport, _ := c.newTransport(ctx, &stderr)

	session, err := client.Connect(ctx, transport, (*mcp.ClientSessionOptions)(nil))
	if err != nil {
		return connectResult{
			session: (*mcp.ClientSession)(nil),
			stderr:  stderr,
		}, fmt.Errorf("proxy tool connect: %w", err)
	}

	return connectResult{session: session, stderr: stderr}, nil
}

// wrapConnectError wraps a connection error with context about the
// target. For command (stdio) mode, any captured stderr output is
// appended to aid debugging.
func (c *config) wrapConnectError(err error, stderr bytes.Buffer) error {
	if c.Command != "" {
		data := stderr.Bytes()

		if len(data) > 0 {
			if len(data) > maxStderrBytes {
				data = data[:maxStderrBytes]
			}

			return fmt.Errorf(
				"proxy tool connect to command %s: %w\nstderr:\n%s",
				c.Command,
				err,
				data,
			)
		}

		return fmt.Errorf("proxy tool connect to command %s: %w", c.Command, err)
	}

	return fmt.Errorf("proxy tool connect to %s: %w", c.URL, err)
}

// newTransport creates the appropriate MCP client transport based on the
// config. The stderrBuffer is populated when the transport launches a
// child process.
func (c *config) newTransport(
	ctx context.Context,
	stderrBuffer *bytes.Buffer,
) (mcp.Transport, *bytes.Buffer) {
	if c.Command != "" {
		cmd := exec.CommandContext(ctx, c.Command, c.Args...)

		if len(c.Env) > 0 {
			env := os.Environ()

			for key, value := range c.Env {
				env = append(env, key+"="+value)
			}

			cmd.Env = env
		}

		// Capture stderr so we can include it in error messages if the
		// process fails.
		cmd.Stderr = stderrBuffer

		return &mcp.CommandTransport{
			Command:           cmd,
			TerminateDuration: 0,
		}, stderrBuffer
	}

	httpClient := &http.Client{
		Transport: &headerRoundTripper{
			base:    &http.Transport{},
			headers: c.Headers,
		},
	}

	switch strings.ToLower(c.Transport) {
	case transportSSE:
		return &mcp.SSEClientTransport{
			Endpoint:   c.URL,
			HTTPClient: httpClient,
		}, stderrBuffer

	default: // "streamable" or empty
		return &mcp.StreamableClientTransport{
			Endpoint:   c.URL,
			HTTPClient: httpClient,
		}, stderrBuffer
	}
}

// listRemoteTools iterates the remote session's tool list and returns
// all discovered *mcp.Tool values.
func listRemoteTools(ctx context.Context, session *mcp.ClientSession) ([]*mcp.Tool, error) {
	var out []*mcp.Tool

	for remoteTool, err := range session.Tools(ctx, (*mcp.ListToolsParams)(nil)) {
		if err != nil {
			return nil, err
		}

		out = append(out, remoteTool)
	}

	return out, nil
}

// makeCallHandler returns an mcp.ToolHandler that forwards the call to
// the named remote tool on the given session. The session must remain
// open for the lifetime of the handler.
func makeCallHandler(session *mcp.ClientSession, remoteName string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if session == nil {
			return nil, errSessionNotReady
		}

		// Forward raw arguments as-is to the remote server. The MCP
		// client handles nil Arguments by sending an empty object.
		var args json.RawMessage

		if len(req.Params.Arguments) > 0 {
			args = req.Params.Arguments
		}

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      remoteName,
			Arguments: args,
		})
		if err != nil {
			return nil, fmt.Errorf("proxy tool call %s: %w", remoteName, err)
		}

		return result, nil
	}
}

// headerRoundTripper wraps an http.RoundTripper and injects custom
// headers before forwarding the request.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

// RoundTrip implements the http.RoundTripper interface, injecting
// custom headers before forwarding the request.
func (rt *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for key, value := range rt.headers {
		req.Header.Set(key, value)
	}

	resp, err := rt.base.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("http round trip: %w", err)
	}

	return resp, nil
}
