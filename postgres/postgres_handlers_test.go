// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package postgres

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"go.amidman.dev/testenv/dbenv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// callToolRequest builds a *mcp.CallToolRequest carrying the given JSON
// arguments. Session and Extra are left as typed nil so the request is
// usable in unit tests (the handler only consumes Params.Arguments).
func callToolRequest(t *testing.T, arguments string) *mcp.CallToolRequest {
	t.Helper()

	return &mcp.CallToolRequest{
		Session: (*mcp.ServerSession)(nil),
		Extra:   (*mcp.RequestExtra)(nil),
		Params: &mcp.CallToolParamsRaw{
			Meta:      mcp.Meta(nil),
			Name:      "postgres",
			Arguments: json.RawMessage(arguments),
		},
	}
}

// closeToolDB closes the *sql.DB on the tool so subsequent calls surface
// "sql: database is closed" from the underlying driver.
func closeToolDB(t *testing.T, tool *Tool) {
	t.Helper()

	require.NoError(t, tool.db.Close())
}

// ---------------------------------------------------------------------------
// handleListSchemas
// ---------------------------------------------------------------------------

// TestHandleListSchemas_OK covers the happy path: a fresh test database
// has the public and (reusable) test schemas visible, and the handler
// returns the list as JSON in Content / StructuredContent.
func TestHandleListSchemas_OK(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t)

	handler := handleListSchemas(tool)

	result, err := handler(t.Context(), callToolRequest(t, "{}"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)

	var resp PostgresSchemasResponse
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &resp))

	names := make([]string, 0, len(resp.Schemas))
	for _, schema := range resp.Schemas {
		names = append(names, schema.Name)
	}

	require.Contains(t, names, "public")

	// pg_* and information_schema must be filtered out.
	for _, schema := range resp.Schemas {
		require.NotEqual(t, "information_schema", schema.Name)
		require.Falsef(t, strings.HasPrefix(schema.Name, "pg_"),
			"schema %q should not start with pg_", schema.Name)
	}

	require.NotNil(t, result.StructuredContent)
}

// TestHandleListSchemas_DBClosed covers the underlying-error branch:
// closing the *sql.DB before invoking the handler makes getSchemas
// fail with a wrapped "sql: database is closed" error.
func TestHandleListSchemas_DBClosed(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t)
	closeToolDB(t, tool)

	handler := handleListSchemas(tool)

	result, err := handler(t.Context(), callToolRequest(t, "{}"))
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "list schemas:")
}

// ---------------------------------------------------------------------------
// handleListTables
// ---------------------------------------------------------------------------

// TestHandleListTables_OK covers the happy path with a populated public
// schema: two tables ("users" and "orders") are created via migrations
// and the handler must return both, with row_count_estimate populated
// from pg_class.reltuples.
func TestHandleListTables_OK(t *testing.T) {
	t.Parallel()

	migrations := dbenv.Queries(
		`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE orders (id SERIAL PRIMARY KEY, user_id INT)`,
		`INSERT INTO users (name) VALUES ('alice'), ('bob')`,
	)

	tool := postgresTestTool(t, migrations)

	handler := handleListTables(tool)

	result, err := handler(t.Context(), callToolRequest(t, `{"schema":"public"}`))
	require.NoError(t, err)
	require.NotNil(t, result)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)

	var resp PostgresTablesResponse
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &resp))

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

	require.NotNil(t, result.StructuredContent)
}

// TestHandleListTables_BadJSON covers the json.Unmarshal error branch:
// passing malformed Arguments must produce a wrapped "parse list_tables args:"
// error before the handler ever touches the database.
func TestHandleListTables_BadJSON(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t)

	handler := handleListTables(tool)

	result, err := handler(t.Context(), callToolRequest(t, `not-json`))
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "parse list_tables args:")
}

// TestHandleListTables_DBClosed covers the underlying-error branch.
func TestHandleListTables_DBClosed(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t)
	closeToolDB(t, tool)

	handler := handleListTables(tool)

	result, err := handler(t.Context(), callToolRequest(t, `{"schema":"public"}`))
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "list tables:")
}

// ---------------------------------------------------------------------------
// handleExecuteQuery
// ---------------------------------------------------------------------------

// TestHandleExecuteQuery_OK covers a successful read-only SELECT: the
// handler runs the query, the response carries the column names, and
// the rows are JSON-marshaled correctly.
func TestHandleExecuteQuery_OK(t *testing.T) {
	t.Parallel()

	migrations := dbenv.Queries(
		`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO users (name) VALUES ('alice'), ('bob')`,
	)

	tool := postgresTestTool(t, migrations)

	handler := handleExecuteQuery(tool)

	result, err := handler(
		t.Context(),
		callToolRequest(t, `{"query":"SELECT id, name FROM users ORDER BY id"}`),
	)
	require.NoError(t, err)
	require.NotNil(t, result)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)

	var resp PostgresExecuteResponse
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &resp))

	require.Equal(t, []string{"id", "name"}, resp.Columns)
	require.Len(t, resp.Rows, 2)

	require.EqualValues(t, 1, resp.Rows[0][0])
	require.Equal(t, "alice", resp.Rows[0][1])
	require.EqualValues(t, 2, resp.Rows[1][0])
	require.Equal(t, "bob", resp.Rows[1][1])

	require.NotNil(t, result.StructuredContent)
}

// TestHandleExecuteQuery_WithParams covers the path that builds the
// prepared-statement call (req.Params > 0): the dollar-sign placeholders
// are bound to the supplied []any values.
func TestHandleExecuteQuery_WithParams(t *testing.T) {
	t.Parallel()

	migrations := dbenv.Queries(
		`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO users (name) VALUES ('alice'), ('bob')`,
	)

	tool := postgresTestTool(t, migrations)

	handler := handleExecuteQuery(tool)

	result, err := handler(
		t.Context(),
		callToolRequest(t, `{"query":"SELECT name FROM users WHERE id = $1","params":[2]}`),
	)
	require.NoError(t, err)
	require.NotNil(t, result)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)

	var resp PostgresExecuteResponse
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &resp))

	require.Equal(t, []string{"name"}, resp.Columns)
	require.Len(t, resp.Rows, 1)
	require.Equal(t, "bob", resp.Rows[0][0])
}

// TestHandleExecuteQuery_ReadOnlyViolation covers the read-only check
// inside executeQuery: an INSERT must be rejected before the database
// is touched, with the query-not-read-only sentinel wrapped.
func TestHandleExecuteQuery_ReadOnlyViolation(t *testing.T) {
	t.Parallel()

	migrations := dbenv.Queries(
		`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL)`,
	)

	tool := postgresTestTool(t, migrations)

	handler := handleExecuteQuery(tool)

	result, err := handler(
		t.Context(),
		callToolRequest(t, `{"query":"INSERT INTO users (name) VALUES ('eve')"}`),
	)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "read-only validation failed")
}

// TestHandleExecuteQuery_BadJSON covers the json.Unmarshal error branch.
func TestHandleExecuteQuery_BadJSON(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t)

	handler := handleExecuteQuery(tool)

	result, err := handler(t.Context(), callToolRequest(t, `not-json`))
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "parse execute_query args:")
}

// TestHandleExecuteQuery_DBClosed covers the underlying-error branch:
// closing the *sql.DB before invoking the handler surfaces the
// driver error wrapped by the handler.
func TestHandleExecuteQuery_DBClosed(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t)
	closeToolDB(t, tool)

	handler := handleExecuteQuery(tool)

	result, err := handler(t.Context(), callToolRequest(t, `{"query":"SELECT 1"}`))
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "execute query:")
}

// ---------------------------------------------------------------------------
// handleGetTableInfo
// ---------------------------------------------------------------------------

// TestHandleGetTableInfo_OK covers the happy path with a single users
// table: the handler recursively fetches table info, the response
// includes the requested table, and the rows estimate is populated.
func TestHandleGetTableInfo_OK(t *testing.T) {
	t.Parallel()

	migrations := dbenv.Queries(
		`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO users (name) VALUES ('alice'), ('bob')`,
	)

	tool := postgresTestTool(t, migrations)

	handler := handleGetTableInfo(tool)

	result, err := handler(t.Context(), callToolRequest(t, `{"schema":"public","table":"users"}`))
	require.NoError(t, err)
	require.NotNil(t, result)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)

	var resp PostgresTableInfoResponse
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &resp))

	require.NotEmpty(t, resp.Tables)

	var found *DetailedTableInfo

	for i := range resp.Tables {
		if resp.Tables[i].Name == "users" {
			found = &resp.Tables[i]
			break
		}
	}

	require.NotNilf(t, found, "users table should be present in the response")

	require.Equal(t, "public", found.Schema)
	require.Equal(t, "BASE TABLE", found.Type)
	// Postgres stores -1 in pg_class.reltuples for tables that haven't been
	// ANALYZE'd yet. The COALESCE only protects against NULL, so the value
	// can legitimately be either 0, a positive count, or -1 (unknown).
	require.GreaterOrEqual(t, found.RowCountEst, int64(-1))
	require.NotEmpty(t, found.Columns)

	require.NotNil(t, result.StructuredContent)
}

// TestHandleGetTableInfo_BadJSON covers the json.Unmarshal error branch.
func TestHandleGetTableInfo_BadJSON(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t)

	handler := handleGetTableInfo(tool)

	result, err := handler(t.Context(), callToolRequest(t, `not-json`))
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "parse get_table_info args:")
}

// TestHandleGetTableInfo_DBClosed covers the underlying-error branch.
func TestHandleGetTableInfo_DBClosed(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t)
	closeToolDB(t, tool)

	handler := handleGetTableInfo(tool)

	result, err := handler(t.Context(), callToolRequest(t, `{"schema":"public","table":"users"}`))
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "get table info:")
}

// _ references sql.ErrNoRows to ensure the database/sql import stays
// even if future edits drop direct usage; otherwise the tests would still
// build but lose access to the package.
var _ = sql.ErrNoRows
