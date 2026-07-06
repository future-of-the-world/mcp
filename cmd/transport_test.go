// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// syncedBuffer is a thread-safe writer for concurrent log reading.
type syncedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (sb *syncedBuffer) Write(data []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	written, err := sb.buf.Write(data)

	return written, fmt.Errorf("write log buffer: %w", err)
}

func (sb *syncedBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	return sb.buf.String()
}

// nopWriteCloser wraps an io.Writer to satisfy io.WriteCloser with a no-op Close.
type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

// getFreePort returns a free port address on localhost by binding to :0.
func getFreePort(t *testing.T) string {
	t.Helper()

	listenCfg := net.ListenConfig{}

	listener, err := listenCfg.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	err = listener.Close()
	if err != nil {
		t.Fatalf("failed to close listener: %v", err)
	}

	return fmt.Sprintf("127.0.0.1:%d", port)
}

// newTestMCPServer creates a minimal MCP server for testing.
func newTestMCPServer(logger *slog.Logger) *mcp.Server {
	return mcp.NewServer(
		(&mcp.Implementation{
			Name:    "test",
			Version: "1.0.0",
		}),
		(&mcp.ServerOptions{
			Logger: logger,
		}),
	)
}

// newTestLogger creates a logger backed by a syncedBuffer.
func newTestLogger() (*slog.Logger, *syncedBuffer) {
	var buf syncedBuffer

	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	return logger, &buf
}

// testHTTPTransportCase holds parameters for testing HTTP-based transports.
type testHTTPTransportCase struct {
	name        string
	transportFn func(ctx context.Context, logger *slog.Logger, srv *mcp.Server, addr string) int
	serverType  string
}

// TestRunHTTPServerStartupAndShutdown exercises SSE and streamable HTTP transports,
// verifying the server starts listening and shuts down cleanly when the context is canceled.
func TestRunHTTPServerStartupAndShutdown(t *testing.T) {
	t.Parallel()

	tests := []testHTTPTransportCase{
		{
			name:        "SSE",
			transportFn: runSSE,
			serverType:  "SSE",
		},
		{
			name:        "streamable",
			transportFn: runStreamable,
			serverType:  "streamable HTTP",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			addr := getFreePort(t)

			logger, logBuf := newTestLogger()

			srv := newTestMCPServer(logger)

			// The server-start timeout is generous on purpose: this test runs
			// in parallel with the rest of ./... (postgres integration tests
			// with Docker, the long-running OAuth poll tests, etc.), and on a
			// loaded CI worker the goroutine that creates the listener can
			// be starved past the original 3 s budget. The shutdown budget
			// is unchanged; only the start-up poll is widened.
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()

			resultCh := make(chan int, 1)

			go func() {
				resultCh <- testCase.transportFn(ctx, logger, srv, addr)
			}()

			require.Eventuallyf(t, func() bool {
				return strings.Contains(logBuf.String(), "server listening")
			},
				30*time.Second, 50*time.Millisecond,
				"expected %s server to start listening",
				testCase.name,
			)

			cancel()

			select {
			case result := <-resultCh:
				require.Equalf(t, 0, result, "expected clean shutdown (exit code 0)")

			case <-time.After(5 * time.Second):
				t.Fatal("test timed out waiting for shutdown")
			}

			logs := logBuf.String()
			require.Containsf(t, logs, "shutting down server", "expected shutdown log")
			require.Containsf(t, logs, testCase.serverType,
				"expected %s server type in logs",
				testCase.serverType,
			)
		})
	}
}

// TestRunHTTPServerListenError exercises the error path in runHTTPServer when the address
// is already in use.
func TestRunHTTPServerListenError(t *testing.T) {
	t.Parallel()

	// Occupy a port so the server can't bind to it.
	listenCfg := net.ListenConfig{}

	listener, err := listenCfg.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() {
		require.NoError(t, listener.Close())
	}()

	blockedAddr := listener.Addr().String()

	logger, logBuf := newTestLogger()

	srv := newTestMCPServer(logger)

	result := runHTTPServer(t.Context(), &httpServerConfig{
		Logger: logger,
		Server: srv,
		Addr:   blockedAddr,
		Factory: func(srv *mcp.Server) http.Handler {
			return mcp.NewSSEHandler(
				func(*http.Request) *mcp.Server { return srv },
				(*mcp.SSEOptions)(nil),
			)
		},
		ServerType: "SSE",
	})

	require.Equalf(t, 1, result, "expected failure when address is in use")

	logs := logBuf.String()
	require.Containsf(t, logs, "server listen error", "expected listen error log")
}

// TestRunWithUnknownTransport verifies that run() returns an error for an unknown transport type.
func TestRunWithUnknownTransport(t *testing.T) {
	t.Parallel()

	server := createSimpleTestServer(t, `{"status":"ok"}`)

	configPath := writeTestConfig(t, formatRunTestConfig(server.URL))

	var logBuf syncedBuffer

	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	result := run(logger, configPath, "grpc", ":8080")
	require.Equalf(t, 1, result, "expected failure with unknown transport")

	logs := logBuf.String()
	require.Containsf(t, logs, "unknown transport type", "expected unknown transport log")
	require.Containsf(t, logs, "grpc", "expected transport name in logs")
}

// TestRunStdioWithCanceledContext exercises the runTransport path used by runStdio
// by canceling the context immediately, causing the server to exit with an error.
//
// It uses mcp.IOTransport with a blocking io.Pipe reader rather than mcp.StdioTransport.
// StdioTransport reads from the real os.Stdin/os.Stdout, which (a) races with the
// context cancellation when stdin EOFs before the select in mcp.Server.Run fires,
// and (b) closes the process's actual stdin/stdout via ss.Close() as a side effect.
func TestRunStdioWithCanceledContext(t *testing.T) {
	t.Parallel()

	logger, logBuf := newTestLogger()

	srv := newTestMCPServer(logger)

	// Pipe reader blocks until either a write happens or the writer is closed
	// (the writer is closed in t.Cleanup). This keeps the read loop alive long
	// enough for the context cancellation to win the select in mcp.Server.Run.
	pipeReader, pipeWriter := io.Pipe()
	t.Cleanup(func() { pipeWriter.CloseWithError(error(nil)) })

	transport := &mcp.IOTransport{
		Reader: pipeReader,
		Writer: nopWriteCloser{io.Discard},
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // Cancel immediately.

	result := runTransport(ctx, logger, srv, transport)
	// runTransport returns 1 when srv.Run fails (context canceled before server could run).
	require.Equalf(t, 1, result, "expected failure with canceled context")

	logs := logBuf.String()
	require.Containsf(t, logs, "server error", "expected server error log")
}
