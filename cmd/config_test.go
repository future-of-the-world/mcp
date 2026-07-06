// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/stretchr/testify/require"

	_ "embed"

	"go.amidman.dev/mcp/source"
)

var (
	//go:embed testdata/server_full.yaml
	serverFullYAML []byte

	//go:embed testdata/server_full.json
	serverFullJSON []byte

	//go:embed testdata/server_partial.yaml
	serverPartialYAML []byte

	//go:embed testdata/server_defaults.yaml
	serverDefaultsYAML []byte

	//go:embed testdata/sources_with_prefix.yaml
	sourcesWithPrefixYAML []byte

	//go:embed testdata/sources_with_remove.yaml
	sourcesWithRemoveYAML []byte

	//go:embed testdata/sources_with_temporal.yaml
	sourcesWithTemporalYAML []byte

	//go:embed testdata/sources_with_temporal.json
	sourcesWithTemporalJSON []byte

	//go:embed testdata/sources_full.yaml
	sourcesFullYAML []byte
)

func Test_ConfigFullYAML(t *testing.T) {
	t.Parallel()

	config := NewConfig()

	err := yaml.Unmarshal(serverFullYAML, config)
	require.NoError(t, err)

	// Verify identity fields
	require.Equalf(t, "test-server", config.Name, "name mismatch")
	require.Equalf(t, "Test MCP Server", config.Title, "title mismatch")
	require.Equalf(t, "2.0.0", config.Version, "version mismatch")

	// Verify the single http source is loaded with the map key as Name.
	require.Len(t, config.Sources, 1)
	require.Equalf(t, "weather", config.Sources[0].Name, "source name mismatch")
	require.Equalf(t, "http", config.Sources[0].Type, "source type mismatch")
}

func Test_ConfigFullJSON(t *testing.T) {
	t.Parallel()

	config := NewConfig()

	err := json.Unmarshal(serverFullJSON, config)
	require.NoError(t, err)

	// Verify identity fields
	require.Equalf(t, "test-server", config.Name, "name mismatch")
	require.Equalf(t, "Test MCP Server", config.Title, "title mismatch")
	require.Equalf(t, "2.0.0", config.Version, "version mismatch")

	// Verify the single http source is loaded with the map key as Name.
	require.Len(t, config.Sources, 1)
	require.Equalf(t, "weather", config.Sources[0].Name, "source name mismatch")
	require.Equalf(t, "http", config.Sources[0].Type, "source type mismatch")
}

func Test_ConfigPartial(t *testing.T) {
	t.Parallel()

	config := NewConfig()

	err := yaml.Unmarshal(serverPartialYAML, config)
	require.NoError(t, err)

	// Verify identity fields - only name and version provided
	require.Equalf(t, "my-server", config.Name, "name mismatch")
	require.Emptyf(t, config.Title, "title should be empty")
	require.Equalf(t, "2.0.0", config.Version, "version mismatch")

	// Verify the single http source is loaded.
	require.Len(t, config.Sources, 1)
	require.Equalf(t, "weather", config.Sources[0].Name, "source name mismatch")
	require.Equalf(t, "http", config.Sources[0].Type, "source type mismatch")
}

func Test_ConfigDefaults(t *testing.T) {
	t.Parallel()

	config := NewConfig()

	err := yaml.Unmarshal(serverDefaultsYAML, config)
	require.NoError(t, err)

	// Verify default values are applied
	config.setDefaults()

	require.Equalf(t, "mcp", config.Name, "default name mismatch")
	require.Emptyf(t, config.Title, "title should be empty")
	require.Equalf(t, "1.0.0", config.Version, "default version mismatch")

	// Verify the single http source is loaded.
	require.Len(t, config.Sources, 1)
	require.Equalf(t, "test", config.Sources[0].Name, "source name mismatch")
	require.Equalf(t, "http", config.Sources[0].Type, "source type mismatch")
}

// Test_ConfigSourcesWithPrefix exercises a config that overrides the
// tool-name prefix via tools.prefix on one source and omits it on the
// other. It verifies the prefix field decodes and that omitting it
// leaves Prefix empty (no source-name fallback is applied at Apply
// time — the tool keeps its base name).
func Test_ConfigSourcesWithPrefix(t *testing.T) {
	t.Parallel()

	config := NewConfig()

	err := yaml.Unmarshal(sourcesWithPrefixYAML, config)
	require.NoError(t, err)

	require.Equalf(t, "prefix-example", config.Name, "name mismatch")
	require.Equalf(t, "1.0.0", config.Version, "version mismatch")
	require.Len(t, config.Sources, 2)

	byName := make(map[string]source.Source, len(config.Sources))
	for _, src := range config.Sources {
		byName[src.Name] = src
	}

	githubSrc, ok := byName["github"]
	require.Truef(t, ok, "expected a github source")
	require.Equalf(t, "proxy", githubSrc.Type, "github type mismatch")
	require.Equalf(t, "gh", githubSrc.Tools.Prefix, "github prefix override mismatch")

	gitlabSrc, ok := byName["gitlab"]
	require.Truef(t, ok, "expected a gitlab source")
	require.Equalf(t, "proxy", gitlabSrc.Type, "gitlab type mismatch")
	require.Emptyf(t, gitlabSrc.Tools.Prefix, "gitlab prefix should be empty (no default)")
}

// Test_ConfigSourcesWithRemove exercises a config that drops tools via
// tools.remove. It verifies the remove pattern list decodes correctly.
func Test_ConfigSourcesWithRemove(t *testing.T) {
	t.Parallel()

	config := NewConfig()

	err := yaml.Unmarshal(sourcesWithRemoveYAML, config)
	require.NoError(t, err)

	require.Equalf(t, "remove-example", config.Name, "name mismatch")
	require.Len(t, config.Sources, 1)

	src := config.Sources[0]
	require.Equalf(t, "forgejo", src.Name, "source name mismatch")
	require.Equalf(t, "proxy", src.Type, "source type mismatch")
	require.Equal(t, []string{"branch_protection"}, src.Tools.Remove)
}

// Test_ConfigSourcesWithTemporalYAML exercises a config that declares a
// single temporal source with host + namespace + tools.prefix. It pins
// the YAML decode pipeline for the temporal source type — the `host`
// field defaults to "localhost:7233" and `namespace` to "default" when
// omitted, so this test covers both the explicit-values and the
// default-applied paths in one shape.
func Test_ConfigSourcesWithTemporalYAML(t *testing.T) {
	t.Parallel()

	config := NewConfig()

	err := yaml.Unmarshal(sourcesWithTemporalYAML, config)
	require.NoError(t, err)

	require.Equalf(t, "temporal-example", config.Name, "name mismatch")
	require.Equalf(t, "1.0.0", config.Version, "version mismatch")
	require.Len(t, config.Sources, 1)

	byName := make(map[string]source.Source, len(config.Sources))
	for _, src := range config.Sources {
		byName[src.Name] = src
	}

	temporalSrc, ok := byName["mytemporal"]
	require.Truef(t, ok, "expected a mytemporal source")
	require.Equalf(t, "temporal", temporalSrc.Type, "temporal type mismatch")
	require.Equalf(t, "t1_", temporalSrc.Tools.Prefix, "temporal prefix mismatch")
	require.Equalf(
		t,
		"localhost:7233",
		temporalSrc.Connect["host"],
		"temporal host mismatch",
	)
	require.Equalf(
		t,
		"default",
		temporalSrc.Connect["namespace"],
		"temporal namespace mismatch",
	)
}

// Test_ConfigSourcesWithTemporalJSON is the JSON twin of
// Test_ConfigSourcesWithTemporalYAML — the cmd config loader accepts
// both formats and the testdata set exercises both paths so the decode
// stays symmetric.
func Test_ConfigSourcesWithTemporalJSON(t *testing.T) {
	t.Parallel()

	config := NewConfig()

	err := json.Unmarshal(sourcesWithTemporalJSON, config)
	require.NoError(t, err)

	require.Equalf(t, "temporal-example", config.Name, "name mismatch")
	require.Len(t, config.Sources, 1)

	temporalSrc := config.Sources[0]
	require.Equalf(t, "mytemporal", temporalSrc.Name, "source name mismatch")
	require.Equalf(t, "temporal", temporalSrc.Type, "source type mismatch")
	require.Equalf(t, "t1_", temporalSrc.Tools.Prefix, "temporal prefix mismatch")
	require.Equalf(
		t,
		"localhost:7233",
		temporalSrc.Connect["host"],
		"temporal host mismatch",
	)
	require.Equalf(
		t,
		"default",
		temporalSrc.Connect["namespace"],
		"temporal namespace mismatch",
	)
}

// Test_ConfigSourcesFull exercises a config that combines multiple source
// types, a tools.prefix override, a tools.remove filter, and an identity title.
// It verifies every source decodes with the expected name, type, and tool
// configuration. Sources are NOT applied (proxy/postgres would dial out);
// this test pins the parsing pipeline only.
func Test_ConfigSourcesFull(t *testing.T) {
	t.Parallel()

	config := NewConfig()

	err := yaml.Unmarshal(sourcesFullYAML, config)
	require.NoError(t, err)

	require.Equalf(t, "full-example", config.Name, "name mismatch")
	require.Equalf(t, "Full Example", config.Title, "title mismatch")
	require.Equalf(t, "1.0.0", config.Version, "version mismatch")
	require.Lenf(t, config.Sources, 5, "expected 5 sources")

	byName := make(map[string]source.Source, len(config.Sources))
	for _, src := range config.Sources {
		byName[src.Name] = src
	}

	maindb, ok := byName["maindb"]
	require.Truef(t, ok, "expected a maindb source")
	require.Equalf(t, "postgres", maindb.Type, "maindb type mismatch")
	require.Equalf(t, "main", maindb.Tools.Prefix, "maindb prefix mismatch")
	require.Containsf(t, maindb.Connect, "datasource", "maindb connect should carry datasource")

	forgejo, ok := byName["forgejo"]
	require.Truef(t, ok, "expected a forgejo source")
	require.Equalf(t, "proxy", forgejo.Type, "forgejo type mismatch")
	require.Equalf(t, "forgejo", forgejo.Tools.Prefix, "forgejo prefix mismatch")
	require.Equal(t, []string{"branch_protection"}, forgejo.Tools.Remove)

	weather, ok := byName["weather"]
	require.Truef(t, ok, "expected a weather source")
	require.Equalf(t, "http", weather.Type, "weather type mismatch")
	require.Equalf(t, "GET", weather.Connect["method"], "weather method mismatch")

	checker, ok := byName["checker"]
	require.Truef(t, ok, "expected a checker source")
	require.Equalf(t, "english", checker.Type, "checker type mismatch")

	search, ok := byName["search"]
	require.Truef(t, ok, "expected a search source")
	require.Equalf(t, "websearch", search.Type, "search type mismatch")
	require.Equalf(
		t,
		"BRAVE_API_KEY",
		search.Connect["brave_api_key_env"],
		"search api key env mismatch",
	)
}

func Test_ConfigSetDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         Config
		expectedName  string
		expectedTitle string
		expectedVer   string
	}{
		{
			name: "all fields set",
			input: Config{
				Name:    "custom-name",
				Title:   "Custom Title",
				Version: "3.0.0",
			},
			expectedName:  "custom-name",
			expectedTitle: "Custom Title",
			expectedVer:   "3.0.0",
		},
		{
			name: "empty name and version",
			input: Config{
				Name:    "",
				Title:   "Custom Title",
				Version: "",
			},
			expectedName:  "mcp",
			expectedTitle: "Custom Title",
			expectedVer:   "1.0.0",
		},
		{
			name: "only name set",
			input: Config{
				Name:    "custom-name",
				Title:   "Custom Title",
				Version: "",
			},
			expectedName:  "custom-name",
			expectedTitle: "Custom Title",
			expectedVer:   "1.0.0",
		},
		{
			name: "only version set",
			input: Config{
				Name:    "",
				Title:   "Custom Title",
				Version: "3.0.0",
			},
			expectedName:  "mcp",
			expectedTitle: "Custom Title",
			expectedVer:   "3.0.0",
		},
		{
			name: "all empty",
			input: Config{
				Name:    "",
				Title:   "",
				Version: "",
			},
			expectedName:  "mcp",
			expectedTitle: "",
			expectedVer:   "1.0.0",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			testCase.input.setDefaults()

			require.Equal(t, testCase.expectedName, testCase.input.Name)
			require.Equal(t, testCase.expectedTitle, testCase.input.Title)
			require.Equal(t, testCase.expectedVer, testCase.input.Version)
		})
	}
}
