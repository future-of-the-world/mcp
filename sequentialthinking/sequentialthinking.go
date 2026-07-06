// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package sequentialthinking implements an MCP source that exposes a
// dynamic, reflective problem-solving tool. The single tool
// (`sequentialthinking`) lets the LLM break a problem into steps,
// revise earlier thoughts, and branch into alternative reasoning paths
// while keeping the reasoning state across calls in the same source
// instance.
//
// This is a Go port of the upstream
// `@modelcontextprotocol/server-sequential-thinking`
// (MIT, modelcontextprotocol org): tool name, input/output schema,
// annotations, and core semantics are kept 1:1 with upstream. The only
// behavioral change is that the upstream chalk-colored ASCII box
// written to stderr is replaced by a structured slog record so it
// composes with the meta-server's LOG_LEVEL / LOG_JSON switches.
//
// Per-type Connect accepts an optional `disable_thought_logging` flag
// via the `connect:` map and returns a single tool. State is created
// once at Connect time and captured in the tool handler's closure; two
// `sequentialthinking` sources in the same config get independent
// state. A single source's state is mutex-protected because the MCP Go
// SDK handles tool calls concurrently.
package sequentialthinking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "embed"

	"go.amidman.dev/mcp/tool"
)

//go:embed schemas/sequentialthinking_input.json
var sequentialthinkingInput json.RawMessage

//go:embed schemas/sequentialthinking_output.json
var sequentialthinkingOutput json.RawMessage

const (
	// toolName is the registered tool name. Matches upstream so existing
	// client code, docs, and prompts written for the TypeScript version
	// keep working; users namespace with `tools.prefix` when they need
	// to disambiguate from another source's tool.
	toolName = "sequentialthinking"

	// connectKeyDisableThoughtLogging is the source-level knob for
	// suppressing the per-thought operator log line. Upstream reads the
	// same name from `process.env.DISABLE_THOUGHT_LOGGING`; we expose it
	// via `connect:` so all of this server's source-level toggles stay
	// in the config file.
	connectKeyDisableThoughtLogging = "disable_thought_logging"
)

// sentinel errors are wrapped by Connect with the source-type prefix
// before they reach source.dispatch; the per-field prefix lives in
// decodeConnect so the final message is single-segment, not double.
var (
	errDisableThoughtLoggingWrongType = errors.New(
		"sequentialthinking: connect.disable_thought_logging must be a boolean or a string bool",
	)

	// errThoughtEmpty is returned by the handler when the caller omits
	// the required `thought` field. Upstream's Zod schema treats an
	// empty string as a valid thought; we reject it because empty
	// thoughts carry no reasoning and are almost always a caller bug.
	errThoughtEmpty = errors.New("sequentialthinking: thought is required")

	// errThoughtNumberInvalid is returned by the handler when the
	// caller passes a thought_number below 1. Upstream's Zod schema
	// enforces `min(1)`; we surface the same constraint as a Go error.
	errThoughtNumberInvalid = errors.New("sequentialthinking: thought_number must be >= 1")

	// errTotalThoughtsInvalid is returned by the handler when the
	// caller passes a total_thoughts below 1. Mirrors upstream's
	// `min(1)` constraint.
	errTotalThoughtsInvalid = errors.New("sequentialthinking: total_thoughts must be >= 1")
)

// ThoughtData is the structured form of a single call to the
// sequentialthinking tool. It mirrors the upstream TypeScript
// `ThoughtData` interface 1:1; field names use snake_case because that
// is what the wire JSON uses (per the input schema).
type ThoughtData struct {
	Thought           string `json:"thought"`
	ThoughtNumber     int    `json:"thought_number"`
	TotalThoughts     int    `json:"total_thoughts"`
	IsRevision        bool   `json:"is_revision,omitzero"`
	RevisesThought    int    `json:"revises_thought,omitzero"`
	BranchFromThought int    `json:"branch_from_thought,omitzero"`
	BranchID          string `json:"branch_id,omitzero"`
	NeedsMoreThoughts bool   `json:"needs_more_thoughts,omitzero"`
	NextThoughtNeeded bool   `json:"next_thought_needed"`
}

// ThoughtResponse is the structured form of the tool's response. It
// mirrors the upstream outputSchema 1:1; field names use snake_case to
// match the JSON contract.
type ThoughtResponse struct {
	ThoughtNumber        int      `json:"thought_number"`
	TotalThoughts        int      `json:"total_thoughts"`
	NextThoughtNeeded    bool     `json:"next_thought_needed"`
	Branches             []string `json:"branches"`
	ThoughtHistoryLength int      `json:"thought_history_length"`
}

// config holds the decoded `connect:` map for a sequentialthinking
// source. The single field is optional and falls back to false.
type config struct {
	DisableThoughtLogging bool
}

// decodeConnect decodes the source's `connect:` map into a config. The
// single field accepts YAML/JSON-native bools, strings "true"/"false",
// and numbers (0 → false, anything else → true) so a misconfigured
// value surfaces as a clear error rather than silently defaulting.
func decodeConnect(connect map[string]any) (config, error) {
	var cfg config

	raw, present := connect[connectKeyDisableThoughtLogging]
	if !present || raw == nil {
		return cfg, nil
	}

	value, decodeErr := decodeBool(raw)
	if decodeErr != nil {
		return cfg, fmt.Errorf("connect.%s: %w", connectKeyDisableThoughtLogging, decodeErr)
	}

	cfg.DisableThoughtLogging = value

	return cfg, nil
}

// decodeBool accepts the natural YAML/JSON shapes a boolean can take:
// a Go bool, a string ("true"/"false", case-insensitive), or an integer
// (0 → false, anything else → true). Maps, slices, and other types are
// rejected with a wrapped decode.ErrWrongType so a misconfigured value
// surfaces as a clear message.
func decodeBool(raw any) (bool, error) {
	switch value := raw.(type) {
	case bool:
		return value, nil

	case string:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false, fmt.Errorf(
				"%w: %q is not a valid bool string",
				errDisableThoughtLoggingWrongType,
				value,
			)
		}

		return parsed, nil

	case int:
		return value != 0, nil

	case int64:
		return value != 0, nil

	case float64:
		return value != 0, nil

	default:
		return false, fmt.Errorf("%w: got %T", errDisableThoughtLoggingWrongType, raw)
	}
}

// sequentialThinkingServer holds the per-source state for one
// sequentialthinking source. A single source owns exactly one
// instance; two sources in the same config get independent instances
// because Connect builds a fresh value every time it is called.
//
// The mutex covers both the thoughtHistory slice and the branches map.
// Lock is held only across the in-memory mutation and the structured
// log emission so log lines are produced in submission order.
type sequentialThinkingServer struct {
	mu                    sync.Mutex
	thoughtHistory        []ThoughtData
	branches              map[string][]ThoughtData
	disableThoughtLogging bool
	logger                *slog.Logger
}

// newSequentialThinkingServer constructs an empty server with the
// supplied config-derived flags and logger. The branches map is
// initialized eagerly because we never want a nil-map panic on first
// write.
func newSequentialThinkingServer(cfg config, logger *slog.Logger) *sequentialThinkingServer {
	return &sequentialThinkingServer{
		mu:                    sync.Mutex{},
		thoughtHistory:        []ThoughtData{},
		branches:              make(map[string][]ThoughtData),
		disableThoughtLogging: cfg.DisableThoughtLogging,
		logger:                logger,
	}
}

// processThought is the per-call entry point. It mutates the server's
// state in a critical section and returns the response payload.
//
// Behavior matches upstream's lib.ts::processThought 1:1:
//  1. If thoughtNumber > totalThoughts, bump totalThoughts up.
//  2. Append the (possibly mutated) thought to thoughtHistory.
//  3. If branchFromThought and branchId are both set, append the
//     thought to branches[branchId] (creating the slice if needed).
//  4. Emit a structured debug log line unless disable_thought_logging
//     is set.
//  5. Return the response payload.
//
// The handler passes its request context through so the server-internal
// log line uses DebugContext as the linter expects.
func (s *sequentialThinkingServer) processThought(
	ctx context.Context,
	input *ThoughtData,
) ThoughtResponse {
	s.mu.Lock()
	defer s.mu.Unlock()

	if input.ThoughtNumber > input.TotalThoughts {
		input.TotalThoughts = input.ThoughtNumber
	}

	s.thoughtHistory = append(s.thoughtHistory, *input)

	if input.BranchFromThought != 0 && input.BranchID != "" {
		s.branches[input.BranchID] = append(s.branches[input.BranchID], *input)
	}

	if !s.disableThoughtLogging {
		s.logger.DebugContext(ctx, "sequentialthinking recorded",
			"thought_number", input.ThoughtNumber,
			"total_thoughts", input.TotalThoughts,
			"is_revision", input.IsRevision,
			"branch_id", input.BranchID,
			"history_length", len(s.thoughtHistory),
		)
	}

	return ThoughtResponse{
		ThoughtNumber:        input.ThoughtNumber,
		TotalThoughts:        input.TotalThoughts,
		NextThoughtNeeded:    input.NextThoughtNeeded,
		Branches:             branchKeys(s.branches),
		ThoughtHistoryLength: len(s.thoughtHistory),
	}
}

// branchKeys returns the sorted set of branch IDs currently in the
// map. Sorting makes the response deterministic for tests and for any
// client that diffs the output.
func branchKeys(branches map[string][]ThoughtData) []string {
	if len(branches) == 0 {
		return []string{}
	}

	out := make([]string, 0, len(branches))
	for key := range branches {
		out = append(out, key)
	}

	sortStrings(out)

	return out
}

// sortStrings is a tiny ascending-string sort, kept local so the
// package does not grow a stdlib `sort` dependency for a single use
// site. The implementation is in-place to avoid an allocation on every
// call.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j-1] > values[j]; j-- {
			values[j-1], values[j] = values[j], values[j-1]
		}
	}
}

// Connect decodes the source's `connect:` map, builds the
// per-source state, and returns the single `sequentialthinking` tool.
// The tool advertises ReadOnlyHint=true, DestructiveHint=false,
// IdempotentHint=true, OpenWorldHint=false — matching upstream's
// annotations 1:1.
func Connect(
	_ context.Context,
	connect map[string]any,
	opts ...tool.Option,
) (tool.Response, error) {
	cfg, err := decodeConnect(connect)
	if err != nil {
		return tool.Response{}, fmt.Errorf("sequentialthinking: decode: %w", err)
	}

	logger := tool.NewOptions(opts...).Logger()

	server := newSequentialThinkingServer(cfg, logger)

	readOnly := true
	destructive := false
	idempotent := true
	openWorld := false

	return tool.Response{
		Tools: []tool.Tool{
			{
				Tool: &mcp.Tool{
					Name:         toolName,
					Description:  toolDescription,
					InputSchema:  sequentialthinkingInput,
					OutputSchema: sequentialthinkingOutput,
					Annotations: &mcp.ToolAnnotations{
						Title:           toolName,
						ReadOnlyHint:    readOnly,
						DestructiveHint: &destructive,
						IdempotentHint:  idempotent,
						OpenWorldHint:   &openWorld,
					},
				},
				Handler: handleSequentialThinking(server),
			},
		},
	}, nil
}
