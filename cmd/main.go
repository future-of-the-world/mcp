// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package main implements the entry point for the MCP (Model Context Protocol) server.
// It provides a configurable server that can expose various tools via MCP transports
// (stdio, SSE, or streamable HTTP). Sources are loaded from a YAML or JSON
// configuration file and applied to the server at startup via the source
// dispatcher, which connects each source and registers its tools.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.amidman.dev/mcp/shell"
	"go.amidman.dev/mcp/source"
	"go.amidman.dev/mcp/tool"
)

const (
	// transportStdio identifies the stdio transport mode.
	transportStdio = "stdio"
	// transportSSE identifies the SSE (Server-Sent Events) transport mode.
	transportSSE = "sse"
	// transportStreamable identifies the streamable HTTP transport mode.
	transportStreamable = "streamable"

	// readHeaderTimeout is the maximum duration for reading HTTP request headers.
	readHeaderTimeout = 30 * time.Second
)

// Config holds the top-level configuration for the MCP server: its identity
// (name/title/version), the list of sources whose tools the server exposes,
// and the optional list of shell commands that must be ready before any
// source connects.
//
// Sources is populated from the top-level `sources:` map, where each key is the
// user-chosen source name and each value is a source.Source. The map key is
// assigned to source.Source.Name during decoding (see sourcesFromMap).
// Before is populated from the top-level `before:` sequence; each entry is a
// long-lived shell command whose TCP-readiness healthcheck must finish
// (passed, failed, or timed out) before source.Apply runs.
type Config struct {
	Name    string `json:"name"            yaml:"name"`
	Title   string `json:"title,omitempty" yaml:"title,omitempty"`
	Version string `json:"version"         yaml:"version"`

	Before []shell.BeforeCommand `json:"before,omitempty" yaml:"before,omitempty"`

	Sources []source.Source `json:"sources,omitempty" yaml:"sources,omitempty"`
}

// setDefaults applies default values for empty identity fields.
func (c *Config) setDefaults() {
	if c.Name == "" {
		c.Name = "mcp"
	}

	if c.Version == "" {
		c.Version = "1.0.0"
	}
}

// UnmarshalYAML implements yaml.Unmarshaler for Config. It decodes the
// top-level identity fields, the optional `before:` list (long-lived
// shell commands that must be ready before sources connect), and the
// `sources:` map, assigning each map key to the corresponding
// source.Source.Name.
func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	type raw struct {
		Name    string                   `yaml:"name"`
		Title   string                   `yaml:"title,omitempty"`
		Version string                   `yaml:"version"`
		Before  []shell.BeforeCommand    `yaml:"before,omitempty"`
		Sources map[string]source.Source `yaml:"sources"`
	}

	var parsed raw

	err := node.Decode(&parsed)
	if err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	c.Name = parsed.Name
	c.Title = parsed.Title
	c.Version = parsed.Version
	c.Before = parsed.Before
	c.Sources = sourcesFromMap(parsed.Sources)

	return nil
}

// UnmarshalJSON implements json.Unmarshaler for Config. It decodes the
// top-level identity fields, the optional `before:` list (long-lived
// shell commands that must be ready before sources connect), and the
// `sources:` map, assigning each map key to the corresponding
// source.Source.Name.
func (c *Config) UnmarshalJSON(data []byte) error {
	type raw struct {
		Name    string                   `json:"name"`
		Title   string                   `json:"title,omitempty"`
		Version string                   `json:"version"`
		Before  []shell.BeforeCommand    `json:"before,omitempty"`
		Sources map[string]source.Source `json:"sources"`
	}

	var parsed raw

	err := json.Unmarshal(data, &parsed)
	if err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	c.Name = parsed.Name
	c.Title = parsed.Title
	c.Version = parsed.Version
	c.Before = parsed.Before
	c.Sources = sourcesFromMap(parsed.Sources)

	return nil
}

// sourcesFromMap flattens a `sources:` map into a slice, assigning each map
// key to its Source.Name. A nil/empty map yields a nil slice. This mirrors
// the map-key-as-name assignment performed by source.LoadSources.
func sourcesFromMap(sources map[string]source.Source) []source.Source {
	if len(sources) == 0 {
		return nil
	}

	out := make([]source.Source, 0, len(sources))

	for name := range sources {
		src := sources[name]

		src.Name = name

		out = append(out, src)
	}

	return out
}

// NewConfig creates an empty Config. Sources are populated during YAML/JSON
// decoding; there is no longer a tool registry to construct.
func NewConfig() *Config {
	return &Config{}
}

// main is the entry point for the MCP server binary.
func main() {
	logger := slog.New(loghandler())

	configPath := flag.String(
		"config", "", "path to config file (yaml or json)",
	)
	transportFlag := flag.String(
		"transport", transportStdio,
		"transport type: stdio, sse, or streamable",
	)
	addrFlag := flag.String(
		"addr", ":8080",
		"address for HTTP transports",
	)

	flag.Parse()

	os.Exit(
		run(
			logger,
			*configPath,
			*transportFlag,
			*addrFlag,
		),
	)
}

// run initializes and starts the MCP server with the given configuration and transport settings.
// It returns an exit code: 0 for success, 1 for failure.
func run(logger *slog.Logger, configPath, transportFlag, addr string) int {
	ctx := context.Background()

	if configPath == "" {
		logger.ErrorContext(ctx, "--config flag is required")

		return 1
	}

	config, err := loadConfig(configPath)
	if err != nil {
		logger.ErrorContext(ctx, "failed to load config", "error", err)

		return 1
	}

	config.setDefaults()

	if len(config.Sources) == 0 {
		logger.ErrorContext(ctx, "no sources found in config")

		return 1
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Run the global "before" section before any source connects. Each
	// entry spawns a long-lived shell command; its healthcheck (if any)
	// is awaited by RunBefore, which only returns once every healthcheck
	// has finished (pass, fail, or timeout). Spawn failures and healthcheck
	// timeouts are logged at ERROR but never block server start — the
	// surviving sources still get to apply via the common path. The
	// server context is the same context that propagates SIGINT/SIGTERM
	// cancellation to the spawned children.
	shell.RunBefore(ctx, config.Before, tool.WithLogger(logger))

	srv := mcp.NewServer(
		&mcp.Implementation{
			Name:    config.Name,
			Title:   config.Title,
			Version: config.Version,
		},
		&mcp.ServerOptions{
			Logger: logger,
		},
	)

	// Apply owns the per-source fan-out, the prefix/remove middlewares,
	// the duplicate-name check, and the registration of the survivors.
	// Apply is tolerant of per-source failures: each failure is logged
	// inside Apply and the surviving sources' tools are still registered.
	// A non-nil error here means no source contributed any tool — refuse
	// to start a server that would have zero tools to offer.
	err = source.Apply(ctx, srv, config.Sources, tool.WithLogger(logger))
	if err != nil {
		logger.ErrorContext(ctx, "failed to apply sources",
			"configured", len(config.Sources),
			"error", err,
		)

		return 1
	}

	if len(config.Sources) == 0 {
		logger.ErrorContext(ctx, "no sources configured")

		return 1
	}

	logger.InfoContext(ctx, "starting MCP server",
		"sources", len(config.Sources),
		"transport", transportFlag,
	)

	switch transportFlag {
	case transportStdio:
		return runStdio(ctx, logger, srv)

	case transportSSE:
		return runSSE(ctx, logger, srv, addr)

	case transportStreamable:
		return runStreamable(ctx, logger, srv, addr)

	default:
		logger.ErrorContext(ctx, "unknown transport type", "transport", transportFlag)

		return 1
	}
}

// runTransport is the shared implementation of runStdio, runSSE, runStreamable.
// It runs srv.Run with the given transport and returns 1 on error, 0 on success.
func runTransport(ctx context.Context, logger *slog.Logger, srv *mcp.Server, t mcp.Transport) int {
	err := srv.Run(ctx, t)
	if err != nil {
		logger.ErrorContext(ctx, "server error", "error", err)

		return 1
	}

	return 0
}

// runStdio starts the MCP server using the stdio transport.
func runStdio(ctx context.Context, logger *slog.Logger, srv *mcp.Server) int {
	return runTransport(ctx, logger, srv, new(mcp.StdioTransport))
}

// handlerFactory is a function that creates an HTTP handler for MCP.
type handlerFactory func(srv *mcp.Server) http.Handler

// httpServerConfig holds configuration for running an MCP HTTP server.
type httpServerConfig struct {
	Logger     *slog.Logger
	Server     *mcp.Server
	Addr       string
	Factory    handlerFactory
	ServerType string
}

// runSSE starts the MCP server using the SSE (Server-Sent Events) transport.
func runSSE(ctx context.Context, logger *slog.Logger, srv *mcp.Server, addr string) int {
	return runHTTPServer(ctx, &httpServerConfig{
		Logger: logger,
		Server: srv,
		Addr:   addr,
		Factory: func(srv *mcp.Server) http.Handler {
			return mcp.NewSSEHandler(
				func(*http.Request) *mcp.Server { return srv },
				(*mcp.SSEOptions)(nil),
			)
		},
		ServerType: "SSE",
	})
}

// runStreamable starts the MCP server using the streamable HTTP transport.
func runStreamable(ctx context.Context, logger *slog.Logger, srv *mcp.Server, addr string) int {
	return runHTTPServer(ctx, &httpServerConfig{
		Logger: logger,
		Server: srv,
		Addr:   addr,
		Factory: func(srv *mcp.Server) http.Handler {
			return mcp.NewStreamableHTTPHandler(
				func(*http.Request) *mcp.Server { return srv },
				(*mcp.StreamableHTTPOptions)(nil),
			)
		},
		ServerType: "streamable HTTP",
	})
}

// runHTTPServer starts an HTTP server for MCP and blocks until the context is canceled or a server
// error occurs.
func runHTTPServer(ctx context.Context, cfg *httpServerConfig) int {
	logger := cfg.Logger

	handler := cfg.Factory(cfg.Server)

	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	// Create listener synchronously to get the actual bound address.
	listenConfig := net.ListenConfig{}

	listener, err := listenConfig.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		logger.ErrorContext(ctx,
			"server listen error",
			"type", cfg.ServerType,
			"error", err,
		)

		return 1
	}

	actualAddr := listener.Addr().String()

	logger.InfoContext(ctx,
		"server listening",
		"type", cfg.ServerType,
		"addr", actualAddr,
	)

	// Shutdown server when function returns to release the port
	defer func() {
		// Create shutdown context before defer to avoid contextcheck warning
		shutdownCtx, shutdownCancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			5*time.Second,
		)
		shutdownCancel()

		err := httpServer.Shutdown(shutdownCtx)
		if err != nil {
			logger.ErrorContext(ctx, "server shutdown error", "error", err)
		}
	}()

	errChan := make(chan error, 1)

	go func() {
		err := httpServer.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.InfoContext(ctx, "shutting down server", "type", cfg.ServerType)

		return 0

	case err := <-errChan:
		logger.ErrorContext(ctx,
			"server error",
			"type", cfg.ServerType,
			"error", err,
		)

		return 1
	}
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	config := NewConfig()

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		err = yaml.Unmarshal(data, config)
		if err != nil {
			return nil, fmt.Errorf("parse yaml config: %w", err)
		}

	case ".json":
		err = json.Unmarshal(data, config)
		if err != nil {
			return nil, fmt.Errorf("parse json config: %w", err)
		}

	default:
		return nil, fmt.Errorf("unsupported config format: %s (use .yaml, .yml, or .json)", ext)
	}

	return config, nil
}

// loghandler creates a slog.Handler configured from environment variables (LOG_JSON,
// LOG_ADD_SOURCE, LOG_LEVEL).
func loghandler() slog.Handler {
	isJSONEnvValue := os.Getenv("LOG_JSON")
	addSourceEnvValue := os.Getenv("LOG_ADD_SOURCE")

	//nolint:errcheck // error returns false, which is the desired default
	isJSON, _ := strconv.ParseBool(isJSONEnvValue)

	//nolint:errcheck // error returns false, which is the desired default
	addSource, _ := strconv.ParseBool(addSourceEnvValue)

	opts := &slog.HandlerOptions{
		AddSource: addSource,
		Level:     loglevel(),
	}
	out := os.Stderr

	if isJSON {
		return slog.NewJSONHandler(out, opts)
	}

	return slog.NewTextHandler(out, opts)
}

// loglevel returns the slog.Level configured via the LOG_LEVEL environment variable. Defaults to
// info.
func loglevel() slog.Level {
	loglevelEnvValue := os.Getenv("LOG_LEVEL")

	switch strings.ToLower(loglevelEnvValue) {
	case "error":
		return slog.LevelError

	case "warn":
		return slog.LevelWarn

	case "debug":
		return slog.LevelDebug

	case "disabled", "-1":
		return slog.Level(math.MaxInt)

	default:
		return slog.LevelInfo
	}
}
