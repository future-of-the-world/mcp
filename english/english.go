// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package english implements the English grammar/spelling validation MCP
// tool. It validates text against the LanguageTool API and returns
// learner-friendly explanations tailored for Russian-speaking English
// learners.
//
// Per-type Connect accepts an optional language and API URL via the
// `connect:` map and returns a single tool ("validate_english") that
// exposes the validation request. The tool is always read-only; its
// Annotations set ReadOnlyHint=true.
package english

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "embed"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/tool"
)

//go:embed schemas/validate_english.json
var validateEnglishInput json.RawMessage

//go:embed schemas/validate_english_output.json
var validateEnglishOutput json.RawMessage

const (
	// defaultLanguageToolServer is the default LanguageTool API base URL.
	defaultLanguageToolServer = "https://api.languagetool.org/v2"

	// defaultLanguage is the default language code for English (US).
	defaultLanguage = "en-US"
)

var (
	errAPIURLInvalid = errors.New("english tool: API URL is invalid")
	errTextEmpty     = errors.New("english tool: text is empty")
)

// ValidateEnglishRequest is the request body for the validate_english tool.
type ValidateEnglishRequest struct {
	Text string `json:"text" jsonschema:"required,English text to validate"`
}

// GrammarError represents a single grammar or spelling error found in the text.
type GrammarError struct {
	Index       int    `json:"index"       jsonschema:"1-based error position"`
	Mistake     string `json:"mistake"     jsonschema:"The incorrect text that was found"`
	Category    string `json:"category"    jsonschema:"Error category, e.g. Grammar"`
	Hint        string `json:"hint"        jsonschema:"What kind of change is needed"`
	Rule        string `json:"rule"        jsonschema:"Short name of the rule violated"`
	Explanation string `json:"explanation" jsonschema:"Explanation for Russian-speaking learners"`
	Context     string `json:"context"     jsonschema:"Surrounding text where the error was found"`
}

// ValidateEnglishResponse is the response body for the validate_english tool.
type ValidateEnglishResponse struct {
	Correct     bool           `json:"correct"               jsonschema:"Text passed validation"`
	Errors      []GrammarError `json:"errors,omitzero"       jsonschema:"Grammar errors found"`
	CleanedText string         `json:"cleaned_text,omitzero" jsonschema:"Cleaned version of text"`
	Warning     string         `json:"warning,omitzero"      jsonschema:"API unreachable warning"`
}

// config holds the decoded `connect:` map for an english source. Both
// fields are optional and fall back to package-level defaults.
type config struct {
	Language string
	APIURL   string
}

// decodeConnect decodes the source's `connect:` map into a config.
// Scalar string fields are decoded through decode.AsString so YAML-natural
// values (numbers, bools, null) are accepted and stringified; non-scalar
// values (maps, slices) produce a wrapped decode.ErrWrongType error so
// genuine config bugs surface as a clear message rather than a silent
// "field is empty" downstream. Both fields are optional and fall back
// to package-level defaults in applyDefaults. Errors here are wrapped
// by Connect as "english: decode: <reason>"; the per-field prefix
// lives here so the final message is single-segment, not double.
func decodeConnect(connect map[string]any) (config, error) {
	var (
		cfg config
		err error
	)

	str, err := decode.AsString(connect["language"])

	switch {
	case err == nil:
		cfg.Language = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — key absent or null; applyDefaults fills in "en-US"

	default:
		return cfg, fmt.Errorf("connect.language: %w", err)
	}

	str, err = decode.AsString(connect["api_url"])

	switch {
	case err == nil:
		cfg.APIURL = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — key absent or null; applyDefaults fills in the public API

	default:
		return cfg, fmt.Errorf("connect.api_url: %w", err)
	}

	return cfg, nil
}

func (c *config) applyDefaults() {
	if c.Language == "" {
		c.Language = defaultLanguage
	}

	if c.APIURL == "" {
		c.APIURL = defaultLanguageToolServer
	}
}

func (c *config) validate() error {
	var err error

	_, err = url.Parse(c.APIURL)
	if err != nil {
		return fmt.Errorf("%w: %w", errAPIURLInvalid, err)
	}

	return nil
}

// languageToolClient is defined in english_client.go along with the
// request/response conversion helpers. This file only declares the
// public Connect entry point and the handler factory.

// toolDescValidateEnglish is the (long) description shown to the model.
const toolDescValidateEnglish = "Call this tool on every user message with English text.\n\n" +
	"Protocol after receiving the response:\n\n" +
	"1. If correct=false (mechanical errors):\n" +
	"   For each error in the errors array:\n" +
	"   - State the category (grammar, spelling, punctuation, capitalization)\n" +
	"   - Explain the rule using the explanation field\n" +
	"   - Give the hint, do NOT reveal or imply the corrected form\n" +
	"   Do NOT answer the user's question. Wait for them to rewrite.\n\n" +
	"2. If correct=true, perform semantic check on cleaned_text:\n" +
	"   - Confusable word pairs: affect/effect, its/it's, their/there/they're,\n" +
	"     then/than, lose/loose, advice/advise, complement/compliment,\n" +
	"     cache/cash, authentication/authorization, principal/principle\n" +
	"   - Tense inconsistency (switching between past and present without reason)\n" +
	"   - Article omission (common for Russian speakers)\n" +
	"   - Wrong prepositions (depend from instead of depend on,\n" +
	"     congratulate with instead of congratulate on)\n" +
	"   - Ambiguous phrasing\n" +
	"   If issues found: describe the rule or distinction, do NOT rewrite for the user.\n" +
	"   Do NOT answer the question. Wait.\n\n" +
	"3. If semantic check passed, perform vocabulary check on cleaned_text:\n" +
	"   Identify simple words that have advanced alternatives:\n" +
	"   - big→enormous/substantial, small→tiny/marginal,\n" +
	"     good→excellent/outstanding, bad→detrimental/adverse\n" +
	"   - important→crucial/essential, thing→aspect/factor,\n" +
	"     get→obtain/acquire, make→create/develop\n" +
	"   - a lot of→numerous/considerable, very→remarkably/exceedingly,\n" +
	"     think→consider/contemplate\n" +
	"   If simpler words found: list them with 1-2 advanced synonyms.\n" +
	"   Do NOT rewrite for the user. Do NOT answer the question. Wait.\n\n" +
	"4. All three checks passed: respond normally.\n\n" +
	"This is a practice tool. The user learns by writing corrections themselves."

// Connect decodes the source's `connect:` map, builds a LanguageTool
// client, and returns the single validate_english tool. The tool is
// always read-only; its Annotations set ReadOnlyHint=true.
func Connect(_ context.Context, connect map[string]any, _ ...tool.Option) (tool.Response, error) {
	cfg, err := decodeConnect(connect)
	if err != nil {
		return tool.Response{}, fmt.Errorf("english: decode: %w", err)
	}

	cfg.applyDefaults()

	validateErr := cfg.validate()
	if validateErr != nil {
		return tool.Response{}, fmt.Errorf("english: validate: %w", validateErr)
	}

	client := newLanguageToolClient(cfg.APIURL, cfg.Language)

	// readOnlyAnnotations is the single annotation block for the
	// validate_english tool. The tool is always read-only; it never
	// mutates LanguageTool state. OpenWorldHint defaults to true
	// (LanguageTool is an external service).
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
					Name:         "validate_english",
					Description:  toolDescValidateEnglish,
					InputSchema:  validateEnglishInput,
					OutputSchema: validateEnglishOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleValidateEnglish(client),
			},
		},
	}, nil
}

// handleValidateEnglish returns the mcp.ToolHandler that drives the
// validate_english tool. It decodes the request, calls the client, and
// wraps the response in a CallToolResult.
func handleValidateEnglish(client *languageToolClient) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args ValidateEnglishRequest

		var err error

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return nil, fmt.Errorf("parse validate_english args: %w", err)
		}

		if args.Text == "" {
			return nil, errTextEmpty
		}

		resp := client.validate(ctx, args.Text)

		data, err := json.Marshal(resp)
		if err != nil {
			return nil, fmt.Errorf("marshal response: %w", err)
		}

		result := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(data)},
			},
		}

		// Per the MCP spec, StructuredContent must marshal to a JSON
		// object. The unmarshal to map[string]any succeeds only when
		// data is a valid JSON object (a JSON array, primitive, or
		// malformed value returns an error). Those non-object cases
		// should be conveyed via Content only.
		var probe map[string]any
		if json.Unmarshal(data, &probe) == nil {
			result.StructuredContent = json.RawMessage(data)
		}

		return result, nil
	}
}
