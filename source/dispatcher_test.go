// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package source

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/tool"
)

// --- Apply: real Connect wiring (end-to-end) ---

// newTestServer returns a fresh in-memory MCP server for Apply tests.
func newTestServer() *mcp.Server {
	return mcp.NewServer(
		&mcp.Implementation{Name: "test", Version: "0.0.0"},
		(*mcp.ServerOptions)(nil),
	)
}

// TestApply_English_RegistersBaseNameTool drives Apply with an english
// source (which is dependency-free at Connect time) and verifies the
// returned tool is registered with its base name (no source-name
// default prefix) and the per-type Annotations flowing through end-to-end.
func TestApply_English_RegistersBaseNameTool(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	err := Apply(t.Context(), server, []Source{{
		Name:    "grammar",
		Type:    "english",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{},
	}})
	require.NoError(t, err)

	listed := listServerTools(t, server)
	require.Len(t, listed, 1)

	// english.Connect returns one tool named "validate_english"; with
	// no tools.prefix override it keeps that name.
	assert.Equal(t, "validate_english", listed[0].Name)

	// The per-type Annotations must round-trip from english.Connect to
	// the registered tool — the dispatcher applies no translation layer.
	require.NotNil(t, listed[0].Annotations)
	assert.True(t, listed[0].Annotations.ReadOnlyHint)
}

// TestApply_ExplicitPrefixIsUsed sets Tools.Prefix and confirms it is
// prepended verbatim to each tool's base name.
func TestApply_ExplicitPrefixIsUsed(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	err := Apply(t.Context(), server, []Source{{
		Name:    "grammar",
		Type:    "english",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{Prefix: "en_"},
	}})
	require.NoError(t, err)

	listed := listServerTools(t, server)
	require.Len(t, listed, 1)

	assert.Equal(t, "en_validate_english", listed[0].Name)
}

// TestApply_NoPrefixLeavesNameUntouched confirms that an empty
// Tools.Prefix leaves tool names unchanged. There is no source-name
// fallback: omit the prefix and the upstream tool's name is the
// public-facing name.
func TestApply_NoPrefixLeavesNameUntouched(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	err := Apply(t.Context(), server, []Source{{
		Name:    "grammar",
		Type:    "english",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{},
	}})
	require.NoError(t, err)

	listed := listServerTools(t, server)
	require.Len(t, listed, 1)

	assert.Equal(t, "validate_english", listed[0].Name)
}

// TestApply_RemoveDropsPrefixedTool configures an english source with an
// explicit prefix and a remove pattern that matches the prefixed name,
// then asserts no tools are registered. This pins the prefix-then-remove
// ordering: the remove pattern is matched against the final prefixed
// name.
func TestApply_RemoveDropsPrefixedTool(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	err := Apply(t.Context(), server, []Source{{
		Name:    "grammar",
		Type:    "english",
		Connect: map[string]any(nil),
		Tools: ToolsConfig{
			Prefix: "grammar_",
			Remove: []string{"^grammar_"},
		},
	}})
	require.NoError(t, err)

	listed := listServerTools(t, server)
	assert.Empty(t, listed)
}

// TestApply_HTTP_RegistersTool uses the http source (no network at
// Connect time) to confirm a second dependency-free type registers its
// single tool through Apply under its base name.
func TestApply_HTTP_RegistersTool(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	err := Apply(t.Context(), server, []Source{{
		Name: "api",
		Type: "http",
		Connect: map[string]any{
			"url":    "https://example.com/api",
			"method": "GET",
		},
		Tools: ToolsConfig{},
	}})
	require.NoError(t, err)

	listed := listServerTools(t, server)
	require.Len(t, listed, 1)

	// With no tools.prefix override, the http tool is registered under
	// its base name "http" (not "<source>http").
	assert.Equal(t, "http", listed[0].Name)
}

// TestApply_SequentialThinking_RegistersTool confirms the new
// sequentialthinking source type registers its single tool through
// Apply under the source name (no tools.prefix override needed —
// sequentialthinking exposes its upstream name directly so existing
// client code, docs, and prompts keep working).
//
// The sequentialthinking source does NOT touch the network or spawn a
// process at Connect time, so this test runs entirely in-process — the
// same dependency-free shape as the english and http test variants.
func TestApply_SequentialThinking_RegistersTool(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	err := Apply(t.Context(), server, []Source{{
		Name:    "think",
		Type:    "sequentialthinking",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{},
	}})
	require.NoError(t, err)

	listed := listServerTools(t, server)
	require.Len(t, listed, 1)

	assert.Equal(t, "sequentialthinking", listed[0].Name)

	require.NotNil(t, listed[0].Annotations)
	assert.Truef(t, listed[0].Annotations.ReadOnlyHint,
		"sequentialthinking must advertise ReadOnlyHint=true")
	require.NotNil(t, listed[0].Annotations.DestructiveHint)
	assert.Falsef(t, *listed[0].Annotations.DestructiveHint,
		"sequentialthinking must advertise DestructiveHint=false")
	assert.Truef(t, listed[0].Annotations.IdempotentHint,
		"sequentialthinking must advertise IdempotentHint=true")
	require.NotNil(t, listed[0].Annotations.OpenWorldHint)
	assert.Falsef(t, *listed[0].Annotations.OpenWorldHint,
		"sequentialthinking must advertise OpenWorldHint=false")
}

// TestApply_SequentialThinking_PrefixApplies verifies the dispatcher
// middleware prepends an explicit tools.prefix to the source's tool
// name when one is supplied.
func TestApply_SequentialThinking_PrefixApplies(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	err := Apply(t.Context(), server, []Source{{
		Name:    "think",
		Type:    "sequentialthinking",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{Prefix: "deep_"},
	}})
	require.NoError(t, err)

	listed := listServerTools(t, server)
	require.Len(t, listed, 1)
	assert.Equal(t, "deep_sequentialthinking", listed[0].Name)
}

// TestApply_Shell_RegistersRunCommand confirms the new shell source
// type registers its single tool through Apply under the source name
// (no tools.prefix override needed — shell defaults the tool name to
// "run_command" so the source-name prefix is the natural collision
// boundary between multiple shell sources).
//
// The shell source does NOT spawn a process at Connect time, so this
// test runs entirely without network or process execution — the same
// dependency-free shape as the english and http test variants.
func TestApply_Shell_RegistersRunCommand(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	err := Apply(t.Context(), server, []Source{{
		Name: "shell",
		Type: "shell",
		Connect: map[string]any{
			"working_dir": "/tmp",
		},
		Tools: ToolsConfig{},
	}})
	require.NoError(t, err)

	listed := listServerTools(t, server)
	require.Len(t, listed, 1)

	assert.Equal(t, "run_command", listed[0].Name)

	require.NotNil(t, listed[0].Annotations)
	assert.Falsef(t, listed[0].Annotations.ReadOnlyHint,
		"shell tool must not advertise ReadOnlyHint=true")
	require.NotNil(t, listed[0].Annotations.DestructiveHint)
	assert.Truef(t, *listed[0].Annotations.DestructiveHint,
		"shell tool must advertise DestructiveHint=true")
}

// TestApply_Shell_PrefixApplies verifies the source-name default prefix
// for shell sources. The dispatcher middleware prepends the source name
// to the tool name; for the shell source the resulting tool name is
// "<source>_run_command".
func TestApply_Shell_PrefixApplies(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	err := Apply(t.Context(), server, []Source{{
		Name: "build",
		Type: "shell",
		Connect: map[string]any{
			"working_dir": "/tmp",
		},
		Tools: ToolsConfig{
			Prefix: "build_",
		},
	}})
	require.NoError(t, err)

	listed := listServerTools(t, server)
	require.Len(t, listed, 1)
	assert.Equal(t, "build_run_command", listed[0].Name)
}

// TestApply_FS_RegistersTools confirms the new fs source type
// registers its twelve tools through Apply under their base names
// (no tools.prefix override). The fs source does no I/O at Connect
// time — only the allowed_paths validation runs — so this test is
// entirely dependency-free, matching the english / http / shell
// pattern. The MCP SDK returns tools from ListTools in a
// non-deterministic order, so we assert on name membership rather
// than positional indexing.
func TestApply_FS_RegistersTools(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	err := Apply(t.Context(), server, []Source{{
		Name: "fs",
		Type: "fs",
		Connect: map[string]any{
			"allowed_paths": []string{"/tmp"},
		},
		Tools: ToolsConfig{},
	}})
	require.NoError(t, err)

	listed := listServerTools(t, server)
	require.Len(t, listed, 13)

	wantNames := map[string]bool{
		"list_allowed_directories": true,
		"read_file":                true,
		"write_file":               true,
		"edit_file":                true,
		"create_directory":         true,
		"list_directory":           true,
		"directory_tree":           true,
		"move_file":                true,
		"copy_file":                true,
		"delete_file":              true,
		"search_files":             true,
		"get_file_info":            true,
		"grep":                     true,
	}

	for _, item := range listed {
		assert.Truef(t, wantNames[item.Name],
			"unexpected tool name %q in server's tool list", item.Name)
	}
}

// TestApply_FS_PrefixApplies verifies the dispatcher middleware
// behavior on the fs source: an explicit Tools.Prefix is prepended
// to every base name. Useful for hosting multiple fs sources
// pointing at different roots. The assertion is membership-based
// because the SDK's ListTools order is not deterministic.
func TestApply_FS_PrefixApplies(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	err := Apply(t.Context(), server, []Source{{
		Name: "work",
		Type: "fs",
		Connect: map[string]any{
			"allowed_paths": []string{"/tmp/work"},
		},
		Tools: ToolsConfig{Prefix: "work_"},
	}})
	require.NoError(t, err)

	listed := listServerTools(t, server)
	require.Len(t, listed, 13)

	var sawPrefixed bool

	for _, item := range listed {
		if item.Name == "work_read_file" {
			sawPrefixed = true
		}
	}

	assert.Truef(t, sawPrefixed,
		"expected work_read_file in server's tool list after prefix application")
}

// TestApply_FS_RequiresAllowedPaths verifies that a misconfigured
// fs source surfaces as a per-source failure during Apply. The
// source name must appear in the joined error so the operator can
// find the broken config entry.
func TestApply_FS_RequiresAllowedPaths(t *testing.T) {
	t.Parallel()

	server := newTestServer()
	logger, logBuf := captureLogger()

	err := Apply(t.Context(), server, []Source{{
		Name:    "broken",
		Type:    "fs",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{},
	}}, tool.WithLogger(logger))

	require.Error(t, err)
	assert.Contains(t, err.Error(), `"broken"`)
	assert.Contains(t, err.Error(), "allowed_paths")
	assert.Containsf(t, logBuf.String(), `"name":"broken"`,
		"the per-source failure must be logged with the source name")
}

// TestApply_Shell_RequiresWorkingDir verifies that a shell source with
// no working_dir in connect surfaces as a per-source failure during
// Apply. The source name must appear in the joined error so the
// operator can find the misconfigured entry in their config.
func TestApply_Shell_RequiresWorkingDir(t *testing.T) {
	t.Parallel()

	server := newTestServer()
	logger, logBuf := captureLogger()

	err := Apply(t.Context(), server, []Source{{
		Name:    "broken",
		Type:    "shell",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{},
	}}, tool.WithLogger(logger))

	require.Error(t, err)
	assert.Contains(t, err.Error(), `"broken"`)
	assert.Contains(t, err.Error(), "working_dir")
	assert.Containsf(t, logBuf.String(), `"name":"broken"`,
		"the per-source failure must be logged with the source name")
}

// TestApply_ConnectErrorIsWrapped drives a per-type Connect that fails
// (http with an empty URL) and asserts the error is wrapped with the
// source name via the `source %q:` prefix. The single-source-failure
// case still surfaces as a non-nil return: with no surviving tools,
// Apply has nothing to register and the server should refuse to start.
func TestApply_ConnectErrorIsWrapped(t *testing.T) {
	t.Parallel()

	server := newTestServer()
	logger, logBuf := captureLogger()

	err := Apply(t.Context(), server, []Source{{
		Name:    "broken",
		Type:    "http",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{},
	}}, tool.WithLogger(logger))

	require.Error(t, err)
	assert.Contains(t, err.Error(), `"broken"`)
	assert.Containsf(t, logBuf.String(), `"name":"broken"`,
		"the per-source failure must be logged even when Apply returns an error")
}

// TestApply_ToleratesPerSourceFailure is the user-facing locking test:
// it pins the new "log and continue" contract. One source fails its
// Connect (http with an empty URL) and one source succeeds (english).
// Apply must return nil — the MCP server should not crash because of a
// single misconfigured source — and the surviving source's tool must
// be registered on the server. The failing source's name and error
// must appear in the structured log output so the operator can find
// the broken config without re-running with debug logging.
func TestApply_ToleratesPerSourceFailure(t *testing.T) {
	t.Parallel()

	server := newTestServer()
	logger, logBuf := captureLogger()

	err := Apply(t.Context(), server, []Source{{
		Name:    "broken",
		Type:    "http",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{},
	}, {
		Name:    "grammar",
		Type:    "english",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{},
	}}, tool.WithLogger(logger))

	require.NoErrorf(t, err, "Apply must not crash on a single per-source failure")

	listed := listServerTools(t, server)
	require.Lenf(t, listed, 1, "the surviving source's tool must be registered")

	assert.Equal(t, "validate_english", listed[0].Name)

	logs := logBuf.String()
	assert.Containsf(t, logs, `"name":"broken"`,
		"the failing source's name must be in the structured log output")
	assert.Containsf(t, logs, `"level":"ERROR"`,
		"the per-source failure must be logged at error level")
	assert.NotContainsf(t, logBuf.String(), `"name":"grammar"`,
		"the surviving source must not produce an error log entry")
}

// TestApply_AllSourcesFailReturnsError is the negative case for the
// new contract. When every source in the batch fails, no tool is
// registered and Apply returns a non-nil error wrapping every
// per-source message — the signal cmd/main.go uses to exit 1 instead
// of starting a server with zero tools.
func TestApply_AllSourcesFailReturnsError(t *testing.T) {
	t.Parallel()

	logger, logBuf := captureLogger()

	err := Apply(t.Context(), (*mcp.Server)(nil), []Source{{
		Name:    "broken1",
		Type:    "http",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{},
	}, {
		Name:    "broken2",
		Type:    "http",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{},
	}}, tool.WithLogger(logger))

	require.Error(t, err)
	assert.Contains(t, err.Error(), `"broken1"`)
	assert.Contains(t, err.Error(), `"broken2"`)

	logs := logBuf.String()
	assert.Contains(t, logs, `"name":"broken1"`)
	assert.Contains(t, logs, `"name":"broken2"`)
}

// TestApply_DuplicateToolNameErrors_LogsAndRegistersSurvivor pins the
// new tolerant behavior for the duplicate-name case (a config bug, not
// a Connect failure). The duplicate is still surfaced as a logged
// error, but the surviving source's tool is now registered — the
// server starts with the tools it can offer instead of refusing to
// start because of a name collision.
func TestApply_DuplicateToolNameErrors_LogsAndRegistersSurvivor(t *testing.T) {
	t.Parallel()

	server := newTestServer()
	logger, logBuf := captureLogger()

	err := Apply(t.Context(), server, []Source{{
		Name: "weather",
		Type: "http",
		Connect: map[string]any{
			"url":    "https://example.com/weather",
			"method": "GET",
		},
		Tools: ToolsConfig{},
	}, {
		Name: "users",
		Type: "http",
		Connect: map[string]any{
			"url":    "https://example.com/users",
			"method": "GET",
		},
		Tools: ToolsConfig{},
	}}, tool.WithLogger(logger))

	require.NoErrorf(t, err,
		"Apply must not crash on a duplicate-name error when at least one source survived")

	listed := listServerTools(t, server)
	require.Lenf(t, listed, 1,
		"the surviving source's tool must be registered alongside the duplicate error")
	assert.Equal(t, "http", listed[0].Name)

	logs := logBuf.String()
	assert.Containsf(t, logs, `duplicate tool name \"http\"`,
		"the duplicate-name message must appear in the log")
	assert.Containsf(t, logs, `"name":"users"`,
		"the losing source in the collision must be the one logged")
}

// TestApply_EmptySourcesIsNoOp confirms that calling Apply with no
// sources returns nil and does not panic. It is the degenerate case
// for the new []Source signature.
func TestApply_EmptySourcesIsNoOp(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	require.NoError(t, Apply(t.Context(), server, []Source(nil)))
	require.NoError(t, Apply(t.Context(), server, []Source{}))

	assert.Empty(t, listServerTools(t, server))
}

// --- Apply: tools.read_only ---

// TestApply_ReadOnlyTrueKeepsReadOnlyEnglishTool verifies the happy
// path: a source with Tools.ReadOnly = true keeps every tool whose
// Annotations.ReadOnlyHint is true. english exposes exactly one tool
// (validate_english) with ReadOnlyHint = true, so the server ends up
// with that single tool registered.
func TestApply_ReadOnlyTrueKeepsReadOnlyEnglishTool(t *testing.T) {
	t.Parallel()

	server := newTestServer()

	err := Apply(t.Context(), server, []Source{{
		Name:    "grammar",
		Type:    "english",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{ReadOnly: true},
	}})
	require.NoError(t, err)

	listed := listServerTools(t, server)
	require.Len(t, listed, 1)
	assert.Equal(t, "validate_english", listed[0].Name)

	require.NotNil(t, listed[0].Annotations)
	assert.Truef(t, listed[0].Annotations.ReadOnlyHint,
		"english tool must advertise ReadOnlyHint=true")
}

// TestApply_ReadOnlyFilterEmitsWarningLogWhenEmpty verifies the
// warning-log path: when a source sets Tools.ReadOnly and the
// per-type Connect returns no read-only tools, Apply still
// succeeds but emits a WarnContext entry that names the source and
// records read_only: true. We drive this with an http source
// configured with a mutating method (POST) — http.Connect returns
// a single tool with ReadOnlyHint = false, so the read-only
// filter drops it and the warning fires.
func TestApply_ReadOnlyFilterEmitsWarningLogWhenEmpty(t *testing.T) {
	t.Parallel()

	server := newTestServer()
	logger, logBuf := captureLogger()

	err := Apply(t.Context(), server, []Source{{
		Name: "mutator",
		Type: "http",
		Connect: map[string]any{
			"url":    "https://example.com/mutate",
			"method": "POST",
		},
		Tools: ToolsConfig{ReadOnly: true},
	}}, tool.WithLogger(logger))

	require.NoError(t, err)
	assert.Emptyf(t, listServerTools(t, server),
		"no read-only tools survive, so the server must register zero tools")

	logs := logBuf.String()
	assert.Containsf(t, logs, `"name":"mutator"`,
		"the warning must identify the source by name")
	assert.Containsf(t, logs, `"level":"WARN"`,
		"the empty-read-only-filter outcome is a warning, not an error")
	assert.Containsf(t, logs, `"read_only":true`,
		"the warning must record the read_only flag value")
	assert.Containsf(t, logs, "zero read-only tools",
		"the warning message must state that zero read-only tools survived")
}

// --- Apply: unknown type ---

func TestApply_UnknownType(t *testing.T) {
	t.Parallel()

	err := Apply(t.Context(), (*mcp.Server)(nil), []Source{{
		Name:    "mystery",
		Type:    "totally-bogus",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{},
	}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `"mystery"`)
	assert.Contains(t, err.Error(), `"totally-bogus"`)
	assert.Contains(t, err.Error(), "unknown type")
}

func TestApply_UnknownType_EmptyName(t *testing.T) {
	t.Parallel()

	// A source with no Name should still produce a useful error — the
	// %q formatter renders an empty string as "" rather than panicking.
	err := Apply(t.Context(), (*mcp.Server)(nil), []Source{{
		Name:    "",
		Type:    "totally-bogus",
		Connect: map[string]any(nil),
		Tools:   ToolsConfig{},
	}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `""`)
	assert.Contains(t, err.Error(), `"totally-bogus"`)
}

// --- listServerTools (shared by the Apply tests above) ---

// listServerTools spins up an in-memory MCP transport pair, connects the
// server on one end and a throwaway client on the other, and returns the
// tools the client sees. It is the read-back mechanism every Apply test
// uses to assert on the server's registered tool set.
func listServerTools(t *testing.T, server *mcp.Server) []*mcp.Tool {
	t.Helper()

	ctx := t.Context()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, (*mcp.ServerSessionOptions)(nil))
	require.NoError(t, err)

	client := mcp.NewClient(
		&mcp.Implementation{Name: "test-client", Version: "0.0.0"},
		(*mcp.ClientOptions)(nil),
	)
	clientSession, err := client.Connect(ctx, clientTransport, (*mcp.ClientSessionOptions)(nil))
	require.NoError(t, err)

	t.Cleanup(func() { //nolint:errcheck // cleanup best-effort; test already passed
		_ = clientSession.Close()
		_ = serverSession.Close() //nolint:errcheck // see above
	})

	result, err := clientSession.ListTools(ctx, (*mcp.ListToolsParams)(nil))
	require.NoError(t, err)

	return result.Tools
}

// --- captureLogger (shared by the Apply tests that assert on logs) ---

// captureLogger returns a *slog.Logger that writes JSON records to the
// returned buffer, plus a pointer to that buffer. Tests pass the logger
// to Apply via tool.WithLogger and then assert on buf.String() — the
// JSON form keeps the structured fields (name, type, error) intact so
// assertions can target a single attribute without regex-parsing the
// human-readable text form.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer

	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	return slog.New(handler), &buf
}
