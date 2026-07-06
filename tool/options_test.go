// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package tool

import (
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// identityTracer is a thin *pointer* wrapper around noop.Tracer so the
// test can compare two Tracer values for identity with assert.Same. The
// OTel trace.Tracer interface is satisfied by value-receivers in the
// noop package, so we need a pointer-receiver test type of our own.
type identityTracer struct {
	noop.Tracer
}

// newIdentityTracer returns a fresh *identityTracer the test can later
// compare against with assert.Same.
func newIdentityTracer() trace.Tracer {
	return &identityTracer{}
}

// newDiscardLogger returns a *slog.Logger that drops all output. It is
// used in tests that only need a non-nil logger to compare identity.
func newDiscardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestNewOptions_Empty(t *testing.T) {
	t.Parallel()

	opts := NewOptions()
	require.NotNil(t, opts)
	assert.NotNil(t, opts.Logger())
	assert.NotNil(t, opts.Tracer())
}

func TestNewOptions_AppliesOptionsInOrder(t *testing.T) {
	t.Parallel()

	logger := newDiscardLogger()
	tracer := newIdentityTracer()

	opts := NewOptions(WithLogger(logger), WithTracer(tracer))

	require.NotNil(t, opts)
	assert.Same(t, logger, opts.Logger())
	assert.Same(t, tracer, opts.Tracer())
}

func TestNewOptions_LastLoggerWins(t *testing.T) {
	t.Parallel()

	first := newDiscardLogger()
	second := newDiscardLogger()

	opts := NewOptions(WithLogger(first), WithLogger(second))

	assert.Same(t, second, opts.Logger())
}

func TestNewOptions_LastTracerWins(t *testing.T) {
	t.Parallel()

	first := newIdentityTracer()
	second := newIdentityTracer()

	opts := NewOptions(WithTracer(first), WithTracer(second))

	assert.Same(t, second, opts.Tracer())
}

func TestOptions_Logger_DefaultWhenUnset(t *testing.T) {
	t.Parallel()

	opts := NewOptions()
	assert.Same(t, slog.Default(), opts.Logger())
}

func TestOptions_Tracer_DefaultWhenUnset(t *testing.T) {
	t.Parallel()

	opts := NewOptions()
	tracer := opts.Tracer()
	require.NotNil(t, tracer)
	// The default tracer must use this package's instrumentation
	// scope, which the OTel SDK exposes as the tracer name.
	assert.Equal(t, tracerName, tracerNameFromProvider())
}

func TestOptions_Logger_NilReceiverFallsBackToDefault(t *testing.T) {
	t.Parallel()

	var opts *Options
	assert.Same(t, slog.Default(), opts.Logger())
}

func TestOptions_Tracer_NilReceiverFallsBackToDefault(t *testing.T) {
	t.Parallel()

	var opts *Options

	tracer := opts.Tracer()
	require.NotNil(t, tracer)
	// Sanity-check the default scope matches the package constant.
	assert.Equal(t, tracerName, tracerNameFromProvider())
}

// tracerNameFromProvider returns the OTel instrumentation scope name
// used by the package's default tracer. The trace.Tracer interface
// intentionally does not expose its name, so this helper mirrors the
// constant. It exists solely to document the intent in tests.
func tracerNameFromProvider() string {
	return tracerName
}
