// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package english

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/english/langtoolapi"
)

// TestConnect returns the single validate_english tool with the
// expected Annotations. english.Connect has no required fields in the
// connect map (no URL, no token, no API key), so a minimal empty
// config is sufficient to exercise the full Connect path.
func TestConnect(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), make(map[string]any))
	require.NoError(t, err)

	require.Len(t, resp.Tools, 1)

	entry := resp.Tools[0]
	require.Equal(t, "validate_english", entry.Name)
	require.NotEmpty(t, entry.Description)
	require.NotNil(t, entry.Handler)

	// validate_english is always read-only; its Annotations must
	// set ReadOnlyHint=true (and the annotations block must exist).
	require.NotNil(t, entry.Annotations)
	assert.True(t, entry.Annotations.ReadOnlyHint)
}

// TestDecodeConnect_NumericLanguageCoercion verifies the new
// decode.AsString coercion: a numeric language value is
// stringified via fmt.Sprint rather than rejected. The english
// tool's defaults then apply normally.
func TestDecodeConnect_NumericLanguageCoercion(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{"language": 42})
	require.NoError(t, err)
	require.Equal(t, "42", cfg.Language)
}

// TestDecodeConnect_NonScalarLanguage verifies the new strict
// path: a non-scalar value (a map) where a string is expected
// produces a wrapped decode.ErrWrongType.
func TestDecodeConnect_NonScalarLanguage(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{"language": map[string]any{"a": "b"}})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
}

// TestDecodeConnect_NonScalarAPIURL mirrors the language test
// for the optional api_url field.
func TestDecodeConnect_NonScalarAPIURL(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{"api_url": map[string]any{"h": "x"}})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
}

// TestDecodeConnect_HappyPath confirms the happy path of the decoder.
func TestDecodeConnect_HappyPath(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"language": "en-GB",
		"api_url":  "https://languagetool.example/v2",
	})
	require.NoError(t, err)
	require.Equal(t, "en-GB", cfg.Language)
	require.Equal(t, "https://languagetool.example/v2", cfg.APIURL)
}

// TestConfig_Validate_InvalidURL covers the URL parse error path in
// config.validate.
func TestConfig_Validate_InvalidURL(t *testing.T) {
	t.Parallel()

	// Control character forces url.Parse to fail.
	cfg := config{Language: "", APIURL: "http://[::1]:bad\x7f"}
	err := cfg.validate()
	require.Error(t, err)
	require.ErrorIs(t, err, errAPIURLInvalid)
}

// mockLanguageToolServer starts an httptest server that mimics the
// LanguageTool /v2/check endpoint. The responseFn is called for each
// request and is expected to write the response body. If responseFn
// is nil, the server returns an empty (but valid) matches response.
//
// The returned URL includes a /v2 suffix so it can be passed to
// newLanguageToolClient as the API URL: the OpenAPI client uses a
// relative path of /check, which the URL parser joins with /v2 to
// produce /v2/check.
func mockLanguageToolServer(
	t *testing.T,
	responseFn func(w http.ResponseWriter, r *http.Request),
) string {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/check", func(w http.ResponseWriter, r *http.Request) {
		if responseFn != nil {
			responseFn(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, `{"matches":[]}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv.URL + "/v2"
}

// callHandler invokes the validate_english tool's handler with the
// given arguments payload and returns the response.
func callHandler(
	t *testing.T,
	connect map[string]any,
	args json.RawMessage,
) (*mcp.CallToolResult, error) {
	t.Helper()

	resp, err := Connect(t.Context(), connect)
	require.NoError(t, err)
	require.Len(t, resp.Tools, 1)

	req := &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta{},
			Name:      "validate_english",
			Arguments: args,
		},
		Extra: (*mcp.RequestExtra)(nil),
	}

	//nolint:wrapcheck // handler error is returned to the caller as-is for assertion
	return resp.Tools[0].Handler(t.Context(), req)
}

// TestHandler_EmptyText covers the early-exit path when the user
// sends an empty text field.
func TestHandler_EmptyText(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, (func(w http.ResponseWriter, r *http.Request))(nil))

	result, err := callHandler(t, map[string]any{"api_url": apiURL},
		json.RawMessage(`{"text":""}`))
	require.Error(t, err)
	require.ErrorIs(t, err, errTextEmpty)
	require.Nil(t, result)
}

// TestHandler_InvalidJSON covers the JSON-parse error path in the
// handler when the arguments payload is malformed.
func TestHandler_InvalidJSON(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, (func(w http.ResponseWriter, r *http.Request))(nil))

	result, err := callHandler(t, map[string]any{"api_url": apiURL},
		json.RawMessage(`{not valid json`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse validate_english args")
	require.Nil(t, result)
}

// TestHandler_CleanNoErrors covers the happy path: clean text, no
// errors from LanguageTool, result has correct=true.
func TestHandler_CleanNoErrors(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, `{"matches":[]}`)
	})

	result, err := callHandler(t, map[string]any{"api_url": apiURL},
		json.RawMessage(`{"text":"This is a clean sentence."}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	var got ValidateEnglishResponse
	require.NoError(t, json.Unmarshal([]byte(extractText(t, result)), &got))
	require.True(t, got.Correct)
	require.Empty(t, got.Errors)
}

// TestHandler_WithErrors covers the path where LanguageTool returns
// matches. The handler must turn each match into a GrammarError with
// the expected fields populated.
func TestHandler_WithErrors(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, `{
			"matches":[{
				"context":{"text":"a typo here","offset":2,"length":4},
				"length":4,
				"message":"Possible spelling mistake",
				"offset":2,
				"replacements":[],
				"rule":{
					"id":"MORFOLOGIK_RULE_EN_US",
					"description":"Spelling rule",
					"category":{"name":"Spelling"}
				}
			}]
		}`)
	})

	result, err := callHandler(t, map[string]any{"api_url": apiURL},
		json.RawMessage(`{"text":"a typo here"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	var got ValidateEnglishResponse
	require.NoError(t, json.Unmarshal([]byte(extractText(t, result)), &got))
	require.False(t, got.Correct)
	require.Len(t, got.Errors, 1)
	require.Equal(t, "typo", got.Errors[0].Mistake)
	require.Equal(t, "Spelling", got.Errors[0].Category)
	require.Equal(t, "Spelling rule", got.Errors[0].Rule)
	require.Equal(t, 1, got.Errors[0].Index)
	require.Equal(t, "a typo here", got.Errors[0].Context)
	require.Contains(t, got.Errors[0].Hint, "misspelled")
	require.NotEqual(t, "Possible spelling mistake", got.Errors[0].Explanation)
}

// TestHandler_APIError covers the path where LanguageTool returns a
// non-200 status. The validate() helper converts the error into a
// warning, so the handler returns a successful (non-error) result
// with the warning populated and correct=true.
func TestHandler_APIError(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, "boom")
	})

	result, err := callHandler(t, map[string]any{"api_url": apiURL},
		json.RawMessage(`{"text":"hello world"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	var got ValidateEnglishResponse
	require.NoError(t, json.Unmarshal([]byte(extractText(t, result)), &got))
	require.Truef(t, got.Correct, "API errors must not flag the text as incorrect")
	require.NotEmptyf(t, got.Warning, "API error should populate a warning")
}

// TestHandler_CyrillicOnlyText covers the short-circuit path: after
// stripping Cyrillic characters, the cleaned text is empty so we
// never call LanguageTool.
func TestHandler_CyrillicOnlyText(t *testing.T) {
	t.Parallel()

	var upstreamHit bool

	apiURL := mockLanguageToolServer(t, func(w http.ResponseWriter, _ *http.Request) {
		upstreamHit = true
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, `{"matches":[]}`)
	})

	result, err := callHandler(t, map[string]any{"api_url": apiURL},
		json.RawMessage(`{"text":"привет мир"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.Falsef(t, upstreamHit, "upstream should not be called when cleaned text is empty")

	var got ValidateEnglishResponse
	require.NoError(t, json.Unmarshal([]byte(extractText(t, result)), &got))
	require.True(t, got.Correct)
	require.Empty(t, got.CleanedText)
}

// TestHandler_NilMatches covers the edge case where LanguageTool
// returns a valid response body but with no matches field (i.e.,
// JSON200 is non-nil but JSON200.Matches is nil). The handler must
// return a clean result.
func TestHandler_NilMatches(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, `{}`)
	})

	result, err := callHandler(t, map[string]any{"api_url": apiURL},
		json.RawMessage(`{"text":"hello"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	var got ValidateEnglishResponse
	require.NoError(t, json.Unmarshal([]byte(extractText(t, result)), &got))
	require.True(t, got.Correct)
}

// TestHandler_StructuredContent covers the path where the result
// body is a valid JSON object. The handler must populate
// StructuredContent in addition to Content.
func TestHandler_StructuredContent(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, (func(w http.ResponseWriter, r *http.Request))(nil))

	result, err := callHandler(t, map[string]any{"api_url": apiURL},
		json.RawMessage(`{"text":"hello world"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNilf(
		t,
		result.StructuredContent,
		"JSON object body should populate StructuredContent",
	)
}

// extractText returns the TextContent.Text payload from a
// CallToolResult, failing the test if Content is missing or not a
// *mcp.TextContent.
func extractText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected *mcp.TextContent, got %T", result.Content[0])

	return textContent.Text
}

// TestNewLanguageToolClient covers the happy path. The error
// branch is unreachable in practice because WithHTTPClient never
// errors, so we don't try to exercise it.
func TestNewLanguageToolClient(t *testing.T) {
	t.Parallel()

	client := newLanguageToolClient("https://example.org/v2", "en-US")
	require.NotNil(t, client)
	require.Equal(t, "https://example.org/v2", client.apiURL)
	require.Equal(t, "en-US", client.language)
	require.NotNil(t, client.client)
}

// TestConfig_ApplyDefaults covers the default-application branches.
func TestConfig_ApplyDefaults(t *testing.T) {
	t.Parallel()

	cfg := config{}
	cfg.applyDefaults()
	require.Equal(t, defaultLanguage, cfg.Language)
	require.Equal(t, defaultLanguageToolServer, cfg.APIURL)
}

// TestConfig_ApplyDefaults_KeepSet ensures that values already set
// on the config are preserved (the default-application is gated by
// the empty-string check).
func TestConfig_ApplyDefaults_KeepSet(t *testing.T) {
	t.Parallel()

	cfg := config{Language: "en-GB", APIURL: "https://custom/v2"}
	cfg.applyDefaults()
	require.Equal(t, "en-GB", cfg.Language)
	require.Equal(t, "https://custom/v2", cfg.APIURL)
}

// TestConfig_ApplyDefaults_OnlyAPIURL covers the case where only
// the API URL is set; the language should still default.
func TestConfig_ApplyDefaults_OnlyAPIURL(t *testing.T) {
	t.Parallel()

	cfg := config{Language: "", APIURL: "https://custom/v2"}
	cfg.applyDefaults()
	require.Equal(t, defaultLanguage, cfg.Language)
	require.Equal(t, "https://custom/v2", cfg.APIURL)
}

// TestConfig_ApplyDefaults_OnlyLanguage covers the case where only
// the language is set; the API URL should still default.
func TestConfig_ApplyDefaults_OnlyLanguage(t *testing.T) {
	t.Parallel()

	cfg := config{Language: "en-GB", APIURL: ""}
	cfg.applyDefaults()
	require.Equal(t, "en-GB", cfg.Language)
	require.Equal(t, defaultLanguageToolServer, cfg.APIURL)
}

// TestConnect_DecodeError covers the wrap-error path when the
// connect map has the wrong (non-scalar) type for one of the
// fields. Under the new decode.AsString contract, numeric values
// are coerced (no error) and only non-scalar values surface a
// wrapped decode.ErrWrongType.
func TestConnect_DecodeError(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{"language": map[string]any{"a": "b"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "english: decode:")
	require.ErrorIs(t, err, decode.ErrWrongType)
}

// TestConnect_ValidateError covers the wrap-error path when the
// decoded API URL is invalid.
func TestConnect_ValidateError(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{"api_url": "http://[::1]:bad\x7f"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "english: validate:")
}

// TestStripIgnoredSegments_NoMatches is a redundant test: confirms
// the helper is a no-op for empty/whitespace-only input.
func TestStripIgnoredSegments_NoMatches(t *testing.T) {
	t.Parallel()

	require.Empty(t, stripIgnoredSegments(""))
	require.Empty(t, stripIgnoredSegments("   "))
}

// TestFilterErrors_EmptyInput covers the empty-input branch.
func TestFilterErrors_EmptyInput(t *testing.T) {
	t.Parallel()

	got := filterErrors([]langtoolapi.Match(nil))
	require.Empty(t, got)
}

// TestFormatErrors_EmptyInput covers the empty-input branch of
// formatErrors.
func TestFormatErrors_EmptyInput(t *testing.T) {
	t.Parallel()

	got := formatErrors([]langtoolapi.Match(nil))
	require.Empty(t, got)
}

// TestCallLanguageTool_RoundTrip exercises callLanguageTool against
// an httptest server. The path of an extra hop (the oapi-codegen
// generated client) makes this an end-to-end test of the
// PostCheckWithFormdataBodyWithResponse flow.
func TestCallLanguageTool_RoundTrip(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Confirm the SDK encoded the body as form-urlencoded. Use
		// t.Errorf directly: the go-require linter disallows require.*
		// calls inside http.HandlerFunc because the assertions can run
		// in a goroutine that outlives the test.
		err := r.ParseForm()
		if err != nil {
			t.Errorf("ParseForm: %v", err)
		}

		if got := r.PostFormValue("text"); got != "hello world" {
			t.Errorf("text form field = %q, want %q", got, "hello world")
		}

		if got := r.PostFormValue("language"); got != "en-US" {
			t.Errorf("language form field = %q, want %q", got, "en-US")
		}

		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(
			w,
			`{"matches":[{"message":"x","context":{"text":"hello world","offset":0,"length":1}}]}`,
		)
	})

	client := newLanguageToolClient(apiURL, "en-US")
	matches, err := client.callLanguageTool(t.Context(), "hello world")
	require.NoError(t, err)
	require.Len(t, matches, 1)
}

// TestCallLanguageTool_500 exercises the non-200 error branch of
// callLanguageTool.
func TestCallLanguageTool_500(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, "upstream down")
	})

	client := newLanguageToolClient(apiURL, "en-US")
	matches, err := client.callLanguageTool(t.Context(), "hello world")
	require.Error(t, err)
	require.Contains(t, err.Error(), "language tool api returned status 502")
	require.Nil(t, matches)
}

// TestCallLanguageTool_TransportError exercises the request-error
// branch of callLanguageTool by pointing the client at a closed
// httptest server.
func TestCallLanguageTool_TransportError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	apiURL := srv.URL
	srv.Close() // immediately close to force a connect error

	client := newLanguageToolClient(apiURL, "en-US")
	matches, err := client.callLanguageTool(t.Context(), "hello world")
	require.Error(t, err)
	require.Contains(t, err.Error(), "language tool api request")
	require.Nil(t, matches)
}

// TestValidate_CleanText exercises the full validate() path with a
// clean input. The result should have Correct=true, no errors, and
// the cleaned text preserved.
func TestValidate_CleanText(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, (func(w http.ResponseWriter, r *http.Request))(nil))
	client := newLanguageToolClient(apiURL, "en-US")

	resp := client.validate(t.Context(), "hello world")
	require.True(t, resp.Correct)
	require.Empty(t, resp.Errors)
	require.Empty(t, resp.Warning)
	require.Equal(t, "hello world", resp.CleanedText)
}

// TestValidate_WithErrors exercises the validate() path when
// LanguageTool returns matches. The errors are filtered and
// formatted into the response.
func TestValidate_WithErrors(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, `{
			"matches":[{
				"context":{"text":"hello world","offset":6,"length":5},
				"length":5,
				"message":"Possible spelling mistake",
				"offset":6,
				"replacements":[],
				"rule":{
					"id":"MORFOLOGIK_RULE_EN_US",
					"description":"Spelling",
					"category":{"name":"Spelling"}
				}
			}]
		}`)
	})

	client := newLanguageToolClient(apiURL, "en-US")
	resp := client.validate(t.Context(), "hello world")
	require.False(t, resp.Correct)
	require.Len(t, resp.Errors, 1)
	require.Equal(t, "world", resp.Errors[0].Mistake)
}

// TestValidate_CyrillicOnly covers the short-circuit path where the
// input is all Cyrillic — stripCyrillic returns "" and we never
// call the upstream.
func TestValidate_CyrillicOnly(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("upstream should not be called when cleaned text is empty")
	})

	client := newLanguageToolClient(apiURL, "en-US")
	resp := client.validate(t.Context(), "привет мир")
	require.True(t, resp.Correct)
	require.Empty(t, resp.CleanedText)
}

// TestValidate_APIError covers the path where LanguageTool errors
// out: the response should have a warning, no errors, and the
// cleaned text preserved.
func TestValidate_APIError(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, "down")
	})

	client := newLanguageToolClient(apiURL, "en-US")
	resp := client.validate(t.Context(), "hello world")
	require.Truef(t, resp.Correct, "API error → not flagged as incorrect")
	require.Empty(t, resp.Errors)
	require.NotEmptyf(t, resp.Warning, "API error should populate a warning")
	require.Equal(t, "hello world", resp.CleanedText)
}

// TestEnrichExplanation_AllKnownRules ensures every well-known rule
// ID in learnerExplanations returns a non-original explanation.
func TestEnrichExplanation_AllKnownRules(t *testing.T) {
	t.Parallel()

	for ruleID := range learnerExplanations {
		t.Run(ruleID, func(t *testing.T) {
			t.Parallel()

			got := enrichExplanation(ruleID, "raw message")
			require.NotEqualf(t, "raw message", got,
				"rule %q should have a learner-friendly explanation", ruleID)
		})
	}
}

// TestHandler_InvalidArgsShape covers the handler's defense against
// arguments that are not a JSON object (e.g. an array or a string).
// The mcp library should never produce these, but the handler must
// still reject them cleanly.
func TestHandler_InvalidArgsShape(t *testing.T) {
	t.Parallel()

	apiURL := mockLanguageToolServer(t, (func(w http.ResponseWriter, r *http.Request))(nil))
	result, err := callHandler(t, map[string]any{"api_url": apiURL},
		json.RawMessage(`"plain string"`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse validate_english args")
	require.Nil(t, result)
}
