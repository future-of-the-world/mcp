// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package source

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- LoadSources ---

func TestLoadSources_YAML(t *testing.T) {
	t.Parallel()

	data := readTestdata(t, "source_basic.yaml")

	sources, err := LoadSources(data)
	require.NoError(t, err)
	require.Len(t, sources, 1)

	assertSourceBasic(t, sources[0])
}

func TestLoadSources_JSON(t *testing.T) {
	t.Parallel()

	data := readTestdata(t, "source_basic.json")

	sources, err := LoadSources(data)
	require.NoError(t, err)
	require.Len(t, sources, 1)

	assertSourceBasic(t, sources[0])
}

func TestLoadSources_EmptyDocument(t *testing.T) {
	t.Parallel()

	t.Run("yaml", func(t *testing.T) {
		t.Parallel()

		sources, err := LoadSources([]byte(""))
		require.NoError(t, err)
		assert.Empty(t, sources)
	})

	t.Run("json", func(t *testing.T) {
		t.Parallel()

		sources, err := LoadSources([]byte("{}"))
		require.NoError(t, err)
		assert.Empty(t, sources)
	})
}

func TestLoadSources_MalformedYAML(t *testing.T) {
	t.Parallel()

	// Tab indentation is illegal in YAML, so this should fail to decode.
	_, err := LoadSources([]byte("sources:\n  basic:\n\tinvalid: yes"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode sources yaml")
}

func TestLoadSources_MalformedJSON(t *testing.T) {
	t.Parallel()

	_, err := LoadSources([]byte("{not json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode sources json")
}

func TestLoadSources_EnableOnlyYAML(t *testing.T) {
	t.Parallel()

	data := readTestdata(t, "source_tools_enable_only.yaml")

	sources, err := LoadSources(data)
	require.NoError(t, err)
	require.Len(t, sources, 1)

	assert.Equal(t, "enable_only_basic", sources[0].Name)
	assert.Equal(t, "postgres", sources[0].Type)
	assert.Equal(t, "pg", sources[0].Tools.Prefix)
	assert.Empty(t, sources[0].Tools.Remove)
	assert.Equal(t, []string{"^pg_list_"}, sources[0].Tools.EnableOnly)
}

func TestLoadSources_EnableOnlyJSON(t *testing.T) {
	t.Parallel()

	data := readTestdata(t, "source_tools_enable_only.json")

	sources, err := LoadSources(data)
	require.NoError(t, err)
	require.Len(t, sources, 1)

	assert.Equal(t, "enable_only_basic", sources[0].Name)
	assert.Equal(t, "postgres", sources[0].Type)
	assert.Equal(t, "pg", sources[0].Tools.Prefix)
	assert.Empty(t, sources[0].Tools.Remove)
	assert.Equal(t, []string{"^pg_list_"}, sources[0].Tools.EnableOnly)
}

func TestLoadSources_RemoveAndEnableOnlyTogetherIsRejected(t *testing.T) {
	t.Parallel()

	t.Run("yaml", func(t *testing.T) {
		t.Parallel()

		data := []byte(`sources:
  conflicting:
    type: postgres
    tools:
      remove:
        - "^pg_"
      enable_only:
        - "^pg_list_"
`)

		_, err := LoadSources(data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting")
		assert.Contains(t, err.Error(), "remove")
		assert.Contains(t, err.Error(), "enable_only")
	})

	t.Run("json", func(t *testing.T) {
		t.Parallel()

		data := []byte(`{
  "sources": {
    "conflicting": {
      "type": "postgres",
      "tools": {
        "remove": ["^pg_"],
        "enable_only": ["^pg_list_"]
      }
    }
  }
}`)

		_, err := LoadSources(data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting")
		assert.Contains(t, err.Error(), "remove")
		assert.Contains(t, err.Error(), "enable_only")
	})
}

func TestLoadSources_OnlyRemoveIsAccepted(t *testing.T) {
	t.Parallel()

	// Regression: the existing fixture sets Remove only and must
	// continue to load cleanly after the EnableOnly mutex check lands.
	data := readTestdata(t, "source_basic.yaml")

	sources, err := LoadSources(data)
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, []string{"^get_"}, sources[0].Tools.Remove)
	assert.Empty(t, sources[0].Tools.EnableOnly)
}

// --- helpers ---

// readTestdata returns the contents of the named testdata file. The
// path is relative to the package directory, where the `go test`
// command runs.
func readTestdata(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoErrorf(t, err, "read testdata %s", name)

	return data
}

// assertSourceBasic checks that src matches the fixture used by both
// the YAML and JSON testdata files.
//
//nolint:gocritic // src is a 128-byte fixture; helper runs once per test so copy cost is irrelevant
func assertSourceBasic(t *testing.T, src Source) {
	t.Helper()

	assert.Equal(t, "basic", src.Name)
	assert.Equal(t, "postgres", src.Type)

	require.NotNilf(t, src.Connect, "connect should be a map, not nil")
	assert.Equal(t, "postgres://localhost:5432/test", src.Connect["datasource"])
	assert.Equal(t, "30s", src.Connect["timeout"])

	assert.Equal(t, "pg", src.Tools.Prefix)
	assert.Equal(t, []string{"^get_"}, src.Tools.Remove)
	assert.True(t, src.Tools.ReadOnly)
}
