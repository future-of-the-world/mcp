// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// getSchemas retrieves all schemas from the database
func (t *Tool) getSchemas(
	ctx context.Context,
) (*PostgresSchemasResponse, error) {
	query := `
		SELECT schema_name
		FROM information_schema.schemata
		WHERE schema_name NOT LIKE 'pg_%'
		AND schema_name != 'information_schema'
		ORDER BY schema_name`

	rows, err := t.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query schemas: %w", err)
	}

	defer rows.Close()

	response := &PostgresSchemasResponse{Schemas: []SchemaInfo{}}

	for rows.Next() {
		var schema SchemaInfo

		err = rows.Scan(&schema.Name)
		if err != nil {
			return nil, fmt.Errorf("scan schema: %w", err)
		}

		response.Schemas = append(response.Schemas, schema)
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("iterate schemas: %w", err)
	}

	return response, nil
}

// getTables retrieves all tables from a specific schema
func (t *Tool) getTables(
	ctx context.Context,
	req PostgresTablesRequest,
) (*PostgresTablesResponse, error) {
	query := `
		SELECT table_schema, table_name, table_type,
			   COALESCE((
				   SELECT reltuples::bigint FROM pg_class c
				   JOIN pg_namespace n ON n.oid = c.relnamespace
				   WHERE c.relname = t.table_name AND n.nspname = t.table_schema
			   ), 0) as row_count_estimate
		FROM information_schema.tables t
		WHERE table_schema = $1
		AND table_type = 'BASE TABLE'
		ORDER BY table_name`

	rows, err := t.db.QueryContext(ctx, query, req.Schema)
	if err != nil {
		return nil, fmt.Errorf("query tables: %w", err)
	}
	defer rows.Close()

	response := &PostgresTablesResponse{Tables: []TableInfo{}}

	for rows.Next() {
		var table TableInfo

		err = rows.Scan(
			&table.Schema,
			&table.Name,
			&table.Type,
			&table.RowCountEstimate,
		)
		if err != nil {
			return nil, fmt.Errorf("scan table: %w", err)
		}

		response.Tables = append(response.Tables, table)
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("iterate tables: %w", err)
	}

	return response, nil
}

// executeQuery executes a read-only SQL query and returns the results
func (t *Tool) executeQuery(
	ctx context.Context,
	req PostgresExecuteRequest,
) (*PostgresExecuteResponse, error) {
	var (
		rows *sql.Rows
		err  error
	)

	// Validate that the query is read-only
	err = isReadOnlyQuery(req.Query)
	if err != nil {
		return nil, fmt.Errorf("read-only validation failed: %w", err)
	}

	if len(req.Params) > 0 {
		rows, err = t.db.QueryContext(ctx, req.Query, req.Params...)
	} else {
		rows, err = t.db.QueryContext(ctx, req.Query)
	}

	if err != nil {
		return nil, fmt.Errorf("execute query: %w", err)
	}

	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("get columns: %w", err)
	}

	response := &PostgresExecuteResponse{
		Columns: columns,
		Rows:    [][]any{},
	}

	response.Rows, err = scanRows(rows, columns)
	if err != nil {
		return nil, err
	}

	return response, nil
}

// parsePostgresArray converts PostgreSQL array string to Go slice
func parsePostgresArray(arrayStr string) []string {
	// Remove surrounding braces
	if len(arrayStr) < minPostgresArrayLength {
		return nil
	}

	arrayStr = strings.Trim(arrayStr, "{}")
	if arrayStr == "" {
		return nil
	}

	// Split by comma
	parts := strings.Split(arrayStr, ",")

	result := make([]string, 0, len(parts))
	for _, part := range parts {
		// Remove quotes if present
		part = strings.Trim(part, "\"")
		if part != "" {
			result = append(result, part)
		}
	}

	return result
}
