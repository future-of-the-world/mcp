// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package postgres implements a source of MCP tools for PostgreSQL database
// introspection. It exposes tools for listing schemas and tables, executing
// read-only SQL queries, and fetching comprehensive table information
// including columns, indexes, constraints, and foreign key relationships with
// recursive traversal.
//
// Per-type Connect establishes a *sql.DB connection from the source's
// `connect:` map, pings the database, and returns the four MCP tools
// with their embedded JSON schemas. The 3 introspection tools
// (list_schemas, list_tables, get_table_info) are tagged ReadOnlyHint
// with a closed world (OpenWorldHint: false). The execute_query tool
// leaves Annotations nil because the implementer cannot tell from a
// query string whether it is read-only — the user/middleware decides.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "embed"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/tool"
)

// Schema bytes are loaded at compile time via go:embed and exposed as
// json.RawMessage on the returned tools. They are extracted from the
// previous inline string constants so the wire format and the source of
// truth live in versioned JSON files.

//go:embed schemas/list_schemas.json
var listSchemasInput json.RawMessage

//go:embed schemas/list_schemas_output.json
var listSchemasOutput json.RawMessage

//go:embed schemas/list_tables.json
var listTablesInput json.RawMessage

//go:embed schemas/list_tables_output.json
var listTablesOutput json.RawMessage

//go:embed schemas/execute_query.json
var executeQueryInput json.RawMessage

//go:embed schemas/execute_query_output.json
var executeQueryOutput json.RawMessage

//go:embed schemas/get_table_info.json
var getTableInfoInput json.RawMessage

//go:embed schemas/get_table_info_output.json
var getTableInfoOutput json.RawMessage

// toolDescListSchemas is the tool description for list_schemas.
const toolDescListSchemas = "List all schemas in the PostgreSQL database"

// toolDescListTables is the tool description for list_tables.
const toolDescListTables = "List all tables in a specific schema"

// toolDescExecuteQuery is the tool description for execute_query.
const toolDescExecuteQuery = "Execute a SQL query on the PostgreSQL database"

// config holds the decoded `connect:` map for a postgres source. Only the
// datasource is required; everything else lives in *sql.DB and the
// dispatcher-provided logger.
type config struct {
	Datasource string
}

// decodeConnect decodes the source's `connect:` map into a config. The
// map is expected to contain a single string field "datasource" carrying
// a pgx-compatible connection string. Scalar string fields are decoded
// through decode.AsString so YAML-natural values (numbers, bools, null)
// are accepted and stringified; non-scalar values (maps, slices) produce
// a wrapped decode.ErrWrongType error so genuine config bugs surface
// as a clear message rather than a silent "field is empty" downstream.
// Errors here are wrapped by Connect as "postgres: decode: <reason>";
// the per-field prefix lives here so the final message is single-
// segment, not double.
func decodeConnect(connect map[string]any) (config, error) {
	var cfg config

	str, err := decode.AsString(connect["datasource"])

	switch {
	case err == nil:
		cfg.Datasource = str

	case errors.Is(err, decode.ErrNotSet):
		// skip — datasource is required; validate() catches the empty value

	default:
		return cfg, fmt.Errorf("connect.datasource: %w", err)
	}

	return cfg, nil
}

// validationTimeout is the maximum time allowed for pinging a new postgres
// connection during Connect. Unreachable databases must not block server
// startup indefinitely.
const validationTimeout = 5 * time.Second

// openDB opens a pgx-backed *sql.DB, pings it with a short timeout, and
// returns it ready for handler use. The returned *sql.DB is owned by the
// caller; the dispatcher keeps it alive as long as the source is loaded.
func openDB(ctx context.Context, cfg config) (*sql.DB, error) {
	ctx, cancel := context.WithTimeout(ctx, validationTimeout)
	defer cancel()

	db, err := sql.Open("pgx", cfg.Datasource)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}

	err = db.PingContext(ctx)
	if err != nil {
		//nolint:errcheck // close error is not critical when ping fails
		db.Close()

		return nil, fmt.Errorf("postgres: ping: %w", err)
	}

	return db, nil
}

// redactDSN strips the password component from a pgx-style DSN so it
// can be safely written to logs. Only the URL form is supported; the
// libpq keyword/value form is returned unchanged.
func redactDSN(dsn string) string {
	const (
		scheme    = "postgres://"
		altScheme = "postgresql://"
	)

	if len(dsn) >= len(scheme) && dsn[:len(scheme)] == scheme {
		return redactURLUserInfo(dsn, len(scheme))
	}

	if len(dsn) >= len(altScheme) && dsn[:len(altScheme)] == altScheme {
		return redactURLUserInfo(dsn, len(altScheme))
	}

	return dsn
}

// redactURLUserInfo replaces the password between ':' and '@' inside the
// URL userinfo with '***'. The start offset is the index just after "://".
func redactURLUserInfo(dsn string, start int) string {
	atIdx := -1

	for i := start; i < len(dsn); i++ {
		if dsn[i] == '@' {
			atIdx = i

			break
		}
	}

	if atIdx < 0 {
		return dsn
	}

	colon := -1

	for i := start; i < atIdx; i++ {
		if dsn[i] == ':' {
			colon = i

			break
		}
	}

	if colon < 0 {
		return dsn
	}

	return dsn[:colon+1] + "***" + dsn[atIdx:]
}

// Connect decodes the source's `connect:` map, opens a *sql.DB, pings it,
// and returns the four PostgreSQL introspection tools. The returned
// Response is ready to be registered with an MCP server by the
// dispatcher. The 3 introspection tools (list_schemas, list_tables,
// get_table_info) set Annotations: ReadOnlyHint=true with
// OpenWorldHint=false (closed world, no external services). The
// execute_query tool leaves Annotations nil because the implementer
// cannot tell from a query string whether it is read-only — the
// user/middleware decides.
//
//nolint:unparam // call sites only need error; success path goes via dispatcher
func Connect(
	ctx context.Context,
	connect map[string]any,
	opts ...tool.Option,
) (tool.Response, error) {
	o := tool.NewOptions(opts...)
	logger := o.Logger()

	cfg, err := decodeConnect(connect)
	if err != nil {
		return tool.Response{}, fmt.Errorf("postgres: decode: %w", err)
	}

	if cfg.Datasource == "" {
		return tool.Response{}, fmt.Errorf("postgres: %w", errEmptyDatasource)
	}

	db, err := openDB(ctx, cfg)
	if err != nil {
		return tool.Response{}, fmt.Errorf("postgres: open: %w", err)
	}

	dbg := &Tool{db: db, logger: logger}

	logger.InfoContext(ctx, "postgres source connected", "datasource", redactDSN(cfg.Datasource))

	// readOnlyAnnotations is the shared annotation block for the three
	// introspection tools below. OpenWorldHint is false because postgres
	// is a closed world to us — we never reach outside the configured
	// database. DestructiveHint/IdempotentHint are irrelevant when
	// ReadOnlyHint is true.
	readOnlyAnnotations := &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    true,
		DestructiveHint: (*bool)(nil),
		IdempotentHint:  false,
		OpenWorldHint:   new(false),
	}

	return tool.Response{
		Tools: []tool.Tool{
			{
				Tool: &mcp.Tool{
					Name:         "list_schemas",
					Description:  toolDescListSchemas,
					InputSchema:  listSchemasInput,
					OutputSchema: listSchemasOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleListSchemas(dbg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "list_tables",
					Description:  toolDescListTables,
					InputSchema:  listTablesInput,
					OutputSchema: listTablesOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleListTables(dbg),
			},
			{
				// execute_query has no Annotations: the implementer
				// cannot tell from a query string whether it is
				// read-only or mutating, so the field is left nil and
				// the user/middleware decides.
				Tool: &mcp.Tool{
					Name:         "execute_query",
					Description:  toolDescExecuteQuery,
					InputSchema:  executeQueryInput,
					OutputSchema: executeQueryOutput,
				},
				Handler: handleExecuteQuery(dbg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "get_table_info",
					Description:  toolDescTableInfo,
					InputSchema:  getTableInfoInput,
					OutputSchema: getTableInfoOutput,
					Annotations:  readOnlyAnnotations,
				},
				Handler: handleGetTableInfo(dbg),
			},
		},
	}, nil
}
