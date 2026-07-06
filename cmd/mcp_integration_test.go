// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/source"
	"go.amidman.dev/mcp/tool"
)

// mcpTestSetup holds the common test harness for MCP integration tests.
type mcpTestSetup struct {
	Server        *httptest.Server
	Config        *Config
	MCPServer     *mcp.Server
	ClientSession *mcp.ClientSession
}

// newMCPTestSetup creates a complete MCP test harness.
// It creates a config file from the provided content, loads it, creates an MCP
// server, applies every source via source.Apply, and connects a client via
// in-memory transports.
func newMCPTestSetup(t *testing.T, server *httptest.Server, configContent string) *mcpTestSetup {
	t.Helper()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	config, err := loadConfig(configPath)
	require.NoErrorf(t, err, "failed to load config")

	ctx := t.Context()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	config.setDefaults()

	srv := mcp.NewServer(
		(&mcp.Implementation{
			Name:    config.Name,
			Title:   config.Title,
			Version: config.Version,
		}),
		&mcp.ServerOptions{
			Logger: logger,
		},
	)

	for _, src := range config.Sources {
		err = source.Apply(ctx, srv, []source.Source{src}, tool.WithLogger(logger))
		require.NoErrorf(t, err, "failed to apply source %q", src.Name)
	}

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := srv.Connect(ctx, serverTransport, (*mcp.ServerSessionOptions)(nil))
	require.NoErrorf(t, err, "failed to connect server")
	t.Cleanup(func() {
		assert.NoErrorf(t, serverSession.Close(), "failed to close server session")
	})

	client := mcp.NewClient(
		(&mcp.Implementation{
			Name:    "test-client",
			Version: "1.0.0",
		}),
		(*mcp.ClientOptions)(nil),
	)

	clientSession, err := client.Connect(ctx, clientTransport, (*mcp.ClientSessionOptions)(nil))
	require.NoErrorf(t, err, "failed to connect client")
	t.Cleanup(func() {
		assert.NoErrorf(t, clientSession.Close(), "failed to close client session")
	})

	return &mcpTestSetup{
		Server:        server,
		Config:        config,
		MCPServer:     srv,
		ClientSession: clientSession,
	}
}

// callTool calls a tool with the given arguments and returns the parsed JSON response.
func (s *mcpTestSetup) callTool(
	t *testing.T,
	toolName string,
	arguments map[string]any,
) map[string]any {
	t.Helper()

	result, err := s.ClientSession.CallTool(
		t.Context(),
		(&mcp.CallToolParams{
			Name:      toolName,
			Arguments: arguments,
		}),
	)
	require.NoErrorf(t, err, "failed to call tool %s", toolName)
	require.Falsef(t, result.IsError, "tool %s returned error: %v", toolName, result.Content)
	require.NotEmptyf(t, result.Content, "tool %s returned empty content", toolName)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected TextContent, got %T", result.Content[0])

	var response map[string]any

	err = json.Unmarshal([]byte(textContent.Text), &response)
	require.NoErrorf(t, err, "failed to parse response JSON from tool %s", toolName)

	return response
}

// callToolExpectError calls a tool and expects it to return an error.
func (s *mcpTestSetup) callToolExpectError(
	t *testing.T,
	toolName string,
	arguments map[string]any,
) {
	t.Helper()

	_, err := s.ClientSession.CallTool(
		t.Context(),
		(&mcp.CallToolParams{
			Name:      toolName,
			Arguments: arguments,
		}),
	)
	require.Errorf(t, err, "expected error when calling tool %s", toolName)
}

// writeJSONResponse writes a JSON response to the http.ResponseWriter.
func writeJSONResponse(t *testing.T, w http.ResponseWriter, status int, response any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	err := json.NewEncoder(w).Encode(response)
	require.NoErrorf(t, err, "failed to encode JSON response")
}

// TestMCPToolExecution tests the full MCP protocol flow:
// 1. Create an httptest server that returns JSON
// 2. Create config referencing the httptest server
// 3. Start MCP server with in-memory transport
// 4. Connect MCP client
// 5. Call tool via client
// 6. Verify the response matches the httptest server response
func TestMCPToolExecution(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/weather":
			writeJSONResponse(t, w, http.StatusOK, map[string]any{
				"location":    "San Francisco",
				"temperature": 18.5,
				"unit":        "celsius",
				"condition":   "partly cloudy",
				"humidity":    72,
			})

		case "/users":
			writeJSONResponse(t, w, http.StatusOK, map[string]any{
				"users": []map[string]any{
					{"id": 1, "name": "Alice"},
					{"id": 2, "name": "Bob"},
				},
				"total": 2,
			})

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() { server.Close() })

	// Two http sources named weather and users. tools.prefix is set
	// explicitly on each so the two sources expose distinct tool names;
	// without it, both would produce a tool named "http" and the
	// atomic apply would surface a duplicate-name config error.
	configContent := fmt.Sprintf(`sources:
  weather:
    type: http
    tools:
      prefix: weather_
    connect:
      url: %s/weather
      method: GET
      description: Get current weather information
  users:
    type: http
    tools:
      prefix: users_
    connect:
      url: %s/users
      method: GET
      description: List all users
`, server.URL, server.URL)

	setup := newMCPTestSetup(t, server, configContent)

	// Test: List tools
	t.Run("list tools", func(t *testing.T) {
		toolsResult, err := setup.ClientSession.ListTools(t.Context(), (*mcp.ListToolsParams)(nil))
		require.NoErrorf(t, err, "failed to list tools")
		require.Lenf(t, toolsResult.Tools, 2, "expected 2 tools")

		toolNames := make(map[string]bool)
		for _, tool := range toolsResult.Tools {
			toolNames[tool.Name] = true
		}

		require.Truef(t, toolNames["weather_http"], "expected 'weather_http' tool to be available")
		require.Truef(t, toolNames["users_http"], "expected 'users_http' tool to be available")
	})

	// Test: Call weather tool
	t.Run("call weather", func(t *testing.T) {
		response := setup.callTool(t, "weather_http", make(map[string]any))

		require.Equalf(t, "San Francisco", response["location"], "location mismatch")
		require.Equalf(t, "partly cloudy", response["condition"], "condition mismatch")

		temp, ok := response["temperature"].(float64)
		require.Truef(t, ok, "expected temperature float64, got %T", response["temperature"])

		require.InEpsilonf(t,
			18.5, temp,
			0.001,
			"temperature mismatch",
		)
	})

	// Test: Call users tool
	t.Run("call users", func(t *testing.T) {
		response := setup.callTool(t, "users_http", make(map[string]any))

		users, ok := response["users"].([]any)
		require.Truef(t, ok, "expected users array, got %T", response["users"])
		require.Lenf(t, users, 2, "expected 2 users")

		total, ok := response["total"].(float64)
		require.Truef(t, ok, "expected total float64, got %T", response["total"])

		require.InEpsilonf(t, 2.0, total, 0.001, "total mismatch")
	})
}

// TestMCPToolExecutionWithQueryParams tests tool execution with query parameters.
func TestMCPToolExecutionWithQueryParams(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, map[string]any{
			"received_query": r.URL.Query(),
			"path":           r.URL.Path,
		})
	}))
	t.Cleanup(func() { server.Close() })

	configContent := fmt.Sprintf(`sources:
  search:
    type: http
    tools:
      prefix: search_
    connect:
      url: %s/search
      method: GET
      description: Search with query parameters
`, server.URL)

	setup := newMCPTestSetup(t, server, configContent)

	response := setup.callTool(t, "search_http", map[string]any{
		"query": map[string]any{
			"q":     "golang",
			"limit": "10",
		},
	})

	// Verify query parameters were received
	receivedQuery, ok := response["received_query"].(map[string]any)
	require.Truef(t, ok, "expected received_query object, got %T", response["received_query"])

	// r.URL.Query() returns map[string][]string, so values are arrays in JSON
	qValues, ok := receivedQuery["q"].([]any)
	require.Truef(t, ok, "expected q array, got %T", receivedQuery["q"])
	require.NotEmptyf(t, qValues, "q values should not be empty")
	require.Equalf(t, "golang", qValues[0], "q parameter mismatch")

	limitValues, ok := receivedQuery["limit"].([]any)
	require.Truef(t, ok, "expected limit array, got %T", receivedQuery["limit"])
	require.NotEmptyf(t, limitValues, "limit values should not be empty")
	require.Equalf(t, "10", limitValues[0], "limit parameter mismatch")
}

// TestMCPToolExecutionWithPostBody tests tool execution with POST body.
func TestMCPToolExecutionWithPostBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST method, got %s", r.Method)
			return
		}

		var body map[string]any

		err := json.NewDecoder(r.Body).Decode(&body)
		if err != nil {
			t.Errorf("failed to decode request body: %v", err)
			return
		}

		writeJSONResponse(t, w, http.StatusCreated, map[string]any{
			"success": true,
			"echo":    body,
			"message": "resource created",
		})
	}))
	t.Cleanup(func() { server.Close() })

	configContent := fmt.Sprintf(`sources:
  create:
    type: http
    tools:
      prefix: create_
    connect:
      url: %s/create
      method: POST
      description: Create a new resource
      headers:
        Content-Type: application/json
`, server.URL)

	setup := newMCPTestSetup(t, server, configContent)

	response := setup.callTool(t, "create_http", map[string]any{
		"body": map[string]any{
			"name":  "test-resource",
			"value": 42,
		},
	})

	// Verify response
	success, ok := response["success"].(bool)
	require.Truef(t, ok, "expected success bool, got %T", response["success"])

	require.Truef(t, success, "expected success=true")
	require.Equalf(t, "resource created", response["message"], "message mismatch")

	// Verify echoed body
	echo, ok := response["echo"].(map[string]any)
	require.Truef(t, ok, "expected echo object, got %T", response["echo"])
	require.Equalf(t, "test-resource", echo["name"], "name mismatch")

	value, ok := echo["value"].(float64)
	require.Truef(t, ok, "expected value float64, got %T", echo["value"])

	require.InEpsilonf(t, 42.0, value, 0.001, "value mismatch")
}

// TestMCPToolExecutionError tests error handling when the HTTP server returns an error.
func TestMCPToolExecutionError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusInternalServerError, map[string]any{
			"error":  "internal_server_error",
			"detail": "something went wrong",
		})
	}))
	t.Cleanup(func() { server.Close() })

	configContent := fmt.Sprintf(`sources:
  error:
    type: http
    connect:
      url: %s/error
      method: GET
      description: A tool that returns errors
`, server.URL)

	setup := newMCPTestSetup(t, server, configContent)

	setup.callToolExpectError(t, "errorhttp", make(map[string]any))
}

// TestMCPServerInfoViaClientRequest tests that the server's name, title,
// and version are correctly transmitted to the client via the InitializeResult.
func TestMCPServerInfoViaClientRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, map[string]any{
			"message": "test response",
		})
	}))
	t.Cleanup(func() { server.Close() })

	configContent := fmt.Sprintf(`name: custom-server
title: "Custom MCP Server"
version: "3.5.1"

sources:
  test:
    type: http
    tools:
      prefix: test_
    connect:
      url: %s/test
      method: GET
      description: "A test tool"
`, server.URL)

	setup := newMCPTestSetup(t, server, configContent)

	// Verify server info from client's InitializeResult
	initResult := setup.ClientSession.InitializeResult()
	require.NotNilf(t, initResult, "InitializeResult should not be nil")
	require.NotNilf(t, initResult.ServerInfo, "ServerInfo should not be nil")

	// Verify the server implementation info matches config
	require.Equalf(t, "custom-server", initResult.ServerInfo.Name, "server name mismatch")
	require.Equalf(t, "Custom MCP Server", initResult.ServerInfo.Title, "server title mismatch")
	require.Equalf(t, "3.5.1", initResult.ServerInfo.Version, "server version mismatch")

	// Also verify we can still call tools
	response := setup.callTool(t, "test_http", make(map[string]any))
	require.Equal(t, "test response", response["message"])
}

// TestMCPMultipleTools tests multiple tools
// pointing to different endpoints on the same server.
func TestMCPMultipleTools(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status":
			writeJSONResponse(t, w, http.StatusOK, map[string]any{
				"status":  "ok",
				"version": "1.0.0",
			})

		case "/api/v1/health":
			writeJSONResponse(t, w, http.StatusOK, map[string]any{
				"healthy": true,
				"checks":  5,
			})

		case "/api/v1/metrics":
			writeJSONResponse(t, w, http.StatusOK, map[string]any{
				"requests":   1000,
				"latency_ms": 42,
			})

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() { server.Close() })

	configContent := fmt.Sprintf(`sources:
  status:
    type: http
    tools:
      prefix: status_
    connect:
      url: %s/api/v1/status
      method: GET
      description: Get system status
  health:
    type: http
    tools:
      prefix: health_
    connect:
      url: %s/api/v1/health
      method: GET
      description: Check system health
  metrics:
    type: http
    tools:
      prefix: metrics_
    connect:
      url: %s/api/v1/metrics
      method: GET
      description: Get system metrics
`, server.URL, server.URL, server.URL)

	setup := newMCPTestSetup(t, server, configContent)

	tests := []struct {
		name         string
		toolName     string
		expectedKeys []string
	}{
		{
			name:         "status",
			toolName:     "status_http",
			expectedKeys: []string{"status", "version"},
		},
		{
			name:         "health",
			toolName:     "health_http",
			expectedKeys: []string{"healthy", "checks"},
		},
		{
			name:         "metrics",
			toolName:     "metrics_http",
			expectedKeys: []string{"requests", "latency_ms"},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			response := setup.callTool(t, testCase.toolName, make(map[string]any))

			for _, key := range testCase.expectedKeys {
				_, exists := response[key]
				require.Truef(t, exists, "expected key '%s' in response", key)
			}
		})
	}
}
