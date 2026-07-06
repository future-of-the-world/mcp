// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package english

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.amidman.dev/mcp/english/langtoolapi"
)

// --- Compiled regexes for text preprocessing ---

// backtick is the backtick character used to build regex patterns containing backticks.
const backtick = "`"

var (
	// reFencedCode matches fenced code blocks using either triple backticks or triple tildes.
	// The (?s) flag makes dot match newline. Optional language specifier after fence markers.
	reFencedCode = regexp.MustCompile(
		`(?s)(?:` +
			backtick + backtick + backtick + `\w*\n.*?\n` + backtick + backtick + backtick +
			`|~~~\w*\n.*?\n~~~)`,
	)

	// reInlineCode matches inline code delimited by single backticks.
	reInlineCode = regexp.MustCompile(backtick + `[^` + backtick + `]+` + backtick)

	// reURL matches HTTP(S) URLs.
	reURL = regexp.MustCompile(`https?://\S+`)

	// reMultiSpace collapses multiple whitespace characters (including newlines) into a single
	// space.
	reMultiSpace = regexp.MustCompile(`\s+`)

	// reCyrillic matches sequences containing Cyrillic characters and surrounding whitespace.
	reCyrillic = regexp.MustCompile(`\s*\p{Cyrillic}+\s*`)
)

const (
	// languageToolClientTimeout is the HTTP timeout for LanguageTool API requests.
	languageToolClientTimeout = 10 * time.Second

	// replacementSpace is used to replace stripped segments with a single space.
	replacementSpace = " "
)

// --- languageToolClient ---

// languageToolClient is an HTTP client for the LanguageTool API.
type languageToolClient struct {
	apiURL   string
	language string
	client   *langtoolapi.ClientWithResponses
}

// newLanguageToolClient creates a new LanguageTool client with the given API base URL and language.
func newLanguageToolClient(apiURL, language string) *languageToolClient {
	cli, err := langtoolapi.NewClientWithResponses(apiURL,
		langtoolapi.WithHTTPClient(&http.Client{
			Timeout: languageToolClientTimeout,
		}),
	)
	if err != nil {
		// NewClient only fails if a ClientOption returns an error;
		// WithHTTPClient never errors, so this should not happen.
		panic(fmt.Sprintf("failed to create LanguageTool client: %v", err))
	}

	return &languageToolClient{
		apiURL:   apiURL,
		language: language,
		client:   cli,
	}
}

// --- Text processing ---

// stripIgnoredSegments removes code blocks, inline code, and URLs from the text,
// then collapses multiple whitespace into single spaces and trims.
func stripIgnoredSegments(text string) string {
	// Remove fenced code blocks first (they may contain inline code)
	text = reFencedCode.ReplaceAllString(text, replacementSpace)
	// Remove inline code
	text = reInlineCode.ReplaceAllString(text, replacementSpace)
	// Remove URLs
	text = reURL.ReplaceAllString(text, replacementSpace)
	// Collapse whitespace
	text = reMultiSpace.ReplaceAllString(text, replacementSpace)

	return strings.TrimSpace(text)
}

// stripCyrillic removes Cyrillic text (Russian characters) from the input,
// leaving only English text for validation.
func stripCyrillic(text string) string {
	text = reCyrillic.ReplaceAllString(text, replacementSpace)
	text = reMultiSpace.ReplaceAllString(text, replacementSpace)

	return strings.TrimSpace(text)
}

// --- LanguageTool API ---

// callLanguageTool sends text to the LanguageTool API and returns the matches.
func (c *languageToolClient) callLanguageTool(
	ctx context.Context,
	text string,
) ([]langtoolapi.Match, error) {
	textVal := text
	langVal := c.language

	resp, err := c.client.PostCheckWithFormdataBodyWithResponse(
		ctx,
		langtoolapi.PostCheckFormdataRequestBody{ //nolint:exhaustruct // optional fields
			Text:     &textVal,
			Language: &langVal,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("language tool api request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf(
			"language tool api returned status %d: %s",
			resp.StatusCode(),
			string(resp.Body),
		)
	}

	if resp.JSON200 == nil || resp.JSON200.Matches == nil {
		return nil, nil
	}

	return *resp.JSON200.Matches, nil
}

// --- Learner-friendly explanations ---

// learnerExplanations maps common LanguageTool rule IDs to explanations
// tailored for Russian-speaking English learners.
var learnerExplanations = map[string]string{
	"EN_A_VS_AN": "In English, the article 'a' becomes 'an' before words " +
		"that start with a vowel SOUND (not just a vowel letter). " +
		"Example: 'an hour' (silent h), 'a university' (starts with /juː/ sound).",

	"I_LOWERCASED": "In English, the pronoun 'I' is always capitalized, " +
		"no matter where it appears in a sentence. " +
		"This is different from Russian, where pronouns are never capitalized.",

	"MORFOLOGIK_RULE_EN_US": "This word is misspelled. English spelling often does not " +
		"match pronunciation, so check each letter carefully.",

	"UPPERCASE_SENTENCE_START": "Every sentence in English must begin with a capital letter.",

	"COMMA_PARENTHESIS": "In English, parenthetical elements (extra information) " +
		"should be set off by commas on both sides.",

	"IT_IS": "Remember: 'its' (no apostrophe) is a possessive pronoun meaning " +
		"'belonging to it.' 'It's' (with apostrophe) is a contraction of 'it is' or 'it has.'",

	"THEYRE": "'Their' is possessive (belonging to them). " +
		"'There' refers to a place. 'They're' is a contraction of 'they are.'",

	"YOUR_YOURE": "'Your' is possessive (belonging to you). " +
		"'You're' is a contraction of 'you are.'",

	"TOO_TO": "'To' is a preposition or infinitive marker. " +
		"'Too' means 'also' or 'excessively.' 'Two' is the number 2.",

	"LOSE_LOOSE": "'Lose' (one o) means to fail to win or to misplace something. " +
		"'Loose' (two o's) means not tight or not firmly fixed.",

	"EFFECT_AFFECT": "'Affect' is usually a verb meaning 'to influence.' " +
		"'Effect' is usually a noun meaning 'a result.' " +
		"Remember: you affect something to produce an effect.",

	"THEN_THAN": "'Then' refers to time or sequence (first this, then that). " +
		"'Than' is used for comparisons (bigger than, more than).",
}

// enrichExplanation returns a learner-friendly explanation for the given rule ID.
// Falls back to the original message if no enriched version is available.
func enrichExplanation(ruleID, originalMessage string) string {
	if enriched, ok := learnerExplanations[ruleID]; ok {
		return enriched
	}

	return originalMessage
}

// generateHint creates a structural hint from the match data without revealing
// the actual correction.
func generateHint(match *langtoolapi.Match) string {
	if match.Rule != nil && match.Rule.Category.Name != nil {
		cat := strings.ToLower(*match.Rule.Category.Name)
		switch cat {
		case "grammar":
			return "Check grammar: word form, agreement, or sentence structure."

		case "spelling", "typos":
			return "A word may be misspelled — check the spelling carefully."

		case "punctuation":
			return "Check punctuation: a comma, period, or other mark may be missing."

		case "capitalization", "casing":
			return "Check capitalization: a word may need to start with a capital letter."
		}
	}

	return "Review this part of the sentence for correctness."
}

// --- Error processing ---

// filterErrors removes matches that belong to skipped categories.
func filterErrors(matches []langtoolapi.Match) []langtoolapi.Match {
	filtered := make([]langtoolapi.Match, 0, len(matches))

	for i := range matches {
		if !shouldSkipMatch(&matches[i]) {
			filtered = append(filtered, matches[i])
		}
	}

	return filtered
}

// formatErrors converts LanguageTool matches into GrammarError structs with
// learner-friendly explanations and structural hints.
func formatErrors(matches []langtoolapi.Match) []GrammarError {
	errors := make([]GrammarError, 0, len(matches))

	for i := range matches {
		match := &matches[i]

		// Extract the mistake substring from the context
		mistake := ""
		ctxText := match.Context.Text
		ctxOff := match.Context.Offset
		ctxLen := match.Context.Length

		if ctxOff+ctxLen <= len(ctxText) {
			mistake = ctxText[ctxOff : ctxOff+ctxLen]
		}

		// Get rule ID for enriched explanations
		ruleID := ""
		if match.Rule != nil {
			ruleID = match.Rule.Id
		}

		// Get rule description
		ruleDesc := ""
		if match.Rule != nil {
			ruleDesc = match.Rule.Description
		}

		// Get category name
		categoryName := ""
		if match.Rule != nil && match.Rule.Category.Name != nil {
			categoryName = *match.Rule.Category.Name
		}

		errors = append(errors, GrammarError{
			Index:       i + 1, // 1-based
			Mistake:     mistake,
			Category:    categoryName,
			Hint:        generateHint(match),
			Rule:        ruleDesc,
			Explanation: enrichExplanation(ruleID, match.Message),
			Context:     ctxText,
		})
	}

	return errors
}

// --- Main validation ---

// validate performs English text validation using the LanguageTool API.
// It preprocesses the text, calls the API, filters and formats errors.
func (c *languageToolClient) validate(ctx context.Context, text string) ValidateEnglishResponse {
	// Strip ignored segments (code blocks, URLs, etc.)
	cleaned := stripIgnoredSegments(text)

	// Strip Cyrillic text
	cleaned = stripCyrillic(cleaned)

	// If nothing left to validate, return clean
	if cleaned == "" {
		return ValidateEnglishResponse{
			Correct:     true,
			Errors:      []GrammarError(nil),
			CleanedText: cleaned,
			Warning:     "",
		}
	}

	// Call LanguageTool API
	matches, err := c.callLanguageTool(ctx, cleaned)
	if err != nil {
		// API unavailable — return clean with warning instead of failing
		return ValidateEnglishResponse{
			Correct:     true,
			Errors:      []GrammarError(nil),
			CleanedText: cleaned,
			Warning:     fmt.Sprintf("LanguageTool API unavailable, skipping validation: %s", err),
		}
	}

	// Filter and format errors
	filtered := filterErrors(matches)
	formatted := formatErrors(filtered)

	return ValidateEnglishResponse{
		Correct:     len(filtered) == 0,
		Errors:      formatted,
		CleanedText: cleaned,
		Warning:     "",
	}
}
