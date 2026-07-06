// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package http implements a single-tool HTTP source. Connect takes a
// `connect:` map carrying a description, URL, HTTP method, and optional
// headers, validates the configuration, and returns one MCP tool
// ("http") that proxies the configured endpoint.
//
// The output schema is intentionally permissive (`{"type": "object"}`)
// because the source returns the upstream response body verbatim as
// raw JSON; the caller is expected to know the response shape.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "embed"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/tool"
)

//go:embed schemas/http.json
var httpInput json.RawMessage

//go:embed schemas/permissive_output.json
var httpOutput json.RawMessage

// headerContentType is the standard Content-Type header key. Extracted
// as a constant so the literal does not appear multiple times.
const headerContentType = "Content-Type"

// HTTPToolRequest is the request shape accepted by the http tool.
type HTTPToolRequest struct {
	Body    any               `json:"body,omitempty"`
	Query   map[string]string `json:"query,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Form    map[string]string `json:"form,omitempty"`
}

// HTTPToolResponse is the response shape: the raw JSON body returned by
// the upstream endpoint.
type HTTPToolResponse = json.RawMessage

// config holds the decoded `connect:` map for an http source. The
// Sentinel errors returned by validate. Declared at package level so
// callers (including tests) can use errors.Is to assert the specific
// validation failure without resorting to string matching.
var (
	errURLEmpty    = errors.New("http: URL is empty")
	errMethodEmpty = errors.New("http: method is empty")
)

// description is required because every MCP tool must have one; URL and
// Method are required by Validate.
type config struct {
	Description string
	URL         string
	Method      string
	Headers     map[string]string
}

// decodeConnect decodes the source's `connect:` map into a config.
// Scalar string fields are decoded through decode.AsString so YAML-natural
// values (numbers, bools, null) are accepted and stringified; non-scalar
// values (maps, slices) produce a wrapped decode.ErrWrongType error so
// genuine config bugs surface as a clear message rather than a silent
// "field is empty" downstream. Map values (headers) are coerced
// element-by-element through the same helper. Errors here are wrapped
// by Connect as "http: decode: <reason>"; the per-field prefix lives
// here so the final message is single-segment, not double.
func decodeConnect(connect map[string]any) (config, error) {
	var (
		cfg config
		err error
	)

	str, err := decode.AsString(connect["description"])

	switch {
	case err == nil:
		cfg.Description = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — description is optional; Connect fills in a default

	default:
		return cfg, fmt.Errorf("connect.description: %w", err)
	}

	str, err = decode.AsString(connect["url"])

	switch {
	case err == nil:
		cfg.URL = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — url is required; validate() catches the empty value

	default:
		return cfg, fmt.Errorf("connect.url: %w", err)
	}

	str, err = decode.AsString(connect["method"])

	switch {
	case err == nil:
		cfg.Method = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — method is required; validate() catches the empty value

	default:
		return cfg, fmt.Errorf("connect.method: %w", err)
	}

	if raw, ok := connect["headers"].(map[string]any); ok {
		headers := make(map[string]string, len(raw))

		for key, v := range raw {
			str, sErr := decode.AsString(v)

			switch {
			case sErr == nil:
				headers[key] = str

			case errors.Is(sErr, decode.ErrNotSet):
				// skip — null header value is treated as not set

			default:
				return cfg, fmt.Errorf("connect.headers[%q]: %w", key, sErr)
			}
		}

		cfg.Headers = headers
	}

	return cfg, nil
}

// defaultHTTPDescription is the common description applied to the http
// tool when the user does not provide one in the connect map. It
// reads "HTTP <METHOD> <URL>" and gives the LLM a concise summary
// of what the tool does without forcing the user to repeat
// information already present in url and method.
const defaultHTTPDescriptionFormat = "HTTP %s %s"

// validate verifies that the decoded config is usable: the URL parses,
// the method is a recognized HTTP verb. The description is optional;
// Connect fills in a default when it is empty.
func (c config) validate() error {
	if c.URL == "" {
		return errURLEmpty
	}

	var err error

	_, err = url.Parse(c.URL)
	if err != nil {
		return fmt.Errorf("http tool URL is invalid: %w", err)
	}

	if c.Method == "" {
		return errMethodEmpty
	}

	method := strings.ToUpper(c.Method)
	if !isValidHTTPMethod(method) {
		return fmt.Errorf("http tool method is invalid: %s", c.Method)
	}

	return nil
}

// isValidHTTPMethod reports whether method is one of the standard HTTP
// methods accepted by net/http.
func isValidHTTPMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodDelete, http.MethodConnect, http.MethodOptions,
		http.MethodTrace, http.MethodPatch:
		return true
	}

	return false
}

// isReadOnlyHTTPMethod reports whether method is one of the HTTP
// "safe" methods defined in RFC 7231 §4.2.1: GET, HEAD, OPTIONS,
// TRACE. These methods are guaranteed not to change the upstream
// state, so an http tool configured with one of them is a clean
// ScopeQuery candidate.
func isReadOnlyHTTPMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}

	return false
}

// Connect decodes the source's `connect:` map, validates the
// configuration, and returns the single http tool. The tool's
// Annotations are derived from the HTTP method: safe methods (GET,
// HEAD, OPTIONS, TRACE) set ReadOnlyHint: true; mutating methods set
// ReadOnlyHint: false with DestructiveHint: new(true).
func Connect(
	_ context.Context,
	connect map[string]any,
	opts ...tool.Option,
) (tool.Response, error) {
	_ = tool.NewOptions(opts...)

	cfg, err := decodeConnect(connect)
	if err != nil {
		return tool.Response{}, fmt.Errorf("http: decode: %w", err)
	}

	validateErr := cfg.validate()
	if validateErr != nil {
		return tool.Response{}, fmt.Errorf("http: validate: %w", validateErr)
	}

	// Apply the common default description when the user did not
	// provide one. Keeps the connect map terse for the common case
	// (URL + method, no description).
	if cfg.Description == "" {
		cfg.Description = fmt.Sprintf(
			defaultHTTPDescriptionFormat,
			strings.ToUpper(cfg.Method),
			cfg.URL,
		)
	}

	// Derive the tool's Annotations from the HTTP method. Safe methods
	// (GET, HEAD, OPTIONS, TRACE) are read-only; everything else
	// mutates upstream state and is marked destructive.
	annotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    true,
		DestructiveHint: (*bool)(nil), // irrelevant when ReadOnly
		IdempotentHint:  false,        // irrelevant when ReadOnly
		OpenWorldHint:   (*bool)(nil), // default true; http hits an external URL
	}
	if !isReadOnlyHTTPMethod(cfg.Method) {
		annotations = &mcp.ToolAnnotations{
			Title:           "",
			ReadOnlyHint:    false,
			DestructiveHint: new(true),
			IdempotentHint:  false,
			OpenWorldHint:   (*bool)(nil), // default true
		}
	}

	return tool.Response{
		Tools: []tool.Tool{
			{
				Tool: &mcp.Tool{
					Name:         "http",
					Description:  cfg.Description,
					InputSchema:  httpInput,
					OutputSchema: httpOutput,
					Annotations:  annotations,
				},
				Handler: handleRequest(cfg),
			},
		},
	}, nil
}

// handleRequest returns the mcp.ToolHandler that drives the http tool.
// It decodes the raw Arguments into HTTPToolRequest, builds the upstream
// HTTP request, executes it, and returns the response body verbatim.
func handleRequest(cfg config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args HTTPToolRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse http args: %w", err)
		}

		body, err := prepareRequestBody(cfg.Method, args)
		if err != nil {
			return nil, err
		}

		upReq, err := buildRequest(ctx, cfg, body, args)
		if err != nil {
			return nil, err
		}

		resp, err := executeRequest(upReq)
		if err != nil {
			return nil, err
		}

		result := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(resp)},
			},
		}

		// Per the MCP spec, StructuredContent must marshal to a JSON
		// object. The unmarshal to map[string]any succeeds only when
		// resp is a valid JSON object (a JSON array, primitive, or
		// malformed/non-JSON value returns an error). Non-object
		// responses are conveyed via Content only.
		var probe map[string]any
		if json.Unmarshal(resp, &probe) == nil {
			result.StructuredContent = resp
		}

		return result, nil
	}
}

// preparedBody is the result of marshaling the per-call body to an
// io.Reader and recording the Content-Type that should be set.
type preparedBody struct {
	Reader      io.Reader
	ContentType string
}

func prepareRequestBody(method string, request HTTPToolRequest) (preparedBody, error) {
	method = strings.ToUpper(method)
	if method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch {
		return preparedBody{Reader: http.NoBody, ContentType: ""}, nil
	}

	if request.Form != nil {
		form := url.Values{}

		for key, value := range request.Form {
			form.Set(key, value)
		}

		return preparedBody{
			Reader:      strings.NewReader(form.Encode()),
			ContentType: "application/x-www-form-urlencoded",
		}, nil
	}

	if request.Body != nil {
		data, err := json.Marshal(request.Body)
		if err != nil {
			return preparedBody{}, fmt.Errorf("marshal request body: %w", err)
		}

		return preparedBody{
			Reader:      bytes.NewBuffer(data),
			ContentType: "application/json",
		}, nil
	}

	return preparedBody{Reader: http.NoBody, ContentType: ""}, nil
}

func buildRequest(
	ctx context.Context,
	cfg config,
	body preparedBody,
	request HTTPToolRequest,
) (*http.Request, error) {
	reqURL, err := buildURLWithQuery(cfg.URL, request.Query)
	if err != nil {
		return nil, err
	}

	method := strings.ToUpper(cfg.Method)

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body.Reader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	for key, value := range cfg.Headers {
		req.Header.Set(key, value)
	}

	for key, value := range request.Headers {
		req.Header.Set(key, value)
	}

	hasBody := body.Reader != nil && body.Reader != http.NoBody

	needsContentType := request.Headers[headerContentType] == "" && body.ContentType != ""
	if hasBody && needsContentType {
		req.Header.Set(headerContentType, body.ContentType)
	}

	return req, nil
}

func buildURLWithQuery(base string, params map[string]string) (string, error) {
	if len(params) == 0 {
		return base, nil
	}

	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}

	query := parsed.Query()
	for key, value := range params {
		query.Set(key, value)
	}

	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}

func executeRequest(req *http.Request) (json.RawMessage, error) {
	client := &http.Client{}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}

	//nolint:errcheck // body close error is not critical
	defer resp.Body.Close()

	body := new(strings.Builder)

	var copyErr error

	_, copyErr = io.Copy(body, resp.Body)
	if copyErr != nil {
		return nil, fmt.Errorf("read response: %w", copyErr)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("request status %d: %s", resp.StatusCode, body)
	}

	return json.RawMessage(body.String()), nil
}
