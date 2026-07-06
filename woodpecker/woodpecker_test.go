// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:wsl_v5 // test fixtures cluster JSON literals tightly
package woodpecker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/woodpeckerapi"
)

// ---------------------------------------------------------------------------
// Connect: returns the seven tools with the right shape
// ---------------------------------------------------------------------------

// TestConnect_RegistersSevenTools confirms that Connect returns one
// tool per documented tool name, with a non-nil handler, a populated
// input/output schema, and a non-empty description that includes the
// investigation workflow preamble. No source-side config is required
// to test the tool inventory itself.
func TestConnect_RegistersSevenTools(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"token":   "ci_test_token",
		"api_url": "https://ci.example.com/api",
	})
	require.NoError(t, err)
	require.Len(t, resp.Tools, 7)

	expected := map[string]struct {
		readOnly bool
		destroy  *bool // nil ⇒ don't assert; new(true) ⇒ DestructiveHint true
		idem     bool
	}{
		"list_repos":       {readOnly: true, destroy: nil, idem: false},
		"list_pipelines":   {readOnly: true, destroy: nil, idem: false},
		"get_pipeline":     {readOnly: true, destroy: nil, idem: false},
		"get_step_logs":    {readOnly: true, destroy: nil, idem: false},
		"restart_pipeline": {readOnly: false, destroy: nil, idem: true},
		"launch_pipeline":  {readOnly: false, destroy: nil, idem: false},
		"cancel_pipeline":  {readOnly: false, destroy: new(true), idem: true},
	}

	for _, entry := range resp.Tools {
		require.NotNilf(t, entry.Tool, "tool has nil *mcp.Tool: %s", entry.Name)
		require.NotNilf(t, entry.Handler, "tool has nil handler: %s", entry.Name)
		require.NotEmptyf(t, entry.Description, "tool has empty description: %s", entry.Name)
		require.NotEmptyf(t, entry.InputSchema, "tool has nil input schema: %s", entry.Name)
		require.NotEmptyf(t, entry.OutputSchema, "tool has nil output schema: %s", entry.Name)
		require.NotNilf(t, entry.Annotations, "tool has nil annotations: %s", entry.Name)

		// Every tool's description must start with the investigation
		// workflow preamble so the model sees it regardless of which
		// tool it discovers first.
		assert.Containsf(t, entry.Description, "investigation workflow",
			"description missing workflow preamble: %s", entry.Name)

		exp, ok := expected[entry.Name]
		require.Truef(t, ok, "unexpected tool name: %s", entry.Name)
		assert.Equalf(t, exp.readOnly, entry.Annotations.ReadOnlyHint,
			"ReadOnlyHint mismatch for %s", entry.Name)
		assert.Equalf(t, exp.idem, entry.Annotations.IdempotentHint,
			"IdempotentHint mismatch for %s", entry.Name)
		if exp.destroy != nil {
			require.NotNilf(t, entry.Annotations.DestructiveHint,
				"DestructiveHint nil but expected %v for %s", *exp.destroy, entry.Name)
			assert.Equalf(t, *exp.destroy, *entry.Annotations.DestructiveHint,
				"DestructiveHint mismatch for %s", entry.Name)
		}

		delete(expected, entry.Name)
	}

	assert.Emptyf(t, expected, "missing tools: %v", expected)
}

// ---------------------------------------------------------------------------
// decodeConnect: scalar coercion, non-scalar rejection, error wrapping
// ---------------------------------------------------------------------------

func TestDecodeConnect_HappyPath(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"token":   "ci_test_token",
		"api_url": "https://ci.example.com/api",
	})
	require.NoError(t, err)
	assert.Equal(t, "ci_test_token", cfg.Token)
	assert.Equal(t, "https://ci.example.com/api", cfg.APIURL)
}

func TestDecodeConnect_TokenAsNumberIsStringified(t *testing.T) {
	t.Parallel()

	// YAML-natural: the user wrote `token: 12345` instead of the
	// quoted form. decode.AsString should accept it.
	cfg, err := decodeConnect(map[string]any{"token": 12345})
	require.NoError(t, err)
	assert.Equal(t, "12345", cfg.Token)
}

func TestDecodeConnect_APIURLAsNumberIsStringified(t *testing.T) {
	t.Parallel()

	// Same YAML-natural coercion for api_url: a numeric value in
	// the connect map is stringified. This path was never hit by
	// any prior test (only string api_url was exercised), so it was
	// uncovered in the function-level diff.
	cfg, err := decodeConnect(map[string]any{
		"token":   "ci_test_token",
		"api_url": 8080,
	})
	require.NoError(t, err)
	assert.Equal(t, "ci_test_token", cfg.Token)
	assert.Equal(t, "8080", cfg.APIURL)
}

func TestDecodeConnect_NonScalarToken(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{"token": map[string]any{"a": "b"}})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
}

func TestDecodeConnect_NonScalarAPIURL(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"token":   "ci_test_token",
		"api_url": []string{"https://ci.example.com/api"},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
}

// ---------------------------------------------------------------------------
// config.validate: empty token, malformed URL
// ---------------------------------------------------------------------------

func TestConfig_Validate_EmptyToken(t *testing.T) {
	t.Parallel()

	cfg := config{Token: "", APIURL: "https://ci.example.com/api"}

	err := cfg.validate()
	require.ErrorIs(t, err, errTokenEmpty)
}

func TestConfig_Validate_EmptyAPIURL(t *testing.T) {
	t.Parallel()

	cfg := config{Token: "ci_test_token", APIURL: ""}

	err := cfg.validate()
	require.ErrorIs(t, err, errAPIURLRequired)
	assert.Containsf(t, err.Error(), "api_url",
		"required-field error should name the field for the user")
}

func TestConfig_Validate_InvalidURL(t *testing.T) {
	t.Parallel()

	// A control character forces url.Parse to fail.
	cfg := config{Token: "ci_test_token", APIURL: "http://[::1]:bad\x7f"}

	err := cfg.validate()
	require.Error(t, err)
	require.ErrorIs(t, err, errAPIURLInvalid)
}

func TestConfig_NoDefaultsForAPIURL(t *testing.T) {
	t.Parallel()

	// Guard the no-default contract: a config with an empty api_url
	// must validate as missing-required-field, not silently backfill
	// a hard-coded host. If this test ever starts passing because
	// applyDefaults sneaks a URL back in, the privacy guarantee the
	// user asked for is broken.
	cfg := config{Token: "ci_test_token", APIURL: ""}
	require.ErrorIsf(t, cfg.validate(), errAPIURLRequired,
		"empty api_url must fail validation, not be silently defaulted")
}

// ---------------------------------------------------------------------------
// Connect error paths
// ---------------------------------------------------------------------------

func TestConnect_DecodeErrorIsWrapped(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{"token": map[string]any{"x": "y"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "woodpecker:")
	assert.Contains(t, err.Error(), "decode:")
}

func TestConnect_ValidateErrorIsWrapped(t *testing.T) {
	t.Parallel()

	// Empty token → decode succeeds (no token key), validate fails.
	_, err := Connect(t.Context(), make(map[string]any, 0))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "woodpecker:")
	assert.Contains(t, err.Error(), "validate:")
}

func TestConnect_RequiresAPIURL(t *testing.T) {
	t.Parallel()

	// Token is set but api_url is absent. The user must be told the
	// api_url is required, not silently routed to a default host.
	_, err := Connect(t.Context(), map[string]any{"token": "ci_test_token"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "woodpecker:")
	assert.Contains(t, err.Error(), "validate:")
	require.ErrorIsf(t, err, errAPIURLRequired,
		"missing api_url must surface errAPIURLRequired, "+
			"not a decode error or a generic token error")
}

// ---------------------------------------------------------------------------
// decodeLogEntries: base64 decode + kind mapping
// ---------------------------------------------------------------------------

func TestDecodeLogEntries_KindMapping(t *testing.T) {
	t.Parallel()

	// Build a LogEntry from JSON. Going through JSON (instead of a
	// struct literal) keeps this test free of exhaustruct complaints
	// about the generated type's dozens of pointer fields. The `data`
	// payloads are base64 strings (the real Woodpecker wire shape —
	// see TestDecodeLogEntries_Base64Regression); the JSON layer
	// base64-decodes them into the *[]byte generated LogEntry.Data.
	mkEntry := func(payload string) woodpeckerapi.LogEntry {
		var entry woodpeckerapi.LogEntry
		require.NoError(t, json.Unmarshal([]byte(payload), &entry))

		return entry
	}

	entries := []woodpeckerapi.LogEntry{
		mkEntry(`{"line": 1, "time": 100, "type": 0, "data": "b2s="}`),     // "ok"
		mkEntry(`{"line": 2, "time": 101, "type": 1, "data": "Ym9vbQ=="}`), // "boom"
		mkEntry(`{"line": 3, "time": 102, "type": 2, "data": "MQ=="}`),     // "1"
		mkEntry(`{"line": 4, "time": 103, "type": 3}`),
		mkEntry(`{"line": 5, "time": 104, "type": 4}`),
		mkEntry(`{"line": 6, "time": 105, "type": 99}`),
		mkEntry(`{}`),
	}

	got := decodeLogEntries(entries)
	require.Len(t, got, 7)

	assert.Equalf(t, "ok", got[0].Text, "stdout text decode")
	assert.Equal(t, "stdout", got[0].Kind)
	assert.Equal(t, 1, got[0].Line)
	assert.Equal(t, int64(100), got[0].Time)

	assert.Equalf(t, "boom", got[1].Text, "stderr text decode")
	assert.Equal(t, "stderr", got[1].Kind)

	assert.Equalf(t, "1", got[2].Text, "exit_code text decode (single byte)")
	assert.Equal(t, "exit_code", got[2].Kind)

	assert.Emptyf(t, got[3].Text, "metadata text is empty when data is nil")
	assert.Equal(t, "metadata", got[3].Kind)

	assert.Empty(t, got[4].Text)
	assert.Equal(t, "progress", got[4].Kind)

	assert.Equalf(t, "unknown", got[5].Kind,
		"unknown kinds map to unknown (defensive default)")
	assert.Empty(t, got[5].Text)

	// nil entry: all fields fall through to their zero values.
	assert.Equal(t, 0, got[6].Line)
	assert.Equal(t, int64(0), got[6].Time)
	assert.Equalf(t, "unknown", got[6].Kind, "nil type falls through to unknown")
	assert.Empty(t, got[6].Text)
}

// TestDecodeLogEntries_Base64Regression is the canary for the
// fix documented in .issues/woodpecker-log-data-base64: the real
// Woodpecker 3.15.x server returns `data` as a base64-encoded string,
// not a JSON array of integers. The previous generated LogEntry.Data
// was *[]int, so json.Unmarshal would fail on the first string with
// `cannot unmarshal string into Go struct field LogEntry.data of
// type []int`. The payloads below are taken verbatim from a real
// failed-pipeline log fetch (see the issue for the originating
// command).
func TestDecodeLogEntries_Base64Regression(t *testing.T) {
	t.Parallel()

	// Real Woodpecker 3.15.x response shape — `data` is a base64
	// string, not a []int. The first payload decodes to the visible
	// `+ go test ...` invocation; the second to the first `=== RUN`
	// banner. Kept as named consts so each long base64 line stays
	// under the 100-char lll budget.
	const (
		b64GoTest = "KyBnbyB0ZXN0IC10cmltcGF0aCAuLy4uLiAtdiAtY291bnQgMSAt" +
			"LXJhY2UgLWNvdmVycHJvZmlsZT1jb3ZlcmFnZS5vdXQgLWNvdmVybW9k" +
			"ZT1hdG9taWMgLS1jb3ZlcnBrZyAuLy4uLg=="

		b64RunBanner = "PT09IFJVTiAgIFRlc3RCaW5hcnlNaXNzaW5nQ29uZmlnRmxhZw=="
	)

	payload := fmt.Appendf([]byte(nil), `[
		{"id": 720053, "step_id": 1067, "time": 0,   "line": 0, "type": 0, "data": %q},
		{"id": 720056, "step_id": 1067, "time": 125, "line": 1, "type": 0, "data": %q}
	]`, b64GoTest, b64RunBanner)

	var entries []woodpeckerapi.LogEntry
	require.NoErrorf(t, json.Unmarshal(payload, &entries),
		"LogEntry.Data must accept a base64 string (real API shape)")

	got := decodeLogEntries(entries)
	require.Len(t, got, 2)

	assert.Equalf(t,
		"+ go test -trimpath ./... -v -count 1 --race "+
			"-coverprofile=coverage.out -covermode=atomic --coverpkg ./...",
		got[0].Text,
		"first line is the visible go test invocation "+
			"(Woodpecker prefixes it with `+ ` and uses `--flag` style)")
	assert.Equal(t, "stdout", got[0].Kind)
	assert.Equal(t, 0, got[0].Line)
	assert.Equal(t, int64(0), got[0].Time)

	assert.Equal(t, "=== RUN   TestBinaryMissingConfigFlag", got[1].Text)
	assert.Equal(t, "stdout", got[1].Kind)
	assert.Equal(t, 1, got[1].Line)
	assert.Equal(t, int64(125), got[1].Time)
}

// ---------------------------------------------------------------------------
// toPipelineSummary: pointer deref, duration math
// ---------------------------------------------------------------------------

func TestToPipelineSummary_DurationMath(t *testing.T) {
	t.Parallel()

	pipe := pipelineFromJSON(t, `{
		"number": 42,
		"status": "success",
		"branch": "main",
		"commit": "abc1234",
		"started": 1000,
		"finished": 1060
	}`)

	got := toPipelineSummary(&pipe)
	assert.Equal(t, 42, got.Number)
	assert.Equal(t, "success", got.Status)
	assert.Equalf(t, 1060-1000, int(got.Duration),
		"Duration should be finished - started in seconds")
}

func TestToPipelineSummary_MissingTimesZeroDuration(t *testing.T) {
	t.Parallel()

	pipe := pipelineFromJSON(t, `{"number": 1, "status": "pending"}`)
	got := toPipelineSummary(&pipe)
	assert.Equalf(t, int64(0), got.Duration,
		"missing started/finished should give 0 duration")
}

func TestToPipelineSummary_ClockSkewClampsToZero(t *testing.T) {
	t.Parallel()

	// finished < started (clock skew) → duration 0, not negative.
	pipe := pipelineFromJSON(t, `{"started": 2000, "finished": 1000}`)
	got := toPipelineSummary(&pipe)
	assert.Equal(t, int64(0), got.Duration)
}

// ---------------------------------------------------------------------------
// toStepWrap: pointer deref
// ---------------------------------------------------------------------------

func TestToStepWrap_PointerDeref(t *testing.T) {
	t.Parallel()

	step := stepFromJSON(t, `{
		"id": 99,
		"name": "test",
		"type": "commands",
		"state": "failure",
		"exit_code": 0
	}`)

	got := toStepWrap(&step)
	assert.Equal(t, 99, got.ID)
	assert.Equal(t, "test", got.Name)
	assert.Equal(t, "commands", got.Type)
	assert.Equal(t, "failure", got.State)
	assert.Equal(t, 0, got.ExitCode)
}

// TestBoolDeref_NilCoversFallback exercises the nil-arg branch of
// boolDeref, which is otherwise only ever called with non-nil pointers
// (the Woodpecker API fills all bool fields it returns). The branch
// is the kind of code that is easy to break in a refactor without any
// test catching it; this test pins the behavior.
func TestBoolDeref_NilCoversFallback(t *testing.T) {
	t.Parallel()

	assert.Falsef(t, boolDeref((*bool)(nil)),
		"boolDeref(nil) must return false, not panic or return a zero bool by accident")
}

func TestBoolDeref_NonNilReturnsValue(t *testing.T) {
	t.Parallel()

	truthy := true
	falsy := false
	assert.True(t, boolDeref(&truthy))
	assert.False(t, boolDeref(&falsy))
}

// TestJSONArrayLen_NilCoversFallback exercises the nil-arg branch of
// jsonArrayLen. Same rationale as TestBoolDeref_NilCoversFallback: the
// woodpeckerapi generated types always populate array fields, so the
// nil fallback would silently rot without an explicit pin.
func TestJSONArrayLen_NilCoversFallback(t *testing.T) {
	t.Parallel()

	var ptr *[]int
	assert.Equalf(t, 0, jsonArrayLen(ptr),
		"jsonArrayLen(nil) must return 0, not panic")

	empty := []int{}
	assert.Equalf(t, 0, jsonArrayLen(&empty),
		"jsonArrayLen(&empty) must return 0, matching len(empty)")

	nonEmpty := []int{1, 2, 3}
	assert.Equal(t, 3, jsonArrayLen(&nonEmpty))
}

// ---------------------------------------------------------------------------
// Handler tests: round-trip through an httptest server
// ---------------------------------------------------------------------------

// mockWoodpeckerServer starts an httptest server that mimics the
// Woodpecker endpoints the seven tools call. handlerFn dispatches
// per-path; if nil, the server returns 204 No Content for write
// endpoints and an empty array for read endpoints.
func mockWoodpeckerServer(
	t *testing.T,
	handlerFn func(writer http.ResponseWriter, request *http.Request),
) string {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		if handlerFn != nil {
			handlerFn(writer, request)
			return
		}

		writer.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv.URL + "/api"
}

// writeStringOrFail is the assertion-bearing helper for HTTP test
// handlers that need to write a response body. We cannot use t.Fatalf
// inside an http.HandlerFunc (it would call runtime.Goexit on a
// different goroutine) so we use t.Errorf + tracking.
func writeStringOrFail(t *testing.T, writer io.Writer, body string) {
	t.Helper()
	_, err := io.WriteString(writer, body)
	if err != nil {
		t.Errorf("WriteString: %v", err)
	}
}

// callHandler drives a single tool's handler with the given args and
// returns the result plus error. Mirrors the shape used by the english
// test helpers.
func callHandler(
	t *testing.T,
	toolName string,
	connect map[string]any,
	args json.RawMessage,
) (*mcp.CallToolResult, error) {
	t.Helper()

	resp, err := Connect(t.Context(), connect)
	require.NoError(t, err)

	idx := -1
	for i, entry := range resp.Tools {
		if entry.Name == toolName {
			idx = i
			break
		}
	}
	require.GreaterOrEqualf(t, idx, 0, "tool %q not registered", toolName)

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta{},
			Name:      toolName,
			Arguments: args,
		},
		Extra: (*mcp.RequestExtra)(nil),
	}

	//nolint:wrapcheck // handler error returned to caller verbatim for assertion
	return resp.Tools[idx].Handler(t.Context(), req)
}

func TestHandler_ListRepos_RoundTrip(t *testing.T) {
	t.Parallel()

	apiURL := mockWoodpeckerServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assert.Equalf(t, "/api/user/repos", request.URL.Path, "unexpected path")
		assert.Equal(t, "GET", request.Method)
		assert.Containsf(t, request.Header.Get("Authorization"), bearerPrefix,
			"Authorization header should carry Bearer prefix")

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)

		writeStringOrFail(t, writer, `[
					{"id": 7, "owner": "octocat", "name": "hello", "full_name": "octocat/hello",
					 "default_branch": "main", "active": true, "private": false,
					 "last_pipeline": {"number": 42, "status": "success"}}
				]`)
	})

	result, err := callHandler(t, "list_repos", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	text := extractTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	repos, ok := got["repos"].([]any)
	require.True(t, ok)
	require.Len(t, repos, 1)

	repo := repos[0].(map[string]any)
	assert.EqualValues(t, 7, repo["id"])
	assert.Equal(t, "octocat/hello", repo["full_name"])
}

func TestHandler_GetStepLogs_RoundTrip(t *testing.T) {
	t.Parallel()

	apiURL := mockWoodpeckerServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assert.Equalf(t, "/api/repos/42/logs/100/5", request.URL.Path, "unexpected path")
		assert.Equal(t, "GET", request.Method)
		assert.Containsf(t, request.Header.Get("Authorization"), bearerPrefix,
			"Authorization header should carry Bearer prefix")

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)

		writeStringOrFail(t, writer, `[
					{"line": 1, "time": 100, "type": 0, "data": [111, 107]},
					{"line": 2, "time": 101, "type": 1, "data": [98, 111, 111, 109]},
					{"line": 3, "time": 102, "type": 2, "data": [49]}
				]`)
	})

	result, err := callHandler(t, "get_step_logs", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{"repo_id": 42, "pipeline_number": 100, "step_id": 5}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	text := extractTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	logs, ok := got["logs"].([]any)
	require.True(t, ok)
	require.Len(t, logs, 3)
	assert.Equal(t, "ok", logs[0].(map[string]any)["text"])
	assert.Equal(t, "stdout", logs[0].(map[string]any)["kind"])
	assert.Equal(t, "boom", logs[1].(map[string]any)["text"])
	assert.Equal(t, "stderr", logs[1].(map[string]any)["kind"])
	assert.Equal(t, "1", logs[2].(map[string]any)["text"])
	assert.Equal(t, "exit_code", logs[2].(map[string]any)["kind"])
}

func TestHandler_CancelPipeline_RoundTrip(t *testing.T) {
	t.Parallel()

	var gotPath, gotMethod string

	apiURL := mockWoodpeckerServer(t, func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotMethod = request.Method
		writer.WriteHeader(http.StatusOK)
	})

	result, err := callHandler(t, "cancel_pipeline", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{"repo_id": 7, "pipeline_number": 42}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)
	assert.Equal(t, "/api/repos/7/pipelines/42/cancel", gotPath)
	assert.Equal(t, "POST", gotMethod)
}

func TestHandler_LaunchPipeline_SendsBody(t *testing.T) {
	t.Parallel()

	var gotBody []byte
	var gotMethod string

	apiURL := mockWoodpeckerServer(t, func(writer http.ResponseWriter, request *http.Request) {
		gotMethod = request.Method

		var readErr error
		gotBody, readErr = io.ReadAll(request.Body)
		if readErr != nil {
			t.Errorf("ReadAll: %v", readErr)

			return
		}

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)

		writeStringOrFail(t, writer, `{"number": 43, "status": "pending"}`)
	})

	result, err := callHandler(t, "launch_pipeline", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{"repo_id": 7, "branch": "feature", "variables": {"FOO": "bar"}}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	assert.Equal(t, "POST", gotMethod)

	var body map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &body))
	assert.Equal(t, "feature", body["branch"])
	assert.EqualValues(t, "bar", body["variables"].(map[string]any)["FOO"])
}

func TestHandler_ListPipelines_RoundTrip(t *testing.T) {
	t.Parallel()

	var gotPath, gotQuery string

	apiURL := mockWoodpeckerServer(t, func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotQuery = request.URL.RawQuery
		assert.Equal(t, "GET", request.Method)
		assert.Containsf(t, request.Header.Get("Authorization"), bearerPrefix,
			"Authorization header should carry Bearer prefix")

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)

		writeStringOrFail(t, writer, `[
					{"id": 100, "number": 100, "event": "push", "status": "success",
					 "branch": "main", "commit": "abcdef", "message": "feat: ship it",
					 "author": "octocat",
					 "created": 1700000000, "started": 1700000010, "finished": 1700000060},
					{"id": 101, "number": 101, "event": "push", "status": "failure",
					 "branch": "feature", "commit": "123456",
					 "created": 1700001000, "started": 1700001010, "finished": 1700001020}
				]`)
	})

	result, err := callHandler(t, "list_pipelines", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{"repo_id": 7, "branch": "main", "status": "success"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	assert.Equalf(t, "/api/repos/7/pipelines", gotPath, "endpoint mismatch")
	assert.Containsf(t, gotQuery, "branch=main", "branch filter should reach the server")
	assert.Containsf(t, gotQuery, "status=success", "status filter should reach the server")

	text := extractTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	pipelines, ok := got["pipelines"].([]any)
	require.True(t, ok)
	require.Len(t, pipelines, 2)

	first := pipelines[0].(map[string]any)
	assert.EqualValues(t, 100, first["number"])
	assert.Equal(t, "success", first["status"])
	assert.EqualValues(t, 50, first["duration"])

	second := pipelines[1].(map[string]any)
	assert.EqualValues(t, 101, second["number"])
	assert.Equal(t, "failure", second["status"])
	assert.EqualValues(t, 10, second["duration"])
}

func TestHandler_GetPipeline_RoundTrip(t *testing.T) {
	t.Parallel()

	apiURL := mockWoodpeckerServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assert.Equalf(t, "/api/repos/7/pipelines/42", request.URL.Path, "endpoint mismatch")
		assert.Equal(t, "GET", request.Method)

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)

		writeStringOrFail(t, writer, `{
					"id": 42, "number": 42, "event": "push", "status": "failure",
					"branch": "main", "commit": "deadbeef",
					"created": 1700000000, "started": 1700000010, "finished": 1700000060,
					"workflows": [
						{"id": 1, "name": "lint", "state": "success",
						 "children": [
							{"id": 11, "name": "golangci-lint", "type": "commands",
							 "state": "success", "exit_code": 0}
						 ]},
						{"id": 2, "name": "test", "state": "failure",
						 "children": [
							{"id": 21, "name": "go test ./...", "type": "commands",
							 "state": "failure", "exit_code": 1, "error": "1 test failed"}
						 ]}
					]
				}`)
	})

	result, err := callHandler(t, "get_pipeline", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{"repo_id": 7, "pipeline_number": 42}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	text := extractTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	pipe, ok := got["pipeline"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 42, pipe["number"])
	assert.Equal(t, "failure", pipe["status"])
	assert.EqualValues(t, 50, pipe["duration"])

	workflows, ok := pipe["workflows"].([]any)
	require.True(t, ok)
	require.Len(t, workflows, 2)

	test := workflows[1].(map[string]any)
	assert.Equal(t, "test", test["name"])
	assert.Equal(t, "failure", test["state"])

	children, ok := test["children"].([]any)
	require.True(t, ok)
	require.Len(t, children, 1)

	failedStep := children[0].(map[string]any)
	assert.EqualValues(t, 21, failedStep["id"])
	assert.Equal(t, "go test ./...", failedStep["name"])
	assert.Equal(t, "failure", failedStep["state"])
}

func TestHandler_RestartPipeline_RoundTrip(t *testing.T) {
	t.Parallel()

	var gotPath, gotMethod, gotQuery string

	apiURL := mockWoodpeckerServer(t, func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotMethod = request.Method
		gotQuery = request.URL.RawQuery

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)

		writeStringOrFail(t, writer, `{
					"id": 200, "number": 200, "event": "manual", "status": "pending",
					"branch": "main", "commit": "deadbeef",
					"created": 1700002000, "started": 1700002010,
					"workflows": []
				}`)
	})

	result, err := callHandler(t, "restart_pipeline", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(
		`{"repo_id": 7, "pipeline_number": 42, "event": "manual", "deploy_to": "prod"}`,
	))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	assert.Equalf(t, "/api/repos/7/pipelines/42", gotPath, "endpoint mismatch")
	assert.Equal(t, "POST", gotMethod)

	// Event and deploy_to are sent as query parameters on the POST,
	// not as a JSON body, so the handler can be sure the server saw
	// them without us needing a Content-Type dance.
	assert.Containsf(t, gotQuery, "event=manual", "event override should reach the server")
	assert.Containsf(t, gotQuery, "deploy_to=prod", "deploy_to override should reach the server")

	text := extractTextContent(t, result)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &got))

	pipe, ok := got["pipeline"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 200, pipe["number"])
	assert.Equal(t, "pending", pipe["status"])
}

func TestHandler_MissingRepoIDRejected(t *testing.T) {
	t.Parallel()

	apiURL := mockWoodpeckerServer(
		t,
		(func(http.ResponseWriter, *http.Request))(nil),
	)

	_, err := callHandler(t, "list_pipelines", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{}`))
	require.ErrorIs(t, err, errRepoIDRequired)
}

func TestHandler_MissingPipelineNumberRejected(t *testing.T) {
	t.Parallel()

	apiURL := mockWoodpeckerServer(
		t,
		(func(http.ResponseWriter, *http.Request))(nil),
	)

	_, err := callHandler(t, "get_pipeline", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{"repo_id": 7}`))
	require.ErrorIs(t, err, errPipelineNumberRequired)
}

func TestHandler_MissingStepIDRejected(t *testing.T) {
	t.Parallel()

	apiURL := mockWoodpeckerServer(
		t,
		(func(http.ResponseWriter, *http.Request))(nil),
	)

	_, err := callHandler(t, "get_step_logs", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{"repo_id": 7, "pipeline_number": 1}`))
	require.ErrorIs(t, err, errStepIDRequired)
}

func TestHandler_APIErrorSurfaced(t *testing.T) {
	t.Parallel()

	apiURL := mockWoodpeckerServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		writeStringOrFail(t, writer, `{"message": "bad token"}`)
	})

	_, err := callHandler(t, "list_repos", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Containsf(t, err.Error(), "401", "error should include status code")
}

// TestHandler_GetStepLogs_APIErrorSurfaced pins the non-200 status
// path through getStepLogs and handleGetStepLogs. The woodpecker
// server returns 500 for a real reason in this scenario; the agent
// should see a wrapped error that includes the status code and the
// body, not a panic and not a silent empty result.
func TestHandler_GetStepLogs_APIErrorSurfaced(t *testing.T) {
	t.Parallel()

	apiURL := mockWoodpeckerServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assert.Equalf(t, "/api/repos/7/logs/42/5", request.URL.Path, "endpoint mismatch")
		assert.Equal(t, "GET", request.Method)

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusInternalServerError)
		writeStringOrFail(t, writer, `{"message": "step log unavailable"}`)
	})

	_, err := callHandler(t, "get_step_logs", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{"repo_id": 7, "pipeline_number": 42, "step_id": 5}`))
	require.Error(t, err)
	assert.Containsf(t, err.Error(), "500", "error should include the upstream status code")
	assert.Containsf(
		t,
		err.Error(),
		"step log unavailable",
		"error should include the upstream body for diagnosis",
	)
}

// TestHandler_CancelPipeline_APIErrorSurfaced pins the non-200 status
// path through cancelPipeline and handleCancelPipeline.
func TestHandler_CancelPipeline_APIErrorSurfaced(t *testing.T) {
	t.Parallel()

	apiURL := mockWoodpeckerServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assert.Equalf(t, "/api/repos/7/pipelines/42/cancel", request.URL.Path, "endpoint mismatch")
		assert.Equal(t, "POST", request.Method)

		writer.WriteHeader(http.StatusConflict)
		writeStringOrFail(t, writer, `{"message": "pipeline already finished"}`)
	})

	_, err := callHandler(t, "cancel_pipeline", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{"repo_id": 7, "pipeline_number": 42}`))
	require.Error(t, err)
	assert.Containsf(t, err.Error(), "409", "error should include the upstream status code")
}

// TestHandler_RestartPipeline_APIErrorSurfaced pins the non-200 status
// path through restartPipeline and handleRestartPipeline.
func TestHandler_RestartPipeline_APIErrorSurfaced(t *testing.T) {
	t.Parallel()

	apiURL := mockWoodpeckerServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assert.Equalf(t, "/api/repos/7/pipelines/42", request.URL.Path, "endpoint mismatch")
		assert.Equal(t, "POST", request.Method)

		writer.WriteHeader(http.StatusForbidden)
		writeStringOrFail(t, writer, `{"message": "no write access to repo"}`)
	})

	_, err := callHandler(t, "restart_pipeline", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{"repo_id": 7, "pipeline_number": 42}`))
	require.Error(t, err)
	assert.Containsf(t, err.Error(), "403", "error should include the upstream status code")
}

// TestHandler_LaunchPipeline_APIErrorSurfaced pins the non-200 status
// path through launchPipeline and handleLaunchPipeline.
func TestHandler_LaunchPipeline_APIErrorSurfaced(t *testing.T) {
	t.Parallel()

	apiURL := mockWoodpeckerServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assert.Equalf(t, "/api/repos/7/pipelines", request.URL.Path, "endpoint mismatch")
		assert.Equal(t, "POST", request.Method)

		writer.WriteHeader(http.StatusBadRequest)
		writeStringOrFail(t, writer, `{"message": "branch not found"}`)
	})

	_, err := callHandler(t, "launch_pipeline", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{"repo_id": 7, "branch": "does-not-exist"}`))
	require.Error(t, err)
	assert.Containsf(t, err.Error(), "400", "error should include the upstream status code")
}

// TestHandler_GetPipeline_APIErrorSurfaced pins the non-200 status
// path through getPipeline and handleGetPipeline.
func TestHandler_GetPipeline_APIErrorSurfaced(t *testing.T) {
	t.Parallel()

	apiURL := mockWoodpeckerServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assert.Equalf(t, "/api/repos/7/pipelines/42", request.URL.Path, "endpoint mismatch")
		assert.Equal(t, "GET", request.Method)

		writer.WriteHeader(http.StatusNotFound)
		writeStringOrFail(t, writer, `{"message": "pipeline not found"}`)
	})

	_, err := callHandler(t, "get_pipeline", map[string]any{
		"token":   "ci_test_token",
		"api_url": apiURL,
	}, json.RawMessage(`{"repo_id": 7, "pipeline_number": 42}`))
	require.Error(t, err)
	assert.Containsf(t, err.Error(), "404", "error should include the upstream status code")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractTextContent returns the TextContent payload from a
// CallToolResult, failing the test if the result has no text content.
// Shared by all per-tool handler tests in this file.
func extractTextContent(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, result.Content)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected TextContent, got %T", result.Content[0])

	return textContent.Text
}

// pipelineFromJSON unmarshals a Woodpecker API JSON payload into the
// generated Pipeline type. Going through JSON (instead of a struct
// literal) keeps this helper free of exhaustruct complaints about the
// generated type's dozens of pointer fields.
func pipelineFromJSON(t *testing.T, payload string) woodpeckerapi.Pipeline {
	t.Helper()

	var pipe woodpeckerapi.Pipeline
	require.NoErrorf(t, json.Unmarshal([]byte(payload), &pipe),
		"unmarshal Pipeline fixture: %s", payload)

	return pipe
}

// stepFromJSON mirrors pipelineFromJSON for the Step type.
func stepFromJSON(t *testing.T, payload string) woodpeckerapi.Step {
	t.Helper()

	var step woodpeckerapi.Step
	require.NoErrorf(t, json.Unmarshal([]byte(payload), &step),
		"unmarshal Step fixture: %s", payload)

	return step
}

// _ ensures context is imported in case the test helpers don't use
// it directly (the linter flags unused imports otherwise).
var _ = context.Background
