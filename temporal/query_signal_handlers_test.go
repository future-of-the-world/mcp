// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:exhaustruct,revive,wsl_v5 // test fixtures use partial structs and cluster assertions
package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.temporal.io/api/query/v1"
	"go.temporal.io/sdk/converter"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonpb "go.temporal.io/api/common/v1"
	sdkclient "go.temporal.io/sdk/client"
)

// -----------------------------------------------------------------------
// Test seam: fakeQuerySignalClient
// -----------------------------------------------------------------------

// fakeQuerySignalClient satisfies the querySignalClient interface by
// routing every method through an optional function field. Tests set
// only the fields they care about; the rest fall through to defaults.
// The pattern mirrors woodpecker's httptest server in spirit but
// lives in-process so the tests can run without the network.
type fakeQuerySignalClient struct {
	queryWorkflowFn  queryWorkflowFnType
	signalWorkflowFn signalWorkflowFnType
}

// errUnexpectedCall is the sentinel returned by test fakes whose
// function field should never fire — for example, a validation
// failure test that sets up a fake only to confirm the SDK is not
// reached. Returning a sentinel error keeps the (response, error)
// return shape honest without reaching for (nil, nil), which the
// nilnil linter rejects.
var errUnexpectedCall = errors.New("fake: unexpected call")

// queryWorkflowFnType and signalWorkflowFnType mirror the SDK
// signatures. Hoisted as named types so the fake's struct fields stay
// under the lll line-length limit.
type (
	queryWorkflowFnType = func(
		ctx context.Context,
		request *sdkclient.QueryWorkflowWithOptionsRequest,
	) (*sdkclient.QueryWorkflowWithOptionsResponse, error)

	signalWorkflowFnType = func(
		ctx context.Context,
		workflowID, runID, signalName string,
		arg any,
	) error
)

func (f *fakeQuerySignalClient) QueryWorkflowWithOptions(
	ctx context.Context,
	request *sdkclient.QueryWorkflowWithOptionsRequest,
) (*sdkclient.QueryWorkflowWithOptionsResponse, error) {
	if f.queryWorkflowFn == nil {
		return nil, errors.New("fakeQuerySignalClient: queryWorkflowFn not set")
	}

	return f.queryWorkflowFn(ctx, request)
}

func (f *fakeQuerySignalClient) SignalWorkflow(
	ctx context.Context,
	workflowID, runID, signalName string,
	arg any,
) error {
	if f.signalWorkflowFn == nil {
		return errors.New("fakeQuerySignalClient: signalWorkflowFn not set")
	}

	return f.signalWorkflowFn(ctx, workflowID, runID, signalName, arg)
}

// -----------------------------------------------------------------------
// Test seam: encodedValueFromPayloads
// -----------------------------------------------------------------------

// encodedValueFromPayloads is a converter.EncodedValue that exposes a
// pre-built *commonpb.Payloads through the ValuesPayloads interface.
// Tests use it to feed a known JSON payload into handleQueryWorkflow
// and assert on the decoded result, without going through the SDK's
// payload encoding machinery.
type encodedValueFromPayloads struct {
	payloads *commonpb.Payloads
}

func (e encodedValueFromPayloads) HasValue() bool {
	return e.payloads != nil && len(e.payloads.Payloads) > 0
}

func (e encodedValueFromPayloads) Get(_ any) error {
	return errors.New("encodedValueFromPayloads: Get not supported; use ValuesPayloads")
}

func (e encodedValueFromPayloads) Payloads() *commonpb.Payloads {
	return e.payloads
}

// encodeTestPayload JSON-encodes value and wraps it in a *commonpb.Payloads
// using the SDK's default data converter so FromPayloads can round-trip
// it cleanly.
func encodeTestPayload(t *testing.T, value any) *commonpb.Payloads {
	t.Helper()

	payloads, err := converter.GetDefaultDataConverter().ToPayloads(value)
	require.NoError(t, err)

	return payloads
}

// -----------------------------------------------------------------------
// Helper: invoke the tool handler with a JSON argument map
// -----------------------------------------------------------------------

// callTool invokes the supplied handler with arguments built from
// argsMap. The arguments are JSON-marshaled and wrapped in a
// CallToolRequest so the handler sees the same shape it would receive
// from the MCP dispatcher.
func callTool(
	t *testing.T,
	handler mcp.ToolHandler,
	argsMap map[string]any,
) (*mcp.CallToolResult, error) {
	t.Helper()

	raw, err := json.Marshal(argsMap)
	require.NoError(t, err)

	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Arguments: raw,
		},
	}

	return handler(t.Context(), req)
}

// decodeResult unmarshals a CallToolResult's first TextContent into a
// generic map[string]any for assertions.
func decodeResult(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()

	require.NotNilf(t, result, "handler must return a result")
	require.NotEmptyf(t, result.Content, "result must carry content")

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "first content entry must be *mcp.TextContent, got %T", result.Content[0])

	var decoded map[string]any

	require.NoErrorf(t, json.Unmarshal([]byte(textContent.Text), &decoded),
		"result text must be JSON: %q", textContent.Text)

	return decoded
}

// -----------------------------------------------------------------------
// query_workflow tests
// -----------------------------------------------------------------------

// TestHandleQueryWorkflow_HappyPath covers the canonical success path:
// both required fields set, args supplied, the fake returns a known
// decoded payload, and the handler marshals it into the response.
func TestHandleQueryWorkflow_HappyPath(t *testing.T) {
	t.Parallel()

	expected := map[string]any{"status": "running", "step": 42}

	var capturedReq *sdkclient.QueryWorkflowWithOptionsRequest

	fake := &fakeQuerySignalClient{
		queryWorkflowFn: func(
			_ context.Context,
			req *sdkclient.QueryWorkflowWithOptionsRequest,
		) (*sdkclient.QueryWorkflowWithOptionsResponse, error) {
			capturedReq = req

			return &sdkclient.QueryWorkflowWithOptionsResponse{
				QueryResult: encodedValueFromPayloads{payloads: encodeTestPayload(t, expected)},
			}, nil
		},
	}

	handler := handleQueryWorkflow(fake)

	result, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"run_id":      "run-1",
		"query_name":  "status",
		"args":        []any{"hello", 7},
	})
	require.NoError(t, err)

	require.NotNilf(t, capturedReq, "fake must capture the request")
	assert.Equal(t, "wf-1", capturedReq.WorkflowID)
	assert.Equal(t, "run-1", capturedReq.RunID)
	assert.Equal(t, "status", capturedReq.QueryType)
	// JSON unmarshal widens integers to float64 in []any, so the
	// captured args round-trip as []any{"hello", float64(7)}.
	assert.Equal(t, []any{"hello", float64(7)}, capturedReq.Args)

	decoded := decodeResult(t, result)
	assert.Equal(t, "wf-1", decoded["workflow_id"])
	assert.Equal(t, "status", decoded["query_name"])
	// result is JSON-decoded from the encoded payload, so the nested
	// map round-trips through json.Unmarshal as map[string]any.
	resultMap, ok := decoded["result"].(map[string]any)
	require.Truef(t, ok, "result must round-trip as map, got %T", decoded["result"])
	assert.Equal(t, "running", resultMap["status"])
	assert.InDelta(t, 42, resultMap["step"], 0)
}

// TestHandleQueryWorkflow_NoArgs covers the case where the caller
// omits the args field entirely — the handler passes nothing
// variadic to the SDK.
func TestHandleQueryWorkflow_NoArgs(t *testing.T) {
	t.Parallel()

	var capturedReq *sdkclient.QueryWorkflowWithOptionsRequest

	fake := &fakeQuerySignalClient{
		queryWorkflowFn: func(
			_ context.Context,
			req *sdkclient.QueryWorkflowWithOptionsRequest,
		) (*sdkclient.QueryWorkflowWithOptionsResponse, error) {
			capturedReq = req

			return &sdkclient.QueryWorkflowWithOptionsResponse{
				QueryResult: encodedValueFromPayloads{payloads: encodeTestPayload(t, "ok")},
			}, nil
		},
	}

	handler := handleQueryWorkflow(fake)

	result, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"query_name":  "ping",
	})
	require.NoError(t, err)
	require.NotNilf(t, capturedReq, "fake must capture the request")
	assert.Nilf(t, capturedReq.Args, "absent args → nil slice (no variadic expansion)")

	decoded := decodeResult(t, result)
	assert.Equal(t, "ping", decoded["query_name"])
	assert.Equal(t, "ok", decoded["result"])
}

// TestHandleQueryWorkflow_EmptyArgsArray covers the case where args
// is an explicit empty JSON array. The spec says "Empty args
// (zero-length) is treated as no args"; this branch is distinct from
// "absent" because decodeArgsSlice still gets called.
func TestHandleQueryWorkflow_EmptyArgsArray(t *testing.T) {
	t.Parallel()

	var capturedReq *sdkclient.QueryWorkflowWithOptionsRequest

	fake := &fakeQuerySignalClient{
		queryWorkflowFn: func(
			_ context.Context,
			req *sdkclient.QueryWorkflowWithOptionsRequest,
		) (*sdkclient.QueryWorkflowWithOptionsResponse, error) {
			capturedReq = req

			return &sdkclient.QueryWorkflowWithOptionsResponse{
				QueryResult: encodedValueFromPayloads{payloads: encodeTestPayload(t, any(nil))},
			}, nil
		},
	}

	handler := handleQueryWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"query_name":  "ping",
		"args":        []any{},
	})
	require.NoError(t, err)
	require.NotNilf(t, capturedReq, "fake must capture the request")
	assert.Emptyf(t, capturedReq.Args, "empty JSON array → empty (non-nil) slice")
}

// TestHandleQueryWorkflow_NoRunID covers the case where run_id is
// omitted. The handler passes the zero string through; the SDK is
// responsible for resolving to the latest run.
func TestHandleQueryWorkflow_NoRunID(t *testing.T) {
	t.Parallel()

	var capturedReq *sdkclient.QueryWorkflowWithOptionsRequest

	fake := &fakeQuerySignalClient{
		queryWorkflowFn: func(
			_ context.Context,
			req *sdkclient.QueryWorkflowWithOptionsRequest,
		) (*sdkclient.QueryWorkflowWithOptionsResponse, error) {
			capturedReq = req

			return &sdkclient.QueryWorkflowWithOptionsResponse{
				QueryResult: encodedValueFromPayloads{payloads: encodeTestPayload(t, "ok")},
			}, nil
		},
	}

	handler := handleQueryWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"query_name":  "ping",
	})
	require.NoError(t, err)
	require.NotNilf(t, capturedReq, "fake must capture the request")
	assert.Empty(t, capturedReq.RunID)
}

// TestHandleQueryWorkflow_MissingWorkflowID confirms that an absent
// workflow_id is rejected with errQueryWorkflowIDRequired. The
// validation happens before the SDK is called.
func TestHandleQueryWorkflow_MissingWorkflowID(t *testing.T) {
	t.Parallel()

	called := false

	fake := &fakeQuerySignalClient{
		queryWorkflowFn: func(
			_ context.Context,
			_ *sdkclient.QueryWorkflowWithOptionsRequest,
		) (*sdkclient.QueryWorkflowWithOptionsResponse, error) {
			called = true

			return nil, errUnexpectedCall
		},
	}

	handler := handleQueryWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"query_name": "ping",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, errQueryWorkflowIDRequired)
	assert.Falsef(t, called, "SDK must not be called when validation fails")
}

// TestHandleQueryWorkflow_MissingQueryName confirms that an absent
// query_name is rejected with errQueryNameRequired.
func TestHandleQueryWorkflow_MissingQueryName(t *testing.T) {
	t.Parallel()

	called := false

	fake := &fakeQuerySignalClient{
		queryWorkflowFn: func(
			_ context.Context,
			_ *sdkclient.QueryWorkflowWithOptionsRequest,
		) (*sdkclient.QueryWorkflowWithOptionsResponse, error) {
			called = true

			return nil, errUnexpectedCall
		},
	}

	handler := handleQueryWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, errQueryNameRequired)
	assert.Falsef(t, called, "SDK must not be called when validation fails")
}

// TestHandleQueryWorkflow_ArgsDecodeError confirms that malformed
// args (e.g. a JSON object where an array is expected) surface as a
// clear parse error before the SDK is called.
func TestHandleQueryWorkflow_ArgsDecodeError(t *testing.T) {
	t.Parallel()

	called := false

	fake := &fakeQuerySignalClient{
		queryWorkflowFn: func(
			_ context.Context,
			_ *sdkclient.QueryWorkflowWithOptionsRequest,
		) (*sdkclient.QueryWorkflowWithOptionsResponse, error) {
			called = true

			return nil, errUnexpectedCall
		},
	}

	handler := handleQueryWorkflow(fake)

	// args as a JSON object — decodeArgsSlice expects an array.
	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"query_name":  "ping",
		"args":        map[string]any{"x": 1},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse args")
	assert.Falsef(t, called, "SDK must not be called when arg decode fails")
}

// TestHandleQueryWorkflow_SDKError confirms that an error returned
// from QueryWorkflowWithOptions is wrapped with the tool name.
func TestHandleQueryWorkflow_SDKError(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("server unavailable")

	fake := &fakeQuerySignalClient{
		queryWorkflowFn: func(
			_ context.Context,
			_ *sdkclient.QueryWorkflowWithOptionsRequest,
		) (*sdkclient.QueryWorkflowWithOptionsResponse, error) {
			return nil, sdkErr
		},
	}

	handler := handleQueryWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"query_name":  "ping",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
	assert.Containsf(t, err.Error(), "query_workflow",
		"error must carry tool-name prefix")
}

// TestHandleQueryWorkflow_Rejected covers the QueryRejected path:
// the server returns a response whose QueryRejected is non-nil,
// and the handler surfaces a clear error carrying the rejection
// status.
func TestHandleQueryWorkflow_Rejected(t *testing.T) {
	t.Parallel()

	fake := &fakeQuerySignalClient{
		queryWorkflowFn: func(
			_ context.Context,
			_ *sdkclient.QueryWorkflowWithOptionsRequest,
		) (*sdkclient.QueryWorkflowWithOptionsResponse, error) {
			return &sdkclient.QueryWorkflowWithOptionsResponse{
				QueryRejected: &query.QueryRejected{},
			}, nil
		},
	}

	handler := handleQueryWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"query_name":  "ping",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rejected")
}

// TestHandleQueryWorkflow_NilResponse covers the edge case where the
// SDK returns (nil, nil). The handler must surface this as an error
// rather than panic on a nil-deref of QueryResult. This case bypasses
// errUnexpectedCall intentionally — the nilnil lint rule that drove
// the sentinel's introduction does not apply to a test that exercises
// the SDK's nil-return path.
func TestHandleQueryWorkflow_NilResponse(t *testing.T) {
	t.Parallel()

	fake := &fakeQuerySignalClient{
		queryWorkflowFn: func(
			_ context.Context,
			_ *sdkclient.QueryWorkflowWithOptionsRequest,
		) (*sdkclient.QueryWorkflowWithOptionsResponse, error) {
			//nolint:nilnil // intentional: exercises the nil-response branch
			return nil, nil
		},
	}

	handler := handleQueryWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"query_name":  "ping",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response")
}

// TestHandleQueryWorkflow_PayloadsUnavailable covers the branch
// where the SDK returns an EncodedValue that does NOT implement
// ValuesPayloads (so converter.GetPayloads returns nil). The handler
// must surface this as a clear error rather than panic.
func TestHandleQueryWorkflow_PayloadsUnavailable(t *testing.T) {
	t.Parallel()

	fake := &fakeQuerySignalClient{
		queryWorkflowFn: func(
			_ context.Context,
			_ *sdkclient.QueryWorkflowWithOptionsRequest,
		) (*sdkclient.QueryWorkflowWithOptionsResponse, error) {
			return &sdkclient.QueryWorkflowWithOptionsResponse{
				QueryResult: encodedValueNoPayloads{},
			}, nil
		},
	}

	handler := handleQueryWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"query_name":  "ping",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "payloads unavailable")
}

// encodedValueNoPayloads is a converter.EncodedValue that does NOT
// implement ValuesPayloads, so converter.GetPayloads returns nil.
type encodedValueNoPayloads struct{}

func (encodedValueNoPayloads) HasValue() bool { return true }
func (encodedValueNoPayloads) Get(_ any) error {
	return nil
}

// TestHandleQueryWorkflow_FromPayloadsError covers the
// decode-result failure branch: payloads are available but
// FromPayloads rejects them. The handler wraps the error with the
// tool name.
func TestHandleQueryWorkflow_FromPayloadsError(t *testing.T) {
	t.Parallel()

	fake := &fakeQuerySignalClient{
		queryWorkflowFn: func(
			_ context.Context,
			_ *sdkclient.QueryWorkflowWithOptionsRequest,
		) (*sdkclient.QueryWorkflowWithOptionsResponse, error) {
			return &sdkclient.QueryWorkflowWithOptionsResponse{
				QueryResult: encodedValueFromPayloads{
					payloads: &commonpb.Payloads{Payloads: []*commonpb.Payload{
						// An unknown-encoding payload that the
						// default data converter cannot decode.
						{Data: []byte("garbage")},
					}},
				},
			}, nil
		},
	}

	handler := handleQueryWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"query_name":  "ping",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode result")
}

// TestHandleQueryWorkflow_ArgsDecodeError confirms the JSON
// unmarshal-level error (malformed JSON, type mismatch on the
// outer arg shape). The "args": "not-an-array" branch is rejected
// by decodeArgsSlice.
func TestHandleQueryWorkflow_OuterArgsDecodeError(t *testing.T) {
	t.Parallel()

	handler := handleQueryWorkflow(&fakeQuerySignalClient{})

	// raw JSON: args field as a string — type mismatch on []any.
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Arguments: json.RawMessage(`{"workflow_id":"wf-1","query_name":"q","args":"oops"}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse args")
}

// -----------------------------------------------------------------------
// signal_workflow tests
// -----------------------------------------------------------------------

// TestHandleSignalWorkflow_HappyPath covers the canonical success
// path: both required fields set, args supplied, the fake confirms
// the SDK call and the handler returns the success envelope.
func TestHandleSignalWorkflow_HappyPath(t *testing.T) {
	t.Parallel()

	var (
		capturedWorkflowID string
		capturedRunID      string
		capturedSignalName string
		capturedArg        any
	)

	fake := &fakeQuerySignalClient{
		signalWorkflowFn: func(
			_ context.Context,
			workflowID, runID, signalName string,
			arg any,
		) error {
			capturedWorkflowID = workflowID
			capturedRunID = runID
			capturedSignalName = signalName
			capturedArg = arg

			return nil
		},
	}

	handler := handleSignalWorkflow(fake)

	result, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"run_id":      "run-1",
		"signal_name": "advance",
		"args":        []any{"next-step"},
	})
	require.NoError(t, err)

	assert.Equal(t, "wf-1", capturedWorkflowID)
	assert.Equal(t, "run-1", capturedRunID)
	assert.Equal(t, "advance", capturedSignalName)
	require.NotNilf(t, capturedArg, "args must reach the SDK as a non-nil payload")
	// SignalWorkflow takes a single arg. The handler passes the
	// decoded []any as that one arg so the workflow's signal handler
	// can inspect it.
	assert.Equal(t, []any{"next-step"}, capturedArg)

	decoded := decodeResult(t, result)
	assert.Equal(t, true, decoded["signaled"])
	assert.Equal(t, "wf-1", decoded["workflow_id"])
	assert.Equal(t, "advance", decoded["signal_name"])
}

// TestHandleSignalWorkflow_NoArgs covers the absent-args case. The
// spec says "Empty args → pass nothing variadic"; the handler
// normalizes nil → []any{} so the workflow's signal handler can
// always inspect args without a nil check.
func TestHandleSignalWorkflow_NoArgs(t *testing.T) {
	t.Parallel()

	var capturedArg any

	fake := &fakeQuerySignalClient{
		signalWorkflowFn: func(
			_ context.Context,
			_, _, _ string,
			arg any,
		) error {
			capturedArg = arg

			return nil
		},
	}

	handler := handleSignalWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"signal_name": "tick",
	})
	require.NoError(t, err)
	require.NotNilf(t, capturedArg, "absent args → non-nil empty slice payload")
	argSlice, ok := capturedArg.([]any)
	require.Truef(t, ok, "payload must be []any, got %T", capturedArg)
	assert.Empty(t, argSlice)
}

// TestHandleSignalWorkflow_NoRunID covers the case where run_id is
// omitted. The handler passes the zero string through to the SDK.
func TestHandleSignalWorkflow_NoRunID(t *testing.T) {
	t.Parallel()

	var capturedRunID string

	fake := &fakeQuerySignalClient{
		signalWorkflowFn: func(
			_ context.Context,
			_, runID, _ string,
			_ any,
		) error {
			capturedRunID = runID

			return nil
		},
	}

	handler := handleSignalWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"signal_name": "tick",
	})
	require.NoError(t, err)
	assert.Empty(t, capturedRunID)
}

// TestHandleSignalWorkflow_MissingWorkflowID confirms the required-
// field check fires before the SDK is called.
func TestHandleSignalWorkflow_MissingWorkflowID(t *testing.T) {
	t.Parallel()

	called := false

	fake := &fakeQuerySignalClient{
		signalWorkflowFn: func(_ context.Context, _, _, _ string, _ any) error {
			called = true

			return nil
		},
	}

	handler := handleSignalWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"signal_name": "tick",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, errSignalWorkflowIDRequired)
	assert.False(t, called)
}

// TestHandleSignalWorkflow_MissingSignalName confirms the
// required-field check fires before the SDK is called.
func TestHandleSignalWorkflow_MissingSignalName(t *testing.T) {
	t.Parallel()

	called := false

	fake := &fakeQuerySignalClient{
		signalWorkflowFn: func(_ context.Context, _, _, _ string, _ any) error {
			called = true

			return nil
		},
	}

	handler := handleSignalWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, errSignalNameRequired)
	assert.False(t, called)
}

// TestHandleSignalWorkflow_ArgsDecodeError confirms that malformed
// args surface as a clear parse error before the SDK is called.
func TestHandleSignalWorkflow_ArgsDecodeError(t *testing.T) {
	t.Parallel()

	called := false

	fake := &fakeQuerySignalClient{
		signalWorkflowFn: func(_ context.Context, _, _, _ string, _ any) error {
			called = true

			return nil
		},
	}

	handler := handleSignalWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"signal_name": "tick",
		"args":        map[string]any{"x": 1},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse args")
	assert.False(t, called)
}

// TestHandleSignalWorkflow_SDKError confirms that an error returned
// from SignalWorkflow is wrapped with the tool name.
func TestHandleSignalWorkflow_SDKError(t *testing.T) {
	t.Parallel()

	sdkErr := errors.New("server unavailable")

	fake := &fakeQuerySignalClient{
		signalWorkflowFn: func(_ context.Context, _, _, _ string, _ any) error {
			return sdkErr
		},
	}

	handler := handleSignalWorkflow(fake)

	_, err := callTool(t, handler, map[string]any{
		"workflow_id": "wf-1",
		"signal_name": "tick",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
	assert.Containsf(t, err.Error(), "signal_workflow",
		"error must carry tool-name prefix")
}

// TestHandleSignalWorkflow_OuterArgsDecodeError covers the JSON
// unmarshal-level type-mismatch branch.
func TestHandleSignalWorkflow_OuterArgsDecodeError(t *testing.T) {
	t.Parallel()

	handler := handleSignalWorkflow(&fakeQuerySignalClient{})

	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Arguments: json.RawMessage(`{"workflow_id":"wf-1","signal_name":"s","args":"oops"}`),
		},
	}

	_, err := handler(t.Context(), req)
	require.Error(t, err)
	assert.Containsf(t, err.Error(), "parse args",
		"expected outer-args parse error, got: %v", err)
}

// -----------------------------------------------------------------------
// clientManager forwarder tests
// -----------------------------------------------------------------------

// TestClientManager_QueryWorkflowWithOptions_NilManager covers the
// nil-receiver guard on the QueryWorkflowWithOptions forwarder. We
// deliberately do NOT exercise the live-SDK path here: the SDK hangs
// for the default gRPC dial timeout (≈90s) when no Temporal server
// is reachable, which would blow up the test suite. The integration
// of the forwarder with a real *client.Client is covered by the
// SDK's own tests; our handler-level tests use the fake.
func TestClientManager_QueryWorkflowWithOptions_NilManager(t *testing.T) {
	t.Parallel()

	var nilCM *clientManager

	_, err := nilCM.QueryWorkflowWithOptions(t.Context(),
		&sdkclient.QueryWorkflowWithOptionsRequest{WorkflowID: "wf-1"})
	require.Error(t, err)
	require.ErrorIs(t, err, errClientManagerUninitialized)
}

// TestClientManager_SignalWorkflow_NilManager covers the
// nil-receiver guard on the SignalWorkflow forwarder. Same rationale
// as the query counterpart above.
func TestClientManager_SignalWorkflow_NilManager(t *testing.T) {
	t.Parallel()

	var nilCM *clientManager

	err := nilCM.SignalWorkflow(t.Context(), "wf-1", "", "tick", []any{})
	require.Error(t, err)
	require.ErrorIs(t, err, errClientManagerUninitialized)
}

// TestClientManager_QueryWorkflowWithOptions_Uninitialized covers the
// nil-SDK-client branch: a *clientManager whose inner client field is
// nil (e.g. constructed in a test) must surface the same
// errClientManagerUninitialized error as the nil-receiver case.
func TestClientManager_QueryWorkflowWithOptions_Uninitialized(t *testing.T) {
	t.Parallel()

	manager := &clientManager{}

	_, err := manager.QueryWorkflowWithOptions(t.Context(),
		&sdkclient.QueryWorkflowWithOptionsRequest{WorkflowID: "wf-1"})
	require.Error(t, err)
	require.ErrorIs(t, err, errClientManagerUninitialized)
}

// TestClientManager_SignalWorkflow_Uninitialized covers the
// nil-SDK-client branch for SignalWorkflow.
func TestClientManager_SignalWorkflow_Uninitialized(t *testing.T) {
	t.Parallel()

	manager := &clientManager{}

	err := manager.SignalWorkflow(t.Context(), "wf-1", "", "tick", []any{})
	require.Error(t, err)
	require.ErrorIs(t, err, errClientManagerUninitialized)
}

// -----------------------------------------------------------------------
// decodeArgsSlice / decodeQuerySignalArgs unit tests
// -----------------------------------------------------------------------

// TestDecodeArgsSlice covers the four branches in decodeArgsSlice:
// empty raw, nil raw, a valid JSON array, and a non-array JSON
// value.
func TestDecodeArgsSlice(t *testing.T) {
	t.Parallel()

	t.Run("empty raw", func(t *testing.T) {
		t.Parallel()

		got, err := decodeArgsSlice(json.RawMessage(nil))
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("null raw", func(t *testing.T) {
		t.Parallel()

		got, err := decodeArgsSlice(json.RawMessage(`null`))
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("valid array", func(t *testing.T) {
		t.Parallel()

		got, err := decodeArgsSlice(json.RawMessage(`["a", 1]`))
		require.NoError(t, err)
		assert.Equal(t, []any{"a", float64(1)}, got)
	})

	t.Run("non-array", func(t *testing.T) {
		t.Parallel()

		_, err := decodeArgsSlice(json.RawMessage(`{"x":1}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse args")
	})
}

// TestDecodeQuerySignalArgs covers the decodeQuerySignalArgs helper:
// nil arguments, valid JSON, and a malformed JSON value.
func TestDecodeQuerySignalArgs(t *testing.T) {
	t.Parallel()

	t.Run("nil arguments", func(t *testing.T) {
		t.Parallel()

		var target struct {
			WorkflowID string `json:"workflow_id"`
		}

		err := decodeQuerySignalArgs("query_workflow",
			&mcp.CallToolRequest{}, &target)
		require.NoErrorf(t, err, "nil arguments → empty struct, no error")
		assert.Empty(t, target.WorkflowID)
	})

	t.Run("valid JSON", func(t *testing.T) {
		t.Parallel()

		var target struct {
			WorkflowID string `json:"workflow_id"`
		}

		err := decodeQuerySignalArgs("query_workflow",
			&mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Arguments: json.RawMessage(`{"workflow_id":"wf-1"}`),
				},
			}, &target)
		require.NoError(t, err)
		assert.Equal(t, "wf-1", target.WorkflowID)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()

		var target struct {
			WorkflowID string `json:"workflow_id"`
		}

		err := decodeQuerySignalArgs("query_workflow",
			&mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Arguments: json.RawMessage(`{not-json`),
				},
			}, &target)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse query_workflow args")
	})
}

// -----------------------------------------------------------------------
// marshalToolResult unit test
// -----------------------------------------------------------------------

// TestMarshalToolResult_Object confirms the happy path: a JSON
// object round-trips through marshalToolResult with StructuredContent
// populated.
func TestMarshalToolResult_Object(t *testing.T) {
	t.Parallel()

	result, err := marshalToolResult(map[string]any{"x": 1})
	require.NoError(t, err)
	require.NotNilf(t, result, "marshalToolResult must return a non-nil result")
	require.NotEmptyf(t, result.StructuredContent, "object payload → StructuredContent")
	assert.Contains(t, result.Content[0].(*mcp.TextContent).Text, `"x":1`)
}

// TestMarshalToolResult_String confirms that a non-object value
// (string) still produces a valid result but with StructuredContent
// empty per the MCP spec.
func TestMarshalToolResult_String(t *testing.T) {
	t.Parallel()

	result, err := marshalToolResult("ok")
	require.NoError(t, err)
	require.NotNilf(t, result, "marshalToolResult must return a non-nil result")
	assert.Emptyf(t, result.StructuredContent, "string payload → no StructuredContent")
	assert.Equal(t, `"ok"`, result.Content[0].(*mcp.TextContent).Text)
}

// TestDecodeQueryResult covers the decodeQueryResult helper with a
// known JSON payload.
func TestDecodeQueryResult(t *testing.T) {
	t.Parallel()

	payloads, err := converter.GetDefaultDataConverter().ToPayloads(map[string]any{"k": "v"})
	require.NoError(t, err)

	result, decodeErr := decodeQueryResult(encodedValueFromPayloads{payloads: payloads})
	require.NoError(t, decodeErr)
	resultMap, ok := result.(map[string]any)
	require.Truef(t, ok, "decoded value must be map, got %T", result)
	assert.Equal(t, "v", resultMap["k"])
}

// TestDecodeQueryResult_PayloadsUnavailable covers the branch where
// the EncodedValue does not expose payloads.
func TestDecodeQueryResult_PayloadsUnavailable(t *testing.T) {
	t.Parallel()

	_, err := decodeQueryResult(encodedValueNoPayloads{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "payloads unavailable")
}

// -----------------------------------------------------------------------
// Connect-side registration tests
// -----------------------------------------------------------------------

// TestConnect_RegistersQueryWorkflowTool confirms that the
// query_workflow tool is in the Connect response with the expected
// shape.
func TestConnect_RegistersQueryWorkflowTool(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{})
	require.NoError(t, err)

	var found bool

	for _, toolEntry := range resp.Tools {
		if toolEntry.Name != "query_workflow" {
			continue
		}

		found = true

		require.NotNilf(t, toolEntry.InputSchema, "InputSchema must be set")
		require.NotNilf(t, toolEntry.OutputSchema, "OutputSchema must be set")
		require.NotNilf(t, toolEntry.Annotations, "annotations must be set")
		assert.Truef(t, toolEntry.Annotations.ReadOnlyHint,
			"query_workflow must be ReadOnlyHint=true")
		assert.Truef(
			t,
			toolEntry.Annotations.OpenWorldHint != nil && *toolEntry.Annotations.OpenWorldHint,
			"query_workflow must be OpenWorldHint=true",
		)
		assert.Contains(t, toolEntry.Description, "querySignalLoop"[0:1])
		assert.Contains(t, toolEntry.Description, "query_workflow")
		assert.NotNil(t, toolEntry.Handler)
	}

	assert.Truef(t, found, "query_workflow tool must be registered")
}

// TestConnect_RegistersSignalWorkflowTool confirms that the
// signal_workflow tool is in the Connect response with the expected
// shape: non-read-only, non-idempotent.
func TestConnect_RegistersSignalWorkflowTool(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{})
	require.NoError(t, err)

	var found bool

	for _, toolEntry := range resp.Tools {
		if toolEntry.Name != "signal_workflow" {
			continue
		}

		found = true

		require.NotNilf(t, toolEntry.InputSchema, "InputSchema must be set")
		require.NotNilf(t, toolEntry.OutputSchema, "OutputSchema must be set")
		require.NotNilf(t, toolEntry.Annotations, "annotations must be set")
		assert.Falsef(t, toolEntry.Annotations.ReadOnlyHint,
			"signal_workflow must be ReadOnlyHint=false")
		assert.Falsef(t, toolEntry.Annotations.IdempotentHint,
			"signal_workflow must be IdempotentHint=false (signals are NOT idempotent)")
		require.NotNilf(t, toolEntry.Annotations.DestructiveHint, "DestructiveHint must be set")
		assert.Falsef(t, *toolEntry.Annotations.DestructiveHint,
			"signal_workflow must be DestructiveHint=false")
		assert.Truef(
			t,
			toolEntry.Annotations.OpenWorldHint != nil && *toolEntry.Annotations.OpenWorldHint,
			"signal_workflow must be OpenWorldHint=true",
		)
		assert.Contains(t, toolEntry.Description, "querySignalLoop"[0:1])
		assert.Contains(t, toolEntry.Description, "signal_workflow")
		assert.NotNil(t, toolEntry.Handler)
	}

	assert.Truef(t, found, "signal_workflow tool must be registered")
}
