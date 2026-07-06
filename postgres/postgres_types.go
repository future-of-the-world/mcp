// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package postgres

import (
	"database/sql"
	"errors"
	"log/slog"

	_ "github.com/jackc/pgx/v5/stdlib" // blank import for pgx driver
)

var (
	// errEmptyDatasource indicates that the datasource connection string is empty.
	errEmptyDatasource = errors.New("postgres tool datasource is empty")
	// errTableNotFound indicates that the requested table was not found in the database.
	errTableNotFound = errors.New("table not found")
)

const (
	toolTypePostgres       = "postgres"
	minPostgresArrayLength = 2 // min len for PostgreSQL array (e.g., "{}")

	toolDescTableInfo = "Get comprehensive information about a table " +
		"including columns, indexes, " +
		"constraints, comments, and recursively fetch " +
		"all related tables through foreign keys, " +
		"including column details and join cardinality for each relationship"
)

// Tool holds the per-source state for a postgres source: the live
// database connection and the logger propagated from the dispatcher.
// The type is exported only because tests in this package construct it
// directly with a pre-opened *sql.DB; production code reaches it via
// Connect and never touches the fields.
type Tool struct {
	db     *sql.DB
	logger *slog.Logger
}

// PostgresSchemasRequest requests schema listing
type PostgresSchemasRequest struct {
	// No parameters required
}

// PostgresSchemasResponse contains schema information
type PostgresSchemasResponse struct {
	Schemas []SchemaInfo `json:"schemas" jsonschema:"List of database schemas"`
}

// SchemaInfo contains information about a database schema
type SchemaInfo struct {
	Name string `json:"name" jsonschema:"Schema name"`
}

// PostgresTablesRequest requests table listing for a schema
type PostgresTablesRequest struct {
	Schema string `json:"schema" jsonschema:"Schema name to list tables from"`
}

// PostgresTablesResponse contains table information
type PostgresTablesResponse struct {
	Tables []TableInfo `json:"tables" jsonschema:"List of tables in the schema"`
}

// TableInfo contains information about a database table
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type TableInfo struct {
	Schema           string `json:"schema"                       jsonschema:"Schema name"`
	Name             string `json:"name"                         jsonschema:"Table name"`
	Type             string `json:"type"                         jsonschema:"Table type (BASE TABLE, VIEW, etc.)"`
	RowCountEstimate int64  `json:"row_count_estimate,omitempty" jsonschema:"Estimated number of rows"`
}

// IndexInfo contains information about a table index
type IndexInfo struct {
	Name       string   `json:"name"       jsonschema:"Index name"`
	IsUnique   bool     `json:"is_unique"  jsonschema:"Whether the index is unique"`
	IsPrimary  bool     `json:"is_primary" jsonschema:"Whether the index is the primary key"`
	Columns    []string `json:"columns"    jsonschema:"List of column names in the index"`
	Method     string   `json:"method"     jsonschema:"Index method (btree, hash, gin, gist, etc.)"`
	IsPartial  bool     `json:"is_partial" jsonschema:"Whether this is a partial index"`
	Predicate  string   `json:"predicate"  jsonschema:"WHERE clause predicate for partial indexes"`
	Definition string   `json:"definition" jsonschema:"Full index definition SQL statement"`
}

// PostgresExecuteRequest requests execution of a SQL query
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type PostgresExecuteRequest struct {
	Query  string `json:"query"            jsonschema:"SQL query to execute"`
	Params []any  `json:"params,omitempty" jsonschema:"Query parameters for prepared statement placeholders"`
}

// PostgresExecuteResponse contains query results
type PostgresExecuteResponse struct {
	Columns []string `json:"columns" jsonschema:"List of column names in the result set"`
	Rows    [][]any  `json:"rows"    jsonschema:"Query result rows"`
}

// PostgresTableInfoRequest requests comprehensive table information
type PostgresTableInfoRequest struct {
	Schema string `json:"schema" jsonschema:"Schema name"`
	Table  string `json:"table"  jsonschema:"Table name"`
}

// PostgresTableInfoResponse contains comprehensive table information
//

type PostgresTableInfoResponse struct {
	Tables []DetailedTableInfo `json:"tables" jsonschema:"List of tables with complete information"`
}

// DetailedTableInfo contains comprehensive information about a database table
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type DetailedTableInfo struct {
	Schema       string               `json:"schema"                       jsonschema:"Schema name"`
	Name         string               `json:"name"                         jsonschema:"Table name"`
	Type         string               `json:"type"                         jsonschema:"Table type (BASE TABLE, VIEW, etc.)"`
	Comment      string               `json:"comment,omitempty"            jsonschema:"Table comment"`
	RowCountEst  int64                `json:"row_count_estimate,omitempty" jsonschema:"Estimated number of rows"`
	Columns      []DetailedColumnInfo `json:"columns"                      jsonschema:"List of columns with complete information"`
	Indexes      []IndexInfo          `json:"indexes"                      jsonschema:"List of indexes on the table"`
	Constraints  []ConstraintInfo     `json:"constraints"                  jsonschema:"List of table constraints"`
	References   []ReferenceInfo      `json:"references"                   jsonschema:"Foreign key relationships to other tables"`
	ReferencedBy []ReferenceInfo      `json:"referenced_by"                jsonschema:"Foreign key relationships from other tables"`
}

// DetailedColumnInfo contains comprehensive information about a table column
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type DetailedColumnInfo struct {
	Name             string `json:"name"                        jsonschema:"Column name"`
	OrdinalPosition  int    `json:"ordinal_position"            jsonschema:"Column position in table"`
	DataType         string `json:"data_type"                   jsonschema:"Column data type"`
	UdtName          string `json:"udt_name,omitempty"          jsonschema:"User-defined type name"`
	CharMaxLength    int    `json:"char_max_length,omitempty"   jsonschema:"Maximum character length"`
	NumericPrecision int    `json:"numeric_precision,omitempty" jsonschema:"Numeric precision"`
	NumericScale     int    `json:"numeric_scale,omitempty"     jsonschema:"Numeric scale"`
	IsNullable       bool   `json:"is_nullable"                 jsonschema:"Whether the column allows NULL values"`
	IsIdentity       bool   `json:"is_identity"                 jsonschema:"Whether the column is an identity column"`
	IsGenerated      bool   `json:"is_generated"                jsonschema:"Whether the column is generated"`
	DefaultValue     string `json:"default_value,omitempty"     jsonschema:"Default value of the column"`
	Comment          string `json:"comment,omitempty"           jsonschema:"Column comment"`
	IsPrimaryKey     bool   `json:"is_primary_key"              jsonschema:"Whether the column is part of the primary key"`
}

// ConstraintInfo contains information about a table constraint
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type ConstraintInfo struct {
	Name              string   `json:"name"                         jsonschema:"Constraint name"`
	Type              string   `json:"type"                         jsonschema:"Constraint type (PRIMARY KEY, FOREIGN KEY, UNIQUE, CHECK)"`
	Columns           []string `json:"columns"                      jsonschema:"Columns involved in the constraint"`
	ReferencedSchema  string   `json:"referenced_schema,omitempty"  jsonschema:"Referenced table schema (for foreign keys)"`
	ReferencedTable   string   `json:"referenced_table,omitempty"   jsonschema:"Referenced table name (for foreign keys)"`
	ReferencedColumns []string `json:"referenced_columns,omitempty" jsonschema:"Referenced columns (for foreign keys)"`
	OnUpdate          string   `json:"on_update,omitempty"          jsonschema:"On update action (for foreign keys)"`
	OnDelete          string   `json:"on_delete,omitempty"          jsonschema:"On delete action (for foreign keys)"`
	CheckClause       string   `json:"check_clause,omitempty"       jsonschema:"Check constraint expression"`
	IsDeferrable      bool     `json:"is_deferrable"                jsonschema:"Whether the constraint is deferrable"`
}

// ReferenceInfo contains information about a foreign key relationship
type ReferenceInfo struct {
	ConstraintName string   `json:"constraint_name" jsonschema:"Foreign key constraint name"`
	FromSchema     string   `json:"from_schema"     jsonschema:"Source table schema"`
	FromTable      string   `json:"from_table"      jsonschema:"Source table name"`
	FromColumns    []string `json:"from_columns"    jsonschema:"Source table columns"`
	ToSchema       string   `json:"to_schema"       jsonschema:"Target table schema"`
	ToTable        string   `json:"to_table"        jsonschema:"Target table name"`
	ToColumns      []string `json:"to_columns"      jsonschema:"Target table columns"`
	OnUpdate       string   `json:"on_update"       jsonschema:"On update action"`
	OnDelete       string   `json:"on_delete"       jsonschema:"On delete action"`
}

// tableInfoState tracks visited tables during recursive table info fetching
type tableInfoState struct {
	visited map[string]bool
	tables  []DetailedTableInfo
}
