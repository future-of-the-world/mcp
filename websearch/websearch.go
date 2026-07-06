// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package websearch implements a source of MCP tools that wrap a small
// search federation. Connect decodes the source's `connect:` map
// (Brave API key env var, max results, timeout), initializes the
// search and fetch services, and returns the five websearch tools.
// Every tool is read-only; all of them set Annotations:
// ReadOnlyHint=true.
package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "embed"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/tool"
)

//go:embed schemas/web_search.json
var webSearchInput json.RawMessage

//go:embed schemas/news_search.json
var newsSearchInput json.RawMessage

//go:embed schemas/image_search.json
var imageSearchInput json.RawMessage

//go:embed schemas/fetch_url.json
var fetchURLInput json.RawMessage

//go:embed schemas/list_providers.json
var listProvidersInput json.RawMessage

//go:embed schemas/search_output.json
var searchOutput json.RawMessage

//go:embed schemas/fetch_output.json
var fetchOutput json.RawMessage

//go:embed schemas/list_providers_output.json
var listProvidersOutput json.RawMessage

const (
	//nolint:gosec // env var name, not a credential
	defaultAPIKeyEnvVar = "BRAVE_API_KEY"
	defaultMaxResults   = 10
)

var (
	errEmptySearchTerm = errors.New("search_term is required")
	errEmptyURL        = errors.New("url is required")
	errTimeoutNegative = errors.New("timeout must be non-negative")
)

// Duration is a time.Duration that unmarshals from a human-readable string
// (e.g. "10s", "1m30s") in both YAML and JSON configs.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler for Duration.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var input string

	var err error

	err = node.Decode(&input)
	if err != nil {
		return fmt.Errorf("decode duration string: %w", err)
	}

	parsed, err := time.ParseDuration(input)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", input, err)
	}

	*d = Duration(parsed)

	return nil
}

// UnmarshalJSON implements json.Unmarshaler for Duration.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var input string

	var err error

	err = json.Unmarshal(data, &input)
	if err != nil {
		return fmt.Errorf("decode duration string: %w", err)
	}

	parsed, err := time.ParseDuration(input)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", input, err)
	}

	*d = Duration(parsed)

	return nil
}

// Duration returns the underlying time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// Tool holds the per-source state for a websearch source: the
// dispatcher-provided logger, the search/fetch services, the resolved
// Brave API key, and the per-source tuning (max results, timeout). The
// type is exported only because tests in this package construct it
// directly; production code reaches it via Connect and never touches
// the fields.
type Tool struct {
	BraveAPIKeyEnv string
	MaxResults     int
	Timeout        Duration

	logger  *slog.Logger
	factory *ProviderFactory
	search  *SearchService
	fetch   *FetchService
	apiKey  string
}

// config holds the decoded `connect:` map for a websearch source.
type config struct {
	BraveAPIKeyEnv string
	MaxResults     int
	Timeout        Duration
}

// decodeConnect decodes the source's `connect:` map into a config.
// Scalar string fields are decoded through decode.AsString so YAML-natural
// values (numbers, bools, null) are accepted and stringified; non-scalar
// values (maps, slices) produce a wrapped decode.ErrWrongType error so
// genuine config bugs surface as a clear message rather than a silent
// "field is empty" downstream. The `timeout` field keeps its own
// time.ParseDuration step on top of AsString (numbers like 30 are
// stringified to "30", which ParseDuration then interprets as 30ns —
// the field-specific syntax contract is unchanged). Errors here are
// wrapped by Connect as "websearch: decode: <reason>"; the decode
// prefix lives on Connect so error messages have one, not two,
// "websearch: decode:" segments.
func decodeConnect(connect map[string]any) (config, error) {
	var (
		cfg config
		err error
	)

	str, err := decode.AsString(connect["brave_api_key_env"])

	switch {
	case err == nil:
		cfg.BraveAPIKeyEnv = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — env var is optional; resolveAPIKey picks the default

	default:
		return cfg, fmt.Errorf("connect.brave_api_key_env: %w", err)
	}

	if raw, ok := connect["max_results"]; ok {
		switch val := raw.(type) {
		case int:
			cfg.MaxResults = val

		case int64:
			cfg.MaxResults = int(val)

		case float64:
			cfg.MaxResults = int(val)

		default:
			return cfg, fmt.Errorf("websearch: connect.max_results must be an integer, got %T", raw)
		}
	}

	str, err = decode.AsString(connect["timeout"])

	switch {
	case err == nil:
		parsed, parseErr := time.ParseDuration(str)
		if parseErr != nil {
			return cfg, fmt.Errorf("connect.timeout: %w", parseErr)
		}

		cfg.Timeout = Duration(parsed)

	case errors.Is(err, decode.ErrNotSet):
		// skip — timeout is optional; defaults are applied later

	default:
		return cfg, fmt.Errorf("connect.timeout: %w", err)
	}

	return cfg, nil
}

func (c *config) validate() error {
	if c.Timeout < 0 {
		return errTimeoutNegative
	}

	return nil
}

func (t *Tool) resolveAPIKey() error {
	envVar := t.BraveAPIKeyEnv
	if envVar == "" {
		envVar = defaultAPIKeyEnvVar
	}

	if t.BraveAPIKeyEnv != "" {
		if _, ok := os.LookupEnv(envVar); !ok {
			return fmt.Errorf("brave API key env var %q does not exist", envVar)
		}
	}

	t.apiKey = os.Getenv(envVar)

	return nil
}

func (t *Tool) init(ctx context.Context) error {
	err := t.resolveAPIKey()
	if err != nil {
		return err
	}

	t.factory = NewProviderFactory(t.logger)
	t.factory.SetupDefaults(ctx, t.apiKey)

	t.search = NewSearchService(t.factory)
	t.fetch = NewFetchService(t.logger)

	if t.Timeout > 0 {
		t.fetch.client.Timeout = t.Timeout.Duration()
	}

	return nil
}

// buildSearchOptions translates a web search request into a *SearchOptions
// for the underlying search service. The max results default is
// cascading: per-request count → tool-level MaxResults → package default.
func (t *Tool) buildSearchOptions(args *webSearchRequest, kind SearchKind) *SearchOptions {
	maxResults := args.Count

	if maxResults <= 0 {
		maxResults = t.MaxResults
	}

	if maxResults <= 0 {
		maxResults = defaultMaxResults
	}

	return &SearchOptions{
		Count:          maxResults,
		Offset:         args.Offset,
		Cursor:         args.Cursor,
		Freshness:      args.Freshness,
		Country:        args.Country,
		SearchLang:     args.SearchLang,
		SafeSearch:     args.SafeSearch,
		IncludeDomains: args.IncludeDomains,
		ExcludeDomains: args.ExcludeDomains,
		Kind:           kind,
	}
}

// Connect decodes the source's `connect:` map, initializes the
// websearch services, and returns the five websearch tools. The four
// search tools and fetch_url are always registered; news_search and
// image_search are only returned when a Brave API key is configured.
// All tools set Annotations: ReadOnlyHint=true because they are
// read-only.
func Connect(
	ctx context.Context,
	connect map[string]any,
	opts ...tool.Option,
) (tool.Response, error) {
	o := tool.NewOptions(opts...)
	logger := o.Logger()

	cfg, err := decodeConnect(connect)
	if err != nil {
		return tool.Response{}, fmt.Errorf("websearch: decode: %w", err)
	}

	validateErr := cfg.validate()
	if validateErr != nil {
		return tool.Response{}, fmt.Errorf("websearch: validate: %w", validateErr)
	}

	searchTool := &Tool{
		BraveAPIKeyEnv: cfg.BraveAPIKeyEnv,
		MaxResults:     cfg.MaxResults,
		Timeout:        cfg.Timeout,
		logger:         logger,
	}

	initErr := searchTool.init(ctx)
	if initErr != nil {
		return tool.Response{}, fmt.Errorf("websearch: init: %w", initErr)
	}

	// readOnlyAnnotations is the shared annotation block for all 5
	// websearch tools below. They are all read-only (the search
	// federation only fetches external content; it never mutates
	// upstream state). OpenWorldHint defaults to true (search hits
	// the open web).
	readOnlyAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    true,
		DestructiveHint: (*bool)(nil),
		IdempotentHint:  false,
		OpenWorldHint:   (*bool)(nil),
	}

	tools := []tool.Tool{
		{
			Tool: &mcp.Tool{
				Name:         "web_search",
				Description:  "Search the web for a given query.",
				InputSchema:  webSearchInput,
				OutputSchema: searchOutput,
				Annotations:  readOnlyAnnotations,
			},
			Handler: handleWebSearch(searchTool),
		},
		{
			Tool: &mcp.Tool{
				Name:         "fetch_url",
				Description:  "Fetch and extract readable content from a URL.",
				InputSchema:  fetchURLInput,
				OutputSchema: fetchOutput,
				Annotations:  readOnlyAnnotations,
			},
			Handler: handleFetchURL(searchTool),
		},
		{
			Tool: &mcp.Tool{
				Name:         "list_providers",
				Description:  "List available search providers and the current default.",
				InputSchema:  listProvidersInput,
				OutputSchema: listProvidersOutput,
				Annotations:  readOnlyAnnotations,
			},
			Handler: handleListProviders(searchTool),
		},
	}

	if searchTool.apiKey != "" {
		tools = append(tools,
			tool.Tool{
				Tool: &mcp.Tool{
					Name:         "news_search",
					Description:  "Search for news articles.",
					InputSchema:  newsSearchInput,
					OutputSchema: searchOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleNewsSearch(searchTool),
			},
			tool.Tool{
				Tool: &mcp.Tool{
					Name:         "image_search",
					Description:  "Search for images.",
					InputSchema:  imageSearchInput,
					OutputSchema: searchOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleImageSearch(searchTool),
			},
		)
	} else {
		logger.InfoContext(ctx,
			"no Brave API key configured, news_search and image_search tools are unavailable",
		)
	}

	return tool.Response{Tools: tools}, nil
}

// ---------------------------------------------------------------------------
// Request types
// ---------------------------------------------------------------------------

type webSearchRequest struct {
	SearchTerm     string   `json:"search_term"`
	Provider       string   `json:"provider"`
	Count          int      `json:"count"`
	Offset         int      `json:"offset"`
	Cursor         string   `json:"cursor"`
	Freshness      string   `json:"freshness"`
	Country        string   `json:"country"`
	SearchLang     string   `json:"search_lang"`
	SafeSearch     string   `json:"safesearch"`
	IncludeDomains []string `json:"include_domains"`
	ExcludeDomains []string `json:"exclude_domains"`
}

type newsSearchRequest struct {
	SearchTerm string `json:"search_term"`
	Count      int    `json:"count"`
	Freshness  string `json:"freshness"`
	Country    string `json:"country"`
}

type imageSearchRequest struct {
	SearchTerm string `json:"search_term"`
	Count      int    `json:"count"`
	SafeSearch string `json:"safesearch"`
}

type fetchURLRequest struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars"`
	Cursor   string `json:"cursor"`
}

type listProvidersResponse struct {
	Providers []string `json:"providers"`
	Default   string   `json:"default"`
}
