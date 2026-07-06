// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:wsl_v5 // client wraps cluster assignments
package woodpecker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"go.amidman.dev/mcp/woodpeckerapi"
)

// --- Client wrapper ---

// woodpeckerClient is an HTTP client for the Woodpecker CI REST API.
// It is a thin wrapper around the generated *woodpeckerapi.ClientWithResponses
// that:
//
//   - injects the personal access token as the Authorization header on
//     every request via woodpeckerapi.WithRequestEditorFn, so individual
//     methods do not need to plumb the token through;
//   - converts the generated []woodpeckerapi.LogEntry returned by the
//     logs endpoint into a model-friendly []logEntry with UTF-8 text
//     and a stable `kind` string per LogEntryType;
//   - maps non-2xx responses into wrapped Go errors so handlers can
//     surface them to the model without parsing HTTP status codes.
type woodpeckerClient struct {
	api    *woodpeckerapi.ClientWithResponses
	apiURL string
	token  string
}

// newWoodpeckerClient constructs a Woodpecker client for the given
// config. The token is injected as a request editor; NewClientWithResponses
// only fails if a ClientOption returns an error, which WithRequestEditorFn
// and WithHTTPClient do not, so this constructor is effectively infallible
// and the error path is unreachable in practice.
func newWoodpeckerClient(cfg config) (*woodpeckerClient, error) {
	api, err := woodpeckerapi.NewClientWithResponses(
		cfg.APIURL,
		woodpeckerapi.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", bearerPrefix+cfg.Token)

			return nil
		}),
		woodpeckerapi.WithHTTPClient(&http.Client{Timeout: woodpeckerClientTimeout}),
	)
	if err != nil {
		return nil, fmt.Errorf("woodpecker client: %w", err)
	}

	return &woodpeckerClient{
		api:    api,
		apiURL: cfg.APIURL,
		token:  cfg.Token,
	}, nil
}

// --- Model-facing types ---

// repoSummary is the model-facing shape of a single repository in
// list_repos output. We keep only the fields the model needs to
// identify a repo and surface its recent activity.
type repoSummary struct {
	ID           int              `json:"id"`
	Owner        string           `json:"owner"`
	Name         string           `json:"name"`
	FullName     string           `json:"full_name"`
	Branch       string           `json:"branch,omitempty"`
	Active       bool             `json:"active"`
	Private      bool             `json:"private"`
	LastPipeline *pipelineSummary `json:"last_pipeline,omitempty"`
}

// pipelineSummary is the lightweight shape used by list_pipelines and
// nested inside list_repos (last_pipeline). It deliberately omits the
// per-step workflows[] tree — get_pipeline returns the full record.
type pipelineSummary struct {
	Number   int    `json:"number"`
	Event    string `json:"event,omitempty"`
	Status   string `json:"status"`
	Branch   string `json:"branch,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Message  string `json:"message,omitempty"`
	Author   string `json:"author,omitempty"`
	Started  int64  `json:"started,omitempty"`
	Finished int64  `json:"finished,omitempty"`
	Duration int64  `json:"duration,omitempty"`
	Created  int64  `json:"created,omitempty"`
}

// pipelineDetail is the full pipeline shape returned by get_pipeline,
// restart_pipeline, launch_pipeline, and cancel_pipeline. workflows[]
// is included so the agent can find the id of a failed step.
type pipelineDetail struct {
	Number    int            `json:"number"`
	Event     string         `json:"event,omitempty"`
	Status    string         `json:"status"`
	Branch    string         `json:"branch,omitempty"`
	Commit    string         `json:"commit,omitempty"`
	Message   string         `json:"message,omitempty"`
	Author    string         `json:"author,omitempty"`
	Started   int64          `json:"started,omitempty"`
	Finished  int64          `json:"finished,omitempty"`
	Duration  int64          `json:"duration,omitempty"`
	Created   int64          `json:"created,omitempty"`
	Workflows []workflowWrap `json:"workflows,omitempty"`
}

// workflowWrap is a Woodpecker workflow (= one of the parallel paths
// declared in .woodpecker.yaml) and its child steps. The agent reads
// this to find the id of the failed step.
type workflowWrap struct {
	ID       int        `json:"id"`
	Name     string     `json:"name"`
	State    string     `json:"state"`
	Children []stepWrap `json:"children,omitempty"`
}

// stepWrap is a single step in a pipeline workflow. The agent uses
// `id` as the step_id argument to get_step_logs.
type stepWrap struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	State    string `json:"state"`
	ExitCode int    `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

// logEntry is the model-facing shape of a single log line returned by
// get_step_logs. text is a UTF-8 string (decoded from the base64-encoded
// `data` field the API returns; the generated LogEntry.Data is *[]byte
// because the spec marks the field as `format: byte`, so the JSON layer
// does the base64 decode for us); kind is a stable string per LogEntryType.
type logEntry struct {
	Line int    `json:"line,omitempty"`
	Time int64  `json:"time,omitempty"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// listPipelinesOpts bundles the optional filters for GET
// /repos/{repo_id}/pipelines. Grouping them into a struct keeps the
// client.listPipelines signature under the revive argument-limit
// (max 4 args) — repoID + ctx + opts + return is exactly 4.
type listPipelinesOpts struct {
	Page    *int
	PerPage *int
	Before  *string
	After   *string
	Branch  *string
	Event   *string
	Status  *string
	Ref     *string
}

// --- Log decoding ---

// logKind maps the LogEntryType integer to a stable string. The five
// documented values map to stdout, stderr, exit_code, metadata, and
// progress; anything else is "unknown" (the enum is open-coded in the
// spec so future values land here safely).
func logKind(t woodpeckerapi.LogEntryType) string {
	switch t {
	case woodpeckerapi.LogEntryStdout:
		return "stdout"
	case woodpeckerapi.LogEntryStderr:
		return "stderr"
	case woodpeckerapi.LogEntryExitCode:
		return "exit_code"
	case woodpeckerapi.LogEntryMetadata:
		return "metadata"
	case woodpeckerapi.LogEntryProgress:
		return "progress"
	default:
		return unknownLogKind
	}
}

// decodeLogEntries converts the generated []woodpeckerapi.LogEntry
// into the model-facing []logEntry slice. text is a UTF-8 decode of
// data; kind is a stable string per LogEntryType. Lives next to the
// API call so unit tests can verify the decoding without spinning up
// a handler.
func decodeLogEntries(entries []woodpeckerapi.LogEntry) []logEntry {
	out := make([]logEntry, 0, len(entries))

	for index := range entries {
		entry := decodeLogEntry(&entries[index])
		out = append(out, entry)
	}

	return out
}

// decodeLogEntry converts a single LogEntry. Pulled out of the loop
// body so the per-entry decoding (which is otherwise a chain of
// nested if/append) stays under the gocognit budget.
func decodeLogEntry(entry *woodpeckerapi.LogEntry) logEntry {
	out := logEntry{Line: 0, Time: 0, Kind: unknownLogKind, Text: ""}
	if entry == nil {
		return out
	}

	if entry.Line != nil {
		out.Line = *entry.Line
	}

	if entry.Time != nil {
		out.Time = int64(*entry.Time)
	}

	if entry.Type != nil {
		out.Kind = logKind(*entry.Type)
	}

	if entry.Data != nil {
		out.Text = decodeLogBytes(*entry.Data)
	}

	return out
}

// decodeLogBytes turns the API's raw byte sequence (the JSON-decoded
// form of the base64-encoded `data` field; the spec marks the field
// as `format: byte`, which `oapi-codegen` emits as `*[]byte`) into a
// UTF-8 string. Binary output is permitted to mojibake; this matches
// what the Woodpecker web UI shows for non-text steps.
func decodeLogBytes(data []byte) string {
	return string(data)
}

// --- Per-tool client methods ---

// listRepos calls GET /user/repos and returns a []repoSummary.
// all and name are the optional query filters from the OpenAPI spec.
func (c *woodpeckerClient) listRepos(
	ctx context.Context,
	all *bool,
	name *string,
) ([]repoSummary, error) {
	params := &woodpeckerapi.GetUserReposParams{
		Authorization: bearerPrefix + c.token,
		All:           all,
		Name:          name,
	}

	resp, err := c.api.GetUserReposWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("woodpecker api GET /user/repos: %w", err)
	}

	status := resp.StatusCode()
	if status != http.StatusOK {
		return nil, fmt.Errorf("woodpecker api GET /user/repos: status %d: %s",
			status, string(resp.Body))
	}

	repos := make([]repoSummary, 0, jsonArrayLen(resp.JSON200))
	for index := range *resp.JSON200 {
		repos = append(repos, toRepoSummary(&(*resp.JSON200)[index]))
	}

	return repos, nil
}

// listPipelines calls GET /repos/{repo_id}/pipelines with the given
// filters and returns lightweight []pipelineSummary.
func (c *woodpeckerClient) listPipelines(
	ctx context.Context, repoID int, opts listPipelinesOpts,
) ([]pipelineSummary, error) {
	params := &woodpeckerapi.GetReposRepoIdPipelinesParams{
		Authorization: bearerPrefix + c.token,
		Page:          opts.Page,
		PerPage:       opts.PerPage,
		Before:        opts.Before,
		After:         opts.After,
		Branch:        opts.Branch,
		Event:         opts.Event,
		Status:        opts.Status,
		Ref:           opts.Ref,
	}

	resp, err := c.api.GetReposRepoIdPipelinesWithResponse(ctx, repoID, params)
	if err != nil {
		return nil, fmt.Errorf("woodpecker api GET /repos/%d/pipelines: %w", repoID, err)
	}

	status := resp.StatusCode()
	if status != http.StatusOK {
		return nil, fmt.Errorf("woodpecker api GET /repos/%d/pipelines: status %d: %s",
			repoID, status, string(resp.Body))
	}

	out := make([]pipelineSummary, 0, jsonArrayLen(resp.JSON200))
	for index := range *resp.JSON200 {
		out = append(out, toPipelineSummary(&(*resp.JSON200)[index]))
	}

	return out, nil
}

// getPipeline calls GET /repos/{repo_id}/pipelines/{pipeline_number} and
// returns a full pipelineDetail including workflows[].children[].
func (c *woodpeckerClient) getPipeline(
	ctx context.Context, repoID, pipelineNumber int,
) (*pipelineDetail, error) {
	resp, err := c.api.GetReposRepoIdPipelinesPipelineNumberWithResponse(
		ctx, repoID, pipelineNumber,
		&woodpeckerapi.GetReposRepoIdPipelinesPipelineNumberParams{
			Authorization: bearerPrefix + c.token,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("woodpecker api GET /repos/%d/pipelines/%d: %w",
			repoID, pipelineNumber, err)
	}

	status := resp.StatusCode()
	if status != http.StatusOK {
		return nil, fmt.Errorf("woodpecker api GET /repos/%d/pipelines/%d: status %d: %s",
			repoID, pipelineNumber, status, string(resp.Body))
	}

	if resp.JSON200 == nil {
		return nil, errPipelineNotFound
	}

	return toPipelineDetail(resp.JSON200), nil
}

// getStepLogs calls GET /repos/{repo_id}/logs/{pipeline_number}/{step_id}
// and returns the decoded []logEntry for that step.
func (c *woodpeckerClient) getStepLogs(
	ctx context.Context, repoID, pipelineNumber, stepID int,
) ([]logEntry, error) {
	resp, err := c.api.GetReposRepoIdLogsPipelineNumberStepIdWithResponse(
		ctx, repoID, pipelineNumber, stepID,
		&woodpeckerapi.GetReposRepoIdLogsPipelineNumberStepIdParams{
			Authorization: bearerPrefix + c.token,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("woodpecker api GET /repos/%d/logs/%d/%d: %w",
			repoID, pipelineNumber, stepID, err)
	}

	status := resp.StatusCode()
	if status != http.StatusOK {
		return nil, fmt.Errorf("woodpecker api GET /repos/%d/logs/%d/%d: status %d: %s",
			repoID, pipelineNumber, stepID, status, string(resp.Body))
	}

	if resp.JSON200 == nil {
		return nil, nil
	}

	return decodeLogEntries(*resp.JSON200), nil
}

// restartOverrides bundles the optional event and deploy_to overrides
// for POST /repos/{repo_id}/pipelines/{pipeline_number}. Grouping them
// keeps restartPipeline at four args (ctx + repoID + pipelineNumber +
// overrides + return) instead of the revive limit of five.
type restartOverrides struct {
	Event    *string
	DeployTo *string
}

// restartPipeline calls POST /repos/{repo_id}/pipelines/{pipeline_number}
// with the optional event and deploy_to overrides.
func (c *woodpeckerClient) restartPipeline(
	ctx context.Context, repoID, pipelineNumber int, overrides restartOverrides,
) (*pipelineDetail, error) {
	params := &woodpeckerapi.PostReposRepoIdPipelinesPipelineNumberParams{
		Authorization: bearerPrefix + c.token,
		Event:         overrides.Event,
		DeployTo:      overrides.DeployTo,
	}

	resp, err := c.api.PostReposRepoIdPipelinesPipelineNumberWithResponse(
		ctx, repoID, pipelineNumber, params,
	)
	if err != nil {
		return nil, fmt.Errorf("woodpecker api POST /repos/%d/pipelines/%d: %w",
			repoID, pipelineNumber, err)
	}

	status := resp.StatusCode()
	if status != http.StatusOK {
		return nil, fmt.Errorf("woodpecker api POST /repos/%d/pipelines/%d: status %d: %s",
			repoID, pipelineNumber, status, string(resp.Body))
	}

	if resp.JSON200 == nil {
		return nil, errPipelineNotFound
	}

	return toPipelineDetail(resp.JSON200), nil
}

// launchPipeline calls POST /repos/{repo_id}/pipelines with a
// PipelineOptions body. branch and variables are optional; the
// server falls back to the repo's default_branch when branch is empty.
func (c *woodpeckerClient) launchPipeline(
	ctx context.Context, repoID int,
	branch *string, variables map[string]string,
) (*pipelineDetail, error) {
	body := woodpeckerapi.PipelineOptions{
		Branch:    branch,
		Variables: (*map[string]string)(nil),
	}
	if len(variables) > 0 {
		body.Variables = &variables
	}

	encoded := jsonBytes(body)

	resp, err := c.api.PostReposRepoIdPipelinesWithBodyWithResponse(
		ctx, repoID,
		&woodpeckerapi.PostReposRepoIdPipelinesParams{
			Authorization: bearerPrefix + c.token,
		},
		"application/json",
		bytes.NewReader(encoded),
	)
	if err != nil {
		return nil, fmt.Errorf("woodpecker api POST /repos/%d/pipelines: %w", repoID, err)
	}

	status := resp.StatusCode()
	if status != http.StatusOK {
		return nil, fmt.Errorf("woodpecker api POST /repos/%d/pipelines: status %d: %s",
			repoID, status, string(resp.Body))
	}

	if resp.JSON200 == nil {
		return nil, errPipelineNotFound
	}

	return toPipelineDetail(resp.JSON200), nil
}

// cancelPipeline calls POST /repos/{repo_id}/pipelines/{pipeline_number}/cancel.
// The endpoint has no request body and an empty 200 response on the
// server. We swallow the body and return nil error on success.
func (c *woodpeckerClient) cancelPipeline(
	ctx context.Context, repoID, pipelineNumber int,
) error {
	resp, err := c.api.PostReposRepoIdPipelinesPipelineNumberCancelWithResponse(
		ctx, repoID, pipelineNumber,
		&woodpeckerapi.PostReposRepoIdPipelinesPipelineNumberCancelParams{
			Authorization: bearerPrefix + c.token,
		},
	)
	if err != nil {
		return fmt.Errorf("woodpecker api POST /repos/%d/pipelines/%d/cancel: %w",
			repoID, pipelineNumber, err)
	}

	status := resp.StatusCode()
	if status != http.StatusOK {
		return fmt.Errorf("woodpecker api POST /repos/%d/pipelines/%d/cancel: status %d: %s",
			repoID, pipelineNumber, status, string(resp.Body))
	}

	return nil
}

// jsonBytes marshals value. The errchkjson linter rule considers json.Marshal
// of struct values infallible, so the error is unchecked here. The
// call is the only json.Marshal in the package and the value is a
// local copy of a fixed-shape struct; any error would indicate a
// deeper bug worth surfacing separately.
//
//nolint:errchkjson // pipeline options body is a fixed-shape struct; see comment above
func jsonBytes(value any) []byte {
	out, _ := json.Marshal(value)

	return out
}

// jsonArrayLen returns the length of a nullable JSON array pointer,
// or 0 if the pointer is nil. Avoids the gocritic rangeValCopy by
// never dereferencing the underlying array at this call site.
func jsonArrayLen[T any](ptr *[]T) int {
	if ptr == nil {
		return 0
	}

	return len(*ptr)
}

// --- Generated type → model-facing type converters ---

// toRepoSummary converts the generated RepoLastPipeline into the
// model-facing repoSummary. The argument is taken by pointer so the
// ~280-byte struct is not copied at every call (hugeParam).
func toRepoSummary(repo *woodpeckerapi.RepoLastPipeline) repoSummary {
	out := repoSummary{}
	if repo == nil {
		return out
	}

	out = repoSummary{
		ID:           intDeref(repo.Id),
		Owner:        strDeref(repo.Owner),
		Name:         strDeref(repo.Name),
		FullName:     strDeref(repo.FullName),
		Branch:       strDeref(repo.DefaultBranch),
		Active:       boolDeref(repo.Active),
		Private:      boolDeref(repo.Private),
		LastPipeline: (*pipelineSummary)(nil),
	}
	if repo.LastPipeline != nil {
		ps := toPipelineSummary(repo.LastPipeline)
		out.LastPipeline = &ps
	}

	return out
}

// toPipelineSummary converts the generated Pipeline into the
// lightweight pipelineSummary used by list_pipelines and as the
// last_pipeline nested field of repoSummary. Pointer argument so
// the ~304-byte struct is not copied (hugeParam).
func toPipelineSummary(pipeline *woodpeckerapi.Pipeline) pipelineSummary {
	out := pipelineSummary{}
	if pipeline == nil {
		return out
	}

	started := intDeref(pipeline.Started)
	finished := intDeref(pipeline.Finished)
	created := intDeref(pipeline.Created)

	return pipelineSummary{
		Number:   intDeref(pipeline.Number),
		Event:    strDeref((*string)(pipeline.Event)),
		Status:   strDeref((*string)(pipeline.Status)),
		Branch:   strDeref(pipeline.Branch),
		Commit:   strDeref(pipeline.Commit),
		Message:  strDeref(pipeline.Message),
		Author:   strDeref(pipeline.Author),
		Started:  int64(started),
		Finished: int64(finished),
		Duration: pipelineDuration(started, finished),
		Created:  int64(created),
	}
}

// toPipelineDetail converts the generated Pipeline into the full
// pipelineDetail used by get_pipeline, restart_pipeline, and
// launch_pipeline. workflows[].children[] is included so the agent
// can find the id of the failed step. Pointer argument (hugeParam).
func toPipelineDetail(pipeline *woodpeckerapi.Pipeline) *pipelineDetail {
	if pipeline == nil {
		return nil
	}

	started := intDeref(pipeline.Started)
	finished := intDeref(pipeline.Finished)
	created := intDeref(pipeline.Created)

	out := &pipelineDetail{
		Number:    intDeref(pipeline.Number),
		Event:     strDeref((*string)(pipeline.Event)),
		Status:    strDeref((*string)(pipeline.Status)),
		Branch:    strDeref(pipeline.Branch),
		Commit:    strDeref(pipeline.Commit),
		Message:   strDeref(pipeline.Message),
		Author:    strDeref(pipeline.Author),
		Started:   int64(started),
		Finished:  int64(finished),
		Duration:  pipelineDuration(started, finished),
		Created:   int64(created),
		Workflows: []workflowWrap(nil),
	}
	if pipeline.Workflows != nil && len(*pipeline.Workflows) > 0 {
		workflows := make([]workflowWrap, 0, len(*pipeline.Workflows))
		for index := range *pipeline.Workflows {
			workflows = append(workflows, toWorkflowWrap(&(*pipeline.Workflows)[index]))
		}

		out.Workflows = workflows
	}

	return out
}

// toWorkflowWrap converts a generated model.Workflow into the
// model-facing workflowWrap. The generated type is anonymous in
// the api.gen.go source (it's inlined via $ref), so the field access
// is on the inline fields. Pointer argument (hugeParam).
func toWorkflowWrap(workflow *woodpeckerapi.ModelWorkflow) workflowWrap {
	out := workflowWrap{}
	if workflow == nil {
		return out
	}

	out = workflowWrap{
		ID:       intDeref(workflow.Id),
		Name:     strDeref(workflow.Name),
		State:    strDeref((*string)(workflow.State)),
		Children: []stepWrap(nil),
	}
	if workflow.Children != nil && len(*workflow.Children) > 0 {
		children := make([]stepWrap, 0, len(*workflow.Children))
		for index := range *workflow.Children {
			children = append(children, toStepWrap(&(*workflow.Children)[index]))
		}

		out.Children = children
	}

	return out
}

// toStepWrap converts a generated Step into the model-facing
// stepWrap. The id is what the agent passes back as step_id to
// get_step_logs. Pointer argument (hugeParam).
func toStepWrap(step *woodpeckerapi.Step) stepWrap {
	if step == nil {
		return stepWrap{}
	}

	return stepWrap{
		ID:       intDeref(step.Id),
		Name:     strDeref(step.Name),
		Type:     strDeref((*string)(step.Type)),
		State:    strDeref((*string)(step.State)),
		ExitCode: intDeref(step.ExitCode),
		Error:    strDeref(step.Error),
	}
}

// pipelineDuration returns (finished - started) in seconds when both
// are set, else 0. The API uses Unix seconds for both fields; a
// negative result (clock skew) is clamped to 0.
func pipelineDuration(started, finished int) int64 {
	if started == 0 || finished == 0 || finished < started {
		return 0
	}

	return int64(finished - started)
}

// strDeref returns the dereferenced value of a *string, or "" if nil.
// Used for optional fields whose zero value is a JSON empty string.
func strDeref(str *string) string {
	if str == nil {
		return ""
	}

	return *str
}

// boolDeref returns the dereferenced value of a *bool, or false if nil.
func boolDeref(value *bool) bool {
	if value == nil {
		return false
	}

	return *value
}

// intDeref returns the dereferenced value of a *int, or 0 if nil.
func intDeref(value *int) int {
	if value == nil {
		return 0
	}

	return *value
}
