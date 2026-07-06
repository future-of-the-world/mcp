// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package tracker implements MCP tools for Yandex Tracker + Wiki.
// Connect decodes the source's `connect:` map, builds an API client
// (with optional OAuth device-flow token exchange), and returns the
// configured tools. Each returned tool sets mcp.ToolAnnotations to
// describe its semantics: the nine read-only tools (search_*, get_*,
// get_wiki_*, get_fields, my_report) set ReadOnlyHint: true; the two
// create_* tools (create_issue, create_comment) set DestructiveHint:
// new(false) and IdempotentHint: false because they only add new
// resources; the two update_* tools (update_issue, update_comment)
// set DestructiveHint: new(true) and IdempotentHint: true because
// they overwrite the same resource.
package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "embed"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/tool"
)

//go:embed schemas/search_issues.json
var searchIssuesInput json.RawMessage

//go:embed schemas/search_issues_output.json
var searchIssuesOutput json.RawMessage

//go:embed schemas/get_issue.json
var getIssueInput json.RawMessage

//go:embed schemas/get_issue_output.json
var getIssueOutput json.RawMessage

//go:embed schemas/get_comments.json
var getCommentsInput json.RawMessage

//go:embed schemas/get_comments_output.json
var getCommentsOutput json.RawMessage

//go:embed schemas/get_wiki_page.json
var getWikiPageInput json.RawMessage

//go:embed schemas/get_wiki_page_output.json
var getWikiPageOutput json.RawMessage

//go:embed schemas/get_wiki_subpages.json
var getWikiSubpagesInput json.RawMessage

//go:embed schemas/get_wiki_subpages_output.json
var getWikiSubpagesOutput json.RawMessage

//go:embed schemas/list_queues.json
var listQueuesInput json.RawMessage

//go:embed schemas/list_queues_output.json
var listQueuesOutput json.RawMessage

//go:embed schemas/get_fields.json
var getFieldsInput json.RawMessage

//go:embed schemas/get_fields_output.json
var getFieldsOutput json.RawMessage

//go:embed schemas/get_links.json
var getLinksInput json.RawMessage

//go:embed schemas/get_links_output.json
var getLinksOutput json.RawMessage

//go:embed schemas/my_report.json
var myReportInput json.RawMessage

//go:embed schemas/my_report_output.json
var myReportOutput json.RawMessage

//go:embed schemas/create_issue.json
var createIssueInput json.RawMessage

//go:embed schemas/create_issue_output.json
var createIssueOutput json.RawMessage

//go:embed schemas/update_issue.json
var updateIssueInput json.RawMessage

//go:embed schemas/update_issue_output.json
var updateIssueOutput json.RawMessage

//go:embed schemas/create_comment.json
var createCommentInput json.RawMessage

//go:embed schemas/create_comment_output.json
var createCommentOutput json.RawMessage

//go:embed schemas/update_comment.json
var updateCommentInput json.RawMessage

//go:embed schemas/update_comment_output.json
var updateCommentOutput json.RawMessage

// Tool description constants for MCP tool registration.
const (
	toolDescSearch = "Search Yandex Tracker issues by queue, filter, or query language. " +
		"Use get_fields tool first to discover valid filter keys and field values. " +
		"Common filter keys: queue, status, assignee, type, priority, tags, sprint. " +
		"For the filter parameter, keys must match Tracker field IDs exactly " +
		"(e.g. 'assignee', NOT 'issue'). For query parameter, use Tracker query " +
		"language (e.g. 'Assignee: user123 Updated: >=2025-01-01'). " +
		"Do NOT combine queue, keys, filter, and query."

	toolDescGetIssue = "Get detailed information about a single Yandex Tracker issue " +
		"by its key or ID, including status, assignee, description, and all metadata"

	toolDescGetComments = "Get all comments for a Yandex Tracker issue"

	toolDescGetWikiPage = "Get a Yandex Wiki page content by its slug (URL path) or numeric page ID"

	toolDescGetWikiSubpages = "List subpages (descendants) of a Yandex Wiki page"

	toolDescListQueues = "List all Yandex Tracker queues available to the authenticated user"

	toolDescGetFields = "Get available Yandex Tracker issue fields. Returns a list of all " +
		"fields (standard and custom) that can be used as filter keys in search_issues " +
		"(filter parameter), as query language fields, and as values for the fields " +
		"parameter. Call this tool to discover valid field names before constructing " +
		"search queries."

	toolDescReport = "Generate a structured retrospective report about work done by " +
		"a specific user in a given time period. " +
		"Aggregates issues, comments, metrics, and optional wiki context."

	toolDescGetLinks = "Get all links (relations) for a Yandex Tracker issue"

	toolDescCreateIssue = "Create a new issue (task, bug, epic, etc.) in a Yandex Tracker queue. " +
		"Requires summary and queue. Optionally set description, type, " +
		"priority, assignee, tags, deadline, and more."

	toolDescUpdateIssue = "Update an existing Yandex Tracker issue by key or ID. " +
		"Only fields explicitly provided will be changed. " +
		"Supports tags add/remove operations. " +
		"Use version parameter for conflict resolution."

	toolDescCreateComment = "Create a new comment on a Yandex Tracker issue. " +
		"Requires the issue key or ID and the comment text."

	toolDescUpdateComment = "Update the text of an existing comment on a Yandex Tracker issue. " +
		"Requires the issue key or ID, comment ID, and new text."
)

// config holds the decoded `connect:` map for a tracker source.
type config struct {
	Token          string
	ClientID       string
	ClientSecret   string
	OAuthTokenURL  string
	OAuthDeviceURL string
	OrgID          string
	BaseURL        string
	WikiBaseURL    string
	CloudOrg       bool
}

// decodeConnect decodes the source's `connect:` map into a config.
// Scalar string fields are decoded through decode.AsString so YAML-natural
// values (numbers, bools, null) are accepted and stringified; non-scalar
// values (maps, slices) produce a wrapped decode.ErrWrongType error so
// genuine config bugs surface as a clear message rather than a silent
// "field is empty" downstream. Errors here are wrapped by Connect as
// "tracker: decode: <reason>"; the per-field prefix lives here so the
// final message is single-segment, not double.
func decodeConnect(connect map[string]any) (config, error) {
	var cfg config

	stringFields := []struct {
		key string
		dst *string
	}{
		{"token", &cfg.Token},
		{"client_id", &cfg.ClientID},
		{"client_secret", &cfg.ClientSecret},
		{"oauth_token_url", &cfg.OAuthTokenURL},
		{"oauth_device_url", &cfg.OAuthDeviceURL},
		{"org_id", &cfg.OrgID},
		{"base_url", &cfg.BaseURL},
		{"wiki_base_url", &cfg.WikiBaseURL},
	}

	for _, field := range stringFields {
		str, sErr := decode.AsString(connect[field.key])

		switch {
		case sErr == nil:
			*field.dst = str

		case errors.Is(sErr, decode.ErrNotSet):
			// skip — key absent or null; downstream validation will catch required fields

		default:
			return cfg, fmt.Errorf("connect.%s: %w", field.key, sErr)
		}
	}

	if raw, ok := connect["cloud_org"]; ok {
		val, ok := raw.(bool)
		if !ok {
			return cfg, fmt.Errorf("%w, got %T", errCloudOrgMustBeBool, raw)
		}

		cfg.CloudOrg = val
	}

	return cfg, nil
}

// validate checks that the decoded config is usable: org_id must be set
// and either a token or both client_id and client_secret.
func (c *config) validate() error {
	if c.OrgID == "" {
		return errOrgIDEmpty
	}

	if c.Token == "" && c.ClientID == "" {
		return errTokenEmpty
	}

	if c.Token == "" && c.ClientSecret == "" {
		return errClientSecretEmpty
	}

	return nil
}

// Connect decodes the source's `connect:` map, builds an API client,
// and returns the configured tools. When a token is provided directly
// it is used as-is; otherwise the OAuth device-flow is invoked with
// the client_id and client_secret.
//
// Each tool's mcp.ToolAnnotations encodes its read-only / mutating
// semantics; see the package doc for the per-tool mapping.
func Connect(
	ctx context.Context,
	connect map[string]any,
	opts ...tool.Option,
) (tool.Response, error) {
	_ = tool.NewOptions(opts...)

	cfg, err := decodeConnect(connect)
	if err != nil {
		return tool.Response{}, fmt.Errorf("tracker: decode: %w", err)
	}

	validateErr := cfg.validate()
	if validateErr != nil {
		return tool.Response{}, fmt.Errorf("tracker: validate: %w", validateErr)
	}

	token := cfg.Token

	// If no direct token but client credentials are provided, use the
	// OAuth device flow.
	if token == "" {
		oauthCfg := oauthConfig{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			TokenURL:     parseOAuthURL(cfg.OAuthTokenURL, defaultOAuthTokenURL),
			DeviceURL:    parseOAuthURL(cfg.OAuthDeviceURL, defaultOAuthDeviceURL),
		}

		oauthToken, oauthErr := exchangeDeviceCode(ctx, oauthCfg)
		if oauthErr != nil {
			return tool.Response{}, fmt.Errorf("tracker: OAuth: %w", oauthErr)
		}

		token = oauthToken
	}

	cli, err := newClient(clientConfig{
		Token:       token,
		OrgID:       cfg.OrgID,
		BaseURL:     cfg.BaseURL,
		WikiBaseURL: cfg.WikiBaseURL,
		CloudOrg:    cfg.CloudOrg,
	})
	if err != nil {
		return tool.Response{}, fmt.Errorf("tracker: init: %w", err)
	}

	// readOnlyAnnotations is the shared annotation block for the nine
	// read-only tools below. DestructiveHint/IdempotentHint are
	// irrelevant when ReadOnlyHint is true; OpenWorldHint defaults to
	// true and is left unset per the per-type Annotations policy.
	readOnlyAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    true,
		DestructiveHint: (*bool)(nil), // irrelevant when ReadOnly
		IdempotentHint:  false,        // irrelevant when ReadOnly
		OpenWorldHint:   (*bool)(nil), // default true; Tracker is a closed world to us
	}

	return tool.Response{
		Tools: []tool.Tool{
			{
				Tool: &mcp.Tool{
					Name:         "search_issues",
					Description:  toolDescSearch,
					InputSchema:  searchIssuesInput,
					OutputSchema: searchIssuesOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleSearchIssues(cli),
			},
			{
				Tool: &mcp.Tool{
					Name:         "get_issue",
					Description:  toolDescGetIssue,
					InputSchema:  getIssueInput,
					OutputSchema: getIssueOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleGetIssue(cli),
			},
			{
				Tool: &mcp.Tool{
					Name:         "get_links",
					Description:  toolDescGetLinks,
					InputSchema:  getLinksInput,
					OutputSchema: getLinksOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleGetLinks(cli),
			},
			{
				Tool: &mcp.Tool{
					Name:         "get_comments",
					Description:  toolDescGetComments,
					InputSchema:  getCommentsInput,
					OutputSchema: getCommentsOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleGetComments(cli),
			},
			{
				Tool: &mcp.Tool{
					Name:         "get_fields",
					Description:  toolDescGetFields,
					InputSchema:  getFieldsInput,
					OutputSchema: getFieldsOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleGetFields(),
			},
			{
				Tool: &mcp.Tool{
					Name:         "get_wiki_page",
					Description:  toolDescGetWikiPage,
					InputSchema:  getWikiPageInput,
					OutputSchema: getWikiPageOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleGetWikiPage(cli),
			},
			{
				Tool: &mcp.Tool{
					Name:         "get_wiki_subpages",
					Description:  toolDescGetWikiSubpages,
					InputSchema:  getWikiSubpagesInput,
					OutputSchema: getWikiSubpagesOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleGetWikiSubpages(cli),
			},
			{
				Tool: &mcp.Tool{
					Name:         "list_queues",
					Description:  toolDescListQueues,
					InputSchema:  listQueuesInput,
					OutputSchema: listQueuesOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleListQueues(cli),
			},
			{
				Tool: &mcp.Tool{
					Name:         "my_report",
					Description:  toolDescReport,
					InputSchema:  myReportInput,
					OutputSchema: myReportOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleReport(cli),
			},
			{
				Tool: &mcp.Tool{
					Name:         "create_issue",
					Description:  toolDescCreateIssue,
					InputSchema:  createIssueInput,
					OutputSchema: createIssueOutput,
					Annotations: &mcp.ToolAnnotations{
						Title:           "",
						ReadOnlyHint:    false,
						DestructiveHint: new(false),
						IdempotentHint:  false,
						OpenWorldHint:   (*bool)(nil), // default true
					},
				},
				Handler: handleCreateIssue(cli),
			},
			{
				Tool: &mcp.Tool{
					Name:         "update_issue",
					Description:  toolDescUpdateIssue,
					InputSchema:  updateIssueInput,
					OutputSchema: updateIssueOutput,
					Annotations: &mcp.ToolAnnotations{
						Title:           "",
						ReadOnlyHint:    false,
						DestructiveHint: new(true),
						IdempotentHint:  true,
						OpenWorldHint:   (*bool)(nil), // default true
					},
				},
				Handler: handleUpdateIssue(cli),
			},
			{
				Tool: &mcp.Tool{
					Name:         "create_comment",
					Description:  toolDescCreateComment,
					InputSchema:  createCommentInput,
					OutputSchema: createCommentOutput,
					Annotations: &mcp.ToolAnnotations{
						Title:           "",
						ReadOnlyHint:    false,
						DestructiveHint: new(false),
						IdempotentHint:  false,
						OpenWorldHint:   (*bool)(nil), // default true
					},
				},
				Handler: handleCreateComment(cli),
			},
			{
				Tool: &mcp.Tool{
					Name:         "update_comment",
					Description:  toolDescUpdateComment,
					InputSchema:  updateCommentInput,
					OutputSchema: updateCommentOutput,
					Annotations: &mcp.ToolAnnotations{
						Title:           "",
						ReadOnlyHint:    false,
						DestructiveHint: new(true),
						IdempotentHint:  true,
						OpenWorldHint:   (*bool)(nil), // default true
					},
				},
				Handler: handleUpdateComment(cli),
			},
		},
	}, nil
}
