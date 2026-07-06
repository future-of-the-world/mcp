// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// scanRows reads all rows from a sql.Rows and returns them as a 2D slice.
func scanRows(rows *sql.Rows, columns []string) ([][]any, error) {
	result := [][]any{}

	for rows.Next() {
		row := make([]any, len(columns))

		valuePtrs := make([]any, len(columns))
		for i := range row {
			valuePtrs[i] = &row[i]
		}

		err := rows.Scan(valuePtrs...)
		if err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		sanitizeRow(row)

		result = append(result, row)
	}

	err := rows.Err()
	if err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	return result, nil
}

// sanitizeRow applies value sanitization to each element in the row.
func sanitizeRow(row []any) {
	for i, v := range row {
		row[i] = sanitizeValue(v)
	}
}

// sanitizeValue dispatches sanitization based on the value's underlying type.
func sanitizeValue(rowValue any) any {
	switch typedRowValue := rowValue.(type) {
	case []byte:
		return sanitizeBinary(typedRowValue)

	default:
		return typedRowValue
	}
}

// sanitizeBinary converts []byte containing valid JSON to json.RawMessage.
// Without this, encoding/json base64-encodes []byte, which is wrong
// for PostgreSQL JSONB/JSON columns. Non-JSON binary data (e.g. bytea) stays
// as []byte so it still gets base64-encoded, which is correct.
func sanitizeBinary(binary []byte) any {
	if json.Valid(binary) {
		return json.RawMessage(binary)
	}

	return binary
}

// stripOneLeadingComment removes one leading comment from a SQL query.
// It returns the stripped query and true if a comment was removed, or the original query and false.
func stripOneLeadingComment(query string) (string, bool) {
	// Remove single-line comments
	if strings.HasPrefix(query, "--") {
		_, after, ok := strings.Cut(query, "\n")
		if ok {
			return strings.TrimSpace(after), true
		}
	}
	// Remove multi-line comments
	if strings.HasPrefix(query, "/*") {
		_, after, ok := strings.Cut(query, "*/")
		if ok {
			return strings.TrimSpace(after), true
		}
	}

	return query, false
}

// stripLeadingComments removes leading single-line and multi-line comments from a SQL query.
func stripLeadingComments(query string) string {
	normalized := query

	for {
		stripped, changed := stripOneLeadingComment(normalized)
		if !changed {
			return stripped
		}

		normalized = stripped
	}
}

// hasReadOnlyPrefix checks if the normalized query starts with a read-only SQL keyword.
func hasReadOnlyPrefix(normalized string) bool {
	readOnlyPrefixes := []string{
		"SELECT",
		"SHOW",
		"EXPLAIN",
		"DESCRIBE",
		"DESC",
		"WITH",   // CTEs - we'll do additional validation
		"TABLE",  // Shorthand for SELECT * FROM
		"VALUES", // Standalone VALUES clause
	}

	for _, prefix := range readOnlyPrefixes {
		if normalized == prefix ||
			strings.HasPrefix(normalized, prefix+" ") ||
			strings.HasPrefix(normalized, prefix+"\t") ||
			strings.HasPrefix(normalized, prefix+"\n") {

			return true
		}
	}

	return false
}

// validateCTEIsReadOnly checks that a WITH (CTE) query doesn't contain modifying statements.
func validateCTEIsReadOnly(normalized string) error {
	modifyingKeywords := []string{
		"INSERT ", "UPDATE ", "DELETE ", "MERGE ",
		"CREATE ", "ALTER ", "DROP ", "TRUNCATE ",
		"GRANT ", "REVOKE ", "REINDEX ", "VACUUM ",
	}

	firstKeyword := ""
	firstIndex := len(normalized) + 1

	for _, keyword := range modifyingKeywords {
		if idx := strings.Index(normalized, keyword); idx != -1 && idx < firstIndex {
			firstIndex = idx
			firstKeyword = keyword
		}
	}

	if firstKeyword != "" {
		return fmt.Errorf(
			"query contains modifying statement: %s",
			strings.TrimSpace(firstKeyword),
		)
	}

	return nil
}

// ErrQueryNotReadOnly is returned when a query is not read-only.
var ErrQueryNotReadOnly = errors.New(
	"query is not read-only: only SELECT, SHOW, EXPLAIN, DESCRIBE, " +
		"and WITH (read-only CTEs) queries are allowed",
)

// isReadOnlyQuery checks if a SQL query is read-only (SELECT, SHOW, EXPLAIN, etc.)
// It returns an error if the query attempts to modify data or schema.
func isReadOnlyQuery(query string) error {
	// Normalize the query: trim whitespace and convert to uppercase for checking
	normalized := strings.TrimSpace(strings.ToUpper(query))

	// Remove leading comments
	normalized = stripLeadingComments(normalized)

	// Check if query starts with a read-only prefix
	if !hasReadOnlyPrefix(normalized) {
		return ErrQueryNotReadOnly
	}

	// Special handling for WITH (CTE) - check if it contains any modifying statements
	if strings.HasPrefix(normalized, "WITH ") {
		cteErr := validateCTEIsReadOnly(normalized)
		if cteErr != nil {
			return cteErr
		}
	}

	return nil
}
