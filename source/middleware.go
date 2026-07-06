// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package source

import (
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.amidman.dev/mcp/tool"
)

// applyPrefix prepends prefix to each tool's Name. An empty prefix leaves
// names unchanged. The prefix is joined to the tool's existing Name without
// a separator: the caller is expected to include any desired separator
// (e.g. "_") in the prefix string itself. The mutation is in-place on the
// embedded *mcp.Tool each tool.Tool carries.
func applyPrefix(tools []tool.Tool, prefix string) []tool.Tool {
	if prefix == "" {
		return tools
	}

	for i := range tools {
		if tools[i].Tool == nil {
			continue
		}

		tools[i].Name = prefix + tools[i].Name
	}

	return tools
}

// applyReadOnly drops every tool whose embedded *mcp.Tool does not
// advertise Annotations.ReadOnlyHint == true. When enabled is false the
// input slice is returned unchanged. A tool with a nil embedded
// *mcp.Tool or nil Annotations is dropped when enabled is true: a
// nil embedded pointer is structurally malformed (nothing to inspect),
// and a nil Annotations field means the implementer chose not to
// classify the tool — the same "unknown" semantics established by
// drop-classify-config.
//
//nolint:revive // flag-parameter: enabled is the natural shape
func applyReadOnly(tools []tool.Tool, enabled bool) []tool.Tool {
	if !enabled {
		return tools
	}

	out := make([]tool.Tool, 0, len(tools))

	for _, entry := range tools {
		if entry.Tool == nil {
			continue
		}

		if entry.Annotations == nil {
			continue
		}

		if !entry.Annotations.ReadOnlyHint {
			continue
		}

		out = append(out, entry)
	}

	return out
}

// applyRemove drops every tool whose Name matches any of the given
// patterns (compiled as regex). A pattern that fails to compile is
// treated as no-match for every tool and is otherwise ignored: a bad
// pattern is a configuration error, not a runtime error, and must not
// take down server startup.
func applyRemove(tools []tool.Tool, patterns []string) []tool.Tool {
	if len(patterns) == 0 {
		return tools
	}

	compiled := compilePatterns(patterns)
	if len(compiled) == 0 {
		return tools
	}

	out := make([]tool.Tool, 0, len(tools))

	for _, entry := range tools {
		if nameMatchesAny(entry.Tool, compiled) {
			continue
		}

		out = append(out, entry)
	}

	return out
}

// applyEnableOnly drops every tool whose Name matches none of the
// given patterns (compiled as regex). The opposite of applyRemove —
// it keeps the matching set and drops the rest. An empty patterns
// slice is a no-op and returns tools unchanged (the upstream code
// path in planTools skips the call when EnableOnly is non-empty, so
// this case is defensive only). A nil embedded *mcp.Tool has no
// public name to test against the whitelist and is always dropped,
// in contrast to applyRemove which keeps nil-tool entries (a name
// that does not match any remove pattern is, by definition, kept).
func applyEnableOnly(tools []tool.Tool, patterns []string) []tool.Tool {
	if len(patterns) == 0 {
		return tools
	}

	compiled := compilePatterns(patterns)
	if len(compiled) == 0 {
		return tools
	}

	out := make([]tool.Tool, 0, len(tools))

	for _, entry := range tools {
		if !nameMatchesAny(entry.Tool, compiled) {
			continue
		}

		out = append(out, entry)
	}

	return out
}

// compilePatterns compiles each pattern into a *regexp.Regexp, skipping
// any that fail to compile. The returned slice is never nil but may be
// empty when no pattern compiled successfully.
func compilePatterns(patterns []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))

	for _, raw := range patterns {
		compiled, err := regexp.Compile(raw)
		if err != nil {
			continue
		}

		out = append(out, compiled)
	}

	return out
}

// nameMatchesAny reports whether the given *mcp.Tool's Name matches any
// of the compiled patterns. A nil tool never matches.
func nameMatchesAny(mcpTool *mcp.Tool, patterns []*regexp.Regexp) bool {
	if mcpTool == nil {
		return false
	}

	for _, pattern := range patterns {
		if pattern.MatchString(mcpTool.Name) {
			return true
		}
	}

	return false
}
