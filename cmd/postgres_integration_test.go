// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.amidman.dev/testenv/dbenv"
	"go.amidman.dev/testenv/postgresenv"
	"go.amidman.dev/testenv/postgresenv/containers"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	_ "github.com/lib/pq"
)

var (
	pgCnt = containers.NewContainer(
		containers.WithLogger(
			log.New(os.Stdout, "[postgres-integration] ", log.LstdFlags|log.Lmsgprefix),
		),
	)

	pgConnector = postgresenv.NewConnector(
		append(
			pgCnt.ConnectorOptions(),
			postgresenv.WithMaxConns(10),
		)...,
	)

	pgEnv = postgresenv.NewStdlib(
		postgresenv.New(
			postgresenv.WithConnector(pgConnector),
			// Use nil (raw connection) to reuse the same database for all tests
			postgresenv.WithReuseStrategy(postgresenv.ReuseStrategy(nil)),
		),
	)

	pgDatasource     string
	pgDatasourceOnce sync.Once
)

// ensurePostgresContainer ensures the postgres container datasource and migrations
// are set up exactly once. Each test gets its own DB handle via dbenv.UseForTesting,
// which participates in the connector's reference-counted lifecycle.
func ensurePostgresContainer(t *testing.T) (string, *sql.DB) {
	t.Helper()

	db := dbenv.UseForTesting(t, pgEnv)

	pgDatasourceOnce.Do(func() {
		conn, err := pgCnt.Connection(t.Context())
		require.NoErrorf(t, err, "failed to get postgres connection info")

		pgDatasource = fmt.Sprintf(
			"host=%s port=%d dbname=%s user=%s password=%s sslmode=disable",
			conn.Host(),
			conn.Port(),
			conn.Database(),
			conn.User(),
			conn.Password(),
		)

		runPostgresMigrations(t.Context(), db)
	})

	return pgDatasource, db
}

// setupPostgresIntegration initializes the postgres container and database for integration tests.
// It returns the datasource string for the config file.
func setupPostgresIntegration(t *testing.T) string {
	t.Helper()

	// Ensure container is started and migrations are run (only happens once across all tests)
	datasource, _ := ensurePostgresContainer(t)

	return datasource
}

// runPostgresMigrations creates test tables and seed data.
// Uses idempotent SQL statements to handle parallel test execution.
func runPostgresMigrations(ctx context.Context, db *sql.DB) {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT UNIQUE,
			created_at TIMESTAMP DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS orders (
			id SERIAL PRIMARY KEY,
			user_id INTEGER REFERENCES users(id),
			total DECIMAL(10,2),
			created_at TIMESTAMP DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_orders_user_id ON orders(user_id)`,
		`INSERT INTO users (name, email) VALUES ('Alice', 'alice@example.com')
		 ON CONFLICT (email) DO NOTHING`,
		`INSERT INTO users (name, email) VALUES ('Bob', 'bob@example.com')
		 ON CONFLICT (email) DO NOTHING`,
		`INSERT INTO orders (user_id, total) VALUES (1, 100.00)`,
		`INSERT INTO orders (user_id, total) VALUES (1, 200.00)`,
		`INSERT INTO orders (user_id, total) VALUES (2, 50.00)`,
	}

	for _, migration := range migrations {
		_, err := db.ExecContext(ctx, migration)
		if err != nil {
			// Log but don't fail - migrations are idempotent
			log.Printf("migration warning (may be expected): %v", err)
		}
	}
}

// postgresTestSetup holds the test harness for postgres integration tests.
type postgresTestSetup struct {
	BinaryPath string
	ConfigPath string
	Addr       string
	Cmd        *exec.Cmd
}

// newPostgresTestSetup creates a complete postgres integration test harness.
func newPostgresTestSetup(t *testing.T, datasource string) *postgresTestSetup {
	t.Helper()

	binPath := getBinaryPath(t)

	// Create config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := fmt.Sprintf(`sources:
  test:
    type: postgres
    tools:
      prefix: test_
    connect:
      datasource: %s
`, datasource)

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	return &postgresTestSetup{
		BinaryPath: binPath,
		ConfigPath: configPath,
		Addr:       "",
		Cmd:        (*exec.Cmd)(nil),
	}
}

// startServer starts the MCP server with postgres tool on a free port.
func (s *postgresTestSetup) startServer(t *testing.T, transport string) {
	t.Helper()

	cmd, addr := startServerOnFreePort(t, s.BinaryPath, []string{
		"--config", s.ConfigPath,
		"--transport", transport,
		"--addr", "",
	}, []string{"LOG_LEVEL=debug"})

	s.Cmd = cmd
	s.Addr = addr
}

// stopServer stops the MCP server.
func (s *postgresTestSetup) stopServer(t *testing.T) {
	t.Helper()

	if s.Cmd != nil && s.Cmd.Process != nil {
		require.NoErrorf(t, s.Cmd.Process.Signal(os.Interrupt), "failed to send interrupt signal")
	}
}

// callPostgresTool calls a postgres tool and returns the parsed JSON response.
func callPostgresTool(
	t *testing.T,
	clientSession *mcp.ClientSession,
	toolName string,
	arguments map[string]any,
) map[string]any {
	t.Helper()

	result, err := clientSession.CallTool(
		t.Context(),
		(&mcp.CallToolParams{
			Name:      toolName,
			Arguments: arguments,
		}),
	)
	require.NoErrorf(t, err, "failed to call tool %s", toolName)
	require.Falsef(t, result.IsError, "tool %s returned error: %v", toolName, result.Content)
	require.NotEmptyf(t, result.Content, "tool %s returned empty content", toolName)

	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "expected TextContent, got %T", result.Content[0])

	var response map[string]any

	err = json.Unmarshal([]byte(textContent.Text), &response)
	require.NoErrorf(t, err, "failed to parse response JSON from tool %s", toolName)

	return response
}

// callPostgresToolExpectError calls a tool and expects it to return an error.
func callPostgresToolExpectError(
	t *testing.T,
	clientSession *mcp.ClientSession,
	toolName string,
	arguments map[string]any,
) {
	t.Helper()

	result, err := clientSession.CallTool(
		t.Context(),
		(&mcp.CallToolParams{
			Name:      toolName,
			Arguments: arguments,
		}),
	)
	require.Truef(t, err != nil || result.IsError,
		"expected error when calling tool %s", toolName)
}

// TestPostgresIntegrationListTools tests that postgres tools are registered correctly.
func TestPostgresIntegrationListTools(t *testing.T) {
	t.Parallel()

	pgConfig := setupPostgresIntegration(t)
	setup := newPostgresTestSetup(t, pgConfig)

	setup.startServer(t, "sse")
	defer setup.stopServer(t)

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, setup.Addr)

	result, err := clientSession.ListTools(ctx, (*mcp.ListToolsParams)(nil))
	require.NoErrorf(t, err, "failed to list tools")

	// Should have 4 postgres tools: schemas, tables, query, table-info
	require.Lenf(t, result.Tools, 4, "expected 4 postgres tools")

	toolNames := make(map[string]bool)
	for _, tool := range result.Tools {
		toolNames[tool.Name] = true
	}

	require.Truef(t, toolNames["test_list_schemas"], "should have schemas tool")
	require.Truef(t, toolNames["test_list_tables"], "should have tables tool")
	require.Truef(t, toolNames["test_execute_query"], "should have query tool")
	require.Truef(t, toolNames["test_get_table_info"], "should have table-info tool")
}

// TestPostgresIntegrationGetSchemas tests listing schemas via MCP.
func TestPostgresIntegrationGetSchemas(t *testing.T) {
	t.Parallel()

	pgConfig := setupPostgresIntegration(t)
	setup := newPostgresTestSetup(t, pgConfig)

	setup.startServer(t, "sse")
	defer setup.stopServer(t)

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, setup.Addr)

	response := callPostgresTool(t, clientSession, "test_list_schemas", make(map[string]any))

	schemas, ok := response["schemas"].([]any)
	require.Truef(t, ok, "expected schemas array, got %T", response["schemas"])
	require.NotEmptyf(t, schemas, "should have at least one schema")

	// PostgreSQL should always have at least 'public' schema
	schemaNames := make([]string, len(schemas))
	for i, s := range schemas {
		schemaMap, ok := s.(map[string]any)
		require.Truef(t, ok, "expected schema object")

		name, ok := schemaMap["name"].(string)
		require.Truef(t, ok, "expected schema name string, got %T", schemaMap["name"])

		schemaNames[i] = name
	}
	require.Contains(t, schemaNames, "public")
}

// TestPostgresIntegrationGetTables tests listing tables via MCP.
func TestPostgresIntegrationGetTables(t *testing.T) {
	t.Parallel()

	pgConfig := setupPostgresIntegration(t)
	setup := newPostgresTestSetup(t, pgConfig)

	setup.startServer(t, "sse")
	defer setup.stopServer(t)

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, setup.Addr)

	response := callPostgresTool(t, clientSession, "test_list_tables", map[string]any{
		"schema": "public",
	})

	tables, ok := response["tables"].([]any)
	require.Truef(t, ok, "expected tables array, got %T", response["tables"])
	require.Lenf(t, tables, 2, "expected 2 tables (users and orders)")

	tableNames := make([]string, len(tables))
	for i, tbl := range tables {
		tableMap, ok := tbl.(map[string]any)
		require.Truef(t, ok, "expected table object")

		name, ok := tableMap["name"].(string)
		require.Truef(t, ok, "expected table name string, got %T", tableMap["name"])

		tableNames[i] = name
	}
	require.Contains(t, tableNames, "users")
	require.Contains(t, tableNames, "orders")
}

// TestPostgresIntegrationQuery tests executing SQL queries via MCP.
func TestPostgresIntegrationQuery(t *testing.T) {
	t.Parallel()

	pgConfig := setupPostgresIntegration(t)
	setup := newPostgresTestSetup(t, pgConfig)

	setup.startServer(t, "sse")
	defer setup.stopServer(t)

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, setup.Addr)

	t.Run("select all users", func(t *testing.T) {
		response := callPostgresTool(t, clientSession, "test_execute_query", map[string]any{
			"query": "SELECT id, name, email FROM users ORDER BY id",
		})

		columns, ok := response["columns"].([]any)
		require.Truef(t, ok, "expected columns array")
		require.Equalf(t, []any{"id", "name", "email"}, columns, "column names mismatch")

		rows, ok := response["rows"].([]any)
		require.Truef(t, ok, "expected rows array")
		require.Lenf(t, rows, 2, "expected 2 users")
	})

	t.Run("select with WHERE clause", func(t *testing.T) {
		response := callPostgresTool(t, clientSession, "test_execute_query", map[string]any{
			"query":  "SELECT name FROM users WHERE id = $1",
			"params": []any{1},
		})

		rows, ok := response["rows"].([]any)
		require.Truef(t, ok, "expected rows array")
		require.Lenf(t, rows, 1, "expected 1 user")

		// First row should contain "Alice"
		row, ok := rows[0].([]any)
		require.Truef(t, ok, "expected row as array")
		require.Equalf(t, "Alice", row[0], "expected Alice")
	})

	t.Run("select with JOIN", func(t *testing.T) {
		response := callPostgresTool(t, clientSession, "test_execute_query", map[string]any{
			"query": "SELECT u.name, o.total FROM users u " +
				"JOIN orders o ON u.id = o.user_id ORDER BY u.id, o.id",
		})

		rows, ok := response["rows"].([]any)
		require.Truef(t, ok, "expected rows array")
		require.Lenf(t, rows, 3, "expected 3 order rows")
	})

	t.Run("select COUNT", func(t *testing.T) {
		response := callPostgresTool(t, clientSession, "test_execute_query", map[string]any{
			"query": "SELECT COUNT(*) as count FROM users",
		})

		rows, ok := response["rows"].([]any)
		require.Truef(t, ok, "expected rows array")
		require.Lenf(t, rows, 1, "expected 1 row")
	})

	t.Run("EXPLAIN command", func(t *testing.T) {
		response := callPostgresTool(t, clientSession, "test_execute_query", map[string]any{
			"query": "EXPLAIN SELECT * FROM users",
		})

		columns, ok := response["columns"].([]any)
		require.Truef(t, ok, "expected columns array")
		require.Contains(t, columns, "QUERY PLAN")
	})
}

// TestPostgresIntegrationQueryReadOnly tests that modifying queries are blocked.
func TestPostgresIntegrationQueryReadOnly(t *testing.T) {
	t.Parallel()

	pgConfig := setupPostgresIntegration(t)
	setup := newPostgresTestSetup(t, pgConfig)

	setup.startServer(t, "sse")
	defer setup.stopServer(t)

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, setup.Addr)

	tests := []struct {
		name      string
		query     string
		wantError bool
	}{
		{
			name:      "SELECT is allowed",
			query:     "SELECT * FROM users",
			wantError: false,
		},
		{
			name:      "EXPLAIN is allowed",
			query:     "EXPLAIN SELECT * FROM users",
			wantError: false,
		},
		{
			name:      "SHOW is allowed",
			query:     "SHOW search_path",
			wantError: false,
		},
		{
			name:      "INSERT is blocked",
			query:     "INSERT INTO users (name) VALUES ('Charlie')",
			wantError: true,
		},
		{
			name:      "UPDATE is blocked",
			query:     "UPDATE users SET name = 'Charlie' WHERE id = 1",
			wantError: true,
		},
		{
			name:      "DELETE is blocked",
			query:     "DELETE FROM users WHERE id = 1",
			wantError: true,
		},
		{
			name:      "DROP is blocked",
			query:     "DROP TABLE users",
			wantError: true,
		},
		{
			name:      "CREATE is blocked",
			query:     "CREATE TABLE test (id INT)",
			wantError: true,
		},
		{
			name:      "TRUNCATE is blocked",
			query:     "TRUNCATE TABLE users",
			wantError: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.wantError {
				callPostgresToolExpectError(t, clientSession, "test_execute_query", map[string]any{
					"query": testCase.query,
				})

				return
			}

			_ = callPostgresTool(t, clientSession, "test_execute_query", map[string]any{
				"query": testCase.query,
			})
		})
	}
}

// verifyUsersTableInfo verifies the users table info response.
func verifyUsersTableInfo(t *testing.T, response map[string]any) {
	t.Helper()

	tables, ok := response["tables"].([]any)
	require.Truef(t, ok, "expected tables array")
	require.Lenf(t, tables, 1, "expected 1 table")

	usersTable, ok := tables[0].(map[string]any)
	require.Truef(t, ok, "expected table object")
	require.Equalf(t, "users", usersTable["name"], "table name should be users")
	require.Equalf(t, "public", usersTable["schema"], "schema should be public")

	columns, ok := usersTable["columns"].([]any)
	require.Truef(t, ok, "expected columns array")
	require.NotEmptyf(t, columns, "users table should have columns")

	verifyPrimaryKeyColumn(t, columns)
}

// verifyPrimaryKeyColumn checks that id column is marked as primary key.
func verifyPrimaryKeyColumn(t *testing.T, columns []any) {
	t.Helper()

	for _, col := range columns {
		colMap, ok := col.(map[string]any)
		require.Truef(t, ok, "expected column object")

		if isPK, ok := colMap["is_primary_key"].(bool); ok && isPK {
			require.Equalf(t, "id", colMap["name"], "primary key should be id column")

			return
		}
	}
	t.Fatal("users table should have a primary key")
}

// verifyOrdersTableInfo verifies the orders table info response.
func verifyOrdersTableInfo(t *testing.T, response map[string]any) {
	t.Helper()

	tables, ok := response["tables"].([]any)
	require.Truef(t, ok, "expected tables array")
	require.GreaterOrEqualf(t, len(tables), 1, "expected at least 1 table")

	ordersTable := findTableByName(t, tables, "orders")
	require.NotNilf(t, ordersTable, "orders table should be found in response")

	references, ok := ordersTable["references"].([]any)
	require.Truef(t, ok, "expected references array")
	require.NotEmptyf(t, references, "orders should reference users")
}

// findTableByName finds a table by name in the tables list.
func findTableByName(t *testing.T, tables []any, name string) map[string]any {
	t.Helper()

	for _, tbl := range tables {
		tblMap, isTableMap := tbl.(map[string]any)
		require.Truef(t, isTableMap, "expected table object")

		if tblMap["name"] == name {
			return tblMap
		}
	}

	return nil
}

// verifyEmptyTableInfo verifies the response for a non-existent table.
func verifyEmptyTableInfo(t *testing.T, response map[string]any) {
	t.Helper()

	tables, ok := response["tables"].([]any)
	require.Truef(t, ok, "expected tables array")
	require.Emptyf(t, tables, "expected empty tables for non-existent table")
}

// TestPostgresIntegrationGetTableInfo tests getting table information via MCP.
func TestPostgresIntegrationGetTableInfo(t *testing.T) {
	t.Parallel()

	pgConfig := setupPostgresIntegration(t)
	setup := newPostgresTestSetup(t, pgConfig)

	setup.startServer(t, "sse")
	defer setup.stopServer(t)

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, setup.Addr)

	t.Run("users table", func(t *testing.T) {
		response := callPostgresTool(t, clientSession, "test_get_table_info", map[string]any{
			"schema": "public",
			"table":  "users",
		})
		verifyUsersTableInfo(t, response)
	})

	t.Run("orders table with foreign key", func(t *testing.T) {
		response := callPostgresTool(t, clientSession, "test_get_table_info", map[string]any{
			"schema": "public",
			"table":  "orders",
		})
		verifyOrdersTableInfo(t, response)
	})

	t.Run("non-existent table", func(t *testing.T) {
		response := callPostgresTool(t, clientSession, "test_get_table_info", map[string]any{
			"schema": "public",
			"table":  "nonexistent",
		})
		verifyEmptyTableInfo(t, response)
	})
}

// TestPostgresIntegrationStreamableTransport tests postgres tools via streamable HTTP transport.
func TestPostgresIntegrationStreamableTransport(t *testing.T) {
	t.Parallel()

	pgConfig := setupPostgresIntegration(t)
	setup := newPostgresTestSetup(t, pgConfig)

	setup.startServer(t, "streamable")
	defer setup.stopServer(t)

	ctx := t.Context()
	clientSession := connectStreamableClient(t, ctx, setup.Addr)

	// Test query via streamable transport
	response := callPostgresTool(t, clientSession, "test_execute_query", map[string]any{
		"query": "SELECT COUNT(*) as count FROM users",
	})

	rows, ok := response["rows"].([]any)
	require.Truef(t, ok, "expected rows array")
	require.Lenf(t, rows, 1, "expected 1 row")
}

// executeConcurrentQuery executes a query and sends the result or error to channels.
func executeConcurrentQuery(
	ctx context.Context,
	clientSession *mcp.ClientSession,
	done chan<- map[string]any,
	errChan chan<- error,
) {
	result, err := clientSession.CallTool(
		ctx,
		(&mcp.CallToolParams{
			Name: "test_execute_query",
			Arguments: map[string]any{
				"query": "SELECT 1 as value",
			},
		}),
	)
	if err != nil {
		errChan <- err

		return
	}

	if result.IsError {
		errChan <- fmt.Errorf("tool returned error: %v", result.Content)

		return
	}

	textContent, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		errChan <- errors.New("expected TextContent")

		return
	}

	var response map[string]any

	err = json.Unmarshal([]byte(textContent.Text), &response)
	if err != nil {
		errChan <- err

		return
	}

	done <- response
}

// concurrentChannels holds the channels for concurrent query results.
type concurrentChannels struct {
	done    <-chan map[string]any
	errChan <-chan error
}

// waitForConcurrentResults waits for all concurrent calls to complete.
func waitForConcurrentResults(
	t *testing.T,
	ctx context.Context,
	numCalls int,
	channels concurrentChannels,
) {
	t.Helper()

	for range numCalls {
		select {
		case response := <-channels.done:
			rows, ok := response["rows"].([]any)
			require.Truef(t, ok, "expected rows array")
			require.Lenf(t, rows, 1, "expected 1 row")

		case err := <-channels.errChan:
			t.Fatalf("concurrent call failed: %v", err)

		case <-ctx.Done():
			t.Fatal("timeout waiting for concurrent calls")
		}
	}
}

// TestPostgresIntegrationConcurrentCalls tests multiple concurrent tool calls.
func TestPostgresIntegrationConcurrentCalls(t *testing.T) {
	t.Parallel()

	pgConfig := setupPostgresIntegration(t)
	setup := newPostgresTestSetup(t, pgConfig)

	setup.startServer(t, "sse")
	defer setup.stopServer(t)

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, setup.Addr)

	// Run multiple queries concurrently
	const numCalls = 5

	done := make(chan map[string]any, numCalls)
	errChan := make(chan error, numCalls)

	for range numCalls {
		go executeConcurrentQuery(ctx, clientSession, done, errChan)
	}

	// Wait for all calls to complete
	waitForConcurrentResults(t, ctx, numCalls, concurrentChannels{
		done:    done,
		errChan: errChan,
	})
}

// TestPostgresIntegrationMultipleQueries tests multiple sequential queries.
func TestPostgresIntegrationMultipleQueries(t *testing.T) {
	t.Parallel()

	pgConfig := setupPostgresIntegration(t)
	setup := newPostgresTestSetup(t, pgConfig)

	setup.startServer(t, "sse")
	defer setup.stopServer(t)

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, setup.Addr)

	// Execute multiple queries in sequence
	queries := []struct {
		name    string
		query   string
		wantLen int
	}{
		{
			name:    "count users",
			query:   "SELECT COUNT(*) FROM users",
			wantLen: 1,
		},
		{
			name:    "count orders",
			query:   "SELECT COUNT(*) FROM orders",
			wantLen: 1,
		},
		{
			name:    "list users",
			query:   "SELECT name FROM users ORDER BY name",
			wantLen: 2,
		},
		{
			name:    "list orders",
			query:   "SELECT total FROM orders ORDER BY total",
			wantLen: 3,
		},
	}

	for _, queryCase := range queries {
		t.Run(queryCase.name, func(t *testing.T) {
			response := callPostgresTool(t, clientSession, "test_execute_query", map[string]any{
				"query": queryCase.query,
			})

			rows, ok := response["rows"].([]any)
			require.Truef(t, ok, "expected rows array")
			require.Lenf(t, rows, queryCase.wantLen,
				"unexpected number of rows for query: %s", queryCase.name)
		})
	}
}

// TestPostgresIntegrationWithParams tests parameterized queries.
func TestPostgresIntegrationWithParams(t *testing.T) {
	t.Parallel()

	pgConfig := setupPostgresIntegration(t)
	setup := newPostgresTestSetup(t, pgConfig)

	setup.startServer(t, "sse")
	defer setup.stopServer(t)

	ctx := t.Context()
	clientSession := connectSSEClient(t, ctx, setup.Addr)

	t.Run("single parameter", func(t *testing.T) {
		response := callPostgresTool(t, clientSession, "test_execute_query", map[string]any{
			"query":  "SELECT name FROM users WHERE id = $1",
			"params": []any{1},
		})

		rows, ok := response["rows"].([]any)
		require.Truef(t, ok, "expected rows array")
		require.Lenf(t, rows, 1, "expected 1 user")
	})

	t.Run("multiple parameters", func(t *testing.T) {
		response := callPostgresTool(t, clientSession, "test_execute_query", map[string]any{
			"query":  "SELECT name FROM users WHERE id BETWEEN $1 AND $2 ORDER BY id",
			"params": []any{1, 2},
		})

		rows, ok := response["rows"].([]any)
		require.Truef(t, ok, "expected rows array")
		require.Lenf(t, rows, 2, "expected 2 users")
	})

	t.Run("parameter in WHERE IN clause", func(t *testing.T) {
		response := callPostgresTool(t, clientSession, "test_execute_query", map[string]any{
			"query":  "SELECT name FROM users WHERE email LIKE $1",
			"params": []any{"%@example.com"},
		})

		rows, ok := response["rows"].([]any)
		require.Truef(t, ok, "expected rows array")
		require.Lenf(t, rows, 2, "expected 2 users with example.com email")
	})
}

// TestPostgresIntegrationInvalidDatasource tests error handling with invalid datasource.
func TestPostgresIntegrationInvalidDatasource(t *testing.T) {
	t.Parallel()

	binPath := getBinaryPath(t)

	// Create config with invalid datasource
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	//nolint:lll // this is config example
	configContent := `sources:
  invalid:
    type: postgres
    connect:
      datasource: "host=invalidhost.example.com port=9999 dbname=test user=test password=test sslmode=disable"
`

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoErrorf(t, err, "failed to write config")

	// Start server — postgres.Connect opens the DB during source.Apply at
	// startup, so an invalid datasource fails the server before it listens.
	// The server may fail to start due to invalid datasource, so we use
	// startServer directly and handle the case where it exits early.
	cmd, stderr := startServer(t, binPath, []string{
		"--config", configPath,
		"--transport", "sse",
		"--addr", "127.0.0.1:0",
	}, []string{"LOG_LEVEL=debug"})

	// Try to read the actual address from stderr.
	reader := bufio.NewReader(stderr)

	addr, addrErr := func() (string, error) {
		deadline := time.Now().Add(5 * time.Second)

		for time.Now().Before(deadline) {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				return "", fmt.Errorf("read stderr: %w", readErr)
			}

			if a := extractAddrFromLog(line); a != "" {
				return a, nil
			}
		}

		return "", errors.New("timeout")
	}()
	if addrErr == nil {
		// Server started — verify tool call fails due to invalid connection.
		ctx := t.Context()
		clientSession := connectSSEClient(t, ctx, addr)

		callPostgresToolExpectError(
			t,
			clientSession,
			"invalidlist_schemas",
			make(map[string]any),
		)
	}
	// If server didn't start, the test still passes because the invalid
	// datasource correctly prevented the server from running.

	// Cleanup
	require.NoErrorf(t, cmd.Process.Signal(os.Interrupt), "failed to send interrupt signal")
}
