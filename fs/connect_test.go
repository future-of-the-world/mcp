// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package fs

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConnect_RegistersAllTools verifies that Connect returns
// exactly the thirteen tools documented in the issue, in a stable
// order, each carrying the right description and annotations.
func TestConnect_RegistersAllTools(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"allowed_paths": []string{"/tmp"},
	})
	require.NoError(t, err)
	require.Len(t, resp.Tools, 13)

	want := []struct {
		name        string
		readOnly    bool
		destructive bool
	}{
		{"list_allowed_directories", true, false},
		{"read_file", true, false},
		{"write_file", false, true},
		{"edit_file", false, true},
		{"create_directory", false, false},
		{"list_directory", true, false},
		{"directory_tree", true, false},
		{"move_file", false, true},
		{"copy_file", false, false},
		{"delete_file", false, true},
		{"search_files", true, false},
		{"get_file_info", true, false},
		{"grep", true, false},
	}

	for index, expected := range want {
		entry := resp.Tools[index]

		assert.Equalf(t, expected.name, entry.Name,
			"tool at index %d must be %q", index, expected.name)

		require.NotNilf(t, entry.Annotations,
			"tool %q must carry annotations", expected.name)
		assert.Equalf(t, expected.readOnly, entry.Annotations.ReadOnlyHint,
			"tool %q ReadOnlyHint mismatch", expected.name)

		// DestructiveHint is *bool: nil means "not declared" and
		// is treated as false by hosts (per the MCP spec). Read-only
		// tools in this source use nil; mutating tools use &false
		// (safe) or &true (destructive).
		switch expected.destructive {
		case true:
			require.NotNilf(t, entry.Annotations.DestructiveHint,
				"tool %q must declare DestructiveHint=true", expected.name)
			assert.Truef(t, *entry.Annotations.DestructiveHint,
				"tool %q DestructiveHint must be true", expected.name)

		case false:
			if entry.Annotations.DestructiveHint != nil {
				assert.Falsef(t, *entry.Annotations.DestructiveHint,
					"tool %q DestructiveHint must be false or nil", expected.name)
			}
		}

		assert.NotEmptyf(t, entry.Description,
			"tool %q must carry a description", expected.name)
	}
}

// TestConnect_RequiresAllowedPaths pins the required-field contract.
func TestConnect_RequiresAllowedPaths(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), make(map[string]any))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowed_paths")
}

// TestConnect_DecodeErrorWrapped verifies that decode failures
// surface with a "fs: decode:" prefix so dispatcher logs are
// grep-friendly.
func TestConnect_DecodeErrorWrapped(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"allowed_paths": "not-a-list",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fs: decode:")
}

// TestConnect_HandlerIsInvocable is a structural test: every
// handler must be non-nil so the dispatcher can register it.
func TestConnect_HandlerIsInvocable(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{
		"allowed_paths": []string{"/tmp"},
	})
	require.NoError(t, err)

	for _, entry := range resp.Tools {
		assert.NotNilf(t, entry.Handler,
			"tool %q must carry a non-nil Handler", entry.Name)
	}
}

// TestConnect_DefaultFollowSymlinksIsFalse pins the safety default:
// the deny-symlinks-by-default policy must not be silently flipped.
func TestConnect_DefaultFollowSymlinksIsFalse(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"allowed_paths": []string{"/tmp"},
	})
	require.NoError(t, err)
	assert.False(t, cfg.FollowSymlinks)
}

// TestSchemas_AllParseAsJSONObjects verifies that every embedded
// schema parses cleanly. A regression here would cause silent
// runtime failures when the dispatcher tries to pass the schema
// to mcp.Server.AddTool.
func TestSchemas_AllParseAsJSONObjects(t *testing.T) {
	t.Parallel()

	schemas := []struct {
		name string
		data json.RawMessage
	}{
		{"listAllowedDirectoriesOutput", listAllowedDirectoriesOutput},
		{"readFileInput", readFileInput},
		{"readFileOutput", readFileOutput},
		{"writeFileInput", writeFileInput},
		{"writeFileOutput", writeFileOutput},
		{"editFileInput", editFileInput},
		{"editFileOutput", editFileOutput},
		{"createDirectoryInput", createDirectoryInput},
		{"createDirectoryOutput", createDirectoryOutput},
		{"listDirectoryInput", listDirectoryInput},
		{"listDirectoryOutput", listDirectoryOutput},
		{"directoryTreeInput", directoryTreeInput},
		{"directoryTreeOutput", directoryTreeOutput},
		{"moveFileInput", moveFileInput},
		{"moveFileOutput", moveFileOutput},
		{"copyFileInput", copyFileInput},
		{"copyFileOutput", copyFileOutput},
		{"deleteFileInput", deleteFileInput},
		{"deleteFileOutput", deleteFileOutput},
		{"searchFilesInput", searchFilesInput},
		{"searchFilesOutput", searchFilesOutput},
		{"getFileInfoInput", getFileInfoInput},
		{"getFileInfoOutput", getFileInfoOutput},
		{"grepInput", grepInput},
		{"grepOutput", grepOutput},
		{"emptyObjectSchema", emptyObjectSchema},
	}

	for _, schema := range schemas {
		t.Run(schema.name, func(t *testing.T) {
			t.Parallel()

			var probe map[string]any

			require.NoErrorf(t, json.Unmarshal(schema.data, &probe),
				"schema %s must be a valid JSON object: %s", schema.name, schema.data)
		})
	}
}
