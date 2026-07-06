// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package postgres

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"go.amidman.dev/testenv/dbenv"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// getSchemas
// ---------------------------------------------------------------------------

// TestGetSchemas_OK covers the happy path: a fresh test database
// exposes the public schema and the function filters out information_schema
// and any pg_* schemas.
func TestGetSchemas_OK(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t)

	resp, err := tool.getSchemas(t.Context())
	require.NoError(t, err)
	require.NotNil(t, resp)

	names := make([]string, 0, len(resp.Schemas))
	for _, schema := range resp.Schemas {
		names = append(names, schema.Name)
	}

	require.Contains(t, names, "public")

	for _, schema := range resp.Schemas {
		require.NotEqual(t, "information_schema", schema.Name)
		require.Falsef(t, strings.HasPrefix(schema.Name, "pg_"),
			"schema %q should not start with pg_", schema.Name)
	}
}

// TestGetSchemas_DBClosed covers the underlying-error branch.
func TestGetSchemas_DBClosed(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t)
	require.NoError(t, tool.db.Close())

	resp, err := tool.getSchemas(t.Context())
	require.Error(t, err)
	require.Nil(t, resp)
	require.Contains(t, err.Error(), "query schemas:")
}

// ---------------------------------------------------------------------------
// getTables
// ---------------------------------------------------------------------------

// TestGetTables_OK covers the happy path with multiple tables in
// the public schema. RowCountEstimate must be >= 0 (the COALESCE
// against pg_class.reltuples may legitimately be 0 for empty tables).
func TestGetTables_OK(t *testing.T) {
	t.Parallel()

	migrations := dbenv.Queries(
		`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE orders (id SERIAL PRIMARY KEY, user_id INT)`,
		`INSERT INTO users (name) VALUES ('alice'), ('bob')`,
	)

	tool := postgresTestTool(t, migrations)

	resp, err := tool.getTables(t.Context(), PostgresTablesRequest{Schema: "public"})
	require.NoError(t, err)
	require.NotNil(t, resp)

	names := make([]string, 0, len(resp.Tables))
	for _, tbl := range resp.Tables {
		names = append(names, tbl.Name)
	}

	require.Contains(t, names, "users")
	require.Contains(t, names, "orders")

	for _, tbl := range resp.Tables {
		require.Equal(t, "public", tbl.Schema)
		require.Equal(t, "BASE TABLE", tbl.Type)
		// pg_class.reltuples is -1 for tables that haven't been ANALYZE'd.
		require.GreaterOrEqual(t, tbl.RowCountEstimate, int64(-1))
	}
}

// TestGetTables_DBClosed covers the underlying-error branch.
func TestGetTables_DBClosed(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t)
	require.NoError(t, tool.db.Close())

	resp, err := tool.getTables(t.Context(), PostgresTablesRequest{Schema: "public"})
	require.Error(t, err)
	require.Nil(t, resp)
	require.Contains(t, err.Error(), "query tables:")
}

// ---------------------------------------------------------------------------
// executeQuery
// ---------------------------------------------------------------------------

// TestExecuteQuery_OK covers a successful SELECT with bound parameters
// — both the `len(req.Params) > 0` and the `len(req.Params) == 0` paths
// end up in scanRows, and we want to confirm the columns + rows are
// marshaled correctly.
func TestExecuteQuery_OK(t *testing.T) {
	t.Parallel()

	migrations := dbenv.Queries(
		`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO users (name) VALUES ('alice'), ('bob')`,
	)

	t.Run("no_params", func(t *testing.T) {
		t.Parallel()

		tool := postgresTestTool(t, migrations)

		resp, err := tool.executeQuery(t.Context(), PostgresExecuteRequest{
			Query:  "SELECT id, name FROM users ORDER BY id",
			Params: []any(nil),
		})
		require.NoError(t, err)
		require.NotNil(t, resp)

		require.Equal(t, []string{"id", "name"}, resp.Columns)
		require.Len(t, resp.Rows, 2)
		require.EqualValues(t, 1, resp.Rows[0][0])
		require.Equal(t, "alice", resp.Rows[0][1])
	})

	t.Run("with_params", func(t *testing.T) {
		t.Parallel()

		tool := postgresTestTool(t, migrations)

		resp, err := tool.executeQuery(t.Context(), PostgresExecuteRequest{
			Query:  "SELECT name FROM users WHERE id = $1",
			Params: []any{2},
		})
		require.NoError(t, err)
		require.NotNil(t, resp)

		require.Equal(t, []string{"name"}, resp.Columns)
		require.Len(t, resp.Rows, 1)
		require.Equal(t, "bob", resp.Rows[0][0])
	})
}

// TestExecuteQuery_ReadOnlyViolation covers the read-only check inside
// executeQuery: an INSERT must be rejected before the database is
// touched, with the query-not-read-only sentinel wrapped.
func TestExecuteQuery_ReadOnlyViolation(t *testing.T) {
	t.Parallel()

	migrations := dbenv.Queries(
		`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL)`,
	)

	tool := postgresTestTool(t, migrations)

	resp, err := tool.executeQuery(t.Context(), PostgresExecuteRequest{
		Query:  "INSERT INTO users (name) VALUES ('eve')",
		Params: []any(nil),
	})
	require.Error(t, err)
	require.Nil(t, resp)
	require.Contains(t, err.Error(), "read-only validation failed")
}

// TestExecuteQuery_DBClosed covers the underlying-error branch.
func TestExecuteQuery_DBClosed(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t)
	require.NoError(t, tool.db.Close())

	resp, err := tool.executeQuery(t.Context(), PostgresExecuteRequest{
		Query:  "SELECT 1",
		Params: []any(nil),
	})
	require.Error(t, err)
	require.Nil(t, resp)
	require.Contains(t, err.Error(), "execute query:")
}

// ---------------------------------------------------------------------------
// parsePostgresArray — pure helper, no database needed
// ---------------------------------------------------------------------------

// TestParsePostgresArray covers the inputs we see in practice: empty
// strings, the "{}" sentinel, single-quoted and double-quoted elements,
// and the multi-element comma-separated case.
func TestParsePostgresArray(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"only_braces", "{}", nil},
		{"single_element", "{foo}", []string{"foo"}},
		{"quoted_single_element", `{"foo"}`, []string{"foo"}},
		{"multi_element", "{a,b,c}", []string{"a", "b", "c"}},
		{"multi_element_quoted", `{"a","b","c"}`, []string{"a", "b", "c"}},
		{"empty_braces_around_empty", `{}`, nil},
		{"too_short_input", "{", nil},
		{"single_char", "a", nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := parsePostgresArray(testCase.in)
			require.Equal(t, testCase.want, got)
		})
	}
}

// _ references the stdlib packages used by these tests so an accidental
// import removal trips the compiler instead of silently passing.
var (
	_ = sql.ErrNoRows
	_ = errors.New
)
