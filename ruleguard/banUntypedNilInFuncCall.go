// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package gorules

import "github.com/quasilyte/go-ruleguard/dsl"

// Rule for detecting untyped nil in arguments of function and method calls.
// Covers the single-argument call as well as the first, last, and
// middle positions in a multi-argument call.
func banUntypedNilInFuncCall(m dsl.Matcher) {
	m.Match(`$f(nil)`).
		Where((m["f"].Node.Is(`Ident`) || m["f"].Node.Is(`SelectorExpr`)) && !m["f"].Object.Is(`TypeName`)).
		Report("use typed nil instead of plain nil (e.g. any(nil), (*T)(nil), []T(nil), map[K]V(nil), chan T(nil))")
	m.Match(`$f($*_, nil, $*_)`).
		Where((m["f"].Node.Is(`Ident`) || m["f"].Node.Is(`SelectorExpr`)) && !m["f"].Object.Is(`TypeName`)).
		Report("use typed nil instead of plain nil (e.g. any(nil), (*T)(nil), []T(nil), map[K]V(nil), chan T(nil))")
}
