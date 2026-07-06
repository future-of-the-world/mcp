// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:exhaustruct,revive,wsl_v5 // test fixtures use partial structs and cluster assertions
package temporal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/tool"
)

// --- Connect ---

// allThirtyToolNames is the canonical allow-list of every tool that
// Connect must register. Used by TestConnect_RegistersAllThirtyTools
// (and its nil-map sibling) to fail loudly when an addition or
// rename drifts. Tool ordering is not asserted — only the
// set-equality of names — so per-feature tool lists can be appended
// in any order without breaking this check.
//
//nolint:gochecknoglobals // test fixture; one-shot allow-list
var allThirtyToolNames = []string{
	// 7 schedule
	"create_schedule",
	"list_schedules",
	"pause_schedule",
	"unpause_schedule",
	"delete_schedule",
	"trigger_schedule",
	"describe_schedule",
	// 8 workflow
	"start_workflow",
	"cancel_workflow",
	"terminate_workflow",
	"get_workflow_result",
	"describe_workflow",
	"list_workflows",
	"get_workflow_history",
	"continue_as_new",
	// 8 activity
	"start_activity",
	"execute_activity",
	"get_activity_result",
	"describe_activity",
	"list_activities",
	"count_activities",
	"cancel_activity",
	"terminate_activity",
	// 2 query/signal
	"query_workflow",
	"signal_workflow",
	// 5 batch
	"batch_signal",
	"batch_cancel",
	"batch_terminate",
	"batch_cancel_activities",
	"batch_terminate_activities",
}

// TestConnect_RegistersAllThirtyTools confirms that Connect with
// an empty connect map decodes the defaults, builds a client
// manager, and returns all thirty tools without an error. Each
// tool is asserted on the inventory shape (count, name set,
// embedded schemas, non-nil handlers) so a regression that drops a
// tool or leaves one without a schema would surface here.
//
// Per-tool behavior is exercised by the per-feature handler test
// files (schedule_handlers_test.go, workflow_handlers_test.go,
// activity_handlers_test.go, query_signal_handlers_test.go,
// batch_handlers_test.go).
func TestConnect_RegistersAllThirtyTools(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any{})
	require.NoError(t, err)
	require.Lenf(t, resp.Tools, len(allThirtyToolNames),
		"Connect must register all %d tools; got %d",
		len(allThirtyToolNames), len(resp.Tools))

	got := make(map[string]struct{}, len(resp.Tools))
	for _, entry := range resp.Tools {
		got[entry.Name] = struct{}{}
	}

	for _, want := range allThirtyToolNames {
		_, ok := got[want]
		require.Truef(t, ok, "missing tool: %s", want)

		entry := findToolByName(resp.Tools, want)
		require.NotNilf(t, entry, "tool not found by name: %s", want)
		require.NotNilf(t, entry.Handler, "tool has nil handler: %s", want)
		require.NotEmptyf(t, entry.Description, "tool has empty description: %s", want)
		require.NotEmptyf(t, entry.InputSchema, "tool has nil input schema: %s", want)
		require.NotEmptyf(t, entry.OutputSchema, "tool has nil output schema: %s", want)
		require.NotNilf(t, entry.Annotations, "tool has nil annotations: %s", want)
	}

	// Preamble presence — pick one tool per feature group. Each
	// tool's description is the per-group preamble concatenated
	// with the per-tool suffix, so the preamble substring must be
	// present. The assertions below cover all five feature groups
	// so a future refactor that drops one preamble is caught.
	for name, preamble := range map[string]string{
		"create_schedule": "Temporal schedule lifecycle",
		"start_workflow":  "Temporal workflow investigation loop",
		"start_activity":  "Temporal standalone activity lifecycle",
		"query_workflow":  "Temporal workflow query + signal",
		"batch_signal":    "Temporal batch operation loop",
	} {
		entry := findToolByName(resp.Tools, name)
		require.NotNilf(t, entry, "preamble check: tool %s not registered", name)
		assert.Containsf(t, entry.Description, preamble,
			"description missing %q preamble: %s", preamble, name)
	}
}

// TestConnect_NilMapRegistersAllThirtyTools is the same as the
// empty-map case but with a nil connect map. This shape comes up
// when the source is declared without a `connect:` block at all.
func TestConnect_NilMapRegistersAllThirtyTools(t *testing.T) {
	t.Parallel()

	resp, err := Connect(t.Context(), map[string]any(nil))
	require.NoError(t, err)
	assert.Lenf(t, resp.Tools, len(allThirtyToolNames),
		"nil connect map must still register all %d tools",
		len(allThirtyToolNames))
}

// findToolByName returns a pointer to the entry with the matching
// Name, or nil if absent. Tool.Tool embeds *mcp.Tool, so its Name
// and Annotations fields are accessed via promotion without a
// prefix.
//
// findToolByName returns a pointer to the entry with the matching
// Name, or nil if absent. Tool.Tool embeds *mcp.Tool, so its Name
// and Annotations fields are accessed via promotion without a
// prefix.
func findToolByName(tools []tool.Tool, name string) *tool.Tool {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}

	return nil
}

// TestConnect_AppliesDefaults confirms that omitting host and namespace
// fills in the documented defaults.
func TestConnect_AppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, "localhost:7233", cfg.Host)
	assert.Equal(t, "default", cfg.Namespace)
	assert.Nilf(t, cfg.TLSEnabled, "tls_enabled absent → tri-state nil")
	assert.Empty(t, cfg.APIKey)
	assert.Empty(t, cfg.TLSClientCertPath)
	assert.Empty(t, cfg.TLSClientKeyPath)
}

// TestConnect_HonorsExplicitValues confirms that every connect-map key
// round-trips through decodeConnect unchanged.
func TestConnect_HonorsExplicitValues(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"host":                 "temporal.prod.example.com:7233",
		"namespace":            "production",
		"api_key":              "tmprl-secret",
		"tls_client_cert_path": "/etc/temporal/cert.pem",
		"tls_client_key_path":  "/etc/temporal/key.pem",
	})
	require.NoError(t, err)
	assert.Equal(t, "temporal.prod.example.com:7233", cfg.Host)
	assert.Equal(t, "production", cfg.Namespace)
	assert.Equal(t, "tmprl-secret", cfg.APIKey)
	assert.Equal(t, "/etc/temporal/cert.pem", cfg.TLSClientCertPath)
	assert.Equal(t, "/etc/temporal/key.pem", cfg.TLSClientKeyPath)
}

// TestConnect_TLSEnabledTriState confirms the bool tri-state: absent →
// nil, true → &true, false → &false.
func TestConnect_TLSEnabledTriState(t *testing.T) {
	t.Parallel()

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		cfg, err := decodeConnect(map[string]any{})
		require.NoError(t, err)
		assert.Nil(t, cfg.TLSEnabled)
	})

	t.Run("true", func(t *testing.T) {
		t.Parallel()
		cfg, err := decodeConnect(map[string]any{"tls_enabled": true})
		require.NoError(t, err)
		require.NotNil(t, cfg.TLSEnabled)
		assert.True(t, *cfg.TLSEnabled)
	})

	t.Run("false", func(t *testing.T) {
		t.Parallel()
		cfg, err := decodeConnect(map[string]any{"tls_enabled": false})
		require.NoError(t, err)
		require.NotNil(t, cfg.TLSEnabled)
		assert.False(t, *cfg.TLSEnabled)
	})
}

// --- decodeConnect: error paths ---

// TestDecodeConnect_TLSEnabledWrongType confirms that a non-bool
// tls_enabled value is rejected with a clear error.
func TestDecodeConnect_TLSEnabledWrongType(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{"tls_enabled": "yes"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect.tls_enabled")
	assert.Contains(t, err.Error(), "expected bool")
}

// TestDecodeConnect_HostWrongType confirms that a non-scalar host value
// is rejected by decode.AsString.
func TestDecodeConnect_HostWrongType(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{"host": []string{"localhost:7233"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect.host")
}

// TestDecodeConnect_PartialMTLS confirms that exactly one of
// tls_client_cert_path / tls_client_key_path is rejected at the
// decode layer via the validate step.
func TestDecodeConnect_PartialMTLS(t *testing.T) {
	t.Parallel()

	t.Run("cert only", func(t *testing.T) {
		t.Parallel()
		_, err := Connect(t.Context(), map[string]any{
			"tls_client_cert_path": "/etc/cert.pem",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, errMTLSPartialCert)
	})

	t.Run("key only", func(t *testing.T) {
		t.Parallel()
		_, err := Connect(t.Context(), map[string]any{
			"tls_client_key_path": "/etc/key.pem",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, errMTLSPartialCert)
	})
}

// TestConnect_ValidateEmptyHost confirms that an explicit empty host
// triggers errHostEmpty through Connect's wrapped error chain.
func TestConnect_ValidateEmptyHost(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{"host": ""})
	require.Error(t, err)
	assert.ErrorIs(t, err, errHostEmpty)
}

// TestConnect_ValidateEmptyNamespace confirms the defensive namespace
// validation.
func TestConnect_ValidateEmptyNamespace(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{"namespace": ""})
	require.Error(t, err)
	assert.ErrorIs(t, err, errNamespaceEmpty)
}

// TestConnect_ErrorWrapping confirms that Connect wraps decode and
// validate errors with single-segment prefixes so log lines stay
// readable.
func TestConnect_ErrorWrapping(t *testing.T) {
	t.Parallel()

	t.Run("decode prefix", func(t *testing.T) {
		t.Parallel()
		_, err := Connect(t.Context(), map[string]any{"tls_enabled": "yes"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "temporal: decode:")
	})

	t.Run("validate prefix", func(t *testing.T) {
		t.Parallel()
		_, err := Connect(t.Context(), map[string]any{"host": ""})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "temporal: validate:")
	})
}

// TestValidate_BothMTLSPathsEmpty confirms that the validate step
// doesn't false-positive on missing mTLS materials.
func TestValidate_BothMTLSPathsEmpty(t *testing.T) {
	t.Parallel()

	cfg := &config{
		Host:      "localhost:7233",
		Namespace: "default",
	}
	require.NoError(t, cfg.validate())
}

// TestValidate_BothMTLSPathsSet confirms that the validate step doesn't
// false-positive on complete mTLS materials. The actual file existence
// is checked lazily by loadMTLSTLSConfig.
func TestValidate_BothMTLSPathsSet(t *testing.T) {
	t.Parallel()

	cfg := &config{
		Host:              "localhost:7233",
		Namespace:         "default",
		TLSClientCertPath: "/tmp/cert.pem",
		TLSClientKeyPath:  "/tmp/key.pem",
	}
	require.NoError(t, cfg.validate())
}

// TestDecodeConnect_NilStringValue confirms that a connect-map
// value set to nil for a string field is treated as "absent" — the
// field keeps its default. decodeConnect must return (cfg, nil)
// without surfacing an error. The early-return branch in applyString
// is exercised for every string field.
func TestDecodeConnect_NilStringValue(t *testing.T) {
	t.Parallel()

	t.Run("host nil", func(t *testing.T) {
		t.Parallel()
		cfg, err := decodeConnect(map[string]any{"host": any(nil)})
		require.NoError(t, err)
		assert.Equalf(t, defaultHost, cfg.Host, "nil host preserves default")
	})

	t.Run("namespace nil", func(t *testing.T) {
		t.Parallel()
		cfg, err := decodeConnect(map[string]any{"namespace": any(nil)})
		require.NoError(t, err)
		assert.Equalf(t, defaultNamespace, cfg.Namespace, "nil namespace preserves default")
	})

	t.Run("api_key nil", func(t *testing.T) {
		t.Parallel()
		cfg, err := decodeConnect(map[string]any{"api_key": any(nil)})
		require.NoError(t, err)
		assert.Emptyf(t, cfg.APIKey, "nil api_key → empty string")
	})

	t.Run("tls_client_cert_path nil", func(t *testing.T) {
		t.Parallel()
		cfg, err := decodeConnect(map[string]any{"tls_client_cert_path": any(nil)})
		require.NoError(t, err)
		assert.Empty(t, cfg.TLSClientCertPath)
	})

	t.Run("tls_client_key_path nil", func(t *testing.T) {
		t.Parallel()
		cfg, err := decodeConnect(map[string]any{"tls_client_key_path": any(nil)})
		require.NoError(t, err)
		assert.Empty(t, cfg.TLSClientKeyPath)
	})
}

// TestDecodeConnect_NonScalarStringField confirms that non-scalar
// values for namespace, api_key, tls_client_cert_path, and
// tls_client_key_path are rejected by decode.AsString. Together with
// TestDecodeConnect_HostWrongType this exercises every error-return
// branch inside decodeConnect, lifting it from 82.6% to 100%.
func TestDecodeConnect_NonScalarStringField(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		key  string
	}{
		{"namespace", "namespace"},
		{"api_key", "api_key"},
		{"tls_client_cert_path", "tls_client_cert_path"},
		{"tls_client_key_path", "tls_client_key_path"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeConnect(map[string]any{testCase.key: []string{"x"}})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "connect."+testCase.key)
		})
	}
}
