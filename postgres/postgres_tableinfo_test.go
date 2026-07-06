// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package postgres

import (
	"log"
	"log/slog"
	"os"
	"testing"

	"go.amidman.dev/testenv/dbenv"
	"go.amidman.dev/testenv/postgresenv"
	"go.amidman.dev/testenv/postgresenv/containers"

	"github.com/stretchr/testify/require"
)

var (
	pgCnt = containers.NewContainer(
		containers.WithLogger(
			log.New(os.Stdout, "[postgres-container] ", log.LstdFlags|log.Lmsgprefix),
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
			postgresenv.WithReuseStrategy(new(postgresenv.ReuseStrategyCreateNewDB)),
		),
	)
)

// postgresTestTool creates a Postgres Tool connected to a test database.
// In the new per-type Connect refactor the *Tool struct holds only the
// *sql.DB and the dispatcher-provided logger; tests that need a
// pre-opened connection can construct it directly.
func postgresTestTool(t *testing.T, migrationOpts ...dbenv.Option) *Tool {
	t.Helper()

	db := dbenv.UseForTesting(t, pgEnv, migrationOpts...)

	return &Tool{db: db, logger: (*slog.Logger)(nil)}
}

// indexTestMigrations creates test tables with various index types for testing
func indexTestMigrations() dbenv.Option {
	return dbenv.Queries(
		// Create a products table with various indexes
		`CREATE TABLE products (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			category TEXT,
			price DECIMAL(10,2),
			active BOOLEAN DEFAULT true,
			created_at TIMESTAMP DEFAULT NOW()
		)`,
		// Regular btree index on single column
		`CREATE INDEX idx_products_name ON products(name)`,
		// Unique composite index
		`CREATE UNIQUE INDEX idx_products_category_price ON products(category, price)`,
		// Partial index with simple WHERE clause
		`CREATE INDEX idx_products_active ON products(category) WHERE active = true`,
		// Partial index with complex WHERE clause
		`CREATE INDEX idx_products_expensive ON products(name) WHERE price > 100`,
		// Hash index
		`CREATE INDEX idx_products_category_hash ON products USING hash (category)`,
		// Insert test data
		`INSERT INTO products (name, category, price, active) VALUES
			('Product A', 'Electronics', 150.00, true),
			('Product B', 'Books', 50.00, true),
			('Product C', 'Electronics', 200.00, false)`,
	)
}

// findTableInfo searches for a table by name in the response
//
//nolint:unparam // tableName is always "products" in current tests but kept for flexibility
func findTableInfo(
	t *testing.T,
	resp *PostgresTableInfoResponse,
	tableName string,
) *DetailedTableInfo {
	t.Helper()

	for i := range resp.Tables {
		if resp.Tables[i].Name == tableName {
			return &resp.Tables[i]
		}
	}

	return nil
}

// findIndex searches for an index by name in the table info
func findIndex(t *testing.T, tableInfo *DetailedTableInfo, indexName string) *IndexInfo {
	t.Helper()

	for i := range tableInfo.Indexes {
		if tableInfo.Indexes[i].Name == indexName {
			return &tableInfo.Indexes[i]
		}
	}

	return nil
}

func TestPostgresTool_FetchTableIndexes_RegularBtree(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t, indexTestMigrations())

	resp, err := tool.getTableInfo(t.Context(), PostgresTableInfoRequest{
		Schema: "public",
		Table:  "products",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Tables)

	productsInfo := findTableInfo(t, resp, "products")
	require.NotNilf(t, productsInfo, "products table should be found")
	require.NotEmptyf(t, productsInfo.Indexes, "products table should have indexes")

	// Test regular btree index
	idx := findIndex(t, productsInfo, "idx_products_name")
	require.NotNilf(t, idx, "idx_products_name should be found")

	require.Equal(t, "idx_products_name", idx.Name)
	require.Equal(t, "btree", idx.Method)
	require.False(t, idx.IsUnique)
	require.False(t, idx.IsPrimary)
	require.False(t, idx.IsPartial)
	require.Empty(t, idx.Predicate)
	require.Equal(t, []string{"name"}, idx.Columns)
	require.NotEmpty(t, idx.Definition)
	require.Contains(t, idx.Definition, "CREATE INDEX")
	require.Contains(t, idx.Definition, "idx_products_name")
}

func TestPostgresTool_FetchTableIndexes_UniqueComposite(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t, indexTestMigrations())

	resp, err := tool.getTableInfo(t.Context(), PostgresTableInfoRequest{
		Schema: "public",
		Table:  "products",
	})
	require.NoError(t, err)

	productsInfo := findTableInfo(t, resp, "products")
	require.NotNil(t, productsInfo)

	// Test unique composite index
	idx := findIndex(t, productsInfo, "idx_products_category_price")
	require.NotNilf(t, idx, "idx_products_category_price should be found")

	require.Equal(t, "idx_products_category_price", idx.Name)
	require.Equal(t, "btree", idx.Method)
	require.True(t, idx.IsUnique)
	require.False(t, idx.IsPrimary)
	require.False(t, idx.IsPartial)
	require.Empty(t, idx.Predicate)
	require.Equal(t, []string{"category", "price"}, idx.Columns)
	require.NotEmpty(t, idx.Definition)
	require.Contains(t, idx.Definition, "CREATE UNIQUE INDEX")
}

//nolint:dupl // Similar to PartialComplex test but tests different index properties
func TestPostgresTool_FetchTableIndexes_PartialSimple(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t, indexTestMigrations())

	resp, err := tool.getTableInfo(t.Context(), PostgresTableInfoRequest{
		Schema: "public",
		Table:  "products",
	})
	require.NoError(t, err)

	productsInfo := findTableInfo(t, resp, "products")
	require.NotNil(t, productsInfo)

	// Test partial index with simple WHERE clause
	idx := findIndex(t, productsInfo, "idx_products_active")
	require.NotNilf(t, idx, "idx_products_active should be found")

	require.Equal(t, "idx_products_active", idx.Name)
	require.Equal(t, "btree", idx.Method)
	require.False(t, idx.IsUnique)
	require.False(t, idx.IsPrimary)
	require.Truef(t, idx.IsPartial, "index should be partial")
	require.NotEmptyf(t, idx.Predicate, "partial index should have predicate")
	require.Containsf(t, idx.Predicate, "active", "predicate should mention active column")
	require.Containsf(t, idx.Predicate, "true", "predicate should mention true value")
	require.Equal(t, []string{"category"}, idx.Columns)
	require.NotEmpty(t, idx.Definition)
	require.Containsf(t, idx.Definition, "WHERE", "definition should contain WHERE clause")
	require.Contains(t, idx.Definition, "idx_products_active")
}

//nolint:dupl // Similar to PartialSimple test but tests different index properties
func TestPostgresTool_FetchTableIndexes_PartialComplex(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t, indexTestMigrations())

	resp, err := tool.getTableInfo(t.Context(), PostgresTableInfoRequest{
		Schema: "public",
		Table:  "products",
	})
	require.NoError(t, err)

	productsInfo := findTableInfo(t, resp, "products")
	require.NotNil(t, productsInfo)

	// Test partial index with complex WHERE clause
	idx := findIndex(t, productsInfo, "idx_products_expensive")
	require.NotNilf(t, idx, "idx_products_expensive should be found")

	require.Equal(t, "idx_products_expensive", idx.Name)
	require.Equal(t, "btree", idx.Method)
	require.False(t, idx.IsUnique)
	require.False(t, idx.IsPrimary)
	require.Truef(t, idx.IsPartial, "index should be partial")
	require.NotEmptyf(t, idx.Predicate, "partial index should have predicate")
	require.Containsf(t, idx.Predicate, "price", "predicate should mention price column")
	require.Containsf(t, idx.Predicate, ">", "predicate should contain comparison operator")
	require.Equal(t, []string{"name"}, idx.Columns)
	require.NotEmpty(t, idx.Definition)
	require.Containsf(t, idx.Definition, "WHERE", "definition should contain WHERE clause")
	require.Contains(t, idx.Definition, "idx_products_expensive")
}

func TestPostgresTool_FetchTableIndexes_HashMethod(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t, indexTestMigrations())

	resp, err := tool.getTableInfo(t.Context(), PostgresTableInfoRequest{
		Schema: "public",
		Table:  "products",
	})
	require.NoError(t, err)

	productsInfo := findTableInfo(t, resp, "products")
	require.NotNil(t, productsInfo)

	// Test hash index
	idx := findIndex(t, productsInfo, "idx_products_category_hash")
	require.NotNilf(t, idx, "idx_products_category_hash should be found")

	require.Equal(t, "idx_products_category_hash", idx.Name)
	require.Equalf(t, "hash", idx.Method, "index method should be hash")
	require.Falsef(t, idx.IsUnique, "hash index should not be unique")
	require.False(t, idx.IsPrimary)
	require.Falsef(t, idx.IsPartial, "hash index should not be partial")
	require.Emptyf(t, idx.Predicate, "non-partial index should have empty predicate")
	require.Equal(t, []string{"category"}, idx.Columns)
	require.NotEmpty(t, idx.Definition)
	require.Containsf(t, idx.Definition, "USING hash", "definition should mention hash method")
}

func TestPostgresTool_FetchTableIndexes_PrimaryKey(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t, indexTestMigrations())

	resp, err := tool.getTableInfo(t.Context(), PostgresTableInfoRequest{
		Schema: "public",
		Table:  "products",
	})
	require.NoError(t, err)

	productsInfo := findTableInfo(t, resp, "products")
	require.NotNil(t, productsInfo)

	// Test primary key index (automatically created)
	idx := findIndex(t, productsInfo, "products_pkey")
	require.NotNilf(t, idx, "products_pkey (primary key) should be found")

	require.Equal(t, "products_pkey", idx.Name)
	require.Equalf(t, "btree", idx.Method, "primary key should use btree method")
	require.Truef(t, idx.IsPrimary, "index should be marked as primary key")
	require.Truef(t, idx.IsUnique, "primary key should be unique")
	require.Falsef(t, idx.IsPartial, "primary key should not be partial")
	require.Empty(t, idx.Predicate)
	require.Equal(t, []string{"id"}, idx.Columns)
	require.NotEmpty(t, idx.Definition)
}

func TestPostgresTool_FetchTableIndexes_AllDefinitionsPresent(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t, indexTestMigrations())

	resp, err := tool.getTableInfo(t.Context(), PostgresTableInfoRequest{
		Schema: "public",
		Table:  "products",
	})
	require.NoError(t, err)

	productsInfo := findTableInfo(t, resp, "products")
	require.NotNil(t, productsInfo)
	require.NotEmpty(t, productsInfo.Indexes)

	// All indexes should have a definition
	for _, idx := range productsInfo.Indexes {
		require.NotEmptyf(t, idx.Definition,
			"index %s should have a definition", idx.Name)
		require.Containsf(t, idx.Definition, "CREATE",
			"definition for %s should contain CREATE keyword", idx.Name)
	}
}

func TestPostgresTool_FetchTableIndexes_AllMethodsPresent(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t, indexTestMigrations())

	resp, err := tool.getTableInfo(t.Context(), PostgresTableInfoRequest{
		Schema: "public",
		Table:  "products",
	})
	require.NoError(t, err)

	productsInfo := findTableInfo(t, resp, "products")
	require.NotNil(t, productsInfo)
	require.NotEmpty(t, productsInfo.Indexes)

	// All indexes should have a method
	for _, idx := range productsInfo.Indexes {
		require.NotEmptyf(t, idx.Method,
			"index %s should have a method", idx.Name)
		// Valid methods in PostgreSQL
		validMethods := map[string]bool{
			"btree":  true,
			"hash":   true,
			"gin":    true,
			"gist":   true,
			"brin":   true,
			"spgist": true,
		}
		require.Truef(t, validMethods[idx.Method],
			"index %s has invalid method: %s", idx.Name, idx.Method)
	}
}

func TestPostgresTool_FetchTableIndexes_PredicateConsistency(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t, indexTestMigrations())

	resp, err := tool.getTableInfo(t.Context(), PostgresTableInfoRequest{
		Schema: "public",
		Table:  "products",
	})
	require.NoError(t, err)

	productsInfo := findTableInfo(t, resp, "products")
	require.NotNil(t, productsInfo)

	// Verify IsPartial flag is consistent with Predicate presence
	for _, idx := range productsInfo.Indexes {
		if idx.IsPartial {
			require.NotEmptyf(t, idx.Predicate,
				"partial index %s should have a predicate", idx.Name)
			require.Containsf(t, idx.Definition, "WHERE",
				"partial index %s definition should contain WHERE", idx.Name)
		} else {
			require.Emptyf(t, idx.Predicate,
				"non-partial index %s should not have a predicate", idx.Name)
		}
	}
}

func TestPostgresTool_FetchTableIndexes_IndexCount(t *testing.T) {
	t.Parallel()

	tool := postgresTestTool(t, indexTestMigrations())

	resp, err := tool.getTableInfo(t.Context(), PostgresTableInfoRequest{
		Schema: "public",
		Table:  "products",
	})
	require.NoError(t, err)

	productsInfo := findTableInfo(t, resp, "products")
	require.NotNil(t, productsInfo)

	// Should have 6 indexes:
	// 1. products_pkey (primary key)
	// 2. idx_products_name (regular btree)
	// 3. idx_products_category_price (unique composite)
	// 4. idx_products_active (partial)
	// 5. idx_products_expensive (partial complex)
	// 6. idx_products_category_hash (hash)
	expectedIndexes := []string{
		"products_pkey",
		"idx_products_name",
		"idx_products_category_price",
		"idx_products_active",
		"idx_products_expensive",
		"idx_products_category_hash",
	}

	require.Lenf(t, productsInfo.Indexes, len(expectedIndexes),
		"should have exactly %d indexes", len(expectedIndexes))

	// Verify all expected indexes are present
	for _, expectedName := range expectedIndexes {
		idx := findIndex(t, productsInfo, expectedName)
		require.NotNilf(t, idx, "expected index %s should be found", expectedName)
	}
}
