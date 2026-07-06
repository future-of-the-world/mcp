// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/decode"
)

// TestConnect_FailsToOpen exercises the full postgres.Connect path up
// to (and including) the openDB attempt. With a syntactically valid
// datasource that points to a closed port, the decode + validate +
// applyDefaults stages pass and the function tries to open the
// connection. The function returns a wrapped error from openDB
// (typically "connection refused" or "context deadline exceeded").
//
// This covers decodeConnect, applyDefaults, validate, the openDB
// entry, and the wrapped-error construction in Connect.
func TestConnect_FailsToOpen(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"datasource": "postgres://localhost:1/test?sslmode=disable",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "postgres:")
}

// TestDecodeConnect_RejectsMissingDatasource verifies the
// errEmptyDatasource sentinel is returned when the connect map has
// no datasource key. The type-assertion path is skipped (the key
// is missing, so cfg.Datasource stays zero); the validate() call
// then catches the empty value.
func TestDecodeConnect_RejectsMissingDatasource(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), make(map[string]any))
	require.Error(t, err)
	require.ErrorIs(t, err, errEmptyDatasource)
}

// TestDecodeConnect_AcceptsNumericDatasource verifies the new
// decode.AsString coercion: a numeric datasource value is
// stringified via fmt.Sprint rather than rejected. The connection
// still fails (port 12345 is not a real postgres host), but the
// decode step no longer rejects the YAML-natural value — that's
// the headline ask of the issue.
func TestDecodeConnect_AcceptsNumericDatasource(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"datasource": 12345,
	})
	// We expect an error from the openDB step (port 12345 is not
	// a real postgres host), but it must NOT be a decode error.
	require.Error(t, err)
	require.NotErrorIs(t, err, decode.ErrWrongType)
	require.NotErrorIs(t, err, decode.ErrNotSet)
}

// TestDecodeConnect_RejectsNonScalarDatasource verifies the
// new strict path: a non-scalar value (a map, here) where a
// string is expected produces a wrapped decode.ErrWrongType
// from Connect.
func TestDecodeConnect_RejectsNonScalarDatasource(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"datasource": map[string]any{"host": "localhost", "port": 5432},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, decode.ErrWrongType)
}

// TestDecodeConnect_RejectsEmptyDatasource verifies the
// errEmptyDatasource sentinel is returned when the datasource
// value is present but empty. The type assertion succeeds
// (empty string IS a string) so the empty string is set and
// then validate() catches it.
func TestDecodeConnect_RejectsEmptyDatasource(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), map[string]any{
		"datasource": "",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, errEmptyDatasource)
}

// TestRedactDSN covers the redaction helper used to keep the
// datasource out of log output and error messages. The function is
// a tiny pure helper; this test locks in its behavior across the
// postgres://, postgresql://, and libpq keyword/value schemes.
func TestRedactDSN(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		out  string
	}{
		{
			"postgres URL with password",
			"postgres://user:secret@host:5432/db",
			"postgres://user:***@host:5432/db",
		},
		{
			"postgresql URL with password",
			"postgresql://user:secret@host:5432/db",
			"postgresql://user:***@host:5432/db",
		},
		{"URL without password", "postgres://user@host:5432/db", "postgres://user@host:5432/db"},
		{
			"keyword/value form is returned unchanged",
			"host=localhost port=5432 user=foo",
			"host=localhost port=5432 user=foo",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, testCase.out, redactDSN(testCase.in))
		})
	}
}
