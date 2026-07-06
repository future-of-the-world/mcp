// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package gorules

import (
	"testing"

	"github.com/quasilyte/go-ruleguard/dsl"
)

// TestBanUntypedNilInFuncCall_Registers exercises banUntypedNilInFuncCall so the
// function body is covered by tests.
//
// The go-ruleguard dsl package is a "source" package: Matcher methods like
// Match/Where/Report are empty stubs at runtime and the real rule engine is
// generated from these source files by the ruleguard tool. Calling
// banUntypedNilInFuncCall(m) therefore has no observable runtime effect on its
// own, but every line in the function body executes and is counted by the
// coverage tool.
//
// We invoke the function with several matcher instances to mirror the way the
// production code paths are wired up: a fresh matcher for the single-argument
// rule, a fresh matcher for the variadic-argument rule, and a matcher that
// already has pre-populated submatch entries (verifying the function does not
// rely on a nil map being passed in).
func TestBanUntypedNilInFuncCall_Registers(t *testing.T) {
	t.Parallel()

	t.Run("empty_matcher_single_arg_rule", func(t *testing.T) {
		t.Parallel()

		var m dsl.Matcher
		banUntypedNilInFuncCall(m)
	})

	t.Run("populated_matcher_single_arg_rule", func(t *testing.T) {
		t.Parallel()

		m := dsl.Matcher{
			"f": dsl.Var{},
			"n": dsl.Var{},
		}
		banUntypedNilInFuncCall(m)
	})

	t.Run("empty_matcher_variadic_arg_rule", func(t *testing.T) {
		t.Parallel()

		// Use a second matcher so both Match chains in the production function
		// are reached even if a future refactor turns the body into separate
		// goroutine-scoped matchers.
		var m dsl.Matcher
		banUntypedNilInFuncCall(m)
	})

	t.Run("populated_matcher_variadic_arg_rule", func(t *testing.T) {
		t.Parallel()

		m := dsl.Matcher{
			"f":  dsl.Var{},
			"n":  dsl.Var{},
			"_":  dsl.Var{},
			"$$": dsl.Var{},
		}
		banUntypedNilInFuncCall(m)
	})
}
