// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package gitlab implements MCP tools for the GitLab API. Connect
// decodes the source's `connect:` map, builds a GitLab API client, and
// returns the configured tools. The two read-only tools
// (get_mr_discussions, get_mr_commits) set Annotations:
// ReadOnlyHint=true.
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "embed"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/tool"
)

//go:embed schemas/get_mr_discussions.json
var getMRDiscussionsInput json.RawMessage

//go:embed schemas/get_mr_discussions_output.json
var getMRDiscussionsOutput json.RawMessage

//go:embed schemas/get_mr_commits.json
var getMRCommitsInput json.RawMessage

//go:embed schemas/get_mr_commits_output.json
var getMRCommitsOutput json.RawMessage

const (
	// reMRURLMatches is the expected number of submatch groups from
	// reMRURL: full match, project path, MR IID.
	reMRURLMatches = 3

	// parseIntBase is the base used for parsing MR IID from URL path.
	parseIntBase = 10

	// parseIntBits is the bit size for parsing MR IID (int64).
	parseIntBits = 64

	// nameGetMRDiscussions is the MCP tool name for the get_mr_discussions tool.
	nameGetMRDiscussions = "get_mr_discussions"

	// nameGetMRCommits is the MCP tool name for the get_mr_commits tool.
	nameGetMRCommits = "get_mr_commits"
)

var (
	errTokenEmpty = errors.New("gitlab tool: token is empty")
	errMRURLParse = errors.New(
		"gitlab tool: failed to parse merge request URL, " +
			"expected format https://<host>/<group>/<project>/-/merge_requests/<iid>",
	)
	errMRURLEmpty   = errors.New("gitlab tool: merge request URL is required")
	errBaseURLParse = errors.New("gitlab tool: base_url is not a valid URL")
)

// Tool description constants for MCP tool registration.
const (
	toolDescGetMRDiscussions = "Fetch all discussion threads (comments, review threads, " +
		"and system notes) for a GitLab merge request. " +
		"Accepts the full MR URL " +
		"(e.g. https://gitlab.example.com/group/project/-/merge_requests/42). " +
		"Returns individual comments and threaded discussions, " +
		"including diff position information for code review comments."

	toolDescGetMRCommits = "Fetch all commits in a GitLab merge request. " +
		"Accepts the full MR URL. " +
		"Returns commit SHA, title, message, author, and timestamps for each commit. " +
		"Useful for extracting commit hashes to look up in a local repository."
)

// reMRURL extracts the project path and merge request IID from a
// GitLab MR URL path.
//
// Expected format: /<group>/<project>/-/merge_requests/<iid>
// With subgroups:  /<group>/<subgroup>/<project>/-/merge_requests/<iid>
var reMRURL = regexp.MustCompile(`^(.+)/-/merge_requests/(\d+)$`)

// mrURLParts holds the parsed components of a GitLab merge request URL.
type mrURLParts struct {
	ProjectPath string
	MRIID       int64
}

// parseMRURL extracts the project path and merge request IID from a
// GitLab MR URL.
func parseMRURL(rawURL string) (*mrURLParts, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse merge request URL: %w", err)
	}

	matches := reMRURL.FindStringSubmatch(parsed.Path)
	if len(matches) != reMRURLMatches {
		return nil, errMRURLParse
	}

	mrIID, err := strconv.ParseInt(matches[2], parseIntBase, parseIntBits)
	if err != nil {
		return nil, fmt.Errorf("parse merge request IID: %w", err)
	}

	// matches[1] is "/group/project" — trim leading slash.
	projectPath := strings.TrimPrefix(matches[1], "/")

	return &mrURLParts{
		ProjectPath: projectPath,
		MRIID:       mrIID,
	}, nil
}

// config holds the decoded `connect:` map for a gitlab source.
type config struct {
	Token   string
	BaseURL string
}

// decodeConnect decodes the source's `connect:` map into a config.
// Scalar string fields are decoded through decode.AsString so YAML-natural
// values (numbers, bools, null) are accepted and stringified; non-scalar
// values (maps, slices) produce a wrapped decode.ErrWrongType error so
// genuine config bugs surface as a clear message rather than a silent
// "field is empty" downstream. Errors here are wrapped by Connect as
// "gitlab: decode: <reason>"; the per-field prefix lives here so the
// final message is single-segment, not double.
func decodeConnect(connect map[string]any) (config, error) {
	var (
		cfg config
		err error
	)

	str, err := decode.AsString(connect["token"])

	switch {
	case err == nil:
		cfg.Token = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — token is required; validate() catches the empty value

	default:
		return cfg, fmt.Errorf("connect.token: %w", err)
	}

	str, err = decode.AsString(connect["base_url"])

	switch {
	case err == nil:
		cfg.BaseURL = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — base_url is optional

	default:
		return cfg, fmt.Errorf("connect.base_url: %w", err)
	}

	return cfg, nil
}

// validate checks that the decoded config is usable: the token is
// non-empty, and the optional base URL parses.
func (c config) validate() error {
	if c.Token == "" {
		return errTokenEmpty
	}

	if c.BaseURL == "" {
		return nil
	}

	parsed, err := url.Parse(c.BaseURL)
	if err != nil {
		return fmt.Errorf("parse base URL: %w", err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return errBaseURLParse
	}

	return nil
}

// Connect decodes the source's `connect:` map, builds a GitLab API
// client, and returns the configured tools. Every tool is read-only
// (the implementer only ever queries the API) so all tools set
// Annotations: ReadOnlyHint=true.
func Connect(
	_ context.Context,
	connect map[string]any,
	opts ...tool.Option,
) (tool.Response, error) {
	_ = tool.NewOptions(opts...)

	cfg, err := decodeConnect(connect)
	if err != nil {
		return tool.Response{}, fmt.Errorf("gitlab: decode: %w", err)
	}

	validateErr := cfg.validate()
	if validateErr != nil {
		return tool.Response{}, fmt.Errorf("gitlab: validate: %w", validateErr)
	}

	var gitlabOpts []gitlab.ClientOptionFunc

	if cfg.BaseURL != "" {
		gitlabOpts = append(gitlabOpts, gitlab.WithBaseURL(cfg.BaseURL))
	}

	client, err := gitlab.NewClient(cfg.Token, gitlabOpts...)
	if err != nil {
		return tool.Response{}, fmt.Errorf("gitlab: init: %w", err)
	}

	// readOnlyAnnotations is the shared annotation block for the two
	// tools below. Both are read-only — the implementer only ever
	// queries the GitLab API. OpenWorldHint defaults to true (GitLab
	// is an external service).
	readOnlyAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    true,
		DestructiveHint: (*bool)(nil),
		IdempotentHint:  false,
		OpenWorldHint:   (*bool)(nil),
	}

	return tool.Response{
		Tools: []tool.Tool{
			{
				Tool: &mcp.Tool{
					Name:         nameGetMRDiscussions,
					Description:  toolDescGetMRDiscussions,
					InputSchema:  getMRDiscussionsInput,
					OutputSchema: getMRDiscussionsOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleGetMRDiscussions(client),
			},
			{
				Tool: &mcp.Tool{
					Name:         nameGetMRCommits,
					Description:  toolDescGetMRCommits,
					InputSchema:  getMRCommitsInput,
					OutputSchema: getMRCommitsOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleGetMRCommits(client),
			},
		},
	}, nil
}

// parseAndValidateURL validates the MR URL and extracts the project
// path and MR IID.
func parseAndValidateURL(mrURL string) (*mrURLParts, error) {
	if mrURL == "" {
		return nil, errMRURLEmpty
	}

	parts, err := parseMRURL(mrURL)
	if err != nil {
		return nil, err
	}

	return parts, nil
}

// handleGetMRDiscussions returns the mcp.ToolHandler for the
// get_mr_discussions tool.
func handleGetMRDiscussions(client *gitlab.Client) mcp.ToolHandler {
	return makeMRHandler(nameGetMRDiscussions,
		func(
			ctx context.Context,
			args *GetMRDiscussionsRequest,
			parts *mrURLParts,
		) (*mcp.CallToolResult, error) {
			opts := &gitlab.ListMergeRequestDiscussionsOptions{
				ListOptions: gitlab.ListOptions{Page: args.Page, PerPage: args.PerPage},
			}

			discussions, _, err := client.Discussions.ListMergeRequestDiscussions(
				parts.ProjectPath, parts.MRIID, opts,
				gitlab.WithContext(ctx),
			)
			if err != nil {
				return nil, fmt.Errorf("list merge request discussions: %w", err)
			}

			return jsonResult(&GetMRDiscussionsResponse{Discussions: discussions})
		},
	)
}

// handleGetMRCommits returns the mcp.ToolHandler for the
// get_mr_commits tool.
func handleGetMRCommits(client *gitlab.Client) mcp.ToolHandler {
	return makeMRHandler(nameGetMRCommits,
		func(
			ctx context.Context,
			args *GetMRCommitsRequest,
			parts *mrURLParts,
		) (*mcp.CallToolResult, error) {
			opts := &gitlab.GetMergeRequestCommitsOptions{
				ListOptions: gitlab.ListOptions{Page: args.Page, PerPage: args.PerPage},
			}

			commits, _, err := client.MergeRequests.GetMergeRequestCommits(
				parts.ProjectPath, parts.MRIID, opts,
				gitlab.WithContext(ctx),
			)
			if err != nil {
				return nil, fmt.Errorf("get merge request commits: %w", err)
			}

			return jsonResult(&GetMRCommitsResponse{Commits: commits})
		},
	)
}

// mrFetchFunc is the per-tool fetch signature passed to makeMRHandler.
type mrFetchFunc[T any] func(
	ctx context.Context,
	args *T,
	parts *mrURLParts,
) (*mcp.CallToolResult, error)

// makeMRHandler builds a CallToolHandler that parses args of type T
// from the request, validates the MR URL, then calls fetch. The error
// message for the JSON-parse step uses toolName.
func makeMRHandler[T any](toolName string, fetch mrFetchFunc[T]) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args T

		err := json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse %s args: %w", toolName, err)
		}

		parts, err := parseAndValidateURL(getURLField(&args))
		if err != nil {
			return nil, err
		}

		return fetch(ctx, &args, parts)
	}
}

// getURLField returns the URL field of args. The two MR arg types
// (GetMRDiscussionsRequest, GetMRCommitsRequest) both expose a URL
// field, but Go generics cannot address it generically; a small
// type-switch would be heavier than this reflective helper. The
// pair of types here is closed and known.
func getURLField(args any) string {
	switch arg := args.(type) {
	case *GetMRDiscussionsRequest:
		return arg.URL

	case *GetMRCommitsRequest:
		return arg.URL
	}

	return ""
}

// jsonResult marshals payload to JSON and wraps it in a CallToolResult
// with a single TextContent. Returns an error if the marshal fails.
func jsonResult(payload any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}

	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}

	// Per the MCP spec, StructuredContent must marshal to a JSON object.
	// The unmarshal to map[string]any succeeds only when data is a valid
	// JSON object (a JSON array, primitive, or malformed value returns
	// an error). Those non-object cases should be conveyed via Content only.
	var probe map[string]any
	if json.Unmarshal(data, &probe) == nil {
		result.StructuredContent = json.RawMessage(data)
	}

	return result, nil
}
