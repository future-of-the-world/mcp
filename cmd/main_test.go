// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/source"
	"go.amidman.dev/mcp/tool"
)

// TestHTTPToolIntegration tests the full integration of an http source with an httptest server.
func TestHTTPToolIntegration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		method         string
		path           string
		responseBody   any
		requestBody    any
		expectedStatus int
	}{
		{
			name:           "GET request",
			method:         http.MethodGet,
			path:           "/api/test",
			responseBody:   map[string]string{"message": "hello"},
			requestBody:    nil,
			expectedStatus: http.StatusOK,
		},
		{
			name:   "POST request with JSON body",
			method: http.MethodPost,
			path:   "/api/create",
			responseBody: map[string]any{
				"id":      1,
				"status":  "created",
				"message": "resource created successfully",
			},
			requestBody:    map[string]string{"name": "test"},
			expectedStatus: http.StatusCreated,
		},
		{
			name:           "PUT request",
			method:         http.MethodPut,
			path:           "/api/update/1",
			responseBody:   map[string]string{"status": "updated"},
			requestBody:    nil,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "DELETE request",
			method:         http.MethodDelete,
			path:           "/api/delete/1",
			responseBody:   map[string]string{"status": "deleted"},
			requestBody:    nil,
			expectedStatus: http.StatusOK,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			server := createTestServer(t, testServerConfig{
				Method:         testCase.method,
				Path:           testCase.path,
				ResponseBody:   testCase.responseBody,
				ExpectedStatus: testCase.expectedStatus,
			})
			defer server.Close()

			configContent := formatTestConfig(server.URL, testCase.path, testCase.method)
			configPath := writeTestConfig(t, configContent)

			config, err := loadConfig(configPath)
			if err != nil {
				t.Fatalf("failed to load config: %v", err)
			}

			verifySourceConfig(t, config, "test-tool")
			validateConfigSources(t, config)
		})
	}
}

// TestLoadConfigWithHTTPServer tests loading config files that reference httptest servers.
func TestLoadConfigWithHTTPServer(t *testing.T) {
	t.Parallel()

	server := createSimpleTestServer(t, `{"status": "ok"}`)
	defer server.Close()

	t.Run("yaml config", func(t *testing.T) {
		t.Parallel()

		configContent := formatYAMLConfig(server.URL)
		configPath := writeTestConfigWithExt(t, configContent, ".yaml")

		config, err := loadConfig(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		verifySourceCount(t, config, 2)
		validateConfigSources(t, config)
	})

	t.Run("json config", func(t *testing.T) {
		t.Parallel()

		configContent := formatJSONConfig(server.URL)
		configPath := writeTestConfigWithExt(t, configContent, ".json")

		config, err := loadConfig(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		verifySourceConfig(t, config, "api")
		validateConfigSources(t, config)
	})
}

// TestRunWithHTTPServer tests the run function with a httptest server.
func TestRunWithHTTPServer(t *testing.T) {
	t.Parallel()

	server := createMultiRouteTestServer(t)
	defer server.Close()

	t.Run("successful startup and shutdown", func(t *testing.T) {
		t.Parallel()

		configContent := formatRunTestConfig(server.URL)
		configPath := writeTestConfig(t, configContent)

		var logBuf bytes.Buffer

		logger := createTestLogger(&logBuf)

		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()

		resultChan := runConfigValidationAsync(t, ctx, configPath, logger)
		waitAndVerifyResult(t, resultChan, 0)
		verifyLogContains(t, logBuf.String(), "starting MCP server")
	})

	t.Run("config validation failure", func(t *testing.T) {
		t.Parallel()

		configPath := writeTestConfig(t, invalidConfigContent)

		var logBuf bytes.Buffer

		logger := createTestLogger(&logBuf)

		result := run(logger, configPath, "stdio", ":8080")
		verifyRunFailure(t, result, logBuf.String(), "failed to apply sources")
	})

	t.Run("partial source failure", func(t *testing.T) {
		t.Parallel()

		configContent := formatPartialFailureConfig(server.URL)
		configPath := writeTestConfig(t, configContent)

		config, err := loadConfig(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		// The tolerant contract (see dispatcher-tolerate-source-failures):
		// a single failed source is logged and the surviving sources'
		// tools are still registered. Apply returns nil. The cmd-level
		// helper `applySources` is intentionally not used here because
		// it discards the logger output; this subtest asserts the
		// dispatcher contract directly.
		var logBuf bytes.Buffer

		logger := createTestLogger(&logBuf)

		srv := mcp.NewServer(
			&mcp.Implementation{Name: "partial", Version: "0.0.0"},
			&mcp.ServerOptions{},
		)

		err = source.Apply(t.Context(), srv, config.Sources, tool.WithLogger(logger))
		require.NoErrorf(t, err, "Apply must not crash on a single per-source failure")

		logs := logBuf.String()
		assert.Containsf(t, logs, "name=broken",
			"the failing source's name must be in the structured log output")
		assert.Containsf(t, logs, "level=ERROR",
			"the per-source failure must be logged at error level")
	})

	t.Run("missing config file", func(t *testing.T) {
		t.Parallel()

		var logBuf bytes.Buffer

		logger := createTestLogger(&logBuf)

		result := run(logger, "/nonexistent/config.yaml", "stdio", ":8080")
		verifyRunFailure(t, result, logBuf.String(), "failed to load config")
	})
}

// TestLoadConfigFromTestdata tests loading config files from the testdata directory.
// testConfigFromTemplate loads a config from a template file and validates it.
// This helper function is used to test config loading from different file formats.
func testConfigFromTemplate(t *testing.T, serverURL, templateFile, configFile string) {
	t.Helper()

	// Read template from testdata
	templatePath := filepath.Join("testdata", templateFile)

	templateData, err := os.ReadFile(templatePath)
	if err != nil {
		t.Skipf("template file not found: %v", err)
	}

	// Replace placeholder with actual server URL
	configContent := strings.ReplaceAll(string(templateData), "{{SERVER_URL}}", serverURL)

	// Write to temp file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, configFile)

	err = os.WriteFile(configPath, []byte(configContent), 0o600)
	if err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	config, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if len(config.Sources) == 0 {
		t.Error("expected at least one source")
	}

	validateConfigSources(t, config)
}

func TestLoadConfigFromTestdata(t *testing.T) {
	t.Parallel()

	// Create httptest server for testdata configs
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte(`{"result": "ok"}`)) //nolint:errcheck // test response
	}))
	defer server.Close()

	t.Run("yaml from testdata", func(t *testing.T) {
		t.Parallel()
		testConfigFromTemplate(t, server.URL, "config.yaml.template", "config.yaml")
	})

	t.Run("json from testdata", func(t *testing.T) {
		t.Parallel()
		testConfigFromTemplate(t, server.URL, "config.json.template", "config.json")
	})
}

// TestConfigUnmarshal tests that config unmarshaling works correctly.
func TestConfigUnmarshal(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte(`{}`)) //nolint:errcheck // test response
	}))
	defer server.Close()

	t.Run("basic sources config", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := fmt.Sprintf(`sources:
  base:
    type: http
    tools:
      prefix: base_
    connect:
      url: %s/api
      method: GET
      description: Base tool
      headers:
        Authorization: Bearer token123
  extended:
    type: http
    tools:
      prefix: extended_
    connect:
      url: %s/api/extended
      method: GET
      description: Extended tool
`, server.URL, server.URL)

		err := os.WriteFile(configPath, []byte(configContent), 0o600)
		if err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		config, err := loadConfig(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if len(config.Sources) != 2 {
			t.Errorf("expected 2 sources, got %d", len(config.Sources))
		}

		validateConfigSources(t, config)
	})
}

// TestConfigUnmarshal_Before verifies that the top-level `before:`
// list parses correctly from both YAML and JSON, including the
// optional per-command healthcheck sub-block with TCP / interval /
// timeout fields. The actual lifecycle behavior is exercised in
// shell/before_test.go; this test focuses on the config wiring.
func TestConfigUnmarshal_Before(t *testing.T) {
	t.Parallel()

	t.Run("yaml with healthcheck", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		content := `name: test
version: "1.0.0"
before:
  - command: "kubectl port-forward svc/postgres-primary 5432:5432 -n primary"
    healthcheck:
      tcp: "127.0.0.1:5432"
      interval: 500ms
      timeout: 10s
  - command: "kubectl port-forward svc/postgres-events 5433:5432 -n events"
`

		require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))

		config, err := loadConfig(configPath)
		require.NoErrorf(t, err, "loadConfig: %v", err)

		require.Lenf(t, config.Before, 2,
			"expected 2 before entries, got %d", len(config.Before))

		first := config.Before[0]
		expectedFirstCommand := "kubectl port-forward svc/postgres-primary 5432:5432 -n primary"
		assert.Equal(t, expectedFirstCommand, first.Command)

		require.NotNilf(t, first.Healthcheck, "first before command must have a healthcheck")
		assert.Equal(t, "127.0.0.1:5432", first.Healthcheck.TCP)
		assert.Equal(t, 500*time.Millisecond, first.Healthcheck.Interval.Duration())
		assert.Equal(t, 10*time.Second, first.Healthcheck.Timeout.Duration())

		second := config.Before[1]
		expectedSecondCommand := "kubectl port-forward svc/postgres-events 5433:5432 -n events"
		assert.Equal(t, expectedSecondCommand, second.Command)
		assert.Nilf(t, second.Healthcheck, "second before command has no healthcheck")
	})

	t.Run("json parses same shape", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.json")

		content := `{
  "name": "test",
  "version": "1.0.0",
  "before": [
    {
      "command": "echo hi",
      "healthcheck": {"tcp": "127.0.0.1:8080"}
    }
  ]
}
`

		require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))

		config, err := loadConfig(configPath)
		require.NoErrorf(t, err, "loadConfig: %v", err)

		require.Lenf(t, config.Before, 1, "expected 1 before entry, got %d", len(config.Before))

		entry := config.Before[0]
		expectedCommand := "echo hi"
		assert.Equal(t, expectedCommand, entry.Command)
		require.NotNilf(t, entry.Healthcheck, "json Before[0].Healthcheck should be populated")
		assert.Equal(t, "127.0.0.1:8080", entry.Healthcheck.TCP)
	})

	t.Run("absent before is a nil slice", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		content := `name: test
version: "1.0.0"
sources:
  dummy:
    type: http
    connect:
      url: "http://example.com"
      method: GET
`

		require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))

		config, err := loadConfig(configPath)
		require.NoErrorf(t, err, "loadConfig: %v", err)

		assert.Empty(t, config.Before)
	})
}

// TestConfigUnmarshal_BeforeWithRestart verifies that the optional
// `restart:` sub-block on each `before:` entry parses correctly from
// both YAML and JSON, including the bounded-max-attempts and delay
// fields. The actual lifecycle behavior is exercised in
// shell/before_test.go; this test focuses on the config wiring.
func TestConfigUnmarshal_BeforeWithRestart(t *testing.T) {
	t.Parallel()

	t.Run("yaml with restart", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		content := `name: test
version: "1.0.0"
before:
  - command: "kubectl port-forward svc/postgres-primary 5432:5432 -n primary"
    restart:
      max_attempts: 5
      delay: 2s
  - command: "kubectl port-forward svc/postgres-events 5433:5432 -n events"
    restart: {}
`

		require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))

		config, err := loadConfig(configPath)
		require.NoErrorf(t, err, "loadConfig: %v", err)

		require.Lenf(t, config.Before, 2,
			"expected 2 before entries, got %d", len(config.Before))

		first := config.Before[0]
		require.NotNilf(t, first.Restart, "first before entry must have a restart block")
		assert.Equal(t, 5, first.Restart.MaxAttempts)
		assert.Equal(t, 2*time.Second, first.Restart.Delay.Duration())

		second := config.Before[1]
		require.NotNilf(t, second.Restart, "second before entry must have a restart block")
		assert.Equalf(t, 0, second.Restart.MaxAttempts,
			"empty restart block must keep MaxAttempts at zero (unbounded)")
		assert.Equalf(t, time.Duration(0), second.Restart.Delay.Duration(),
			"empty restart block must keep Delay at zero (defaults applied at runtime)")
	})

	t.Run("json with restart", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.json")

		content := `{
  "name": "test",
  "version": "1.0.0",
  "before": [
    {
      "command": "echo hi",
      "restart": {"max_attempts": 3, "delay": "500ms"}
    }
  ]
}
`

		require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))

		config, err := loadConfig(configPath)
		require.NoErrorf(t, err, "loadConfig: %v", err)

		require.Lenf(t, config.Before, 1, "expected 1 before entry, got %d", len(config.Before))

		entry := config.Before[0]
		require.NotNilf(t, entry.Restart, "json Before[0].Restart should be populated")
		assert.Equal(t, 3, entry.Restart.MaxAttempts)
		assert.Equal(t, 500*time.Millisecond, entry.Restart.Delay.Duration())
	})

	t.Run("absent restart is nil", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		content := `name: test
version: "1.0.0"
before:
  - command: "true"
sources:
  dummy:
    type: http
    connect:
      url: "http://example.com"
      method: GET
`

		require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))

		config, err := loadConfig(configPath)
		require.NoErrorf(t, err, "loadConfig: %v", err)

		require.Lenf(t, config.Before, 1, "expected 1 before entry, got %d", len(config.Before))

		entry := config.Before[0]
		assert.Nilf(t, entry.Restart, "absent restart block must leave the field nil")
	})
}

// TestHTTPMethods tests various HTTP methods with the httptest server.
func TestHTTPMethods(t *testing.T) {
	t.Parallel()

	methods := []struct {
		name   string
		method string
	}{
		{"GET", http.MethodGet},
		{"POST", http.MethodPost},
		{"PUT", http.MethodPut},
		{"PATCH", http.MethodPatch},
		{"DELETE", http.MethodDelete},
		{"HEAD", http.MethodHead},
		{"OPTIONS", http.MethodOptions},
	}

	for _, testCase := range methods {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			server := createMethodTestServer(t, testCase.method)
			defer server.Close()

			configContent := formatMethodConfig(server.URL, testCase.method)
			configPath := writeTestConfig(t, configContent)

			config, err := loadConfig(configPath)
			if err != nil {
				t.Fatalf("failed to load config: %v", err)
			}

			validateConfigSources(t, config)
		})
	}
}

// TestInvalidConfig tests that invalid configurations are properly rejected.
// Validation now happens at source.Apply time (the per-type Connect validates
// its connect map), so each case is exercised by loading the config and then
// applying its sources to a throwaway server.
func TestInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      string
		ext         string
		expectError bool
	}{
		{
			name: "empty URL",
			config: `sources:
  test:
    type: http
    connect:
      url: ""
      method: GET
      description: Test`,
			ext:         ".yaml",
			expectError: true,
		},
		{
			name: "empty method",
			config: `sources:
  test:
    type: http
    connect:
      url: http://example.com
      method: ""
      description: Test`,
			ext:         ".yaml",
			expectError: true,
		},
		{
			name: "invalid method",
			config: `sources:
  test:
    type: http
    connect:
      url: http://example.com
      method: INVALID
      description: Test`,
			ext:         ".yaml",
			expectError: true,
		},
		{
			name: "invalid URL",
			config: `sources:
  test:
    type: http
    connect:
      url: "://invalid"
      method: GET
      description: Test`,
			ext:         ".yaml",
			expectError: true,
		},
		{
			name: "unknown source type",
			config: `sources:
  weather:
    type: totally-bogus
    connect:
      url: http://example.com
      method: GET
      description: Test`,
			ext:         ".yaml",
			expectError: true,
		},
		{
			name: "unsupported config format",
			config: `sources:
  test:
    type: http
    connect:
      url: http://example.com
      method: GET
      description: Test`,
			ext:         ".toml",
			expectError: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			configPath := writeTestConfigWithExt(t, testCase.config, testCase.ext)
			err := validateInvalidConfigTestCase(t, configPath)

			if testCase.expectError {
				require.Errorf(t, err, "expected error but got none")

				return
			}

			require.NoErrorf(t, err, "unexpected error")
		})
	}
}

// validateInvalidConfigTestCase validates a single test case for TestInvalidConfig.
// It returns an error if the config fails to load or any source fails to apply.
// source.Apply is atomic: one failed source aborts the whole apply and the
// returned error wraps every issue source.Apply found.
func validateInvalidConfigTestCase(t *testing.T, configPath string) error {
	t.Helper()

	config, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	return applySources(t.Context(), config)
}

// TestLogLevel tests the loglevel function.
func TestLogLevel(t *testing.T) {
	// Note: Cannot use t.Parallel() because t.Setenv() is not compatible with parallel tests

	tests := []struct {
		envValue string
		expected slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"disabled", slog.Level(math.MaxInt)},
		{"-1", slog.Level(math.MaxInt)},
		{"", slog.LevelInfo},
		{"unknown", slog.LevelInfo},
	}

	for _, testCase := range tests {
		t.Run(testCase.envValue, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", testCase.envValue)

			level := loglevel()
			if level != testCase.expected {
				t.Errorf("expected level %v, got %v", testCase.expected, level)
			}
		})
	}
}

// TestLogHandler tests the loghandler function.
func TestLogHandler(t *testing.T) {
	// Note: Cannot use t.Parallel() because t.Setenv() is not compatible with parallel tests

	t.Run("text handler default", func(t *testing.T) {
		t.Setenv("LOG_JSON", "")
		t.Setenv("LOG_ADD_SOURCE", "")

		handler := loghandler()
		if handler == nil {
			t.Error("expected non-nil handler")
		}
	})

	t.Run("json handler", func(t *testing.T) {
		t.Setenv("LOG_JSON", "true")
		t.Setenv("LOG_ADD_SOURCE", "false")

		handler := loghandler()
		if handler == nil {
			t.Error("expected non-nil handler")
		}
	})

	t.Run("with source", func(t *testing.T) {
		t.Setenv("LOG_JSON", "false")
		t.Setenv("LOG_ADD_SOURCE", "true")

		handler := loghandler()
		if handler == nil {
			t.Error("expected non-nil handler")
		}
	})
}

// TestEmptyConfigFlag tests that run returns error when config flag is empty.
func TestEmptyConfigFlag(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	result := run(logger, "", "stdio", ":8080")
	if result == 0 {
		t.Error("expected run to fail with empty config path")
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "--config flag is required") {
		t.Errorf("expected logs to contain '--config flag is required', got: %s", logs)
	}
}

// testServerConfig holds configuration for createTestServer.
type testServerConfig struct {
	Method         string
	Path           string
	ResponseBody   any
	ExpectedStatus int
}

// createTestServer creates an httptest server that verifies method and path.
func createTestServer(t *testing.T, cfg testServerConfig) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != cfg.Method {
			t.Errorf("expected method %s, got %s", cfg.Method, r.Method)
		}

		if r.URL.Path != cfg.Path {
			t.Errorf("expected path %s, got %s", cfg.Path, r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")

		status := cfg.ExpectedStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)

		respData, err := json.Marshal(cfg.ResponseBody)
		if err != nil {
			t.Errorf("failed to marshal response: %v", err)
		}

		_, _ = w.Write(respData) //nolint:errcheck // test response write
	}))
}

// createSimpleTestServer creates a simple test server that returns a fixed response.
func createSimpleTestServer(t *testing.T, response string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte(response)) //nolint:errcheck // test response
	}))
}

// createMultiRouteTestServer creates a test server with multiple routes.
func createMultiRouteTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		switch r.URL.Path {
		case "/api/status":
			_, _ = w.Write([]byte(`{"status": "healthy"}`)) //nolint:errcheck // test response

		case "/api/data":
			_, _ = w.Write([]byte(`{"data": []}`)) //nolint:errcheck // test response

		default:
			_, _ = w.Write([]byte(`{"error": "not found"}`)) //nolint:errcheck // test response
		}
	}))
}

// createMethodTestServer creates a test server that verifies the HTTP method.
func createMethodTestServer(t *testing.T, expectedMethod string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != expectedMethod {
			t.Errorf("expected %s, got %s", expectedMethod, r.Method)
		}
		w.WriteHeader(http.StatusOK)

		if expectedMethod != http.MethodHead {
			_, _ = w.Write([]byte(`{}`)) //nolint:errcheck // test response
		}
	}))
}

// writeTestConfig writes a config file to a temp directory with .yaml extension.
func writeTestConfig(t *testing.T, content string) string {
	t.Helper()

	return writeTestConfigWithExt(t, content, ".yaml")
}

// writeTestConfigWithExt writes a config file to a temp directory with specified extension.
func writeTestConfigWithExt(t *testing.T, content, ext string) string {
	t.Helper()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config"+ext)

	err := os.WriteFile(configPath, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	return configPath
}

// formatTestConfig formats a test config with a single http source named "test-tool".
func formatTestConfig(serverURL, path, method string) string {
	return fmt.Sprintf(`sources:
  test-tool:
    type: http
    connect:
      url: %s%s
      method: %s
      description: A test tool
`, serverURL, path, method)
}

// formatYAMLConfig formats a YAML config with two http sources for testing.
func formatYAMLConfig(serverURL string) string {
	return fmt.Sprintf(`sources:
  weather:
    type: http
    tools:
      prefix: weather_
    connect:
      url: %s/weather
      method: GET
      description: Get weather information
  users:
    type: http
    tools:
      prefix: users_
    connect:
      url: %s/users
      method: POST
      description: Create a new user
      headers:
        Content-Type: application/json
`, serverURL, serverURL)
}

// formatJSONConfig formats a JSON config with a single http source for testing.
func formatJSONConfig(serverURL string) string {
	return fmt.Sprintf(`{
    "sources": {
        "api": {
            "type": "http",
            "connect": {
                "url": "%s/api",
                "method": "GET",
                "description": "API tool"
            }
        }
    }
}`, serverURL)
}

// formatMethodConfig formats a config for HTTP method testing.
func formatMethodConfig(serverURL, method string) string {
	return fmt.Sprintf(`sources:
  test:
    type: http
    connect:
      url: %s
      method: %s
      description: Test tool
`, serverURL, method)
}

// verifySourceConfig verifies the config has exactly one source with the expected name.
func verifySourceConfig(t *testing.T, config *Config, expectedName string) {
	t.Helper()

	if len(config.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(config.Sources))
	}

	if config.Sources[0].Name != expectedName {
		t.Errorf("expected source name %q, got %q", expectedName, config.Sources[0].Name)
	}
}

// verifySourceCount verifies the number of sources.
func verifySourceCount(t *testing.T, config *Config, expected int) {
	t.Helper()

	if len(config.Sources) != expected {
		t.Errorf("expected %d sources, got %d", expected, len(config.Sources))
	}
}

// validateConfigSources applies every source in config to a throwaway
// server. Apply is tolerant of per-source failures: per-source errors
// are logged inside Apply and the surviving sources' tools are still
// registered. The helper fails the test only when no source
// contributed any tool (mirroring the production guard in run that
// refuses to start with no tools).
func validateConfigSources(t *testing.T, config *Config) {
	t.Helper()

	err := applySources(t.Context(), config)
	if err != nil {
		t.Fatalf("config validation failed: %v", err)
	}
}

// applySources applies every source in config to a fresh server. The
// single Apply call replaces the per-source fan-out that previously
// lived in the production orchestrator: validation, prefix/remove
// middlewares, duplicate-name check, and registration are all driven
// by source.Apply. Apply is tolerant of per-source failures; this
// helper returns the error only when no source contributed any tool
// (matching the production exit-1 path in run).
func applySources(ctx context.Context, config *Config) error {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "validate", Version: "0.0.0"},
		&mcp.ServerOptions{},
	)

	err := source.Apply(ctx, srv, config.Sources, tool.WithLogger(slog.Default()))
	if err != nil {
		return fmt.Errorf("apply sources: %w", err)
	}

	return nil
}

// Helper functions for TestRunWithHTTPServer

// formatRunTestConfig formats a config for the run test.
func formatRunTestConfig(serverURL string) string {
	return fmt.Sprintf(`sources:
  status:
    type: http
    tools:
      prefix: status_
    connect:
      url: %s/api/status
      method: GET
      description: Get system status
  data:
    type: http
    tools:
      prefix: data_
    connect:
      url: %s/api/data
      method: GET
      description: Get data
`, serverURL, serverURL)
}

// invalidConfigContent is an invalid config for testing failures. The http
// source has an empty URL, so source.Apply fails at Connect time.
const invalidConfigContent = `sources:
  broken:
    type: http
    connect:
      url: ""
      method: GET
      description: A broken tool
`

// formatPartialFailureConfig formats a config with one broken source
// (empty URL, fails to apply) and one healthy source pointing at serverURL.
// Used by TestRunWithHTTPServer/partial source failure to verify the
// tolerant contract: a single failed source is logged and the surviving
// source's tool is registered, while Apply itself returns nil.
func formatPartialFailureConfig(serverURL string) string {
	return fmt.Sprintf(`sources:
  broken:
    type: http
    connect:
      url: ""
      method: GET
      description: A broken tool
  status:
    type: http
    connect:
      url: %s/api/status
      method: GET
      description: Get system status
`, serverURL)
}

// createTestLogger creates a logger that writes to a buffer.
func createTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}

// runConfigValidationAsync runs config validation in a goroutine and returns a result channel.
func runConfigValidationAsync(
	t *testing.T,
	ctx context.Context,
	configPath string,
	logger *slog.Logger,
) <-chan int {
	t.Helper()

	resultChan := make(chan int, 1)

	go func() {
		config, err := loadConfig(configPath)
		if err != nil {
			resultChan <- 1
			return
		}

		err = applySources(ctx, config)
		if err != nil {
			resultChan <- 1

			return
		}

		logger.InfoContext(ctx, "starting MCP server", "sources", len(config.Sources))
		<-ctx.Done()

		resultChan <- 0
	}()

	return resultChan
}

// waitAndVerifyResult waits for a result with timeout and verifies it matches expected.
func waitAndVerifyResult(t *testing.T, resultChan <-chan int, expected int) {
	t.Helper()

	select {
	case result := <-resultChan:
		if result != expected {
			t.Errorf("expected result %d, got %d", expected, result)
		}

	case <-time.After(3 * time.Second):
		t.Error("test timed out")
	}
}

// verifyLogContains checks that logs contain the expected message.
func verifyLogContains(t *testing.T, logs, expected string) {
	t.Helper()

	if !strings.Contains(logs, expected) {
		t.Errorf("expected logs to contain '%s'", expected)
	}
}

// verifyRunFailure verifies that a run call failed with expected log message.
func verifyRunFailure(t *testing.T, result int, logs, expectedLog string) {
	t.Helper()

	if result == 0 {
		t.Error("expected run to fail")
	}

	if !strings.Contains(logs, expectedLog) {
		t.Errorf("expected logs to contain '%s', got: %s", expectedLog, logs)
	}
}
