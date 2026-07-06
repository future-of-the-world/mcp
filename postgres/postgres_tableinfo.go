// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Masterminds/squirrel"
)

// getTableInfo fetches comprehensive information about a table and its dependencies
func (t *Tool) getTableInfo(
	ctx context.Context,
	req PostgresTableInfoRequest,
) (*PostgresTableInfoResponse, error) {
	state := tableInfoState{
		visited: make(map[string]bool),
		tables:  []DetailedTableInfo{},
	}

	err := t.fetchTableInfoRecursive(ctx, req.Schema, req.Table, &state)
	if err != nil {
		return nil, err
	}

	return &PostgresTableInfoResponse{Tables: state.tables}, nil
}

// fetchTableInfoRecursive recursively fetches table information following foreign keys
func (t *Tool) fetchTableInfoRecursive(
	ctx context.Context,
	schema, table string,
	state *tableInfoState,
) error {
	// Create unique key for table
	tableKey := schema + "." + table

	// Skip if already visited (prevents infinite loops)
	if state.visited[tableKey] {
		return nil
	}

	state.visited[tableKey] = true

	// Fetch table information
	tableInfo, err := t.fetchDetailedTableInfo(ctx, schema, table)
	if err != nil {
		// Table not found is expected when recursively fetching referenced tables
		if errors.Is(err, errTableNotFound) {
			return nil
		}

		return err
	}

	state.tables = append(state.tables, *tableInfo)

	// Recursively fetch referenced tables (foreign keys pointing TO other tables)
	for i := range tableInfo.References {
		err := t.fetchTableInfoRecursive(
			ctx,
			tableInfo.References[i].ToSchema,
			tableInfo.References[i].ToTable,
			state,
		)
		if err != nil {
			return err
		}
	}

	return nil
}

// fetchDetailedTableInfo fetches complete information about a single table
func (t *Tool) fetchDetailedTableInfo(
	ctx context.Context,
	schema, table string,
) (*DetailedTableInfo, error) {
	// Fetch basic table info
	tableInfo, err := t.fetchTableBasicInfo(ctx, schema, table)
	if err != nil {
		return nil, err
	}

	if tableInfo == nil {
		return nil, errTableNotFound
	}

	// Fetch columns with detailed information
	columns, err := t.fetchDetailedColumns(ctx, schema, table)
	if err != nil {
		return nil, err
	}

	tableInfo.Columns = columns

	// Fetch indexes
	indexes, err := t.fetchTableIndexes(ctx, schema, table)
	if err != nil {
		return nil, err
	}

	tableInfo.Indexes = indexes

	// Fetch constraints
	constraints, err := t.fetchTableConstraints(ctx, schema, table)
	if err != nil {
		return nil, err
	}

	tableInfo.Constraints = constraints

	// Fetch foreign key references (this table -> other tables)
	references, err := t.fetchTableReferences(ctx, schema, table)
	if err != nil {
		return nil, err
	}

	tableInfo.References = references

	// Fetch reverse references (other tables -> this table)
	referencedBy, err := t.fetchTableReferencedBy(ctx, schema, table)
	if err != nil {
		return nil, err
	}

	tableInfo.ReferencedBy = referencedBy

	return tableInfo, nil
}

// fetchTableBasicInfo fetches basic table metadata
func (t *Tool) fetchTableBasicInfo(
	ctx context.Context,
	schema, table string,
) (*DetailedTableInfo, error) {
	query := `
		SELECT
			t.table_schema,
			t.table_name,
			t.table_type,
			COALESCE(obj_description(c.oid), '') as comment,
			COALESCE(c.reltuples::bigint, 0) as row_count_estimate
		FROM information_schema.tables t
		LEFT JOIN pg_class c ON c.relname = t.table_name
		LEFT JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = t.table_schema
		WHERE t.table_schema = $1 AND t.table_name = $2`

	var (
		info    DetailedTableInfo
		comment sql.NullString
	)

	err := t.db.QueryRowContext(ctx, query, schema, table).Scan(
		&info.Schema,
		&info.Name,
		&info.Type,
		&comment,
		&info.RowCountEst,
	)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, errTableNotFound

	case err != nil:
		return nil, fmt.Errorf("query table basic info: %w", err)
	}

	info.Comment = comment.String

	return &info, nil
}

// fetchDetailedColumns fetches comprehensive column information
func (t *Tool) fetchDetailedColumns(
	ctx context.Context,
	schema, table string,
) ([]DetailedColumnInfo, error) {
	query := `
		SELECT
			c.column_name,
			c.ordinal_position,
			c.data_type,
			COALESCE(c.udt_name, ''),
			COALESCE(c.character_maximum_length, 0),
			COALESCE(c.numeric_precision, 0),
			COALESCE(c.numeric_scale, 0),
			c.is_nullable = 'YES',
			COALESCE(c.is_identity, 'NO') = 'YES',
			COALESCE(c.is_generated, 'NEVER') != 'NEVER',
			COALESCE(c.column_default, ''),
			COALESCE(col_description(pgc.oid, c.ordinal_position), ''),
			COALESCE((
				SELECT true
				FROM information_schema.table_constraints tc
				JOIN information_schema.key_column_usage kcu
					ON tc.constraint_name = kcu.constraint_name
					AND tc.table_schema = kcu.table_schema
				WHERE tc.constraint_type = 'PRIMARY KEY'
				AND tc.table_schema = c.table_schema
				AND tc.table_name = c.table_name
				AND kcu.column_name = c.column_name
				LIMIT 1
			), false) as is_primary_key
		FROM information_schema.columns c
		LEFT JOIN pg_class pgc ON pgc.relname = c.table_name
		LEFT JOIN pg_namespace pgn ON pgn.oid = pgc.relnamespace AND pgn.nspname = c.table_schema
		WHERE c.table_schema = $1 AND c.table_name = $2
		ORDER BY c.ordinal_position`

	rows, err := t.db.QueryContext(ctx, query, schema, table)
	if err != nil {
		return nil, fmt.Errorf("query detailed columns: %w", err)
	}
	defer rows.Close()

	columns := []DetailedColumnInfo{}

	for rows.Next() {
		var (
			col                                DetailedColumnInfo
			udtName, defaultVal, comment       sql.NullString
			charMaxLen, numPrecision, numScale sql.NullInt64
		)

		err = rows.Scan(
			&col.Name,
			&col.OrdinalPosition,
			&col.DataType,
			&udtName,
			&charMaxLen,
			&numPrecision,
			&numScale,
			&col.IsNullable,
			&col.IsIdentity,
			&col.IsGenerated,
			&defaultVal,
			&comment,
			&col.IsPrimaryKey,
		)
		if err != nil {
			return nil, fmt.Errorf("scan detailed column: %w", err)
		}

		col.UdtName = udtName.String
		col.CharMaxLength = int(charMaxLen.Int64)
		col.NumericPrecision = int(numPrecision.Int64)
		col.NumericScale = int(numScale.Int64)
		col.DefaultValue = defaultVal.String
		col.Comment = comment.String

		columns = append(columns, col)
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("iterate detailed columns: %w", err)
	}

	return columns, nil
}

// fetchTableIndexes fetches index information for a table
func (t *Tool) fetchTableIndexes(
	ctx context.Context,
	schema, table string,
) ([]IndexInfo, error) {
	query := `
		SELECT
			i.relname as index_name,
			ix.indisunique as is_unique,
			ix.indisprimary as is_primary,
			am.amname as index_method,
			ix.indpred is not null as is_partial,
			COALESCE(pg_get_expr(ix.indpred, ix.indrelid), '') as predicate,
			pg_get_indexdef(ix.indexrelid) as index_definition,
			array_agg(a.attname ORDER BY array_position(ix.indkey, a.attnum)) as columns
		FROM pg_class t
		JOIN pg_index ix ON t.oid = ix.indrelid
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		JOIN pg_am am ON am.oid = i.relam
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(ix.indkey)
		WHERE n.nspname = $1 AND t.relname = $2
		GROUP BY
			i.relname,
			ix.indisunique,
			ix.indisprimary,
			am.amname,
			ix.indpred,
			ix.indrelid,
			ix.indexrelid
		ORDER BY i.relname`

	rows, err := t.db.QueryContext(ctx, query, schema, table)
	if err != nil {
		return nil, fmt.Errorf("query indexes: %w", err)
	}
	defer rows.Close()

	indexes := []IndexInfo{}

	for rows.Next() {
		var (
			idx     IndexInfo
			columns []byte
		)

		err = rows.Scan(
			&idx.Name,
			&idx.IsUnique,
			&idx.IsPrimary,
			&idx.Method,
			&idx.IsPartial,
			&idx.Predicate,
			&idx.Definition,
			&columns,
		)
		if err != nil {
			return nil, fmt.Errorf("scan index: %w", err)
		}

		idx.Columns = parsePostgresArray(string(columns))
		indexes = append(indexes, idx)
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("iterate indexes: %w", err)
	}

	return indexes, nil
}

// fetchTableConstraints fetches all constraints for a table
func (t *Tool) fetchTableConstraints(
	ctx context.Context,
	schema, table string,
) ([]ConstraintInfo, error) {
	query := `
		SELECT
			tc.constraint_name,
			tc.constraint_type,
			COALESCE(
				array_agg(kcu.column_name ORDER BY kcu.ordinal_position)
					FILTER (WHERE kcu.column_name IS NOT NULL), '{}'
			) as columns,
			COALESCE(ccu.table_schema, '') as referenced_schema,
			COALESCE(ccu.table_name, '') as referenced_table,
			COALESCE(
				array_agg(kcu2.column_name ORDER BY kcu2.ordinal_position)
					FILTER (WHERE kcu2.column_name IS NOT NULL), '{}'
			) as referenced_columns,
			COALESCE(rc.update_rule, '') as on_update,
			COALESCE(rc.delete_rule, '') as on_delete,
			COALESCE(cc.check_clause, '') as check_clause,
			COALESCE(tc.is_deferrable, 'NO') = 'YES' as is_deferrable
		FROM information_schema.table_constraints tc
		LEFT JOIN information_schema.key_column_usage kcu
			ON tc.constraint_name = kcu.constraint_name
			AND tc.table_schema = kcu.table_schema
		LEFT JOIN information_schema.referential_constraints rc
			ON tc.constraint_name = rc.constraint_name
			AND tc.table_schema = rc.constraint_schema
		LEFT JOIN information_schema.constraint_column_usage ccu
			ON rc.unique_constraint_name = ccu.constraint_name
			AND rc.unique_constraint_schema = ccu.constraint_schema
		LEFT JOIN information_schema.key_column_usage kcu2
			ON rc.unique_constraint_name = kcu2.constraint_name
			AND rc.unique_constraint_schema = kcu2.table_schema
		LEFT JOIN information_schema.check_constraints cc
			ON tc.constraint_name = cc.constraint_name
			AND tc.table_schema = cc.constraint_schema
		WHERE tc.table_schema = $1 AND tc.table_name = $2
		GROUP BY
			tc.constraint_name,
			tc.constraint_type,
			ccu.table_schema,
			ccu.table_name,
			rc.update_rule,
			rc.delete_rule,
			cc.check_clause,
			tc.is_deferrable
		ORDER BY tc.constraint_type, tc.constraint_name`

	rows, err := t.db.QueryContext(ctx, query, schema, table)
	if err != nil {
		return nil, fmt.Errorf("query constraints: %w", err)
	}
	defer rows.Close()

	constraints := []ConstraintInfo{}

	for rows.Next() {
		var (
			con                                                  ConstraintInfo
			columns, refColumns                                  []byte
			refSchema, refTable, onUpdate, onDelete, checkClause sql.NullString
		)

		err = rows.Scan(
			&con.Name,
			&con.Type,
			&columns,
			&refSchema,
			&refTable,
			&refColumns,
			&onUpdate,
			&onDelete,
			&checkClause,
			&con.IsDeferrable,
		)
		if err != nil {
			return nil, fmt.Errorf("scan constraint: %w", err)
		}

		con.Columns = parsePostgresArray(string(columns))
		con.ReferencedSchema = refSchema.String
		con.ReferencedTable = refTable.String
		con.ReferencedColumns = parsePostgresArray(string(refColumns))
		con.OnUpdate = onUpdate.String
		con.OnDelete = onDelete.String
		con.CheckClause = checkClause.String

		constraints = append(constraints, con)
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("iterate constraints: %w", err)
	}

	return constraints, nil
}

// fetchTableReferences fetches foreign keys from this table to other tables
func (t *Tool) fetchTableReferences(
	ctx context.Context,
	schema, table string,
) ([]ReferenceInfo, error) {
	query := buildTableReferencesBaseQuery().Where(squirrel.Eq{
		"tc.table_schema": schema,
		"tc.table_name":   table,
	})

	return t.executeTableReferencesQuery(ctx, query)
}

// fetchTableReferencedBy fetches foreign keys from other tables to this table
func (t *Tool) fetchTableReferencedBy(
	ctx context.Context,
	schema, table string,
) ([]ReferenceInfo, error) {
	query := buildTableReferencesBaseQuery().Where(squirrel.Eq{
		"tc2.table_schema": schema,
		"tc2.table_name":   table,
	})

	return t.executeTableReferencesQuery(ctx, query)
}

// buildTableReferencesBaseQuery creates the base squirrel query for FK references
func buildTableReferencesBaseQuery() squirrel.SelectBuilder {
	return squirrel.Select(
		"tc.constraint_name",
		"tc.table_schema as from_schema",
		"tc.table_name as from_table",
		"array_agg(kcu.column_name ORDER BY kcu.ordinal_position) as from_columns",
		"tc2.table_schema as to_schema",
		"tc2.table_name as to_table",
		"array_agg(kcu2.column_name ORDER BY kcu2.ordinal_position) as to_columns",
		"rc.update_rule as on_update",
		"rc.delete_rule as on_delete",
	).
		From("information_schema.table_constraints tc").
		//nolint:lll // complex SQL join conditions
		Join("information_schema.key_column_usage kcu ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema").

		//nolint:lll // complex SQL join conditions
		Join("information_schema.referential_constraints rc ON tc.constraint_name = rc.constraint_name AND tc.table_schema = rc.constraint_schema").

		//nolint:lll // complex SQL join conditions
		Join("information_schema.table_constraints tc2 ON rc.unique_constraint_name = tc2.constraint_name AND rc.unique_constraint_schema = tc2.table_schema").

		//nolint:lll // complex SQL join conditions
		Join("information_schema.key_column_usage kcu2 ON rc.unique_constraint_name = kcu2.constraint_name AND rc.unique_constraint_schema = kcu2.constraint_schema").
		Where(squirrel.Eq{"tc.constraint_type": "FOREIGN KEY"}).
		//nolint:lll // group by all required columns
		GroupBy("tc.constraint_name", "tc.table_schema", "tc.table_name", "tc2.table_schema", "tc2.table_name", "rc.update_rule", "rc.delete_rule").
		OrderBy("tc.constraint_name").
		PlaceholderFormat(squirrel.Dollar)
}

// executeTableReferencesQuery executes the squirrel query and scans the results
func (t *Tool) executeTableReferencesQuery(
	ctx context.Context,
	query squirrel.SelectBuilder,
) ([]ReferenceInfo, error) {
	sqlQuery, args, err := query.ToSql()
	if err != nil {
		return nil, fmt.Errorf("build query: %w", err)
	}

	rows, err := t.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("query table references: %w", err)
	}
	defer rows.Close()

	references := []ReferenceInfo{}

	for rows.Next() {
		var (
			ref              ReferenceInfo
			fromCols, toCols []byte
		)

		err = rows.Scan(
			&ref.ConstraintName,
			&ref.FromSchema,
			&ref.FromTable,
			&fromCols,
			&ref.ToSchema,
			&ref.ToTable,
			&toCols,
			&ref.OnUpdate,
			&ref.OnDelete,
		)
		if err != nil {
			return nil, fmt.Errorf("scan table reference: %w", err)
		}

		ref.FromColumns = parsePostgresArray(string(fromCols))
		ref.ToColumns = parsePostgresArray(string(toCols))
		references = append(references, ref)
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("iterate table references: %w", err)
	}

	return references, nil
}

// AddTool is intentionally removed in the per-type Connect refactor. The
// old per-instance tool registration has been replaced by postgres.Connect
// which returns a tool.Response carrying pre-built mcp.ToolHandlers. This
// stub keeps the file free of stale references and exists only to make
// the deletion explicit. There is no AddTool method on *Tool.
