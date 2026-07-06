// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package source

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.amidman.dev/mcp/english"
	"go.amidman.dev/mcp/fs"
	"go.amidman.dev/mcp/github"
	"go.amidman.dev/mcp/gitlab"
	"go.amidman.dev/mcp/http"
	"go.amidman.dev/mcp/postgres"
	"go.amidman.dev/mcp/proxy"
	"go.amidman.dev/mcp/sequentialthinking"
	"go.amidman.dev/mcp/shell"
	"go.amidman.dev/mcp/temporal"
	"go.amidman.dev/mcp/tool"
	"go.amidman.dev/mcp/tracker"
	"go.amidman.dev/mcp/websearch"
	"go.amidman.dev/mcp/woodpecker"
)

// Apply connects every source in parallel, runs the prefix and remove
// middlewares over the returned tools, validates that no two tools end
// up with the same name, and registers the survivors with server.
//
// Apply is tolerant of per-source failures: every Connect error, unknown
// source type, or duplicate tool name is logged at error level (with the
// source name, type, and underlying error) and the surviving sources'
// tools are still registered. Apply returns a non-nil error only when
// no source contributed any tool — the signal cmd/main.go uses to
// refuse to start a server with zero tools to offer. The MCP Go SDK
// silently replaces a tool of the same name on AddTool; this function
// surfaces that situation as a logged config error rather than
// aborting the apply.
//
// The per-type Connect calls fan out via sync.WaitGroup; the validation
// and registration phases run sequentially. Each Connect writes to a
// distinct indexed slot in the results slice, so the goroutines don't
// need locks for the write half.
func Apply(ctx context.Context, server *mcp.Server, sources []Source, opts ...tool.Option) error {
	logger := tool.NewOptions(opts...).Logger()

	results := fanOutConnects(ctx, sources, opts)

	planned, errs := planTools(ctx, logger, results)
	for _, perr := range errs {
		logger.ErrorContext(ctx, "source apply failed",
			"name", perr.src.Name,
			"type", perr.src.Type,
			"error", perr.err,
		)
	}

	if len(planned) == 0 && len(errs) > 0 {
		return joinPlanErrors(errs)
	}

	registerPlanned(server, planned)

	return nil
}

// joinPlanErrors unwraps every planError to its underlying error and
// returns the errors.Join of the slice. It exists so Apply can return
// a flat error chain that mirrors the previous "joined error" return
// shape — callers and existing tests that assert on error messages
// see the same wrapping as before.
func joinPlanErrors(errs []planError) error {
	wrapped := make([]error, len(errs))
	for i, e := range errs {
		wrapped[i] = e.err
	}

	return errors.Join(wrapped...)
}

// dispatchResult is the per-source Connect outcome. It holds a pointer
// to the source (to avoid copying the 80-byte struct on every result
// write) and the tool list and error from the Connect call.
type dispatchResult struct {
	src   *Source
	tools []tool.Tool
	err   error
}

// fanOutConnects runs every source's Connect in parallel and returns
// the outcomes in source order. The validation and registration phases
// read the slice sequentially after this returns.
func fanOutConnects(ctx context.Context, sources []Source, opts []tool.Option) []dispatchResult {
	results := make([]dispatchResult, len(sources))

	var wg sync.WaitGroup

	for i, src := range sources {
		wg.Go(func() {
			resp, err := dispatch(ctx, &src, opts...)

			results[i] = dispatchResult{src: &src, tools: resp.Tools, err: err}
		})
	}

	wg.Wait()

	return results
}

// planError pairs a per-source error with the source it came from so
// the caller (Apply) can log the source name and type as structured
// fields without re-parsing the error message. The underlying err
// already carries the source name in its text (via the "source %q:"
// wrappers in dispatch and addPlanned), so callers that just want a
// human-readable string can unwrap to err.Error().
type planError struct {
	src *Source
	err error
}

// planTools runs the read-only, prefix, and remove middlewares over
// each source's tools and validates that no two tools end up with the
// same name. It returns the planned tools (in source order) and every
// error found, each tagged with the source that produced it. The
// errors slice may be non-empty alongside a non-empty planned slice —
// callers that want to fail only on zero survivors must check planned
// first.
//
// When a source sets Tools.ReadOnly and the per-source filtered count
// after the full middleware chain is zero, planTools emits a warning
// log entry identifying the source. The warning is non-fatal: the
// apply path continues, and the warning surfaces a likely config
// mistake (the user asked for read-only tools but the per-type Connect
// did not produce any) without aborting server startup.
func planTools(
	ctx context.Context, logger *slog.Logger, results []dispatchResult,
) ([]tool.Tool, []planError) {
	seen := make(map[string]string)

	var planned []tool.Tool

	var errs []planError

	for _, res := range results {
		if res.err != nil {
			errs = append(errs, planError{src: res.src, err: res.err})

			continue
		}

		filtered := filterSourceTools(res.tools, res.src.Tools)
		warnIfReadOnlyEmpty(ctx, logger, res.src, filtered)

		if len(res.src.Tools.EnableOnly) > 0 {
			filtered = applyEnableOnly(filtered, res.src.Tools.EnableOnly)
		}

		for _, item := range filtered {
			err := addPlanned(res.src.Name, item, seen, &planned)
			if err != nil {
				errs = append(errs, planError{src: res.src, err: err})
			}
		}
	}

	return planned, errs
}

// filterSourceTools runs the read-only, prefix, and remove middlewares
// in that order against a single source's tools. It exists as a
// separate helper so planTools stays under the gocognit limit; the
// order is documented on planTools above.
func filterSourceTools(tools []tool.Tool, cfg ToolsConfig) []tool.Tool {
	readOnly := applyReadOnly(tools, cfg.ReadOnly)
	prefixed := applyPrefix(readOnly, cfg.Prefix)
	filtered := applyRemove(prefixed, cfg.Remove)

	return filtered
}

// warnIfReadOnlyEmpty logs a WarnContext entry when a source requested
// the read-only filter and zero tools survived the full middleware
// chain. The message identifies the source by name, records its type,
// and includes the read_only flag value so operators can grep for the
// configuration intent in a single search. The log is the only signal:
// the apply path continues and registers zero tools for the source.
func warnIfReadOnlyEmpty(
	ctx context.Context, logger *slog.Logger, src *Source, filtered []tool.Tool,
) {
	if !src.Tools.ReadOnly {
		return
	}

	if len(filtered) > 0 {
		return
	}

	logger.WarnContext(ctx, "source applied with zero read-only tools",
		"name", src.Name,
		"type", src.Type,
		"read_only", true,
	)
}

// addPlanned validates a single tool against the duplicate-name set and
// appends it to planned if it is valid. It returns a non-nil error for
// empty names and duplicate names; the seen map and planned slice are
// only mutated on success.
func addPlanned(
	sourceName string, item tool.Tool, seen map[string]string, planned *[]tool.Tool,
) error {
	name := item.Name
	if name == "" {
		return fmt.Errorf("source %q: tool has empty name", sourceName)
	}

	if prev, dup := seen[name]; dup {
		return fmt.Errorf(
			"source %q: duplicate tool name %q (already registered by source %q)",
			sourceName, name, prev)
	}

	seen[name] = sourceName

	*planned = append(*planned, item)

	return nil
}

// registerPlanned attaches every tool in planned to server via
// mcp.Server.AddTool, in source order. The MCP Go SDK mutates its
// internal tools map on AddTool without locking, so the loop is
// sequential on purpose — parallel registration would race the SDK.
func registerPlanned(server *mcp.Server, planned []tool.Tool) {
	for _, item := range planned {
		server.AddTool(item.Tool, item.Handler)
	}
}

// dispatch runs the per-type Connect for one source and wraps the
// result with the source name. It is the only place that knows the
// per-type switch; Apply fans it out concurrently over a batch of
// sources.
func dispatch(ctx context.Context, src *Source, opts ...tool.Option) (tool.Response, error) {
	var (
		resp tool.Response
		err  error
	)

	switch src.Type {
	case "postgres":
		resp, err = postgres.Connect(ctx, src.Connect, opts...)

	case "http":
		resp, err = http.Connect(ctx, src.Connect, opts...)

	case "proxy":
		resp, err = proxy.Connect(ctx, src.Connect, opts...)

	case "gitlab":
		resp, err = gitlab.Connect(ctx, src.Connect, opts...)

	case "tracker":
		resp, err = tracker.Connect(ctx, src.Connect, opts...)

	case "english":
		resp, err = english.Connect(ctx, src.Connect, opts...)

	case "github":
		resp, err = github.Connect(ctx, src.Connect, opts...)

	case "websearch":
		resp, err = websearch.Connect(ctx, src.Connect, opts...)

	case "woodpecker":
		resp, err = woodpecker.Connect(ctx, src.Connect, opts...)

	case "shell":
		resp, err = shell.Connect(ctx, src.Connect, opts...)

	case "fs":
		resp, err = fs.Connect(ctx, src.Connect, opts...)

	case "sequentialthinking":
		resp, err = sequentialthinking.Connect(ctx, src.Connect, opts...)

	case "temporal":
		resp, err = temporal.Connect(ctx, src.Connect, opts...)

	default:
		return tool.Response{}, fmt.Errorf("source %q: unknown type %q", src.Name, src.Type)
	}

	if err != nil {
		return tool.Response{}, fmt.Errorf("source %q: %w", src.Name, err)
	}

	return resp, nil
}
