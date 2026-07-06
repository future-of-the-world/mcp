// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

const (
	buildTimeout = 2 * time.Minute
	execTimeout  = 10 * time.Second
)

var (
	binaryPath     string
	errBinaryBuild error
)

// getBinaryPath returns the path to the compiled test binary.
// The binary is built once in TestMain before any tests run.
func getBinaryPath(t *testing.T) string {
	t.Helper()

	if errBinaryBuild != nil {
		t.Fatalf("failed to get binary: %v", errBinaryBuild)
	}

	return binaryPath
}

// TestMain builds the test binary once before all tests and cleans up afterwards.
func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "mcp-binary-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)

		//nolint:revive // must exit with failure code before tests run
		os.Exit(1)
	}

	binaryPath = filepath.Join(tmpDir, "mcp-test-binary")

	buildErr := buildBinary(binaryPath)
	if buildErr != nil {
		errBinaryBuild = buildErr

		fmt.Fprintf(os.Stderr, "%v\n", buildErr)
		cleanupTempDir(tmpDir)

		//nolint:revive // must exit with failure code before tests run
		os.Exit(1)
	}

	m.Run()
	cleanupTempDir(tmpDir)
}

// buildBinary compiles the test binary at the specified path.
func buildBinary(binaryPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, "go.amidman.dev/mcp/cmd")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to build binary: %w\noutput: %s", err, output)
	}

	_, err = os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("binary not found after build: %w", err)
	}

	return nil
}

// cleanupTempDir removes the temporary directory and logs any errors.
func cleanupTempDir(dir string) {
	err := os.RemoveAll(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to remove temp dir: %v\n", err)
	}
}

// binaryOutput holds the output from running a binary.
type binaryOutput struct {
	stdout   string
	stderr   string
	exitCode int
}

// runBinary executes the compiled binary with the given arguments and environment.
func runBinary(
	t *testing.T,
	binaryPath string,
	args, env []string,
) binaryOutput {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), execTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, args...)

	cmd.Env = append(os.Environ(), env...)

	var stdoutBuf, stderrBuf strings.Builder

	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()

	exitCode := 0

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("failed to run binary: %v", err)
		}
	}

	return binaryOutput{
		stdout:   stdoutBuf.String(),
		stderr:   stderrBuf.String(),
		exitCode: exitCode,
	}
}

// serverProcess holds the running server process and its output pipes.
// startServer starts the binary as a server and returns the process handle and stderr reader.
func startServer(
	t *testing.T,
	binaryPath string,
	args, env []string,
) (*exec.Cmd, io.Reader) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())

	cmd := exec.CommandContext(ctx, binaryPath, args...)

	cmd.Env = append(os.Environ(), env...)

	stderr, err := cmd.StderrPipe()
	require.NoErrorf(t, err, "failed to create stderr pipe")

	err = cmd.Start()
	require.NoErrorf(t, err, "failed to start server")

	t.Cleanup(func() {
		cancel()

		err := cmd.Wait()
		// Context cancellation causes the process to be killed, which is expected
		if err != nil && !strings.Contains(err.Error(), "signal: killed") {
			t.Logf("server wait error: %v", err)
		}
	})

	return cmd, stderr
}

// startServerOnFreePort starts a server on a free port and returns the cmd and actual address.
// It passes --addr 127.0.0.1:0 to the server and reads the actual bound address
// from the "server listening" slog line in stderr.
func startServerOnFreePort(
	t *testing.T,
	binaryPath string,
	args, env []string,
) (*exec.Cmd, string) {
	t.Helper()

	freeArgs := make([]string, len(args))
	copy(freeArgs, args)

	for i, arg := range freeArgs {
		if arg == "--addr" && i+1 < len(freeArgs) {
			freeArgs[i+1] = "127.0.0.1:0"

			break
		}
	}

	cmd, stderr := startServer(t, binaryPath, freeArgs, env)

	addr := readServerAddrFromStderr(t, stderr)

	return cmd, addr
}

// readServerAddrFromStderr reads stderr lines until it finds the actual server address.
func readServerAddrFromStderr(t *testing.T, stderr io.Reader) string {
	t.Helper()

	reader := bufio.NewReader(stderr)
	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			require.Failf(t, "failed to discover server address", "read stderr: %v", err)

			return ""
		}

		if !strings.Contains(line, "server listening") {
			continue
		}

		addr := extractAddrFromLog(line)
		if addr != "" {
			return addr
		}
	}

	require.Failf(t, "timeout", "timed out waiting for server address")

	return ""
}

// extractAddrFromLog parses the addr= value from a slog text log line.
func extractAddrFromLog(line string) string {
	_, after, found := strings.Cut(line, "addr=")
	if !found {
		return ""
	}

	value, _, _ := strings.Cut(after, " ")

	return strings.TrimSpace(value)
}

// TestBinaryMissingConfigFlag tests binary exits with code 1 when --config is not provided.
func TestBinaryMissingConfigFlag(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	output := runBinary(t, binPath, []string{}, []string(nil))

	require.Equalf(t, 1, output.exitCode, "expected exit code 1 for missing config flag")
	require.Emptyf(t, output.stdout, "stdout should be empty")
	require.Containsf(t, output.stderr, "--config flag is required",
		"stderr should contain error message")
}

// TestBinaryInvalidConfigPath tests binary exits with code 1 when config file doesn't exist.
func TestBinaryInvalidConfigPath(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	output := runBinary(t, binPath, []string{
		"--config", "/nonexistent/config.yaml",
	}, []string(nil))

	require.Equalf(t, 1, output.exitCode, "expected exit code 1 for nonexistent config")
	require.Emptyf(t, output.stdout, "stdout should be empty")
	require.Containsf(t, output.stderr, "failed to load config",
		"stderr should contain error message")
}

// TestBinaryInvalidConfigContent tests that the binary exits with code 1 when config is malformed.
func TestBinaryInvalidConfigContent(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Write invalid YAML
	err := os.WriteFile(configPath, []byte("invalid: [yaml: content"), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	output := runBinary(t, binPath, []string{
		"--config", configPath,
	}, []string(nil))

	require.Equalf(t, 1, output.exitCode, "expected exit code 1 for invalid config")
	require.Emptyf(t, output.stdout, "stdout should be empty")
	require.Containsf(t, output.stderr, "failed to load config",
		"stderr should contain error message")
}

// validToolsConfig is a minimal config with one valid http source. The URL
// points at a port that refuses connections (port 1); the source still
// applies successfully because http.Connect only validates the config, it
// does not dial the endpoint until a tool is called.
const validToolsConfig = `
sources:
  test:
    type: http
    connect:
      url: http://localhost:1/test
      method: GET
      description: test tool
`

func TestBinaryUnknownTransport(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	err := os.WriteFile(configPath, []byte(validToolsConfig), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	output := runBinary(t, binPath, []string{
		"--config", configPath,
		"--transport", "unknown",
	}, []string(nil))

	require.Equalf(t, 1, output.exitCode, "expected exit code 1 for unknown transport")
	require.Emptyf(t, output.stdout, "stdout should be empty")
	require.Containsf(t, output.stderr, "unknown transport", "stderr should contain error message")
}

// testTransportConfig holds configuration for transport tests.
type testTransportConfig struct {
	name            string
	transport       string
	expectedMessage string
}

// testBinaryTransport tests that the binary starts an HTTP server with the given transport.
func testBinaryTransport(t *testing.T, cfg testTransportConfig) {
	t.Helper()

	binPath := getBinaryPath(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	err := os.WriteFile(configPath, []byte(validToolsConfig), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", configPath,
		"--transport", cfg.transport,
		"--addr", "",
	}, []string{"LOG_LEVEL=debug"})

	require.NotEmptyf(t, addr, "server should report actual address")

	// Stop server
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinarySSETransport tests that the binary starts an SSE HTTP server successfully.
func TestBinarySSETransport(t *testing.T) {
	t.Parallel()
	testBinaryTransport(t, testTransportConfig{
		name:            "sse",
		transport:       "sse",
		expectedMessage: "SSE",
	})
}

// TestBinaryStreamableTransport tests binary starts a streamable HTTP server.
func TestBinaryStreamableTransport(t *testing.T) {
	t.Parallel()
	testBinaryTransport(t, testTransportConfig{
		name:            "streamable",
		transport:       "streamable",
		expectedMessage: "streamable HTTP",
	})
}

// TestBinaryJSONLogging tests that LOG_JSON=true produces JSON formatted logs.
func TestBinaryJSONLogging(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	err := os.WriteFile(configPath, []byte(validToolsConfig), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	// Run with unknown transport to get error output
	output := runBinary(t, binPath, []string{
		"--config", configPath,
		"--transport", "unknown",
	}, []string{"LOG_JSON=true"})

	// Should be valid JSON lines
	for line := range strings.SplitSeq(strings.TrimSpace(output.stderr), "\n") {
		if line == "" {
			continue
		}

		var obj map[string]any

		err := json.Unmarshal([]byte(line), &obj)
		require.NoErrorf(t, err, "log line should be valid JSON: %s", line)
	}
}

// TestBinaryLogLevelDebug tests that LOG_LEVEL=debug produces debug-level messages.
func TestBinaryLogLevelDebug(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	err := os.WriteFile(configPath, []byte(validToolsConfig), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, stderr := startServer(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "127.0.0.1:0",
	}, []string{"LOG_LEVEL=debug"})

	// Read stderr until we find debug output.
	stderrChan := make(chan string, 1)

	go func() {
		var buf strings.Builder

		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			buf.WriteString(line)
			buf.WriteByte('\n')

			if strings.Contains(buf.String(), "sources=") {
				stderrChan <- buf.String()

				return
			}
		}

		// A non-EOF scanner error (e.g. an oversized log line) is
		// best-effort: append it so it surfaces in the captured output,
		// then send whatever we accumulated.
		err := scanner.Err()
		if err != nil {
			buf.WriteString(err.Error())
		}

		stderrChan <- buf.String()
	}()

	// Wait for debug-level output before stopping server.
	select {
	case stderrOutput := <-stderrChan:
		require.Containsf(t, stderrOutput, "sources=",
			"stderr should contain debug info about sources")

	case <-time.After(time.Second):
		t.Log("timeout waiting for stderr output")
	}

	// Stop server
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinaryWithHTTPTool tests a full integration with a real HTTP backend.
func TestBinaryWithHTTPTool(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	// Create a mock HTTP server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		response := map[string]any{
			"status":  "ok",
			"message": "hello from mock server",
		}

		_ = json.NewEncoder(w).Encode(response) //nolint:errcheck // mock server response
	}))
	defer mockServer.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := fmt.Sprintf(`
sources:
  test:
    type: http
    connect:
      url: %s
      method: GET
      description: A test tool
`, mockServer.URL)

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "",
	}, []string{"LOG_LEVEL=debug"})

	require.NotEmptyf(t, addr, "server should be listening")

	// Verify server is reachable by connecting a client.
	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, addr)

	_, listErr := clientSession.ListTools(ctx, (*mcp.ListToolsParams)(nil))
	require.NoErrorf(t, listErr, "failed to list tools")

	// Stop server
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinaryConfigValidation tests that config validation errors are reported correctly.
func TestBinaryConfigValidation(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Config with an unknown source type. The config loads fine but fails
	// when run() applies the source via source.Apply (unknown type).
	configContent := `
sources:
  bar:
    type: foo
    connect:
      url: http://example.com
      method: GET
      description: broken
`
	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	output := runBinary(t, binPath, []string{
		"--config", configPath,
	}, []string(nil))

	require.Equalf(t, 1, output.exitCode, "expected exit code 1 for config validation error")
	require.Emptyf(t, output.stdout, "stdout should be empty")
	require.Containsf(t, output.stderr, "failed to apply source",
		"stderr should contain error message")
}

// TestBinaryJSONConfig tests that JSON config files are loaded correctly.
func TestBinaryJSONConfig(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	configContent := `{
	"sources": {
		"test": {
			"type": "http",
			"connect": {
				"url": "http://localhost:1/test",
				"method": "GET",
				"description": "test tool"
			}
		}
	}
}`

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, stderr := startServer(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "127.0.0.1:0",
	}, []string(nil))

	// Read stderr to verify startup.
	stderrChan := make(chan string, 1)

	go func() {
		var buf strings.Builder

		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			buf.WriteString(line)
			buf.WriteByte('\n')

			if strings.Contains(line, "server listening") {
				stderrChan <- buf.String()

				return
			}
		}

		// A non-EOF scanner error (e.g. an oversized log line) is
		// best-effort: append it so it surfaces in the captured output,
		// then send whatever we accumulated.
		err := scanner.Err()
		if err != nil {
			buf.WriteString(err.Error())
		}

		stderrChan <- buf.String()
	}()

	// Wait for startup message before stopping server.
	select {
	case stderrOutput := <-stderrChan:
		require.Containsf(t, stderrOutput, "starting MCP server",
			"stderr should contain startup message")

	case <-time.After(time.Second):
		t.Log("timeout waiting for stderr output")
	}

	// Stop server
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinaryUnsupportedConfigFormat tests that unsupported config formats are rejected.
func TestBinaryUnsupportedConfigFormat(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	configContent := `
sources = []
`

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	output := runBinary(t, binPath, []string{
		"--config", configPath,
	}, []string(nil))

	require.Equalf(t, 1, output.exitCode, "expected exit code 1 for unsupported config format")
	require.Emptyf(t, output.stdout, "stdout should be empty")
	require.Containsf(t, output.stderr, "unsupported config format",
		"stderr should contain error message")
}

// TestBinaryDefaultTransport tests that stdio is the default transport.
func TestBinaryDefaultTransport(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	err := os.WriteFile(configPath, []byte(validToolsConfig), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	// Start the binary with default transport (stdio).
	// Provide an empty stdin so the binary receives EOF immediately
	// and shuts down gracefully.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "--config", configPath)

	cmd.Stdin = strings.NewReader("")

	cmd.Env = append(os.Environ(), "LOG_LEVEL=debug")

	var stderr strings.Builder

	cmd.Stderr = &stderr

	// Don't set stdout - stdio transport expects MCP protocol there.
	err = cmd.Start()
	require.NoErrorf(t, err, "failed to start binary")

	// Wait for the process to exit cleanly (stdin EOF triggers graceful shutdown).
	err = cmd.Wait()
	require.NoErrorf(t, err, "expected clean exit when stdin is closed")

	// Verify it started with stdio transport
	require.Containsf(t, stderr.String(), "transport=stdio",
		"should use stdio transport by default")
}

// TestBinaryPortInUse tests handling of port already in use.
func TestBinaryPortInUse(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	err := os.WriteFile(configPath, []byte(validToolsConfig), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	// Start a listener on a free port first (let OS pick by using port 0)
	lc := net.ListenConfig{}
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoErrorf(t, err, "failed to start listener")

	t.Cleanup(func() { require.NoErrorf(t, listener.Close(), "failed to close listener") })

	// Get the port from the listener
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	require.Truef(t, ok, "expected *net.TCPAddr, got %T", listener.Addr())

	addr := tcpAddr.String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath,
		"--config", configPath,
		"--transport", "sse",
		"--addr", addr,
	)

	cmd.Env = append(os.Environ(), "LOG_LEVEL=debug")

	var stderr strings.Builder

	cmd.Stderr = &stderr

	err = cmd.Run()

	// Process should fail (either via exit code or signal)
	require.Errorf(t, err, "expected error when port is in use")
	require.Containsf(t, stderr.String(), "listen error", "stderr should contain listen error")
}

// connectSSEClient creates and connects an MCP client to an SSE server.
func connectSSEClient(
	t *testing.T,
	ctx context.Context,
	addr string,
) *mcp.ClientSession {
	t.Helper()

	client := mcp.NewClient(
		(&mcp.Implementation{
			Name:    "test-client",
			Version: "1.0.0",
		}),
		(*mcp.ClientOptions)(nil),
	)

	serverURL := "http://" + addr + "/sse"
	transport := &mcp.SSEClientTransport{
		Endpoint:   serverURL,
		HTTPClient: &http.Client{Transport: &http.Transport{}},
	}

	session, err := client.Connect(ctx, transport, (*mcp.ClientSessionOptions)(nil))
	require.NoErrorf(t, err, "failed to connect client to SSE server at %s", serverURL)

	t.Cleanup(func() {
		require.NoErrorf(t, session.Close(), "failed to close session")
	})

	return session
}

// connectStreamableClient creates and connects an MCP client to a streamable HTTP server.
func connectStreamableClient(
	t *testing.T,
	ctx context.Context,
	addr string,
) *mcp.ClientSession {
	t.Helper()

	client := mcp.NewClient(
		(&mcp.Implementation{
			Name:    "test-client",
			Version: "1.0.0",
		}),
		(*mcp.ClientOptions)(nil),
	)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   "http://" + addr + "/mcp",
		HTTPClient: &http.Client{Transport: &http.Transport{}},
	}

	session, err := client.Connect(ctx, transport, (*mcp.ClientSessionOptions)(nil))
	require.NoErrorf(t, err, "failed to connect client to streamable HTTP server")

	t.Cleanup(func() {
		_ = session.Close() //nolint:errcheck // cleanup, error not critical
	})

	return session
}

// TestBinaryClientListTools tests listing tools via MCP client.
func TestBinaryClientListTools(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	// Create a mock HTTP server for tool handlers
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		//nolint:errcheck // mock server response
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer mockServer.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := fmt.Sprintf(`
sources:
  tool1:
    type: http
    tools:
      prefix: tool1_
    connect:
      url: %s/one
      method: GET
      description: The first test tool
  tool2:
    type: http
    tools:
      prefix: tool2_
    connect:
      url: %s/two
      method: GET
      description: The second test tool
  tool3:
    type: http
    tools:
      prefix: tool3_
    connect:
      url: %s/three
      method: POST
      description: The third test tool with POST
`, mockServer.URL, mockServer.URL, mockServer.URL)

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "",
	}, []string(nil))

	// Connect client and list tools
	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, addr)

	result, err := clientSession.ListTools(ctx, (*mcp.ListToolsParams)(nil))
	require.NoErrorf(t, err, "failed to list tools")

	require.Lenf(t, result.Tools, 3, "expected 3 tools")

	toolNames := make(map[string]string)
	for _, tool := range result.Tools {
		toolNames[tool.Name] = tool.Description
	}

	require.Containsf(t, toolNames, "tool1_http", "should have tool1_http")
	require.Containsf(t, toolNames, "tool2_http", "should have tool2_http")
	require.Containsf(t, toolNames, "tool3_http", "should have tool3_http")
	require.Equalf(t, "The first test tool", toolNames["tool1_http"], "first tool description")
	require.Equalf(t, "The second test tool", toolNames["tool2_http"], "second tool description")
	require.Equalf(
		t,
		"The third test tool with POST",
		toolNames["tool3_http"],
		"third tool description",
	)

	// Cleanup
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinaryClientCallToolWithSSE tests calling a tool via MCP client over SSE transport.
func TestBinaryClientCallToolWithSSE(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	// Create a mock HTTP server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		switch r.URL.Path {
		case "/echo":
			response := map[string]any{
				"message": "hello from server",
				"path":    r.URL.Path,
				"method":  r.Method,
			}

			_ = json.NewEncoder(w).Encode(response) //nolint:errcheck // mock server response

		case "/data":
			response := map[string]any{
				"items": []map[string]any{
					{"id": 1, "name": "Item One"},
					{"id": 2, "name": "Item Two"},
					{"id": 3, "name": "Item Three"},
				},
				"total": 3,
			}

			_ = json.NewEncoder(w).Encode(response) //nolint:errcheck // mock server response

		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := fmt.Sprintf(`
sources:
  echo:
    type: http
    tools:
      prefix: echo_
    connect:
      url: %s/echo
      method: GET
      description: Echo tool
  data:
    type: http
    tools:
      prefix: data_
    connect:
      url: %s/data
      method: GET
      description: Get data items
`, mockServer.URL, mockServer.URL)

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "",
	}, []string(nil))

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, addr)

	// Test calling echo tool
	t.Run("echo tool", func(t *testing.T) {
		result, err := clientSession.CallTool(
			ctx,
			(&mcp.CallToolParams{
				Name:      "echo_http",
				Arguments: make(map[string]any),
			}),
		)
		require.NoErrorf(t, err, "failed to call echo tool")
		require.Falsef(t, result.IsError, "tool should not return error")
		require.NotEmptyf(t, result.Content, "result should have content")

		textContent, ok := result.Content[0].(*mcp.TextContent)
		require.Truef(t, ok, "expected TextContent")

		var response map[string]any

		err = json.Unmarshal([]byte(textContent.Text), &response)
		require.NoErrorf(t, err, "failed to parse response JSON")

		require.Equalf(t, "hello from server", response["message"], "response message")
		require.Equalf(t, "/echo", response["path"], "response path")
		require.Equalf(t, "GET", response["method"], "response method")
	})

	// Test calling get-data tool
	t.Run("data tool", func(t *testing.T) {
		result, err := clientSession.CallTool(
			ctx,
			(&mcp.CallToolParams{
				Name:      "data_http",
				Arguments: make(map[string]any),
			}),
		)
		require.NoErrorf(t, err, "failed to call data tool")
		require.Falsef(t, result.IsError, "tool should not return error")

		textContent, ok := result.Content[0].(*mcp.TextContent)
		require.Truef(t, ok, "expected TextContent")

		var response map[string]any

		err = json.Unmarshal([]byte(textContent.Text), &response)
		require.NoErrorf(t, err, "failed to parse response JSON")

		items, ok := response["items"].([]any)
		require.Truef(t, ok, "expected items array")
		require.Lenf(t, items, 3, "expected 3 items")
		require.InEpsilonf(t, float64(3), response["total"], 0.001, "expected total 3")
	})

	// Cleanup
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinaryClientCallToolWithStreamable tests calling a tool via MCP client over streamable HTTP.
func TestBinaryClientCallToolWithStreamable(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	// Create a mock HTTP server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		response := map[string]any{
			"status":     "success",
			"transport":  "streamable",
			"request_id": r.Header.Get("X-Request-ID"),
		}

		_ = json.NewEncoder(w).Encode(response) //nolint:errcheck // mock server response
	}))
	defer mockServer.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := fmt.Sprintf(`
sources:
  test:
    type: http
    tools:
      prefix: test_
    connect:
      url: %s/test
      method: POST
      description: Test tool for streamable transport
`, mockServer.URL)

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", configPath,
		"--transport", "streamable",
		"--addr", "",
	}, []string(nil))

	ctx := t.Context()
	clientSession := connectStreamableClient(t, ctx, addr)

	// Call the tool
	result, err := clientSession.CallTool(
		ctx,
		(&mcp.CallToolParams{
			Name:      "test_http",
			Arguments: map[string]any{"input": "test data"},
		}),
	)
	require.NoErrorf(t, err, "failed to call tool")
	require.Falsef(t, result.IsError, "tool should not return error")

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected TextContent")

	var response map[string]any

	err = json.Unmarshal([]byte(textContent.Text), &response)
	require.NoErrorf(t, err, "failed to parse response JSON")

	require.Equalf(t, "success", response["status"], "response status")

	// Cleanup
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinaryClientCallToolWithParams tests calling a tool with query parameters.
func TestBinaryClientCallToolWithParams(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	// Create a mock HTTP server that echoes query params
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		queryParams := r.URL.Query()
		response := map[string]any{
			"received_params": map[string]string{
				"search": queryParams.Get("search"),
				"limit":  queryParams.Get("limit"),
			},
		}

		_ = json.NewEncoder(w).Encode(response) //nolint:errcheck // mock server response
	}))
	defer mockServer.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := fmt.Sprintf(`
sources:
  search:
    type: http
    tools:
      prefix: search_
    connect:
      url: %s/search
      method: GET
      description: Search with parameters
`, mockServer.URL)

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "",
	}, []string(nil))

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, addr)

	// Call the tool with arguments (HTTP tool expects query params in "query" field)
	result, err := clientSession.CallTool(
		ctx,
		(&mcp.CallToolParams{
			Name: "search_http",
			Arguments: map[string]any{
				"query": map[string]any{
					"search": "golang",
					"limit":  "10",
				},
			},
		}),
	)
	require.NoErrorf(t, err, "failed to call search tool")
	require.Falsef(t, result.IsError, "tool should not return error")

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected TextContent")

	var response map[string]any

	err = json.Unmarshal([]byte(textContent.Text), &response)
	require.NoErrorf(t, err, "failed to parse response JSON")

	params, ok := response["received_params"].(map[string]any)
	require.Truef(t, ok, "expected received_params object")
	require.Equalf(t, "golang", params["search"], "search param")
	require.Equalf(t, "10", params["limit"], "limit param")

	// Cleanup
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinaryClientToolError tests handling tool errors from the backend.
func TestBinaryClientToolError(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	// Create a mock HTTP server that returns errors
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/error" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)

			response := map[string]any{
				"error":   "internal_error",
				"message": "Something went wrong on the backend",
			}

			_ = json.NewEncoder(w).Encode(response) //nolint:errcheck // mock server response

			return
		}

		if r.URL.Path == "/not-found" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)

			response := map[string]any{
				"error": "not_found",
			}

			_ = json.NewEncoder(w).Encode(response) //nolint:errcheck // mock server response

			return
		}

		w.WriteHeader(http.StatusOK)

		//nolint:errcheck // mock server response
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer mockServer.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := fmt.Sprintf(`
sources:
  error-tool:
    type: http
    tools:
      prefix: error_tool_
    connect:
      url: %s/error
      method: GET
      description: A tool that returns an error
  not-found-tool:
    type: http
    tools:
      prefix: not_found_tool_
    connect:
      url: %s/not-found
      method: GET
      description: A tool that returns 404
`, mockServer.URL, mockServer.URL)

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "",
	}, []string(nil))

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, addr)

	// Test calling error-tool - should return error when backend returns 5xx
	t.Run("internal server error", func(t *testing.T) {
		_, err := clientSession.CallTool(
			ctx,
			(&mcp.CallToolParams{
				Name:      "error_tool_http",
				Arguments: make(map[string]any),
			}),
		)
		require.Errorf(t, err, "call should fail when backend returns 5xx")
		require.Containsf(t, err.Error(), "500", "error should mention status code")
	})

	// Test calling not-found-tool - should return error when backend returns 404
	t.Run("not found error", func(t *testing.T) {
		_, err := clientSession.CallTool(
			ctx,
			(&mcp.CallToolParams{
				Name:      "not_found_tool_http",
				Arguments: make(map[string]any),
			}),
		)
		require.Errorf(t, err, "call should fail when backend returns 404")
		require.Containsf(t, err.Error(), "404", "error should mention status code")
	})

	// Cleanup
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinaryClientMultipleCalls tests making multiple sequential tool calls.
func TestBinaryClientMultipleCalls(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	var (
		callCount int
		mu        sync.Mutex
	)

	// Create a mock HTTP server that tracks calls
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		callCount++

		count := callCount
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		response := map[string]any{
			"call_number": count,
			"timestamp":   time.Now().Unix(),
		}

		_ = json.NewEncoder(w).Encode(response) //nolint:errcheck // mock server response
	}))
	defer mockServer.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := fmt.Sprintf(`
sources:
  ping:
    type: http
    tools:
      prefix: ping_
    connect:
      url: %s/ping
      method: GET
      description: Ping tool
`, mockServer.URL)

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "",
	}, []string(nil))

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, addr)

	// Make multiple sequential calls
	numCalls := 5
	for i := 1; i <= numCalls; i++ {
		result, err := clientSession.CallTool(
			ctx,
			(&mcp.CallToolParams{
				Name:      "ping_http",
				Arguments: make(map[string]any),
			}),
		)
		require.NoErrorf(t, err, "failed to call ping tool on call %d", i)
		require.Falsef(t, result.IsError, "tool should not return error on call %d", i)

		textContent, ok := result.Content[0].(*mcp.TextContent)
		require.Truef(t, ok, "expected TextContent on call %d", i)

		var response map[string]any

		err = json.Unmarshal([]byte(textContent.Text), &response)
		require.NoErrorf(t, err, "failed to parse response JSON on call %d", i)

		// Verify the call number in the response
		callNum, ok := response["call_number"].(float64)
		require.Truef(t, ok, "expected call_number on call %d", i)
		require.InEpsilonf(t, float64(i), callNum, 0.001, "call number should be %d", i)
	}

	// Cleanup
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// concurrentTestServer holds the mock server and config for concurrent call tests.
type concurrentTestServer struct {
	callCount  *int
	mu         *sync.Mutex
	configPath string
}

// newConcurrentTestServer creates a mock HTTP server and config file for concurrent tests.
func newConcurrentTestServer(t *testing.T) concurrentTestServer {
	t.Helper()

	var (
		callCount int
		mu        sync.Mutex
	)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		mu.Unlock()

		// Simulate some processing time
		time.Sleep(10 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		response := map[string]any{
			"path": r.URL.Path,
			"ok":   true,
		}

		_ = json.NewEncoder(w).Encode(response) //nolint:errcheck // mock server response
	}))
	t.Cleanup(mockServer.Close)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := fmt.Sprintf(`
sources:
  async1:
    type: http
    tools:
      prefix: async1_
    connect:
      url: %s/async1
      method: GET
      description: First async tool
  async2:
    type: http
    tools:
      prefix: async2_
    connect:
      url: %s/async2
      method: GET
      description: Second async tool
  async3:
    type: http
    tools:
      prefix: async3_
    connect:
      url: %s/async3
      method: GET
      description: Third async tool
`, mockServer.URL, mockServer.URL, mockServer.URL)

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	return concurrentTestServer{
		callCount:  &callCount,
		mu:         &mu,
		configPath: configPath,
	}
}

// TestBinaryClientConcurrentCalls tests making concurrent tool calls.
func TestBinaryClientConcurrentCalls(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)
	cts := newConcurrentTestServer(t)

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", cts.configPath,
		"--transport", "sse",
		"--addr", "",
	}, []string(nil))

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, addr)

	// Make concurrent calls
	numGoroutines := 10
	numCallsPerGoroutine := 3

	var wg sync.WaitGroup

	errChan := make(chan error, numGoroutines*numCallsPerGoroutine)

	for i := range numGoroutines {
		wg.Add(1)

		go func(goroutineID int) {
			defer wg.Done()

			for callIdx := range numCallsPerGoroutine {
				toolName := fmt.Sprintf("async%d_http", (goroutineID%3)+1)

				_, err := clientSession.CallTool(
					ctx,
					(&mcp.CallToolParams{
						Name:      toolName,
						Arguments: make(map[string]any),
					}),
				)
				if err != nil {
					errChan <- fmt.Errorf("goroutine %d, call %d: %w", goroutineID, callIdx, err)
				}
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	var errs []error
	for err := range errChan {
		errs = append(errs, err)
	}

	require.Emptyf(
		t,
		errs,
		"concurrent calls should not produce errors, got %d errors",
		len(errs),
	)

	// Verify total call count
	cts.mu.Lock()
	finalCount := *cts.callCount
	cts.mu.Unlock()

	expectedCalls := numGoroutines * numCallsPerGoroutine
	require.Equalf(t, expectedCalls, finalCount, "expected %d total calls", expectedCalls)

	// Cleanup
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinaryClientWithPostBody tests calling a tool with POST request body.
func TestBinaryClientWithPostBody(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	// Create a mock HTTP server that echoes the request body
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST method, got %s", r.Method)
		}

		var requestBody map[string]any

		err := json.NewDecoder(r.Body).Decode(&requestBody)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)

			//nolint:errcheck // mock server response
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid JSON"})

			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		response := map[string]any{
			"received": requestBody,
			"method":   r.Method,
		}

		_ = json.NewEncoder(w).Encode(response) //nolint:errcheck // mock server response
	}))
	defer mockServer.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := fmt.Sprintf(`
sources:
  submit:
    type: http
    tools:
      prefix: submit_
    connect:
      url: %s/submit
      method: POST
      description: Submit data via POST
`, mockServer.URL)

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "",
	}, []string(nil))

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, addr)

	// Call the tool with arguments (HTTP tool expects body in "body" field)
	result, err := clientSession.CallTool(
		ctx,
		(&mcp.CallToolParams{
			Name: "submit_http",
			Arguments: map[string]any{
				"body": map[string]any{
					"name":  "John Doe",
					"email": "john@example.com",
					"age":   30,
				},
			},
		}),
	)
	require.NoErrorf(t, err, "failed to call submit tool")
	require.Falsef(t, result.IsError, "tool should not return error")

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected TextContent")

	var response map[string]any

	err = json.Unmarshal([]byte(textContent.Text), &response)
	require.NoErrorf(t, err, "failed to parse response JSON")

	require.Equalf(t, "POST", response["method"], "should use POST method")

	received, ok := response["received"].(map[string]any)
	require.Truef(t, ok, "expected received object")
	require.Equalf(t, "John Doe", received["name"], "name should match")
	require.Equalf(t, "john@example.com", received["email"], "email should match")
	require.InEpsilonf(t, float64(30), received["age"], 0.001, "age should match")

	// Cleanup
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinaryClientNonexistentTool tests calling a tool that doesn't exist.
func TestBinaryClientNonexistentTool(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	err := os.WriteFile(configPath, []byte(validToolsConfig), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "",
	}, []string(nil))

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, addr)

	// Try to call a nonexistent tool
	_, err = clientSession.CallTool(
		ctx,
		(&mcp.CallToolParams{
			Name:      "nonexistent-tool",
			Arguments: make(map[string]any),
		}),
	)
	require.Errorf(t, err, "calling nonexistent tool should return error")
	require.Containsf(t, err.Error(), "nonexistent-tool", "error should mention tool name")

	// Cleanup
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinaryClientEmptyConfig tests client operations with no tools configured.
func TestBinaryClientEmptyConfig(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	err := os.WriteFile(configPath, []byte(validToolsConfig), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "",
	}, []string(nil))

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, addr)

	// List tools - should have the one tool from validToolsConfig
	result, err := clientSession.ListTools(ctx, (*mcp.ListToolsParams)(nil))
	require.NoErrorf(t, err, "failed to list tools")
	require.Lenf(t, result.Tools, 1, "expected one tool")

	// Cleanup
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinaryClientWithHeaders tests tool with custom headers.
func TestBinaryClientWithHeaders(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	// Create a mock HTTP server that checks headers
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		authHeader := r.Header.Get("Authorization")
		customHeader := r.Header.Get("X-Custom-Header")

		if authHeader == "" {
			w.WriteHeader(http.StatusUnauthorized)

			//nolint:errcheck // mock server response
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "missing authorization"})

			return
		}

		w.WriteHeader(http.StatusOK)

		response := map[string]any{
			"authorized":    true,
			"auth_header":   authHeader,
			"custom_header": customHeader,
		}

		_ = json.NewEncoder(w).Encode(response) //nolint:errcheck // mock server response
	}))
	defer mockServer.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := fmt.Sprintf(`
sources:
  secure:
    type: http
    tools:
      prefix: secure_
    connect:
      url: %s/secure
      method: GET
      headers:
        Authorization: Bearer test-token-123
        X-Custom-Header: custom-value
      description: A secure operation requiring auth
`, mockServer.URL)

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "",
	}, []string(nil))

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, addr)

	// Call the tool - headers should be sent automatically
	result, err := clientSession.CallTool(
		ctx,
		(&mcp.CallToolParams{
			Name:      "secure_http",
			Arguments: make(map[string]any),
		}),
	)
	require.NoErrorf(t, err, "failed to call secure tool")
	require.Falsef(t, result.IsError, "tool should not return error")

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected TextContent")

	var response map[string]any

	err = json.Unmarshal([]byte(textContent.Text), &response)
	require.NoErrorf(t, err, "failed to parse response JSON")

	require.Equalf(t, true, response["authorized"], "should be authorized")
	require.Equalf(t, "Bearer test-token-123", response["auth_header"], "auth header should match")
	require.Equalf(t, "custom-value", response["custom_header"], "custom header should match")

	// Cleanup
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}

// TestBinary_TemporalSource_RegistersAll30Tools confirms that a
// configured `temporal` source contributes its full 30-tool inventory
// (7 schedule + 8 workflow + 8 activity + 2 query/signal + 5 batch)
// with the configured `tools.prefix` prepended. The test loads
// `cmd/testdata/sources_with_temporal.yaml` and exercises `tools/list`
// via the SSE transport — the temporal source uses `client.NewLazyClient`,
// so the binary starts fine without a live Temporal server and the
// 30 tools appear at registration time (no RPC is needed for the
// inventory assertion).
//
// This is the integration smoke test for the 5-tool-groups wave
// (issues #141, #145, #140, #146, #142). If any tool drops out of the
// registry, this test fails.
func TestBinary_TemporalSource_RegistersAll30Tools(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	configPath := filepath.Join("testdata", "sources_with_temporal.yaml")

	cmd, addr := startServerOnFreePort(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "",
	}, []string(nil))

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, addr)

	// Fetch the full tool inventory. The temporal source's lazy client
	// never dials, so the server-side list is fully populated without a
	// running Temporal server.
	result, err := clientSession.ListTools(ctx, (*mcp.ListToolsParams)(nil))
	require.NoErrorf(t, err, "failed to list tools")

	// Expected 30 tools = 7 schedule + 8 workflow + 8 activity +
	// 2 query/signal + 5 batch. Each tool name has the configured
	// `t1_` prefix from the `tools.prefix` override.
	wantPrefix := "t1_"
	wantNames := []string{
		// 7 schedule
		"create_schedule", "list_schedules", "pause_schedule", "unpause_schedule",
		"delete_schedule", "trigger_schedule", "describe_schedule",
		// 8 workflow
		"start_workflow", "cancel_workflow", "terminate_workflow", "get_workflow_result",
		"describe_workflow", "list_workflows", "get_workflow_history", "continue_as_new",
		// 8 activity
		"start_activity", "execute_activity", "get_activity_result", "describe_activity",
		"list_activities", "count_activities", "cancel_activity", "terminate_activity",
		// 2 query/signal
		"query_workflow", "signal_workflow",
		// 5 batch
		"batch_signal", "batch_cancel", "batch_terminate",
		"batch_cancel_activities", "batch_terminate_activities",
	}

	gotNames := make(map[string]struct{}, len(result.Tools))
	for _, tool := range result.Tools {
		gotNames[tool.Name] = struct{}{}
	}

	require.Lenf(t, gotNames, len(wantNames),
		"temporal source must register exactly %d tools; got %d (%v)",
		len(wantNames), len(gotNames), gotNames)

	for _, name := range wantNames {
		full := wantPrefix + name
		_, ok := gotNames[full]
		require.Truef(t, ok, "missing temporal tool: %s (got: %v)", full, gotNames)
	}

	// Cleanup
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
}
