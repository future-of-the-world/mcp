// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package gorules

import "github.com/quasilyte/go-ruleguard/dsl"

// Rule for detecting untyped nil in fields of composite literals.
//
// The named wildcard $k enables per-field diagnostics: when a composite
// literal has several nil fields, each one gets its own diagnostic.
func banUntypedNilInCompositeLit(m dsl.Matcher) {
	m.Match(`$_{$k: nil}`).
		Report("use typed nil for field $k instead of plain nil (e.g. any(nil), (*T)(nil), []T(nil), map[K]V(nil), chan T(nil))")
	m.Match(`$_{$*_, $k: nil, $*_}`).
		Report("use typed nil for field $k instead of plain nil (e.g. any(nil), (*T)(nil), []T(nil), map[K]V(nil), chan T(nil))")
}
