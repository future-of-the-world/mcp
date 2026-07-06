// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package github implements an MCP tool source that exposes the official
// GitHub MCP server (github.com/github/github-mcp-server) as a first-class
// `type: github` source. Connect decodes the source's `connect:` map, builds
// the upstream GitHub MCP server in-process, bridges it onto an in-memory
// MCP transport, and re-registers the upstream's tools on the parent MCP
// server with forwarding handlers. The upstream code lives in Go's module
// cache and is not analyzed by this project's linter, tests, or coverage.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/github/github-mcp-server/pkg/lockdown"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shurcooL/githubv4"

	upstream "github.com/github/github-mcp-server/pkg/github"
	upstreaminv "github.com/github/github-mcp-server/pkg/inventory"
	upstreamobs "github.com/github/github-mcp-server/pkg/observability"
	upstreammetrics "github.com/github/github-mcp-server/pkg/observability/metrics"
	upstreamraw "github.com/github/github-mcp-server/pkg/raw"
	upstreamtranslations "github.com/github/github-mcp-server/pkg/translations"
	upstreamutils "github.com/github/github-mcp-server/pkg/utils"
	gogithub "github.com/google/go-github/v87/github"

	"go.amidman.dev/mcp/tool"
)

const (
	// keyToken is the connect-map key for the GitHub personal access token.
	keyToken = "token"

	// keyHost is the connect-map key for the GitHub host (defaults to github.com).
	keyHost = "host"

	// keyToolsets is the connect-map key for the enabled toolset names.
	keyToolsets = "toolsets"

	// keyReadOnly is the connect-map key for the read-only mode flag.
	keyReadOnly = "read_only"
)

var (
	// errTokenEmpty is returned when the connect map omits the required token.
	errTokenEmpty = errors.New("github tool: token is empty")

	// errNoRemoteTools is returned when the upstream server exposes no tools.
	errNoRemoteTools = errors.New("github tool found no tools on upstream server")

	// errSessionNotReady is returned when a forwarding handler fires after
	// the client session has been closed.
	errSessionNotReady = errors.New("github tool: session is not established")
)

// config holds the decoded `connect:` map for a github source.
type config struct {
	// Token is the GitHub personal access token used for API authentication.
	Token string

	// Host is the GitHub host (e.g. "github.com" or "github.enterprise.com").
	// Empty defaults to "github.com" via upstream utils.NewAPIHost.
	Host string

	// Toolsets is the list of toolset names to enable on the upstream server.
	// Nil means "use upstream defaults".
	Toolsets []string

	// ReadOnly restricts the upstream server to read-only tools when true.
	ReadOnly bool
}

// decodeString decodes a string-typed field from the connect map. It is
// extracted from decodeConnect to keep the parent's cognitive complexity
// under the linter's threshold. The key parameter is the connect-map key
// used in error messages.
func decodeString(connect map[string]any, key string) (string, error) {
	raw, ok := connect[key]
	if !ok || raw == nil {
		return "", nil
	}

	str, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("connect.%s: expected string, got %T", key, raw)
	}

	return str, nil
}

// decodeToolsetsSlice decodes a YAML-natural value into a []string for the
// `toolsets` connect-map key. Accepted shapes: nil (returns nil, no error),
// []any with string elements, or a single string (treated as a
// one-element slice for ergonomics with comma-separated env-var-style
// values). Other shapes produce an error.
func decodeToolsetsSlice(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}

	switch value := raw.(type) {
	case []any:
		out := make([]string, 0, len(value))
		for index, item := range value {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf(
					"connect.toolsets[%d]: expected string, got %T",
					index, item,
				)
			}

			out = append(out, str)
		}

		return out, nil

	case string:
		return []string{value}, nil

	default:
		return nil, fmt.Errorf(
			"connect.toolsets: expected string or []string, got %T",
			raw,
		)
	}
}

// decodeConnect decodes the source's `connect:` map into a config. The
// required token field is decoded as a string; optional fields (host,
// toolsets, read_only) are decoded with type-checks. Errors here are
// wrapped by Connect as "github: decode: <reason>"; the per-field
// prefix lives here so the final message is single-segment, not double.
//
// The function is split across two helpers (decodeConnect itself and
// decodeReadOnly) to keep cognitive complexity under the linter's
// threshold.
func decodeConnect(connect map[string]any) (config, error) {
	var cfg config

	token, err := decodeString(connect, keyToken)
	if err != nil {
		return cfg, err
	}

	if token == "" {
		return cfg, errTokenEmpty
	}

	cfg.Token = token

	host, err := decodeString(connect, keyHost)
	if err != nil {
		return cfg, err
	}

	cfg.Host = host

	if raw, ok := connect[keyToolsets]; ok && raw != nil {
		toolsets, derr := decodeToolsetsSlice(raw)
		if derr != nil {
			return cfg, derr
		}

		cfg.Toolsets = toolsets
	}

	readOnly, err := decodeReadOnly(connect)
	if err != nil {
		return cfg, err
	}

	cfg.ReadOnly = readOnly

	return cfg, nil
}

// decodeReadOnly decodes the read_only flag from the connect map. The
// helper exists to keep decodeConnect's cognitive complexity under the
// linter's threshold.
func decodeReadOnly(connect map[string]any) (bool, error) {
	raw, ok := connect[keyReadOnly]
	if !ok || raw == nil {
		return false, nil
	}

	asBool, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("connect.%s: expected bool, got %T", keyReadOnly, raw)
	}

	return asBool, nil
}

// validate checks that the decoded config is usable: the token is
// non-empty. Other fields are validated at use site (host normalization,
// upstream inventory build).
func (c config) validate() error {
	if c.Token == "" {
		return errTokenEmpty
	}

	return nil
}

// Connect decodes the source's `connect:` map, builds the upstream GitHub
// MCP server in-process, bridges it onto an in-memory MCP transport, and
// returns a tool.Response carrying one tool.Tool per upstream tool. The
// upstream *mcp.Tool (including Title and Annotations) is passed through
// unchanged so the registered tool advertises exactly the same hints as
// the upstream. Forwarding handlers call into the upstream through the
// in-memory client session, which lives for the lifetime of the parent
// server.
func Connect(
	ctx context.Context,
	connect map[string]any,
	opts ...tool.Option,
) (tool.Response, error) {
	toolOpts := tool.NewOptions(opts...)

	cfg, err := decodeConnect(connect)
	if err != nil {
		return tool.Response{}, fmt.Errorf("github: decode: %w", err)
	}

	err = cfg.validate()
	if err != nil {
		return tool.Response{}, fmt.Errorf("github: validate: %w", err)
	}

	bundle, err := buildUpstreamServer(ctx, cfg, toolOpts.Logger())
	if err != nil {
		return tool.Response{}, fmt.Errorf("github: build upstream: %w", err)
	}

	session, err := bridgeUpstream(ctx, bundle.server)
	if err != nil {
		return tool.Response{}, fmt.Errorf("github: bridge: %w", err)
	}

	tools, err := listRemoteTools(ctx, session)
	if err != nil {
		return tool.Response{}, fmt.Errorf("github: list tools: %w", err)
	}

	if len(tools) == 0 {
		return tool.Response{}, errNoRemoteTools
	}

	out := make([]tool.Tool, 0, len(tools))

	for _, remoteTool := range tools {
		// Copy: the dispatcher holds the *mcp.Tool pointer past the loop
		// body and past the bridge's lifetime, so the upstream pointer
		// must not be aliased. The copy is shallow (struct value of
		// *mcp.Tool) — Annotations and Meta are themselves pointers, so
		// they remain shared with the upstream value.
		remote := *remoteTool

		out = append(out, tool.Tool{
			Tool:    &remote,
			Handler: makeCallHandler(session, remoteTool.Name),
		})
	}

	return tool.Response{Tools: out}, nil
}

// upstreamBundle groups the upstream server with the resources that must
// outlive it (deps, inventory). Returning one struct from
// buildUpstreamServer keeps the public function's return count within
// the project's two-value convention.
type upstreamBundle struct {
	server    *mcp.Server
	deps      upstream.ToolDependencies
	inventory *upstreaminv.Inventory
}

// buildUpstreamServer constructs the upstream GitHub MCP server, its
// ToolDependencies, and its tool inventory. The server has all upstream
// tools registered against it but is not yet running; bridgeUpstream
// starts it on an in-memory transport.
func buildUpstreamServer(
	ctx context.Context,
	cfg config,
	logger *slog.Logger,
) (*upstreamBundle, error) {
	normalizedHost, err := normalizeHost(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("normalize host: %w", err)
	}

	clients, err := buildClients(ctx, normalizedHost, cfg.Token)
	if err != nil {
		return nil, err
	}

	translator, _ := upstreamtranslations.TranslationHelper()

	noopMetrics := upstreammetrics.NewNoopMetrics()

	obs, err := upstreamobs.NewExporters(logger, noopMetrics)
	if err != nil {
		return nil, fmt.Errorf("create observability exporters: %w", err)
	}

	deps := upstream.NewBaseDeps(
		clients.rest,
		clients.gql,
		clients.raw,
		(*lockdown.RepoAccessCache)(nil), // repoAccess cache is only used in lockdown mode
		translator,
		upstream.FeatureFlags{LockdownMode: false},
		0, // contentWindowSize — use upstream default
		noFeatureFlags,
		obs,
	)

	inv, err := buildInventory(translator, cfg)
	if err != nil {
		return nil, err
	}

	server, err := buildUpstreamMCPServer(ctx, cfg, deps, inv, logger)
	if err != nil {
		return nil, err
	}

	return &upstreamBundle{
		server:    server,
		deps:      deps,
		inventory: inv,
	}, nil
}

// noFeatureFlags is the feature-flag checker used in this first pass: the
// upstream's feature-flag surface is not exposed through the connect map,
// so every flag is reported as disabled. The static signature matches
// upstreaminv.FeatureFlagChecker.
func noFeatureFlags(_ context.Context, _ string) (bool, error) {
	return false, nil
}

// apiClients groups the GitHub HTTP clients (REST, GraphQL, Raw) the
// upstream server needs to issue API calls. They are constructed in
// buildClients and passed by reference into NewBaseDeps.
type apiClients struct {
	rest *gogithub.Client
	gql  *githubv4.Client
	raw  *upstreamraw.Client
}

// buildClients constructs the GitHub REST, GraphQL, and Raw clients
// against the resolved API host. Each client carries its own HTTP
// transport (rather than sharing http.DefaultTransport) so that the
// User-Agent and Authorization headers we inject do not race across
// concurrent sources.
func buildClients(ctx context.Context, host, token string) (*apiClients, error) {
	apiHost, err := upstreamutils.NewAPIHost(host)
	if err != nil {
		return nil, fmt.Errorf("parse host: %w", err)
	}

	restURL, err := apiHost.BaseRESTURL(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve REST URL: %w", err)
	}

	uploadURL, err := apiHost.UploadURL(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve upload URL: %w", err)
	}

	graphQLURL, err := apiHost.GraphqlURL(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve GraphQL URL: %w", err)
	}

	rawURL, err := apiHost.RawURL(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve raw URL: %w", err)
	}

	restTransport := &userAgentTransport{
		base:  &http.Transport{},
		agent: buildUserAgent(),
	}

	restClient, err := gogithub.NewClient(
		gogithub.WithHTTPClient(&http.Client{Transport: restTransport}),
		gogithub.WithAuthToken(token),
		gogithub.WithEnterpriseURLs(restURL.String(), uploadURL.String()),
	)
	if err != nil {
		return nil, fmt.Errorf("create REST client: %w", err)
	}

	gqlHTTPClient := &http.Client{
		Transport: &bearerAuthTransport{
			token: token,
			base:  &http.Transport{},
		},
	}

	gqlClient := githubv4.NewEnterpriseClient(graphQLURL.String(), gqlHTTPClient)

	rawClient, err := upstreamraw.NewClient(restClient, rawURL)
	if err != nil {
		return nil, fmt.Errorf("create raw client: %w", err)
	}

	return &apiClients{
		rest: restClient,
		gql:  gqlClient,
		raw:  rawClient,
	}, nil
}

// buildInventory assembles the upstream tool inventory from the user's
// configuration. The toolset list follows the upstream convention: nil
// means "use defaults", an explicit slice means "exactly these".
func buildInventory(
	translator upstreamtranslations.TranslationHelperFunc,
	cfg config,
) (*upstreaminv.Inventory, error) {
	enabledToolsets := upstream.ResolvedEnabledToolsets(cfg.Toolsets, []string(nil))

	invBuilder := upstream.NewInventory(translator).
		WithDeprecatedAliases(upstream.DeprecatedToolAliases).
		WithReadOnly(cfg.ReadOnly).
		WithToolsets(enabledToolsets).
		WithTools(upstream.CleanTools([]string(nil))).
		WithServerInstructions().
		WithFeatureChecker(noFeatureFlags)

	inv, err := invBuilder.Build()
	if err != nil {
		return nil, fmt.Errorf("build inventory: %w", err)
	}

	return inv, nil
}

// buildUpstreamMCPServer constructs the upstream *mcp.Server with the
// given configuration and dependencies. The server is fully populated
// (all tools registered) but not yet running.
//
// constructed upstream; bundling into a struct would obscure the call site
// without saving any allocations.
//
//nolint:revive // argument-limit: every argument is distinct and
func buildUpstreamMCPServer(
	ctx context.Context,
	cfg config,
	deps upstream.ToolDependencies,
	inv *upstreaminv.Inventory,
	logger *slog.Logger,
) (*mcp.Server, error) {
	translator, _ := upstreamtranslations.TranslationHelper()

	upstreamCfg := upstream.MCPServerConfig{
		Version:           upstreamVersion,
		Host:              cfg.Host,
		Token:             cfg.Token,
		EnabledToolsets:   cfg.Toolsets,
		ReadOnly:          cfg.ReadOnly,
		Translator:        translator,
		ContentWindowSize: 0, // upstream default
		LockdownMode:      false,
		InsidersMode:      false,
		Logger:            logger,
	}

	server, err := upstream.NewMCPServer(ctx, &upstreamCfg, deps, inv)
	if err != nil {
		return nil, fmt.Errorf("new upstream MCP server: %w", err)
	}

	return server, nil
}

// buildUserAgent returns the User-Agent string sent on every REST call
// the upstream issues. It includes both the embedded upstream version
// and the local mcp server version so GitHub's API analytics can
// distinguish embedded use from standalone use.
func buildUserAgent() string {
	return "github-mcp-server/" + upstreamVersion + " (mcp/" + mcpServerVersion + ")"
}

// bridgeUpstream starts the given upstream server on one side of a
// paired in-memory MCP transport and returns a client session connected
// to the other side. The upstream server runs in a goroutine bound to
// the parent context; when the parent context is canceled, the
// returned session is closed, which in turn closes the client transport
// and lets the server's Run return.
//
// The server's Run error is not actionable: the in-memory transport
// signals shutdown by closing, and any other failure is unrecoverable.
// We do not use context.WithCancel here because the in-memory transport
// closes itself when the session closes — there is no leak to prevent.
func bridgeUpstream(
	ctx context.Context,
	upstreamServer *mcp.Server,
) (*mcp.ClientSession, error) {
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	runDone := make(chan struct{})

	go func() {
		defer close(runDone)
		//nolint:errcheck // serverTransport is closed by the parent context
		_ = upstreamServer.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "mcp-github-bridge",
			Version: mcpServerVersion,
		},
		(*mcp.ClientOptions)(nil),
	)

	session, err := client.Connect(ctx, clientTransport, (*mcp.ClientSessionOptions)(nil))
	if err != nil {
		<-runDone

		return nil, fmt.Errorf("client connect: %w", err)
	}

	// Tie the upstream server's lifetime to the parent context: when
	// the parent server is shutting down, the parent context is
	// canceled and we close the session, which closes the client
	// transport, which causes the server's Run to return.
	go func() {
		<-ctx.Done()

		//nolint:errcheck // best-effort: session is already closing
		_ = session.Close()
	}()

	return session, nil
}

// listRemoteTools iterates the client session's tool list and returns all
// discovered *mcp.Tool values. Mirrors mcp/proxy.listRemoteTools but works
// against an in-process client session.
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
// open for the lifetime of the handler. Mirrors mcp/proxy.makeCallHandler
// but operates on a client session instead of a stdio subprocess session.
func makeCallHandler(session *mcp.ClientSession, remoteName string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if session == nil {
			return nil, errSessionNotReady
		}

		var args json.RawMessage

		if len(req.Params.Arguments) > 0 {
			args = req.Params.Arguments
		}

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      remoteName,
			Arguments: args,
		})
		if err != nil {
			return nil, fmt.Errorf("github tool call %s: %w", remoteName, err)
		}

		return result, nil
	}
}

// userAgentTransport wraps an http.RoundTripper and injects a User-Agent
// header. We construct one per Connect call rather than reusing
// http.DefaultTransport so concurrent sources don't race on Agent.
type userAgentTransport struct {
	base  http.RoundTripper
	agent string
}

// RoundTrip implements http.RoundTripper.
func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", t.agent)

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("github REST round trip: %w", err)
	}

	return resp, nil
}

// bearerAuthTransport wraps an http.RoundTripper and injects a Bearer
// Authorization header for GraphQL requests.
type bearerAuthTransport struct {
	token string
	base  http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *bearerAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("github GraphQL round trip: %w", err)
	}

	return resp, nil
}

// normalizeHost accepts either a bare hostname ("github.example.com")
// or a full URL ("https://github.example.com") and returns a form the
// upstream's utils.NewAPIHost can consume. An empty string is passed
// through unchanged so the upstream's default (github.com) takes effect.
func normalizeHost(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}

	if strings.Contains(trimmed, "://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", fmt.Errorf("parse %q: %w", trimmed, err)
		}

		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return "", fmt.Errorf(
				"unsupported scheme %q (use http or https): %s",
				parsed.Scheme, trimmed,
			)
		}

		return trimmed, nil
	}

	// Bare hostname: prepend https://. The upstream's host parser
	// accepts both http and https for GitHub Enterprise; we default
	// to https because that's the recommended setting for production
	// GitHub Enterprise deployments.
	return "https://" + trimmed, nil
}

// upstreamVersion is the version string the upstream server reports via
// its Implementation.Version field. We pin a sentinel because the
// upstream's own version is set via -ldflags at build time and is empty
// in a library context.
const upstreamVersion = "1.3.1-embedded"

// mcpServerVersion is the version our own mcp server reports when acting
// as a client to the upstream. It follows the local server's version
// field; for now we use a static string matching the wrapper's
// identification.
const mcpServerVersion = "1.0.0"

// We import time here so the goimports of the upstream's transitive
// package set stays stable; time is used by upstream clients when they
// construct their own internal timeouts.
var _ = time.Second
