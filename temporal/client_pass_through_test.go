// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:exhaustruct // test fixtures use partial structs and cluster assertions
package temporal

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonpb "go.temporal.io/api/common/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservicepb "go.temporal.io/api/workflowservice/v1"
)

// The pass-through methods on *clientManager (batchSignal,
// batchCancelWorkflow, batchTerminateWorkflow, batchListWorkflows,
// batchListActivities, batchGetActivityHandle) are tiny delegation
// layers around the SDK's client.Client. The handlers that use them
// are tested through fakeTemporalClient (the package-local interface
// seam) — but the *clientManager methods themselves are unreachable
// from those tests. To keep per-package coverage above the 85%
// floor, we exercise the pass-through methods here against a
// minimal gRPC health-check backend that the lazy client can dial.

// startFakeTemporalServer spins up a thin gRPC server implementing
// WorkflowService (the minimum surface batchList* needs) and
// returns the address + a stop func. The server is sufficient for
// batchSignal / batchCancelWorkflow / batchTerminateWorkflow to
// successfully call the relevant RPC stubs (which the SDK dispatches
// against the server-side stub).
func startFakeTemporalServer(t *testing.T) (addr string, stop func()) {
	t.Helper()

	//nolint:noctx // test server binds to ephemeral local port
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()

	workflowservicepb.RegisterWorkflowServiceServer(srv, &fakeWorkflowService{})

	go func() {
		// Serve blocks until Stop() is called. Errors here
		// (server already closed) are expected at shutdown.
		_ = srv.Serve(lis) //nolint:errcheck // server lifetime managed via srv.Stop in stop()
	}()

	return lis.Addr().String(), func() {
		srv.Stop()
	}
}

// fakeWorkflowService is a no-op implementation of the Temporal
// WorkflowServiceServer gRPC interface. We only need it to accept
// the SignalWorkflow / CancelWorkflow / TerminateWorkflow /
// ListWorkflowExecutions / ListActivityExecutions calls; returning
// nil error is enough for the *clientManager delegation tests.
//
// We embed UnimplementedWorkflowServiceServer for forward
// compatibility — adding new server methods the SDK might start
// calling does not panic.
type fakeWorkflowService struct {
	workflowservicepb.UnimplementedWorkflowServiceServer

	listCount atomic.Int32
}

func (*fakeWorkflowService) SignalWorkflowExecution(
	_ context.Context,
	_ *workflowservicepb.SignalWorkflowExecutionRequest,
) (*workflowservicepb.SignalWorkflowExecutionResponse, error) {
	return &workflowservicepb.SignalWorkflowExecutionResponse{}, nil
}

func (*fakeWorkflowService) RequestCancelWorkflowExecution(
	_ context.Context,
	_ *workflowservicepb.RequestCancelWorkflowExecutionRequest,
) (*workflowservicepb.RequestCancelWorkflowExecutionResponse, error) {
	return &workflowservicepb.RequestCancelWorkflowExecutionResponse{}, nil
}

func (*fakeWorkflowService) TerminateWorkflowExecution(
	_ context.Context,
	_ *workflowservicepb.TerminateWorkflowExecutionRequest,
) (*workflowservicepb.TerminateWorkflowExecutionResponse, error) {
	return &workflowservicepb.TerminateWorkflowExecutionResponse{}, nil
}

func (f *fakeWorkflowService) ListWorkflowExecutions(
	_ context.Context,
	_ *workflowservicepb.ListWorkflowExecutionsRequest,
) (*workflowservicepb.ListWorkflowExecutionsResponse, error) {
	f.listCount.Add(1)

	return &workflowservicepb.ListWorkflowExecutionsResponse{
		Executions: []*workflowpb.WorkflowExecutionInfo{
			{
				Execution: &commonpb.WorkflowExecution{WorkflowId: "wf-1", RunId: "run-1"},
			},
		},
	}, nil
}

func (*fakeWorkflowService) ListActivityExecutions(
	_ context.Context,
	_ *workflowservicepb.ListActivityExecutionsRequest,
) (*workflowservicepb.ListActivityExecutionsResponse, error) {
	return &workflowservicepb.ListActivityExecutionsResponse{}, nil
}

// mkTestManager builds a *clientManager with a private gRPC
// dial target that points at the local fake server. Returns the
// manager and a cleanup registered with t.Cleanup.
func mkTestManager(t *testing.T, addr string) *clientManager {
	t.Helper()

	manager, err := newClientManager(t.Context(), &config{Host: addr, Namespace: "default"})
	require.NoError(t, err)
	t.Cleanup(manager.Close)

	return manager
}

// TestClientManager_PassThroughMethods covers every batch-handler
// pass-through method on *clientManager. They dial a local fake
// gRPC server and assert that the SDK was reached (i.e., the method
// body executed). The body of each method is small (one SDK call +
// the nil-guard) so the test simply checks for "no panic, no
// unexpected shape". Coverage on the batch_* receiver is what we
// actually pay for here.
func TestClientManager_PassThroughMethods(t *testing.T) {
	t.Parallel()

	addr, stop := startFakeTemporalServer(t)
	t.Cleanup(stop)

	// Give the gRPC server a moment to start. Goroutine-bound, so a
	// brief sleep is fine here.
	time.Sleep(50 * time.Millisecond)

	manager := mkTestManager(t, addr)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	t.Run("batchSignal", func(t *testing.T) {
		err := manager.batchSignal(ctx, "wf-1", "run-1", "tick", any(nil))
		assert.NoError(t, err)
	})

	t.Run("batchCancelWorkflow", func(t *testing.T) {
		err := manager.batchCancelWorkflow(ctx, "wf-1", "run-1")
		assert.NoError(t, err)
	})

	t.Run("batchTerminateWorkflow", func(t *testing.T) {
		err := manager.batchTerminateWorkflow(ctx, "wf-1", "run-1", "cleanup", []any{"admin"})
		assert.NoError(t, err)
	})

	t.Run("batchListWorkflows", func(t *testing.T) {
		execs, err := manager.batchListWorkflows(ctx, "ExecutionStatus = 'Running'", 100)
		require.NoError(t, err)
		assert.Len(t, execs, 1)
		assert.Equal(t, "wf-1", execs[0].Execution.WorkflowId)
	})

	t.Run("batchListActivities", func(t *testing.T) {
		// The fake server returns an empty Executions slice; the
		// SDK iterates it and the handler returns an empty slice.
		// We don't need to assert the activity shapes here — only
		// that the method executed without error.
		_, err := manager.batchListActivities(ctx, "ActivityType = 'X'", 100)
		assert.NoError(t, err)
	})

	t.Run("batchGetActivityHandle", func(t *testing.T) {
		handle, err := manager.batchGetActivityHandle("act-1", "arun-1")
		require.NoError(t, err)
		assert.NotNil(t, handle)
	})
}
