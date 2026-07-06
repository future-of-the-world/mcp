// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package source

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/tool"
)

// makeTool builds a tool.Tool carrying an *mcp.Tool with the given name.
// The Handler is a no-op because the middleware tests never invoke it;
// they only read the (possibly mutated) *mcp.Tool.Name.
func makeTool(name string) tool.Tool {
	return tool.Tool{
		Tool: &mcp.Tool{
			Name:        name,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: noopHandler(),
	}
}

// makeReadOnlyTool builds a tool.Tool whose embedded *mcp.Tool has the
// given name and Annotations.ReadOnlyHint set to readOnly. Tests that
// exercise applyReadOnly use this helper to fabricate tools with
// well-defined annotations; makeTool produces tools with nil
// annotations, which is the implementer "I don't know" signal.
//
// All five ToolAnnotations fields are populated (even the ones that
// are semantically irrelevant when ReadOnlyHint is set) so the helper
// satisfies the project's exhaustruct lint rule.
func makeReadOnlyTool(name string, readOnly bool) tool.Tool {
	return tool.Tool{
		Tool: &mcp.Tool{
			Name:        name,
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Annotations: &mcp.ToolAnnotations{
				Title:           "",
				ReadOnlyHint:    readOnly,
				DestructiveHint: (*bool)(nil),
				IdempotentHint:  false,
				OpenWorldHint:   (*bool)(nil),
			},
		},
		Handler: noopHandler(),
	}
}

// noopHandler returns a no-op mcp.ToolHandler. It exists only so makeTool
// can populate every field of tool.Tool (satisfying exhaustruct); the
// middleware tests never invoke the handler.
func noopHandler() mcp.ToolHandler {
	return func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return new(mcp.CallToolResult), nil
	}
}

// names extracts the Name from each tool.Tool's embedded *mcp.Tool. It is
// the projection used by every middleware test assertion.
func names(tools []tool.Tool) []string {
	out := make([]string, 0, len(tools))

	for _, entry := range tools {
		out = append(out, entry.Name)
	}

	return out
}

// --- applyPrefix ---

func TestApplyPrefix_OverrideUsesExplicitPrefix(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{makeTool("list_tables"), makeTool("execute_query")}

	got := applyPrefix(tools, "pg_")

	assert.Equal(t, []string{"pg_list_tables", "pg_execute_query"}, names(got))
}

func TestApplyPrefix_EmptyAppliesNoPrefix(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{makeTool("list_tables"), makeTool("execute_query")}

	// An empty prefix leaves names unchanged. There is no source-name
	// fallback: the user opts in by setting tools.prefix explicitly.
	got := applyPrefix(tools, "")

	assert.Equal(t, []string{"list_tables", "execute_query"}, names(got))
}

func TestApplyPrefix_EmptyInputIsNoOp(t *testing.T) {
	t.Parallel()

	got := applyPrefix([]tool.Tool(nil), "pg_")

	assert.Empty(t, got)
}

func TestApplyPrefix_NilEmbeddedToolIsSkipped(t *testing.T) {
	t.Parallel()

	// A tool.Tool with a nil embedded *mcp.Tool must not panic; it is
	// skipped rather than dereferenced.
	tools := []tool.Tool{
		{Tool: nil, Handler: nil},
		makeTool("list_tables"),
	}

	got := applyPrefix(tools, "pg_")

	require.Len(t, got, 2)
	assert.Nil(t, got[0].Tool)
	assert.Equal(t, "pg_list_tables", got[1].Name)
}

// --- applyRemove ---

func TestApplyRemove_MatchingPatternDropsTool(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{
		makeTool("pg_list_tables"),
		makeTool("pg_execute_query"),
	}

	// The anchored pattern matches only the first tool.
	got := applyRemove(tools, []string{"^pg_list_"})

	assert.Equal(t, []string{"pg_execute_query"}, names(got))
}

func TestApplyRemove_NonMatchingPatternKeepsAll(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{
		makeTool("pg_list_tables"),
		makeTool("pg_execute_query"),
	}

	got := applyRemove(tools, []string{"^forgejo_"})

	assert.Equal(t, []string{"pg_list_tables", "pg_execute_query"}, names(got))
}

func TestApplyRemove_MultiplePatternsAnyMatchDrops(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{
		makeTool("pg_list_tables"),
		makeTool("pg_execute_query"),
		makeTool("pg_get_table_info"),
	}

	// Two patterns: each tool matching either is dropped.
	got := applyRemove(tools, []string{"^pg_list_", "get_"})

	assert.Equal(t, []string{"pg_execute_query"}, names(got))
}

func TestApplyRemove_EmptyPatternsKeepsAll(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{makeTool("a"), makeTool("b")}

	got := applyRemove(tools, []string(nil))

	assert.Equal(t, []string{"a", "b"}, names(got))
}

func TestApplyRemove_BadPatternIsIgnored(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{
		makeTool("pg_list_tables"),
		makeTool("pg_execute_query"),
	}

	// An unparseable regex must not abort the whole filter; it is
	// skipped and the remaining valid patterns still apply.
	got := applyRemove(tools, []string{"(", "^pg_execute_"})

	assert.Equal(t, []string{"pg_list_tables"}, names(got))
}

func TestApplyRemove_AllBadPatternsKeepsAll(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{makeTool("a"), makeTool("b")}

	// When every pattern fails to compile, nothing is removed.
	got := applyRemove(tools, []string{"(", "["})

	assert.Equal(t, []string{"a", "b"}, names(got))
}

func TestApplyRemove_NilEmbeddedToolIsKept(t *testing.T) {
	t.Parallel()

	// A tool.Tool whose embedded *mcp.Tool is nil never matches any
	// pattern and is therefore kept.
	tools := []tool.Tool{
		{Tool: nil, Handler: nil},
		makeTool("keep_me"),
	}

	got := applyRemove(tools, []string{".*"})

	require.Len(t, got, 1)
	assert.Nil(t, got[0].Tool)
}

// --- applyPrefix then applyRemove (order) ---

func TestPrefixThenRemove_PrefixedNamesAreFiltered(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{
		makeTool("list_tables"),
		makeTool("execute_query"),
		makeTool("get_table_info"),
	}

	// applyPrefix runs first, then applyRemove matches against the
	// prefixed names. This lets the user write remove patterns against
	// the final public-facing names.
	prefixed := applyPrefix(tools, "pg_")
	removed := applyRemove(prefixed, []string{"^pg_get_"})

	assert.Equal(t, []string{"pg_list_tables", "pg_execute_query"}, names(removed))
}

// --- applyEnableOnly ---

func TestApplyEnableOnly_MatchingPatternKeepsTool(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{
		makeTool("pg_list_tables"),
		makeTool("pg_execute_query"),
	}

	// The anchored pattern matches only the first tool.
	got := applyEnableOnly(tools, []string{"^pg_list_"})

	assert.Equal(t, []string{"pg_list_tables"}, names(got))
}

func TestApplyEnableOnly_NonMatchingPatternDropsAll(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{
		makeTool("pg_list_tables"),
		makeTool("pg_execute_query"),
	}

	got := applyEnableOnly(tools, []string{"^forgejo_"})

	assert.Empty(t, names(got))
}

func TestApplyEnableOnly_MultiplePatternsAnyMatchKeeps(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{
		makeTool("pg_list_tables"),
		makeTool("pg_execute_query"),
		makeTool("pg_get_table_info"),
	}

	// Two patterns: a tool matching either is kept.
	got := applyEnableOnly(tools, []string{"^pg_list_", "get_"})

	assert.Equal(t, []string{"pg_list_tables", "pg_get_table_info"}, names(got))
}

func TestApplyEnableOnly_EmptyPatternsKeepsAll(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{makeTool("a"), makeTool("b")}

	got := applyEnableOnly(tools, []string(nil))

	assert.Equal(t, []string{"a", "b"}, names(got))
}

func TestApplyEnableOnly_BadPatternIsIgnored(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{
		makeTool("pg_list_tables"),
		makeTool("pg_execute_query"),
	}

	// An unparseable regex must not abort the whole filter; it is
	// skipped and the remaining valid patterns still apply.
	got := applyEnableOnly(tools, []string{"(", "^pg_list_"})

	assert.Equal(t, []string{"pg_list_tables"}, names(got))
}

func TestApplyEnableOnly_AllBadPatternsKeepsAll(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{makeTool("a"), makeTool("b")}

	// When every pattern fails to compile and a whitelist is requested
	// with no usable patterns, no tool can be matched against the
	// (empty) compiled set, but the policy is to fail soft (mirror
	// applyRemove's "all-bad ⇒ no-op" semantics) so we keep all
	// tools. The caller still has EnableOnly set, but with zero
	// compiled patterns we have no way to safely apply a whitelist.
	got := applyEnableOnly(tools, []string{"(", "["})

	assert.Equal(t, []string{"a", "b"}, names(got))
}

func TestApplyEnableOnly_NilEmbeddedToolIsDropped(t *testing.T) {
	t.Parallel()

	// A tool.Tool whose embedded *mcp.Tool is nil has no public name
	// to test against the whitelist and is therefore dropped.
	tools := []tool.Tool{
		{Tool: nil, Handler: nil},
		makeTool("pg_list_tables"),
	}

	got := applyEnableOnly(tools, []string{".*"})

	require.Len(t, got, 1)
	assert.Equal(t, "pg_list_tables", got[0].Name)
}

// --- applyReadOnly ---

func TestApplyReadOnly_DisabledKeepsAll(t *testing.T) {
	t.Parallel()

	// A mixed set of tools (read-only, mutating, nil-annotations) is
	// passed through unchanged when the flag is off: this is the
	// default behavior every existing config relies on.
	tools := []tool.Tool{
		makeReadOnlyTool("list_things", true),
		makeReadOnlyTool("delete_thing", false),
		makeTool("untagged"),
	}

	got := applyReadOnly(tools, false)

	assert.Equal(t, []string{"list_things", "delete_thing", "untagged"}, names(got))
}

func TestApplyReadOnly_EnabledKeepsOnlyReadOnly(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{
		makeReadOnlyTool("list_things", true),
		makeReadOnlyTool("delete_thing", false),
		makeReadOnlyTool("get_thing", true),
	}

	got := applyReadOnly(tools, true)

	assert.Equal(t, []string{"list_things", "get_thing"}, names(got))
}

func TestApplyReadOnly_EnabledDropsNilAnnotations(t *testing.T) {
	t.Parallel()

	// A tool whose Annotations is nil signals "I don't know" from the
	// implementer. We honor that signal by not classifying the tool as
	// read-only, so it is dropped when read_only: true is set. This
	// matches drop-classify-config's nil-annotations semantics.
	tools := []tool.Tool{
		makeTool("untagged"),
		makeReadOnlyTool("readable", true),
	}

	got := applyReadOnly(tools, true)

	assert.Equal(t, []string{"readable"}, names(got))
}

func TestApplyReadOnly_EnabledDropsNilEmbeddedTool(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{
		{Tool: nil, Handler: nil},
		makeReadOnlyTool("readable", true),
	}

	// The nil-embedded entry has nothing to inspect, so it is dropped
	// under read_only: true; only the well-formed read-only tool
	// survives.
	got := applyReadOnly(tools, true)

	require.Len(t, got, 1)
	assert.Equal(t, "readable", got[0].Name)
}

func TestApplyReadOnly_EnabledEmptyInputIsEmpty(t *testing.T) {
	t.Parallel()

	got := applyReadOnly([]tool.Tool(nil), true)

	assert.Empty(t, got)
}

func TestApplyReadOnly_EnabledAllMutatingProducesEmpty(t *testing.T) {
	t.Parallel()

	tools := []tool.Tool{
		makeReadOnlyTool("delete_thing", false),
		makeReadOnlyTool("create_thing", false),
	}

	got := applyReadOnly(tools, true)

	assert.Empty(t, got)
}
