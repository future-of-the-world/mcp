// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package english: types, request/response shapes, and category-filtering
// helpers used by the validate_english tool. The main Connect entry point
// and request/response structs live in english.go.
package english

import (
	"strings"

	"go.amidman.dev/mcp/english/langtoolapi"
)

// skipCategories contains LanguageTool category names whose matches should be skipped.
var skipCategories = map[string]bool{
	"style":      true,
	"typography": true,
	"whitespace": true,
	"redundancy": true,
}

// shouldSkipMatch returns true if the match belongs to a category that should be skipped.
func shouldSkipMatch(match *langtoolapi.Match) bool {
	if match.Rule == nil {
		return false
	}

	name := match.Rule.Category.Name
	if name == nil {
		return false
	}

	return skipCategories[strings.ToLower(*name)]
}
