// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package shell

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDuration_UnmarshalYAML exercises the YAML decoder directly. The
// Decode path is normally reached via the Source struct, but the unit
// test pins the contract for direct callers.
func TestDuration_UnmarshalYAML(t *testing.T) {
	t.Parallel()

	t.Run("valid", func(t *testing.T) {
		t.Parallel()

		var dur Duration

		node := yamlScalar(t, "5m30s")
		require.NoError(t, dur.UnmarshalYAML(node))
		assert.Equal(t, 5*60+30, int(dur.Duration().Seconds()))
	})

	t.Run("invalid", func(t *testing.T) {
		t.Parallel()

		var dur Duration

		node := yamlScalar(t, "not-a-duration")
		err := dur.UnmarshalYAML(node)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid duration")
	})
}

// TestDuration_UnmarshalJSON exercises the JSON decoder directly.
func TestDuration_UnmarshalJSON(t *testing.T) {
	t.Parallel()

	t.Run("valid", func(t *testing.T) {
		t.Parallel()

		var dur Duration

		require.NoError(t, dur.UnmarshalJSON([]byte(`"2s"`)))
		assert.Equal(t, 2, int(dur.Duration().Seconds()))
	})

	t.Run("invalid", func(t *testing.T) {
		t.Parallel()

		var dur Duration

		err := dur.UnmarshalJSON([]byte(`"bogus"`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid duration")
	})
}

// TestTextResult_ObjectSetsStructuredContent pins the spec-required
// dual-write: a JSON object value produces both Content (TextContent)
// and StructuredContent. This is what downstream MCP clients consume.
func TestTextResult_ObjectSetsStructuredContent(t *testing.T) {
	t.Parallel()

	value := map[string]any{"foo": "bar", "n": float64(42)}

	res, err := textResult(value)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Len(t, res.Content, 1)

	text, ok := res.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "Content[0] must be *mcp.TextContent")
	assert.Contains(t, text.Text, `"foo":"bar"`)
	assert.Contains(t, text.Text, `"n":42`)

	require.NotNilf(t, res.StructuredContent,
		"a JSON object value must populate StructuredContent")
}

// TestCapWriter_AlreadyOverCap exercises the `pre >= limit` branch of
// capWriter.Write. The writer silently drops bytes (returning success)
// once the cap has already been exceeded on a previous write.
func TestCapWriter_AlreadyOverCap(t *testing.T) {
	t.Parallel()

	var dest bytes.Buffer

	var used atomic.Int64

	var truncated atomic.Bool

	writer := &capWriter{
		used:      &used,
		truncated: &truncated,
		limit:     10,
		dest:      &dest,
	}

	// First write fills the cap exactly.
	count, err := writer.Write([]byte("0123456789"))
	require.NoError(t, err)
	assert.Equal(t, 10, count)

	// Second write happens after the cap is full. The Write must succeed
	// (so io.Copy / cmd.Stdout never block) but the bytes must be dropped.
	count, err = writer.Write([]byte("ABCDE"))
	require.NoError(t, err)
	assert.Equalf(t, 5, count, "Write must report all 5 bytes consumed")
	assert.Equalf(t, "0123456789", dest.String(),
		"already-over-cap writes must not extend the underlying buffer")
	assert.Truef(t, truncated.Load(), "truncated must be set")
}

// TestCapWriter_ExactBoundaryFill exercises the partial-write branch:
// the cap is partially full when a Write arrives, only the bytes that
// fit are forwarded to the underlying writer.
func TestCapWriter_ExactBoundaryFill(t *testing.T) {
	t.Parallel()

	var dest bytes.Buffer

	var used atomic.Int64

	var truncated atomic.Bool

	writer := &capWriter{
		used:      &used,
		truncated: &truncated,
		limit:     10,
		dest:      &dest,
	}

	// Fill 7 bytes first.
	_, err := writer.Write([]byte("0123456"))
	require.NoError(t, err)

	// Now write 10 more bytes; only 3 should be kept, 7 dropped.
	count, err := writer.Write([]byte("ABCDEFGHIJ"))
	require.NoError(t, err)
	assert.Equalf(t, 10, count, "Write must report all 10 bytes consumed")
	assert.Equalf(t, "0123456ABC", dest.String(),
		"first 7 + first 3 of next batch must reach the buffer")
	assert.Truef(t, truncated.Load(), "truncation must be set")
}

// TestCapWriter_FullWriteKeepsAllBytes verifies the happy path: a
// Write whose length fits inside the remaining cap is forwarded
// verbatim.
func TestCapWriter_FullWriteKeepsAllBytes(t *testing.T) {
	t.Parallel()

	var dest bytes.Buffer

	var used atomic.Int64

	var truncated atomic.Bool

	writer := &capWriter{
		used:      &used,
		truncated: &truncated,
		limit:     100,
		dest:      &dest,
	}

	count, err := writer.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, 5, count)
	assert.Equal(t, "hello", dest.String())
	assert.Falsef(t, truncated.Load(), "under-cap writes must not flag truncation")
}

// TestHandleRunCommand_EndToEnd constructs an *mcp.CallToolRequest
// directly and exercises the full handler path: argument decode,
// buildRunOpts merging, runShell invocation, and textResult marshaling.
// This is the one place we test the handler outside an in-memory MCP
// transport.
func TestHandleRunCommand_EndToEnd(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // Timeout, MaxOutputBytes, Shell use defaults
		WorkingDir: "/tmp",
		Env: map[string]string{
			"LANG": "C.UTF-8",
		},
	}

	root, canon := openTestRoot(t, cfg.WorkingDir)

	handler := handleRunCommand(cfg, root, canon)

	req := &mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
		Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
			Name:      "run_command",
			Arguments: json.RawMessage(`{"command":"echo hello", "directory":"/tmp"}`),
		},
	}

	res, err := handler(t.Context(), req)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Len(t, res.Content, 1)

	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)

	assert.Contains(t, text.Text, `"exit_code":0`)
	assert.Contains(t, text.Text, "hello")
}

// TestHandleRunCommand_EmptyCommand verifies that the handler surfaces
// errEmptyCommand when the JSON args contain an empty command string.
func TestHandleRunCommand_EmptyCommand(t *testing.T) {
	t.Parallel()

	cfg := &config{WorkingDir: "/tmp"} //nolint:exhaustruct // only WorkingDir matters here
	root, canon := openTestRoot(t, cfg.WorkingDir)
	handler := handleRunCommand(cfg, root, canon)

	req := &mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
		Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
			Name:      "run_command",
			Arguments: json.RawMessage(`{"command":""}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, errEmptyCommand)
}

// TestHandleRunCommand_InvalidJSONArgs verifies the handler returns a
// wrapped JSON-decode error when the request arguments are malformed.
func TestHandleRunCommand_InvalidJSONArgs(t *testing.T) {
	t.Parallel()

	cfg := &config{WorkingDir: "/tmp"} //nolint:exhaustruct // only WorkingDir matters here
	root, canon := openTestRoot(t, cfg.WorkingDir)
	handler := handleRunCommand(cfg, root, canon)

	req := &mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
		Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
			Name:      "run_command",
			Arguments: json.RawMessage(`{not valid json`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse run_command args")
}

// TestHandleRunCommand_MissingDirectory verifies the contract that the
// per-call `directory` is mandatory: a JSON request that omits it
// surfaces errDirectoryRequired without spawning the child. The
// matching schema-level enforcement lives in
// mcp/shell/schemas/shell_run_input.json (the `required` array); this
// test exercises the Go-side guard so direct callers stay honest
// even when the schema is bypassed.
func TestHandleRunCommand_MissingDirectory(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // Timeouts and other fields use defaults
		WorkingDir:     "/tmp",
		Timeout:        Duration(5 * time.Second),
		MaxOutputBytes: 1 << 20,
	}
	root, canon := openTestRoot(t, cfg.WorkingDir)
	handler := handleRunCommand(cfg, root, canon)

	req := &mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
		Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
			Name:      "run_command",
			Arguments: json.RawMessage(`{"command":"pwd"}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, errDirectoryRequired)
}

// TestHandleRunCommand_EmptyDirectoryString is the empty-string arm
// of the missing-directory contract: the schema only enforces
// presence, not non-empty, so a request like `{"directory":""}`
// passes the schema but must still be rejected by the Go-side guard.
func TestHandleRunCommand_EmptyDirectoryString(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // Timeouts and other fields use defaults
		WorkingDir:     "/tmp",
		Timeout:        Duration(5 * time.Second),
		MaxOutputBytes: 1 << 20,
	}
	root, canon := openTestRoot(t, cfg.WorkingDir)
	handler := handleRunCommand(cfg, root, canon)

	req := &mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
		Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
			Name:      "run_command",
			Arguments: json.RawMessage(`{"command":"pwd", "directory":""}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, errDirectoryRequired)
}

// TestHandleRunCommand_NonZeroExitIsNotError verifies the contract:
// non-zero exit codes are returned in exit_code, not as a Go error.
// The LLM should see the exit code as data and react accordingly.
func TestHandleRunCommand_NonZeroExitIsNotError(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // Env and Shell use defaults
		WorkingDir:     "/tmp",
		Timeout:        Duration(5 * time.Second),
		MaxOutputBytes: 1 << 20,
	}
	root, canon := openTestRoot(t, cfg.WorkingDir)
	handler := handleRunCommand(cfg, root, canon)

	req := &mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
		Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
			Name:      "run_command",
			Arguments: json.RawMessage(`{"command":"exit 7", "directory":"/tmp"}`),
		},
	}

	res, err := handler(t.Context(), req)
	require.NoError(t, err)
	require.NotNil(t, res)

	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, `"exit_code":7`)
}

// TestHandleRunCommand_ContextDeadlineErrors verifies that handler
// errors from runShell are wrapped with the shell: run: prefix.
func TestHandleRunCommand_ContextDeadlineErrors(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // Shell uses default /bin/sh
		WorkingDir:     "/tmp",
		Timeout:        Duration(50 * time.Millisecond),
		MaxOutputBytes: 1 << 20,
		Env: map[string]string{
			"PATH": "/usr/bin:/bin",
		},
	}
	root, canon := openTestRoot(t, cfg.WorkingDir)
	handler := handleRunCommand(cfg, root, canon)

	req := &mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
		Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
			Name:      "run_command",
			Arguments: json.RawMessage(`{"command":"sleep 30", "directory":"/tmp"}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shell: run:")
}

// TestConnect_DecodeErrorIsWrapped pins the wrapping convention:
// decode errors carry the "shell: decode:" prefix so the dispatcher
// can chain "source %q:" on top without doubling "shell: shell:".
func TestConnect_DecodeErrorIsWrapped(t *testing.T) {
	t.Parallel()

	// Missing working_dir is a validate-time error, so feed the
	// decoder something it cannot handle to trigger decodeConnect's
	// own error path.
	_, err := Connect(t.Context(), map[string]any{
		"working_dir":      "/tmp",
		"max_output_bytes": "not-a-number",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shell: decode:")
}

// TestConnect_ValidateErrorIsWrapped verifies the validate-time error
// wrapping.
func TestConnect_ValidateErrorIsWrapped(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"timeout": "-1s",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shell: validate:")
}

// TestResolveDirectory pins the contract for the per-call directory
// resolver. The happy path returns the absolute path unchanged; escape
// attempts, missing paths, and the required-field guard all return the
// right sentinel so the handler can surface a useful tool error to
// the LLM.
func TestResolveDirectory(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()

	root, canon := openTestRoot(t, workDir)

	require.NoError(
		t,
		os.Mkdir(filepath.Join(canon, "sub"), 0o750),
	)

	t.Run("empty is errDirectoryRequired", func(t *testing.T) {
		t.Parallel()

		_, err := resolveDirectory(root, canon, "")
		require.Error(t, err)
		require.ErrorIs(t, err, errDirectoryRequired)
	})

	t.Run("absolute subdir inside root", func(t *testing.T) {
		t.Parallel()

		target := filepath.Join(canon, "sub")

		got, err := resolveDirectory(root, canon, target)
		require.NoError(t, err)
		assert.Equal(t, target, got)
	})

	t.Run("non-absolute path is escape", func(t *testing.T) {
		t.Parallel()

		_, err := resolveDirectory(root, canon, "sub")
		require.Error(t, err)
		require.ErrorIs(t, err, errDirectoryEscapes)
		assert.Contains(t, err.Error(), "must be an absolute path")
	})

	t.Run("absolute path outside root is escape", func(t *testing.T) {
		t.Parallel()

		_, err := resolveDirectory(root, canon, "/etc")
		require.Error(t, err)
		require.ErrorIs(t, err, errDirectoryEscapes)
	})

	t.Run("parent directory absolute is escape", func(t *testing.T) {
		t.Parallel()

		parent := filepath.Dir(canon)

		_, err := resolveDirectory(root, canon, parent)
		require.Error(t, err)
		require.ErrorIs(t, err, errDirectoryEscapes)
	})

	t.Run("nonexistent absolute path is not-exist", func(t *testing.T) {
		t.Parallel()

		missing := filepath.Join(canon, "no_such_dir")

		_, err := resolveDirectory(root, canon, missing)
		require.Error(t, err)
		require.ErrorIs(t, err, errDirectoryNotExist)
	})

	t.Run("regular file is not-exist", func(t *testing.T) {
		t.Parallel()

		filePath := filepath.Join(canon, "afile.txt")
		require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o600))

		_, err := resolveDirectory(root, canon, filePath)
		require.Error(t, err)
		assert.ErrorIs(t, err, errDirectoryNotExist)
	})

	t.Run("symlink under root pointing outside is escape", func(t *testing.T) {
		t.Parallel()

		// /etc exists on every supported host and is outside t.TempDir.
		// Linking to /tmp would also work; /etc is the canonical
		// "do not let me cd here" target.
		require.NoError(t, os.Symlink("/etc", filepath.Join(canon, "link_out")))

		_, err := resolveDirectory(root, canon, filepath.Join(canon, "link_out"))
		require.Error(t, err)
		assert.ErrorIs(t, err, errDirectoryEscapes)
	})
}

// TestHandleRunCommand_DirectoryOverride is the end-to-end test for the
// per-call directory field: the child process sees the requested
// directory as its cwd. The requested directory is passed as an
// absolute path so the schema contract is exercised.
func TestHandleRunCommand_DirectoryOverride(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()

	root, canon := openTestRoot(t, workDir)

	subDir := filepath.Join(canon, "sub")
	require.NoError(
		t,
		os.Mkdir(subDir, 0o750),
	)

	cfg := &config{ //nolint:exhaustruct // Timeouts and other fields use defaults
		WorkingDir:     workDir,
		Timeout:        Duration(5 * time.Second),
		MaxOutputBytes: 1 << 20,
		Env: map[string]string{
			"PATH": "/usr/bin:/bin",
		},
	}

	handler := handleRunCommand(cfg, root, canon)

	req := &mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
		Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
			Name:      "run_command",
			Arguments: json.RawMessage(`{"command":"pwd", "directory":"` + subDir + `"}`),
		},
	}

	res, err := handler(t.Context(), req)
	require.NoError(t, err)
	require.NotNil(t, res)

	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, subDir)
}

// TestHandleRunCommand_DirectoryEscapeRejected verifies that the
// handler surfaces a tool error (and does NOT spawn the child) when
// the per-call directory would escape working_dir.
func TestHandleRunCommand_DirectoryEscapeRejected(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()

	root, canon := openTestRoot(t, workDir)

	cfg := &config{ //nolint:exhaustruct // Timeouts and other fields use defaults
		WorkingDir:     workDir,
		Timeout:        Duration(5 * time.Second),
		MaxOutputBytes: 1 << 20,
	}

	handler := handleRunCommand(cfg, root, canon)

	req := &mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
		Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
			Name:      "run_command",
			Arguments: json.RawMessage(`{"command":"pwd", "directory":"/etc"}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	require.ErrorIs(t, err, errDirectoryEscapes)
}

// TestHandleRunCommand_DirectoryNotExistRejected verifies that the
// handler surfaces a not-exist tool error when the requested directory
// does not exist.
func TestHandleRunCommand_DirectoryNotExistRejected(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()

	root, canon := openTestRoot(t, workDir)

	missing := filepath.Join(canon, "no_such_dir")

	cfg := &config{ //nolint:exhaustruct // Timeouts and other fields use defaults
		WorkingDir:     workDir,
		Timeout:        Duration(5 * time.Second),
		MaxOutputBytes: 1 << 20,
	}

	handler := handleRunCommand(cfg, root, canon)

	req := &mcp.CallToolRequest{ //nolint:exhaustruct // partial literal is intentional
		Params: &mcp.CallToolParamsRaw{ //nolint:exhaustruct // partial literal is intentional
			Name:      "run_command",
			Arguments: json.RawMessage(`{"command":"pwd", "directory":"` + missing + `"}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	require.ErrorIs(t, err, errDirectoryNotExist)
}

// --- helpers ---

// yamlScalar constructs a *yaml.Node with kind StringScalar from a
// string value. Used to feed the Duration.UnmarshalYAML decoder
// without going through the full yaml.Unmarshal path.
func yamlScalar(t *testing.T, value string) *yaml.Node {
	t.Helper()

	return &yaml.Node{ //nolint:exhaustruct // partial literal is intentional
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: value,
	}
}

// openTestRoot canonicalizes workingDir and opens an os.Root at the
// canonical path, returning both for use in handler-level tests. The
// root is closed via t.Cleanup so callers don't need to manage its
// lifetime. Use t.TempDir() (or any directory guaranteed to exist and
// be writable) when a test wants full control over the contents.
func openTestRoot(t *testing.T, workingDir string) (*os.Root, string) {
	t.Helper()

	canon, err := filepath.EvalSymlinks(workingDir)
	require.NoErrorf(t, err, "EvalSymlinks(%q)", workingDir)

	root, err := os.OpenRoot(canon)
	require.NoErrorf(t, err, "OpenRoot(%q)", canon)

	t.Cleanup(func() {
		_ = root.Close() //nolint:errcheck // cleanup best-effort; test already passed
	})

	return root, canon
}
