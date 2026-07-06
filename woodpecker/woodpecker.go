// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package woodpecker implements the Woodpecker CI/CD source for the MCP
// server. It wraps the generated woodpeckerapi client and exposes a
// focused subset of the Woodpecker REST API as MCP tools.
//
// The seven tools form a small "investigate a failed pipeline" loop:
// the agent can list the user's repos, list pipelines with a status
// filter, fetch a single pipeline to see its workflows and steps, and
// pull the decoded logs of the failed step — all without the user
// copy-pasting CI output into the session.
//
//	woodpecker_list_repos        (read-only) → discover repo_id from full_name
//	woodpecker_list_pipelines    (read-only) → find a failed pipeline by status/branch
//	woodpecker_get_pipeline      (read-only) → full Pipeline incl. workflows[].children[]
//	woodpecker_get_step_logs     (read-only) → decoded log entries for one step
//	woodpecker_restart_pipeline  (mutating)  → restart an existing pipeline
//	woodpecker_launch_pipeline   (mutating)  → trigger a new manual pipeline
//	woodpecker_cancel_pipeline   (mutating)  → cancel a running pipeline
//
// All seven share the same `connect:` map: a Woodpecker personal access
// token and the Woodpecker API base URL. Both fields are required:
// `api_url` must be set explicitly so the source never silently talks
// to a default host on the user's behalf. The token is injected as a
// request editor on the generated client so individual handlers do
// not need to plumb it through.
package woodpecker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "embed"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/tool"
)

// --- Embedded input/output JSON Schemas ---

//go:embed schemas/list_repos.json
var listReposInput json.RawMessage

//go:embed schemas/list_repos_output.json
var listReposOutput json.RawMessage

//go:embed schemas/list_pipelines.json
var listPipelinesInput json.RawMessage

//go:embed schemas/list_pipelines_output.json
var listPipelinesOutput json.RawMessage

//go:embed schemas/get_pipeline.json
var getPipelineInput json.RawMessage

//go:embed schemas/get_pipeline_output.json
var getPipelineOutput json.RawMessage

//go:embed schemas/get_step_logs.json
var getStepLogsInput json.RawMessage

//go:embed schemas/get_step_logs_output.json
var getStepLogsOutput json.RawMessage

//go:embed schemas/restart_pipeline.json
var restartPipelineInput json.RawMessage

//go:embed schemas/restart_pipeline_output.json
var restartPipelineOutput json.RawMessage

//go:embed schemas/launch_pipeline.json
var launchPipelineInput json.RawMessage

//go:embed schemas/launch_pipeline_output.json
var launchPipelineOutput json.RawMessage

//go:embed schemas/cancel_pipeline.json
var cancelPipelineInput json.RawMessage

//go:embed schemas/cancel_pipeline_output.json
var cancelPipelineOutput json.RawMessage

// --- Constants and sentinel errors ---

// bearerPrefix is the standard Woodpecker Authorization header value
// prefix. Used by the client wrapper when injecting the token via
// WithRequestEditorFn.
const bearerPrefix = "Bearer "

// woodpeckerClientTimeout is the HTTP timeout for all Woodpecker API
// requests issued by the generated client.
const woodpeckerClientTimeout = 10 * time.Second

// unknownLogKind is the kind string returned for LogEntryType values
// outside the five documented enum cases (stdout, stderr, exit_code,
// metadata, progress). The Woodpecker enum is open-coded in the
// spec, so this defensive default keeps us forward-compatible.
const unknownLogKind = "unknown"

var (
	errAPIURLRequired         = errors.New("woodpecker tool: api_url is required")
	errAPIURLInvalid          = errors.New("woodpecker tool: API URL is invalid")
	errTokenEmpty             = errors.New("woodpecker tool: token is empty")
	errRepoIDRequired         = errors.New("woodpecker tool: repo_id is required and must be > 0")
	errPipelineNumberRequired = errors.New(
		"woodpecker tool: pipeline_number is required and must be > 0")
	errStepIDRequired   = errors.New("woodpecker tool: step_id is required and must be > 0")
	errPipelineNotFound = errors.New("woodpecker tool: pipeline not found in response body")
)

// containsKey reports whether the given raw JSON object contains a
// top-level key. Used by handleListRepos to distinguish an absent
// `all` (treated as nil) from an explicit `all: false` (treated as
// &false so the server still receives the parameter).
func containsKey(raw json.RawMessage, key string) bool {
	var obj map[string]json.RawMessage

	err := json.Unmarshal(raw, &obj)
	if err != nil {
		return false
	}

	_, has := obj[key]

	return has
}

// --- Configuration decoding ---

// config holds the decoded `connect:` map for a woodpecker source.
// Both fields are required: Token is the personal access token,
// APIURL is the Woodpecker server's base URL (e.g.
// "https://ci.example.com/api"). There is no default for either — the
// source must not silently talk to a host the user did not name.
type config struct {
	Token  string
	APIURL string
}

// decodeConnect decodes the source's `connect:` map into a config.
// Scalar string fields are decoded through decode.AsString so YAML-natural
// values (numbers, bools, null) are accepted and stringified; non-scalar
// values produce a wrapped decode.ErrWrongType error so genuine config
// bugs surface as a clear message rather than a silent "field is empty"
// downstream. Both fields are required; emptiness is enforced by
// validate. Errors here are wrapped by Connect as
// "woodpecker: decode: <reason>"; the per-field prefix lives here so
// the final message is single-segment, not double.
func decodeConnect(connect map[string]any) (config, error) {
	var cfg config

	str, err := decode.AsString(connect["token"])

	switch {
	case err == nil:
		cfg.Token = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — key absent or null; validate fills in the "token is empty" error

	default:
		return cfg, fmt.Errorf("connect.token: %w", err)
	}

	str, err = decode.AsString(connect["api_url"])

	switch {
	case err == nil:
		cfg.APIURL = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — key absent or null; validate fills in the "api_url is required" error

	default:
		return cfg, fmt.Errorf("connect.api_url: %w", err)
	}

	return cfg, nil
}

func (c *config) validate() error {
	if c.Token == "" {
		return errTokenEmpty
	}

	if c.APIURL == "" {
		return errAPIURLRequired
	}

	_, err := url.Parse(c.APIURL)
	if err != nil {
		return fmt.Errorf("%w: %w", errAPIURLInvalid, err)
	}

	return nil
}

// --- Tool description ---

// investigationWorkflow is the package-level narrative explaining how
// the seven tools are meant to be used together. It is prepended to
// each tool's description so the agent sees it regardless of which
// tool it discovers first via `tools.list`.
const investigationWorkflow = "Woodpecker CI/CD investigation workflow:\n" +
	"1. woodpecker_list_repos (once) → discover repo_id from a known full_name.\n" +
	"2. woodpecker_list_pipelines({repo_id, status: \"failure\"}) → find the most " +
	"recent failed pipeline number.\n" +
	"3. woodpecker_get_pipeline({repo_id, pipeline_number}) → get the full " +
	"Pipeline including workflows[].children[].\n" +
	"4. Pick the failed step from workflows[].children[] (state == \"failure\" " +
	"or exit_code != 0).\n" +
	"5. woodpecker_get_step_logs({repo_id, pipeline_number, step_id}) → decoded " +
	"log entries for that step.\n" +
	"6. Diagnose from the logs; then either woodpecker_restart_pipeline (if " +
	"flaky) or report back to the user.\n" +
	"This loop removes the need for the user to copy-paste CI output into the " +
	"session."

// --- Connect entry point ---

// Connect decodes the source's `connect:` map, builds a Woodpecker
// client, and returns the seven woodpecker_* tools. The read-only
// tools (list_repos, list_pipelines, get_pipeline, get_step_logs)
// set ReadOnlyHint=true and OpenWorldHint=true. The mutating tools
// set IdempotentHint/DestructiveHint to match their semantics — see
// the tool-by-tool table in the package doc.
func Connect(_ context.Context, connect map[string]any, _ ...tool.Option) (tool.Response, error) {
	cfg, err := decodeConnect(connect)
	if err != nil {
		return tool.Response{}, fmt.Errorf("woodpecker: decode: %w", err)
	}

	validateErr := cfg.validate()
	if validateErr != nil {
		return tool.Response{}, fmt.Errorf("woodpecker: validate: %w", validateErr)
	}

	client, err := newWoodpeckerClient(cfg)
	if err != nil {
		return tool.Response{}, fmt.Errorf("woodpecker: client: %w", err)
	}

	// OpenWorldHint is *bool: nil means "absent" and is the default
	// per the MCP spec, but every woodpecker tool talks to a remote CI
	// so the explicit `new(true)` ("yes, this is an open-world tool")
	// is correct. ReadOnlyHint and IdempotentHint are bool values;
	// DestructiveHint is *bool for the same reason.
	readOnlyAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    true,
		DestructiveHint: (*bool)(nil),
		IdempotentHint:  false,
		OpenWorldHint:   new(true),
	}

	// restartAnnotations marks the restart tool as non-destructive and
	// idempotent: re-restarting the same pipeline with the same
	// overrides produces a new run in pathological cases, but the safe
	// default for the model is to treat it as idempotent.
	restartAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    false,
		DestructiveHint: new(false),
		IdempotentHint:  true,
		OpenWorldHint:   new(true),
	}

	// launchAnnotations marks the launch tool as non-destructive and
	// non-idempotent: each call creates a new pipeline run.
	launchAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    false,
		DestructiveHint: new(false),
		IdempotentHint:  false,
		OpenWorldHint:   new(true),
	}

	// cancelAnnotations marks the cancel tool as destructive but
	// idempotent: canceling an already-canceled pipeline is a no-op
	// on the server, so the model can retry safely.
	cancelAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    false,
		DestructiveHint: new(true),
		IdempotentHint:  true,
		OpenWorldHint:   new(true),
	}

	return tool.Response{
		Tools: []tool.Tool{
			{
				Tool: &mcp.Tool{
					Name:         "list_repos",
					Description:  investigationWorkflow + listReposDescription,
					InputSchema:  listReposInput,
					OutputSchema: listReposOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleListRepos(client),
			},
			{
				Tool: &mcp.Tool{
					Name:         "list_pipelines",
					Description:  investigationWorkflow + listPipelinesDescription,
					InputSchema:  listPipelinesInput,
					OutputSchema: listPipelinesOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleListPipelines(client),
			},
			{
				Tool: &mcp.Tool{
					Name:         "get_pipeline",
					Description:  investigationWorkflow + getPipelineDescription,
					InputSchema:  getPipelineInput,
					OutputSchema: getPipelineOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleGetPipeline(client),
			},
			{
				Tool: &mcp.Tool{
					Name:         "get_step_logs",
					Description:  investigationWorkflow + getStepLogsDescription,
					InputSchema:  getStepLogsInput,
					OutputSchema: getStepLogsOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleGetStepLogs(client),
			},
			{
				Tool: &mcp.Tool{
					Name:         "restart_pipeline",
					Description:  investigationWorkflow + restartPipelineDescription,
					InputSchema:  restartPipelineInput,
					OutputSchema: restartPipelineOutput,
					Annotations:  restartAnnotations,
				},
				Handler: handleRestartPipeline(client),
			},
			{
				Tool: &mcp.Tool{
					Name:         "launch_pipeline",
					Description:  investigationWorkflow + launchPipelineDescription,
					InputSchema:  launchPipelineInput,
					OutputSchema: launchPipelineOutput,
					Annotations:  launchAnnotations,
				},
				Handler: handleLaunchPipeline(client),
			},
			{
				Tool: &mcp.Tool{
					Name:         "cancel_pipeline",
					Description:  investigationWorkflow + cancelPipelineDescription,
					InputSchema:  cancelPipelineInput,
					OutputSchema: cancelPipelineOutput,
					Annotations:  cancelAnnotations,
				},
				Handler: handleCancelPipeline(client),
			},
		},
	}, nil
}

// Per-tool description suffixes. Kept as named consts (rather than
// inline string literals) so the lll line-length rule is happy and
// the per-tool phrasing is editable in one place.
const (
	listReposDescription = "\n\nList the repositories the Woodpecker " +
		"token can see. Use this once to discover the integer repo_id " +
		"for a known full_name, then pass that id to the pipeline tools."

	listPipelinesDescription = "\n\nList pipelines for a repository. " +
		"Filter by status (e.g. \"failure\", \"success\", \"running\") or " +
		"branch to narrow down. Returns lightweight pipeline summaries."

	getPipelineDescription = "\n\nGet one pipeline by repo_id and " +
		"pipeline_number, including its workflows[].children[] " +
		"(the steps). Use the returned children to find the id of " +
		"a failed step, then call get_step_logs."

	getStepLogsDescription = "\n\nFetch the decoded log entries for a " +
		"single step. text is a UTF-8 string per entry; kind is one " +
		"of stdout, stderr, exit_code, metadata, progress. This is the " +
		"tool that lets the agent read CI output without the user " +
		"copy-pasting it."

	restartPipelineDescription = "\n\nRestart an existing pipeline. " +
		"Optional event and deploy_to overrides change the trigger " +
		"type and target environment."

	launchPipelineDescription = "\n\nTrigger a new manual pipeline for " +
		"a repository. branch defaults to the repo's default_branch " +
		"on the server; variables are passed through as CI_* env vars."

	cancelPipelineDescription = "\n\nCancel a running pipeline. " +
		"Idempotent: canceling an already-canceled or finished " +
		"pipeline is a no-op on the server."
)
