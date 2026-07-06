// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package tool

import (
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// tracerName is the OTel instrumentation scope used when the caller has
// not provided an explicit tracer via WithTracer. It is the import path
// of this module so that spans produced by the MCP server are grouped
// under a single library scope.
const tracerName = "go.amidman.dev/mcp"

// Option mutates an Options value during construction. The functional
// options pattern lets callers override individual dependencies (logger,
// tracer) without having to construct and pass the full Options struct.
type Option func(*Options)

// Options holds the cross-cutting dependencies (logger, tracer) that
// flow from cmd/main.go through source.Apply into each per-type Connect.
// Fields are unexported: populate via WithLogger/WithTracer and read via
// the Logger() and Tracer() getters.
type Options struct {
	logger *slog.Logger
	tracer trace.Tracer
}

// NewOptions applies the given options to a fresh Options value. It
// always returns a non-nil *Options; if no options are provided, the
// returned value still yields working defaults from its getters.
func NewOptions(opts ...Option) *Options {
	target := &Options{}

	for _, apply := range opts {
		apply(target)
	}

	return target
}

// WithLogger overrides the default slog logger used by source-level
// code. Passing a nil logger is treated as "use the default" so that
// callers can pass an optional *slog.Logger without nil-checking first.
func WithLogger(logger *slog.Logger) Option {
	return func(o *Options) {
		o.logger = logger
	}
}

// WithTracer overrides the default OTel tracer used by source-level
// code. Passing a nil tracer is treated as "use the default" so that
// callers can pass an optional trace.Tracer without nil-checking first.
func WithTracer(tracer trace.Tracer) Option {
	return func(o *Options) {
		o.tracer = tracer
	}
}

// Logger returns the configured *slog.Logger, or slog.Default() if the
// receiver is nil or no logger was configured. The returned value is
// never nil so callers can chain slog calls without a nil check.
func (o *Options) Logger() *slog.Logger {
	if o == nil || o.logger == nil {
		return slog.Default()
	}

	return o.logger
}

// Tracer returns the configured trace.Tracer, or the default OTel
// tracer for this package's instrumentation scope if the receiver is
// nil or no tracer was configured. The returned value is never nil.
func (o *Options) Tracer() trace.Tracer {
	if o == nil || o.tracer == nil {
		return otel.GetTracerProvider().Tracer(tracerName)
	}

	return o.tracer
}
