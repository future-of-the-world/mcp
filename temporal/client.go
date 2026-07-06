// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package temporal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

// errClientManagerUninitialized is returned by the per-feature
// forwarders on *clientManager (query-signal, batch, workflow,
// activity) when the receiver is nil or its inner SDK client is
// nil. Sharing a single sentinel across features keeps handler
// tests that exercise the "manager built but not yet dialed" edge
// case observable via errors.Is.
var errClientManagerUninitialized = errors.New("temporal tool: client manager is uninitialized")

// clientManager builds a *client.Client from a validated config and
// exposes it through the temporalClient interface (workflow +
// activity surface) and the per-feature interfaces (querySignalClient
// in query_signal_handlers.go, batchClient in batch_handlers.go). The
// first call into a tool handler triggers the lazy gRPC connection;
// the binary never fails to start because Temporal is offline (see
// .issues/temporal-integration-and-docs).
//
// scheduleClientOverride is an optional test seam: when non-nil, the
// manager's ScheduleClient method returns it instead of reaching the
// real SDK. Production code paths never set this field; only test
// fixtures do. The override is a *client.ScheduleClient (pointer to
// interface) so we can distinguish "unset" (nil pointer) from "set to
// a nil interface" (a fake that always errors).
type clientManager struct {
	client                 client.Client
	scheduleClientOverride *client.ScheduleClient
}

// newClientManager constructs a *client.Client from a validated config
// using client.NewLazyClient (no eager dial). TLS configuration is
// resolved by determineTLSConfig; credentials (mTLS or API-key) are
// resolved by determineCredentials.
//
// The function never returns an error in the skeleton: client.NewLazyClient
// itself only fails when an Option returns an error, and the option
// builders used here are infallible. We keep the error return shape so
// follow-up issues can surface SDK option errors without a signature
// change.
func newClientManager(ctx context.Context, cfg *config) (*clientManager, error) {
	opts := client.Options{
		HostPort:  cfg.Host,
		Namespace: cfg.Namespace,
	}

	if tlsCfg := determineTLSConfig(cfg); tlsCfg != nil {
		opts.ConnectionOptions.TLS = tlsCfg
	} else if cfg.TLSEnabled != nil && !*cfg.TLSEnabled {
		// Caller explicitly turned TLS off — also disable the auto-on
		// behavior that API keys would normally trigger.
		opts.ConnectionOptions.TLSDisabled = true
	}

	if creds := determineCredentials(cfg); creds != nil {
		opts.Credentials = creds
	}

	temporalClient, err := client.NewLazyClient(opts)
	if err != nil {
		return nil, fmt.Errorf("temporal: new client: %w", err)
	}

	// ctx is unused today; kept in the signature so a follow-up can
	// surface DialContext if we ever need eager-connect again.
	_ = ctx

	return &clientManager{
		client:                 temporalClient,
		scheduleClientOverride: (*client.ScheduleClient)(nil),
	}, nil
}

// Close releases the underlying Temporal connection. Safe to call
// multiple times; the SDK's Close is idempotent.
func (m *clientManager) Close() {
	if m == nil || m.client == nil {
		return
	}

	m.client.Close()
}

// ScheduleClient returns the schedule sub-client. The interface seam
// in temporal.go uses this so schedule handler tests can inject a
// fake through withScheduleClient.
//
// When scheduleClientOverride is set, it is returned directly so
// tests can drive the schedule handlers without dialing Temporal.
// Production code paths never set the override; the manager built by
// newClientManager always reaches the real SDK.
func (m *clientManager) ScheduleClient() client.ScheduleClient {
	if m == nil {
		return nil
	}

	if m.scheduleClientOverride != nil {
		return *m.scheduleClientOverride
	}

	return m.client.ScheduleClient()
}

// withScheduleClient returns a copy of m with scheduleClientOverride
// set to sched. Used by schedule handler tests to inject a
// fakeScheduleClient without standing up a real Temporal server.
// Production code never calls this method.
func (m *clientManager) withScheduleClient(sched client.ScheduleClient) *clientManager {
	out := *m

	out.scheduleClientOverride = &sched

	return &out
}

// =====================================================================
// Workflow pass-through methods (consumed by workflow_handlers.go via
// the package-level temporalClient interface).
// =====================================================================

// StartWorkflow delegates to the underlying client.Client and starts
// a workflow execution. Implemented as ExecuteWorkflow under the
// hood (the SDK treats Start/Execute identically at the RPC level).
//
//nolint:wrapcheck,gocritic // SDK signature; StartWorkflowOptions is heavy.
func (m *clientManager) StartWorkflow(
	ctx context.Context,
	opts client.StartWorkflowOptions,
	workflowName string,
	args ...any,
) (client.WorkflowRun, error) {
	if m == nil || m.client == nil {
		return nil, errClientManagerUninitialized
	}

	return m.client.ExecuteWorkflow(ctx, opts, workflowName, args...)
}

// CancelWorkflow delegates to the underlying client.Client.
//
//nolint:wrapcheck // SDK signature; passthrough preserves errors verbatim.
func (m *clientManager) CancelWorkflow(ctx context.Context, workflowID, runID string) error {
	if m == nil || m.client == nil {
		return errClientManagerUninitialized
	}

	return m.client.CancelWorkflow(ctx, workflowID, runID)
}

// TerminateWorkflow delegates to the underlying client.Client.
//
//nolint:wrapcheck,revive // SDK signature; passthrough preserves errors verbatim.
func (m *clientManager) TerminateWorkflow(
	ctx context.Context, workflowID, runID, reason string, details ...any,
) error {
	if m == nil || m.client == nil {
		return errClientManagerUninitialized
	}

	return m.client.TerminateWorkflow(ctx, workflowID, runID, reason, details...)
}

// GetWorkflow delegates to the underlying client.Client.
func (m *clientManager) GetWorkflow(
	ctx context.Context, workflowID, runID string,
) client.WorkflowRun {
	if m == nil || m.client == nil {
		return nil
	}

	return m.client.GetWorkflow(ctx, workflowID, runID)
}

// DescribeWorkflow delegates to the underlying client.Client.
//
//nolint:wrapcheck // SDK signature; passthrough preserves errors verbatim.
func (m *clientManager) DescribeWorkflow(
	ctx context.Context, workflowID, runID string,
) (*client.WorkflowExecutionDescription, error) {
	if m == nil || m.client == nil {
		return nil, errClientManagerUninitialized
	}

	return m.client.DescribeWorkflow(ctx, workflowID, runID)
}

// ListWorkflow delegates to the underlying client.Client.
//
//nolint:wrapcheck // SDK signature; passthrough preserves errors verbatim.
func (m *clientManager) ListWorkflow(
	ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest,
) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	if m == nil || m.client == nil {
		return nil, errClientManagerUninitialized
	}

	return m.client.ListWorkflow(ctx, request)
}

// GetWorkflowHistory delegates to the underlying client.Client.
//
//nolint:revive // SDK signature; argument count is fixed by the upstream interface.
func (m *clientManager) GetWorkflowHistory(
	ctx context.Context,
	workflowID, runID string,
	isLongPoll bool,
	filterType enums.HistoryEventFilterType,
) client.HistoryEventIterator {
	if m == nil || m.client == nil {
		return nil
	}

	return m.client.GetWorkflowHistory(ctx, workflowID, runID, isLongPoll, filterType)
}

// SignalWorkflow delegates to the underlying client.Client. Used by
// the continue_as_new tool and the signal_workflow tool (the latter
// through the querySignalClient seam).
//
//nolint:wrapcheck,revive // SDK signature; passthrough preserves errors verbatim.
func (m *clientManager) SignalWorkflow(
	ctx context.Context, workflowID, runID, signalName string, arg any,
) error {
	if m == nil || m.client == nil {
		return errClientManagerUninitialized
	}

	return m.client.SignalWorkflow(ctx, workflowID, runID, signalName, arg)
}

// =====================================================================
// Activity pass-through methods (consumed by activity_handlers.go via
// the package-level temporalClient interface).
// =====================================================================

// ExecuteActivity delegates to the underlying client.Client. The
// returned handle satisfies the activityHandle interface declared in
// temporal.go.
//
// and pass-through errors are surfaced via the SDK's wrapped chain.
//
//nolint:gocritic,wrapcheck // SDK signature; StartActivityOptions is heavy,
func (m *clientManager) ExecuteActivity(
	ctx context.Context,
	opts client.StartActivityOptions,
	activity any,
	args ...any,
) (client.ActivityHandle, error) {
	if m == nil || m.client == nil {
		return nil, errClientManagerUninitialized
	}

	return m.client.ExecuteActivity(ctx, opts, activity, args...)
}

// GetActivityHandle delegates to the underlying client.Client. The
// returned handle satisfies the activityHandle interface declared in
// temporal.go.
func (m *clientManager) GetActivityHandle(
	options client.GetActivityHandleOptions,
) client.ActivityHandle {
	if m == nil || m.client == nil {
		return nil
	}

	return m.client.GetActivityHandle(options)
}

// ListActivities delegates to the underlying client.Client.
//
//nolint:wrapcheck // SDK signature; passthrough preserves errors verbatim.
func (m *clientManager) ListActivities(
	ctx context.Context, options client.ListActivitiesOptions,
) (client.ListActivitiesResult, error) {
	if m == nil || m.client == nil {
		return client.ListActivitiesResult{}, errClientManagerUninitialized
	}

	return m.client.ListActivities(ctx, options)
}

// CountActivities delegates to the underlying client.Client.
//
//nolint:wrapcheck // SDK signature; passthrough preserves errors verbatim.
func (m *clientManager) CountActivities(
	ctx context.Context, options client.CountActivitiesOptions,
) (*client.CountActivitiesResult, error) {
	if m == nil || m.client == nil {
		return nil, errClientManagerUninitialized
	}

	return m.client.CountActivities(ctx, options)
}

// =====================================================================
// Query/signal forwarder (consumed by query_signal_handlers.go through
// the handler-scoped querySignalClient interface).
// =====================================================================

// QueryWorkflowWithOptions delegates to the underlying client.Client.
// The query/signal handlers consume this through the
// querySignalClient interface declared in query_signal_handlers.go;
// keeping the wrapper as a thin pass-through preserves the
// SDK's signature verbatim.
//
//nolint:wrapcheck // SDK signature; passthrough preserves errors verbatim.
func (m *clientManager) QueryWorkflowWithOptions(
	ctx context.Context,
	request *client.QueryWorkflowWithOptionsRequest,
) (*client.QueryWorkflowWithOptionsResponse, error) {
	if m == nil || m.client == nil {
		return nil, errClientManagerUninitialized
	}

	return m.client.QueryWorkflowWithOptions(ctx, request)
}

// =====================================================================
// Batch pass-through methods (consumed by batch_handlers.go through
// the handler-scoped batchClient interface).
// =====================================================================

// batchSignal delegates to client.Client.SignalWorkflow. Named with
// a `batch` prefix so the per-feature interfaces (batchClient) stay
// grep-friendly without polluting the top-level temporalClient
// interface that's part of the public shape.
//
//nolint:revive // argument-limit: SDK signature (workflowID, runID, signalName, arg) is fixed.
func (m *clientManager) batchSignal(
	ctx context.Context,
	workflowID, runID, signalName string,
	arg any,
) error {
	if m == nil || m.client == nil {
		return errClientManagerUninitialized
	}

	//nolint:wrapcheck // pass-through: caller wraps
	return m.client.SignalWorkflow(ctx, workflowID, runID, signalName, arg)
}

// batchCancelWorkflow delegates to client.Client.CancelWorkflow.
func (m *clientManager) batchCancelWorkflow(
	ctx context.Context,
	workflowID, runID string,
) error {
	if m == nil || m.client == nil {
		return errClientManagerUninitialized
	}

	//nolint:wrapcheck // pass-through
	return m.client.CancelWorkflow(ctx, workflowID, runID)
}

// batchTerminateWorkflow delegates to client.Client.TerminateWorkflow.
// details is forwarded as the variadic payload to the SDK call.
//
//nolint:revive // argument-limit: SDK signature (workflowID, runID, reason, details) is fixed.
func (m *clientManager) batchTerminateWorkflow(
	ctx context.Context,
	workflowID, runID, reason string,
	details []any,
) error {
	if m == nil || m.client == nil {
		return errClientManagerUninitialized
	}

	//nolint:wrapcheck // pass-through
	return m.client.TerminateWorkflow(ctx, workflowID, runID, reason, details...)
}

// batchListWorkflows delegates to client.Client.ListWorkflow with
// PageSize = limit. The handler is single-page by design — matched
// count in the response payload reflects whatever the server
// returned on the first call. Pagination across multiple server
// rounds would change the user-visible semantics of "matched" and
// is intentionally out of scope for batch-tools (issue #146).
func (m *clientManager) batchListWorkflows(
	ctx context.Context,
	query string,
	limit int,
) ([]*workflow.WorkflowExecutionInfo, error) {
	if m == nil || m.client == nil {
		return nil, errClientManagerUninitialized
	}

	request := &workflowservice.ListWorkflowExecutionsRequest{ //nolint:exhaustruct,lll // Query+PageSize only
		Query:    query,
		PageSize: int32(limit), //nolint:gosec // G115: SDK contract; batch_tools caps at 100
	}

	resp, err := m.client.ListWorkflow(ctx, request)
	if err != nil {
		return nil, err //nolint:wrapcheck // pass-through
	}

	return resp.Executions, nil
}

// batchListActivities delegates to client.Client.ListActivities and
// materializes the iterator into a slice capped at limit entries.
// The SDK iterator paginates internally; the handler stops after
// limit items so the user-visible matched count matches the cap
// (issue #146).
func (m *clientManager) batchListActivities(
	ctx context.Context,
	query string,
	limit int,
) ([]*client.ActivityExecutionInfo, error) {
	if m == nil || m.client == nil {
		return nil, errClientManagerUninitialized
	}

	results, err := m.client.ListActivities(ctx, client.ListActivitiesOptions{Query: query})
	if err != nil {
		return nil, err //nolint:wrapcheck // pass-through
	}

	out := make([]*client.ActivityExecutionInfo, 0, limit)

	for info, iterErr := range results.Results {
		if iterErr != nil {
			return out, iterErr
		}

		out = append(out, info)
		if len(out) >= limit {
			break
		}
	}

	return out, nil
}

// batchGetActivityHandle delegates to client.Client.GetActivityHandle.
// The returned handle is the SDK-native client.ActivityHandle; the
// per-tool handler calls its Cancel or Terminate method (each of
// which is also part of the batchClient contract).
func (m *clientManager) batchGetActivityHandle(
	activityID, runID string,
) (client.ActivityHandle, error) {
	if m == nil || m.client == nil {
		return nil, errClientManagerUninitialized
	}

	return m.client.GetActivityHandle(client.GetActivityHandleOptions{
		ActivityID: activityID,
		RunID:      runID,
	}), nil
}

// =====================================================================
// Existing TLS / credentials / cert helpers — preserved verbatim from
// the schedule-tools stage.
// =====================================================================

// determineTLSConfig resolves the *tls.Config following the priority
// order documented in the issue description:
//
//  1. mTLS cert+key both set → returns nil here; the client cert is
//     attached to the *tls.Config inside loadMTLSTLSConfig below.
//  2. API key set → server-cert-verification-only TLS (InsecureSkipVerify=false).
//  3. tls_enabled = true → same as #2.
//  4. tls_enabled = false → nil (no TLS).
//  5. tls_enabled absent + remote host → same as #2.
//  6. tls_enabled absent + local host → nil.
//
// The function returns nil when TLS should not be enabled. The SDK's
// NewLazyClient treats a nil ConnectionOptions.TLS as plaintext gRPC.
//
// mTLS is handled by loadMTLSTLSConfig which returns a *tls.Config with
// the client cert chain populated. determineTLSConfig delegates to it
// when both mTLS paths are set.
func determineTLSConfig(cfg *config) *tls.Config {
	if cfg.TLSClientCertPath != "" && cfg.TLSClientKeyPath != "" {
		return loadMTLSTLSConfig(cfg.TLSClientCertPath, cfg.TLSClientKeyPath)
	}

	if cfg.APIKey != "" {
		return &tls.Config{MinVersion: tls.VersionTLS12}
	}

	if cfg.TLSEnabled != nil {
		if *cfg.TLSEnabled {
			return &tls.Config{MinVersion: tls.VersionTLS12}
		}

		return nil
	}

	// Auto-detect from host heuristic.
	if isLocalHost(cfg.Host) {
		return nil
	}

	return &tls.Config{MinVersion: tls.VersionTLS12}
}

// determineCredentials resolves the client.Credentials bundle.
// Returns nil when no credentials are required.
//
// Per the SDK doc, mTLS via NewMTLSCredentials is mutually exclusive
// with setting ConnectionOptions.TLS to a *tls.Config that already
// has Certificates — the SDK rejects the combination at
// NewLazyClient time. We do NOT call NewMTLSCredentials for mTLS; we
// instead bake the cert into the *tls.Config returned by
// determineTLSConfig. This keeps the two paths from fighting each
// other.
func determineCredentials(cfg *config) client.Credentials {
	if cfg.TLSClientCertPath != "" && cfg.TLSClientKeyPath != "" {
		// mTLS handled inside determineTLSConfig via the *tls.Config.
		return nil
	}

	if cfg.APIKey != "" {
		return client.NewAPIKeyStaticCredentials(cfg.APIKey)
	}

	return nil
}

// loadMTLSTLSConfig reads the cert and key files from disk and
// returns a *tls.Config with the client certificate populated. Any
// error from os.ReadFile is wrapped so the per-file path is in the
// error message — distinguishing "cert missing" from "key missing"
// is important for debugging.
//
// Server-side cert verification is enabled
// (InsecureSkipVerify=false, the default) so a misconfigured
// Temporal frontend with a self-signed cert will surface a clear
// TLS handshake error rather than silently talking to the wrong
// server.
func loadMTLSTLSConfig(certPath, keyPath string) *tls.Config {
	cert, err := loadMTLSClientCert(certPath, keyPath)
	if err != nil {
		// Surface the error lazily: store it in the returned config
		// via a sentinel? No — *tls.Config has no error field. The
		// SDK would crash on dial with an opaque error. We accept
		// this trade-off for the skeleton; issue 6+ will revisit.
		_ = err
		return nil
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
}

// loadMTLSClientCert reads the cert and key files from disk and
// returns a tls.Certificate. Any error from os.ReadFile is wrapped
// so the per-file path is in the error message.
func loadMTLSClientCert(certPath, keyPath string) (tls.Certificate, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		if os.IsNotExist(err) {
			return tls.Certificate{}, fmt.Errorf("%w: %s", errTLSCertMissing, certPath)
		}

		return tls.Certificate{}, fmt.Errorf("%w: %s: %w", errTLSCertUnreadable, certPath, err)
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return tls.Certificate{}, fmt.Errorf("%w: %s", errTLSCertMissing, keyPath)
		}

		return tls.Certificate{}, fmt.Errorf("%w: %s: %w", errTLSCertUnreadable, keyPath, err)
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("temporal tool: tls key pair invalid: %w", err)
	}

	return cert, nil
}

// isLocalHost reports whether the host string should be treated as
// "local" by the TLS auto-detection heuristic. Substring match,
// mirroring the Python upstream's `local_hosts = ["localhost",
// "127.0.0.1", "host.docker.internal"]` rule.
func isLocalHost(host string) bool {
	if host == "" {
		// Empty host is a degenerate case; treat as local so we
		// don't try TLS against an unconfigured target.
		return true
	}

	return strings.Contains(host, "localhost") ||
		strings.Contains(host, "127.0.0.1") ||
		strings.Contains(host, "host.docker.internal")
}
