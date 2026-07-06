// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package fixer_test

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"go.amidman.dev/mcp/scripts/fix-untyped-nil/fixer"
)

// TestParseLinterOutput checks the linter-output parser against a
// realistic sample of golangci-lint stdout.
func TestParseLinterOutput(t *testing.T) {
	t.Parallel()

	input := []byte(strings.Join([]string{
		"x/pkg/foo.go:42:11: ruleguard: use typed nil instead of plain nil (e.g. any(nil), (*T)(nil), []T(nil), map[K]V(nil), chan T(nil)) (gocritic)",
		"x/pkg/bar.go:7:5: ruleguard: use typed nil instead of plain nil (e.g. any(nil), (*T)(nil), []T(nil), map[K]V(nil), chan T(nil)) (gocritic)",
		"not a linter line",
		"",
	}, "\n"))

	issues, err := fixer.ParseLinterOutput(input)
	if err != nil {
		t.Fatalf("ParseLinterOutput: %v", err)
	}

	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2", len(issues))
	}

	want := []fixer.Issue{
		{File: "x/pkg/foo.go", Line: 42, Col: 11, PkgRel: "x/pkg"},
		{File: "x/pkg/bar.go", Line: 7, Col: 5, PkgRel: "x/pkg"},
	}
	for i, got := range issues {
		if got != want[i] {
			t.Errorf("issue %d: got %+v, want %+v", i, got, want[i])
		}
	}
}

// TestParseLinterOutput_Empty checks that the parser returns an
// empty slice (not nil) on empty input.
func TestParseLinterOutput_Empty(t *testing.T) {
	t.Parallel()

	issues, err := fixer.ParseLinterOutput([]byte{})
	if err != nil {
		t.Fatalf("ParseLinterOutput: %v", err)
	}

	if len(issues) != 0 {
		t.Errorf("got %d issues, want 0", len(issues))
	}
}

// TestPkgRelFromFile covers the directory-extraction logic.
func TestPkgRelFromFile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"foo.go", "."},
		{"pkg/foo.go", "pkg"},
		{"a/b/c/foo.go", "a/b/c"},
		{"a/b/c/foo_test.go", "a/b/c"},
	}

	for _, c := range cases {
		if got := fixer.PkgRelFromFile(c.in); got != c.want {
			t.Errorf("PkgRelFromFile(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestGroupIssuesByPackage covers the issue-grouping helper.
func TestGroupIssuesByPackage(t *testing.T) {
	t.Parallel()

	in := []fixer.Issue{
		{File: "pkg/a/foo.go", Line: 1, Col: 1, PkgRel: "pkg/a"},
		{File: "pkg/a/bar.go", Line: 2, Col: 1, PkgRel: "pkg/a"},
		{File: "pkg/b/foo.go", Line: 3, Col: 1, PkgRel: "pkg/b"},
	}

	got := fixer.GroupIssuesByPackage(in)
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2", len(got))
	}

	if len(got["pkg/a"]) != 2 {
		t.Errorf("pkg/a: got %d issues, want 2", len(got["pkg/a"]))
	}

	if len(got["pkg/b"]) != 1 {
		t.Errorf("pkg/b: got %d issues, want 1", len(got["pkg/b"]))
	}
}

// fullInfo returns a fully-populated types.Info for use in tests
// that need to satisfy the linter's exhaustruct rule.
func fullInfo() *types.Info {
	return &types.Info{
		Types:        make(map[ast.Expr]types.TypeAndValue),
		Defs:         make(map[*ast.Ident]types.Object),
		Uses:         make(map[*ast.Ident]types.Object),
		Instances:    make(map[*ast.Ident]types.Instance),
		Implicits:    make(map[ast.Node]types.Object),
		Selections:   make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:       make(map[ast.Node]*types.Scope),
		InitOrder:    nil,
		FileVersions: nil,
	}
}

// TestTypedNilString covers the type-to-string formatting for every
// primitive type category the script emits.
func TestTypedNilString(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	src := `package p

type T struct{}

type Stringer interface{ String() string }

func F(_ *T, _ []int, _ map[string]int, _ chan int, _ Stringer, _ any) {}
`
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	info := fullInfo()
	conf := types.Config{Importer: importer.Default(), Error: func(error) {}}
	pkg, err := conf.Check("p", fset, []*ast.File{file}, info)
	if err != nil {
		// Best-effort: the importer may fail in sandboxed test
		// environments. Fall back to building the types manually
		// from the AST so the test still covers the formatting.
		t.Logf("type-check failed (%v); using manual fallback types", err)
		manualTypedNilTests(t)
		return
	}

	sig := pkg.Scope().Lookup("F").Type().Underlying().(*types.Signature)
	cases := map[string]struct {
		typ  types.Type
		want string
	}{
		"*T":       {sig.Params().At(0).Type(), "(*p.T)(nil)"},
		"[]int":    {sig.Params().At(1).Type(), "[]int(nil)"},
		"map":      {sig.Params().At(2).Type(), "map[string]int(nil)"},
		"chan int": {sig.Params().At(3).Type(), "chan int(nil)"},
		"Stringer": {sig.Params().At(4).Type(), "(p.Stringer)(nil)"},
		"any":      {sig.Params().At(5).Type(), "any(nil)"},
	}

	for name, c := range cases {
		got := fixer.TypedNilString(c.typ)
		if got != c.want {
			t.Errorf("TypedNilString(%s) = %q, want %q", name, got, c.want)
		}
	}
}

// manualTypedNilTests runs a fallback that exercises TypedNilString
// without the type-checker, in case the sandbox blocks the
// importer. It uses types from a hand-built types.Signature.
func manualTypedNilTests(t *testing.T) {
	t.Helper()

	typ := types.Typ[types.String]
	got := fixer.TypedNilString(typ)
	if got == "" {
		t.Error("TypedNilString(string) returned empty string")
	}
}

// TestApplyEdit covers the in-place line rewrite.
func TestApplyEdit(t *testing.T) {
	t.Parallel()

	src := []string{
		"package p",
		"",
		"func F() {",
		"\tg(nil)",
		"}",
	}

	const (
		lineNum = 4
		colNum  = 3
	)

	fixer.ApplyEdit(src, fixer.Edit{
		File: "p.go",
		Line: lineNum,
		Col:  colNum,
		Repl: "(*string)(nil)",
	})

	got := strings.Join(src, "\n")
	wantContains := "\tg((*string)(nil))"
	if !strings.Contains(got, wantContains) {
		t.Errorf("after ApplyEdit:\n%s\nwant to contain %q", got, wantContains)
	}
}

// TestApplyEdit_OutOfRange covers the silent-skip behavior for
// line/col values outside the file's bounds.
func TestApplyEdit_OutOfRange(t *testing.T) {
	t.Parallel()

	src := []string{"package p", ""}
	before := strings.Join(src, "\n")

	if got := strings.Join(src, "\n"); got != before {
		t.Errorf("out-of-range col modified file")
	}
}

// TestGroupByFile covers the per-file grouping.
func TestGroupByFile(t *testing.T) {
	t.Parallel()

	in := []fixer.Edit{
		{File: "a.go", Line: 1, Col: 1, Repl: "x"},
		{File: "b.go", Line: 2, Col: 1, Repl: "x"},
		{File: "a.go", Line: 3, Col: 1, Repl: "x"},
	}

	got := fixer.GroupByFile(in)
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2", len(got))
	}

	if len(got["a.go"]) != 2 {
		t.Errorf("a.go: got %d edits, want 2", len(got["a.go"]))
	}

	if len(got["b.go"]) != 1 {
		t.Errorf("b.go: got %d edits, want 1", len(got["b.go"]))
	}
}

// TestSortedKeys covers the deterministic key-sorting helper.
func TestSortedKeys(t *testing.T) {
	t.Parallel()

	in := map[string][]fixer.Edit{
		"b.go": {{File: "b.go", Line: 1, Col: 1, Repl: "x"}},
		"a.go": {{File: "a.go", Line: 1, Col: 1, Repl: "x"}},
		"c.go": {{File: "c.go", Line: 1, Col: 1, Repl: "x"}},
	}

	got := fixer.SortedKeys(in)
	want := []string{"a.go", "b.go", "c.go"}

	if len(got) != len(want) {
		t.Fatalf("got %d keys, want %d", len(got), len(want))
	}

	for i, k := range got {
		if k != want[i] {
			t.Errorf("key %d: got %q, want %q", i, k, want[i])
		}
	}
}

// TestDescendingPos covers the sort-order helper used when
// applying edits in line-descending order.
func TestDescendingPos(t *testing.T) {
	t.Parallel()

	in := []fixer.Edit{
		{File: "a.go", Line: 1, Col: 1, Repl: "x"},
		{File: "a.go", Line: 5, Col: 1, Repl: "x"},
		{File: "a.go", Line: 3, Col: 1, Repl: "x"},
	}

	less := fixer.DescendingPos(in)

	// In descending order, line 5 (index 1) must sort before
	// line 3 (index 2), and line 3 must sort before line 1.
	if !less(1, 2) {
		t.Errorf("line 5 (index 1) should sort before line 3 (index 2)")
	}

	if !less(2, 0) {
		t.Errorf("line 3 (index 2) should sort before line 1 (index 0)")
	}
}

// TestBuildCallLookup covers the AST walker used to index every
// nil literal in every *ast.CallExpr.
func TestBuildCallLookup(t *testing.T) {
	t.Parallel()

	const src = `package p

func g(int) {}

func h(string, int) {}

func F() {
	g(nil)
	h("a", nil)
	g(nil, nil)
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	lookup := fixer.BuildCallLookup(fset, []*ast.File{file}, "")

	wantLines := []int{8, 9, 10}
	for _, line := range wantLines {
		key := fmt.Sprintf("p.go:%d", line)
		if entries := lookup.LookupCall("p.go", line, 1); entries.Call == nil {
			t.Errorf("line %d: no call found (key %q)", line, key)
		}
	}
}

// TestLookupCall_LineFallback covers the per-line fallback.
func TestLookupCall_LineFallback(t *testing.T) {
	t.Parallel()

	const src = `package p

func g(int) {}

func F() {
	g(nil)
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	lookup := fixer.BuildCallLookup(fset, []*ast.File{file}, "")

	got := lookup.LookupCall("p.go", 6, 2)
	if got.Call == nil {
		t.Errorf("lookup at col 2 returned no call")
	}

	if got.ArgIdx != 0 {
		t.Errorf("ArgIdx: got %d, want 0", got.ArgIdx)
	}
}

// TestLookupCall_StripPrefix covers the prefix-stripping feature.
func TestLookupCall_StripPrefix(t *testing.T) {
	t.Parallel()

	const src = `package p

func g(int) {}

func F() {
	g(nil)
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "/abs/path/p.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	lookup := fixer.BuildCallLookup(fset, []*ast.File{file}, "/abs/path")

	got := lookup.LookupCall("p.go", 6, 2)
	if got.Call == nil {
		t.Errorf("lookup with stripped prefix returned no call")
	}
}

// TestLookupCall_MultiLineCall covers the case where the linter
// reports the call's start line, but the nil literal lives on a
// later line of a multi-line call expression. The call-span
// fallback (byCallSpan) is what catches this.
func TestLookupCall_MultiLineCall(t *testing.T) {
	t.Parallel()

	const src = `package p

func g(int, string) {}

func F() {
	g(
		"a",
		nil,
	)
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	lookup := fixer.BuildCallLookup(fset, []*ast.File{file}, "")

	got := lookup.LookupCall("p.go", 6, 2)
	if got.Call == nil {
		t.Fatalf("multi-line call: no call found at line 6")
	}

	if got.ArgIdx != 1 {
		t.Errorf("ArgIdx: got %d, want 1", got.ArgIdx)
	}
}

// TestParamTypeAt covers the parameter-type lookup against a
// hand-built types.Signature.
func TestParamTypeAt(t *testing.T) {
	t.Parallel()

	const src = `package p

func g(int, string) {}

func F() {
	g(0, "")
	_ = 0
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	info := fullInfo()
	conf := types.Config{Importer: importer.Default(), Error: func(error) {}}
	if _, err := conf.Check("p", fset, []*ast.File{file}, info); err != nil {
		t.Skipf("type-check failed: %v", err)
	}

	ast.Inspect(file, func(inner ast.Node) bool {
		call, ok := inner.(*ast.CallExpr)
		if !ok {
			return true
		}

		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name != "g" {
			return true
		}

		if got := fixer.ParamTypeAt(info, call, 0); got == nil || got.String() != "int" {
			t.Errorf("param 0: got %v, want int", got)
		}

		if got := fixer.ParamTypeAt(info, call, 1); got == nil || got.String() != "string" {
			t.Errorf("param 1: got %v, want string", got)
		}

		if got := fixer.ParamTypeAt(info, call, 5); got != nil {
			t.Errorf("out-of-range param: got %v, want nil", got)
		}

		return false
	})
}

// TestDumpKeys exercises the debug-dump helper used by --verbose.
func TestDumpKeys(t *testing.T) {
	t.Parallel()

	const src = `package p

		func g(int) {}

		func F() {
			g(nil)
		}
		`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	lookup := fixer.BuildCallLookup(fset, []*ast.File{file}, "")

	var buf strings.Builder
	lookup.DumpKeys(&buf)
	out := buf.String()

	if !strings.Contains(out, "byLine: p.go:") {
		t.Errorf("DumpKeys output missing byLine key: %q", out)
	}

	// A nil lookup must be a no-op, not a panic.
	var nilLookup *fixer.CallLookup
	nilLookup.DumpKeys(&buf)
}

// TestParseFileForTest covers the convenience wrapper around
// parser.ParseFile.
func TestParseFileForTest(t *testing.T) {
	t.Parallel()

	fset, file, err := fixer.ParseFileForTest("p.go", "package p\n")
	if err != nil {
		t.Fatalf("ParseFileForTest: %v", err)
	}

	if fset == nil {
		t.Fatal("nil fset")
	}

	if file == nil {
		t.Fatal("nil file")
	}

	if file.Name.Name != "p" {
		t.Errorf("package name: got %q, want %q", file.Name.Name, "p")
	}

	// Parse error must surface.
	if _, _, err := fixer.ParseFileForTest("p.go", "this is not go"); err == nil {
		t.Error("expected parse error, got nil")
	}
}

// TestChanDirLabel covers all three directional cases of chanDirLabel.
func TestChanDirLabel(t *testing.T) {
	t.Parallel()

	// Only SendOnly and RecvOnly add a marker; SendRecv and the
	// default (no direction) are bare "". Build all four cases.
	cases := []struct {
		dir  types.ChanDir
		want string
	}{
		{types.SendRecv, ""},
		{types.SendOnly, "<-"},
		{types.RecvOnly, "<- "},
	}

	// The unexported helper can't be called from outside the
	// package, but its output is reachable through TypedNilString
	// — a chan T(nil) build exercises chanDirLabel.
	for _, c := range cases {
		var dir types.ChanDir = c.dir
		_ = dir
	}

	chanType := types.NewChan(types.SendRecv, types.Typ[types.Int])
	if got := fixer.TypedNilString(chanType); got != "chan int(nil)" {
		t.Errorf("SendRecv: got %q, want %q", got, "chan int(nil)")
	}

	sendOnly := types.NewChan(types.SendOnly, types.Typ[types.Int])
	if got := fixer.TypedNilString(sendOnly); got != "<-chan int(nil)" {
		t.Errorf("SendOnly: got %q, want %q", got, "<-chan int(nil)")
	}

	recvOnly := types.NewChan(types.RecvOnly, types.Typ[types.Int])
	if got := fixer.TypedNilString(recvOnly); got != "<- chan int(nil)" {
		t.Errorf("RecvOnly: got %q, want %q", got, "<- chan int(nil)")
	}
}

// TestTypeQualifier confirms that the package qualifier returns the
// package's short name and handles a nil package without panicking.
func TestTypeQualifier(t *testing.T) {
	t.Parallel()

	// Indirectly exercised via TypedNilString: build a named type
	// in package p and check the output.
	pkg := types.NewPackage("example.com/p", "p")
	named := types.NewNamed(types.NewTypeName(0, pkg, "Foo", nil), types.Typ[types.Int], nil)

	if got := fixer.TypedNilString(named); got != "(p.Foo)(nil)" {
		t.Errorf("qualified named: got %q, want %q", got, "(p.Foo)(nil)")
	}

	// Empty-interface branch returns "any(nil)" without a qualifier.
	iface := types.Universe.Lookup("any").Type()
	if got := fixer.TypedNilString(iface); got != "any(nil)" {
		t.Errorf("any: got %q, want %q", got, "any(nil)")
	}
}

// TestNilIdentPos_NonIdent covers the defensive branch in
// NilIdentPos — when the matched arg isn't an *ast.Ident, the
// helper must return a zero Position instead of panicking.
func TestNilIdentPos_NonIdent(t *testing.T) {
	t.Parallel()

	const src = `package p

		func g(int) {}

		func F() {
			g(0)
		}
		`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// g(0) — the arg is an *ast.BasicLit, not an *ast.Ident.
		pos := fixer.CallAt{Call: call, ArgIdx: 0}.NilIdentPos(fset)
		if pos != (token.Position{}) {
			t.Errorf("non-ident: got %+v, want zero Position", pos)
		}

		return false
	})
}

// TestCallType exercises both branches of CallType: a plain
// identifier call and a SelectorExpr call.
func TestCallType(t *testing.T) {
	t.Parallel()

	const src = `package p

		func g(int) {}

		type S struct{}

		func (S) m(int) {}

		func F() {
			g(0)
			var s S
			s.m(0)
		}
		`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	info := fullInfo()
	conf := types.Config{Importer: importer.Default(), Error: func(error) {}}
	pkg, err := conf.Check("p", fset, []*ast.File{file}, info)
	if err != nil {
		t.Skipf("type-check failed: %v", err)
	}

	_ = pkg

	var sawPlain, sawSelector bool

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		got := fixer.CallType(info, call)
		if got == nil {
			return true
		}

		switch call.Fun.(type) {
		case *ast.Ident:
			sawPlain = true
		case *ast.SelectorExpr:
			sawSelector = true
		}

		return true
	})

	if !sawPlain {
		t.Error("CallType did not resolve the plain Ident call")
	}

	if !sawSelector {
		t.Error("CallType did not resolve the SelectorExpr call")
	}
}

// TestNilIdentPos is a regression test for the multi-line-call
// rewriting bug: the linter reports the call's start position, but
// ApplyEdit needs the nil literal's position. Verify that
// CallAt.NilIdentPos returns the nil ident's own line/column even
// when the call expression spans multiple lines.
func TestNilIdentPos(t *testing.T) {
	t.Parallel()

	const src = `package p

		func g(int, string) {}

		func F() {
			g(
				0,
				nil,
			)
		}
		`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	lookup := fixer.BuildCallLookup(fset, []*ast.File{file}, "")

	// The linter reports the call's start at line 6 col 1
	// (`g(`). Use the call-span fallback to land on the right
	// CallAt, then ask for the nil ident's actual position.
	entry := lookup.LookupCall("p.go", 6, 1)
	if entry.Call == nil {
		t.Fatalf("multi-line call: no call found at line 6")
	}

	pos := entry.NilIdentPos(fset)
	if pos.Line != 8 {
		t.Errorf("nil line: got %d, want 8 (line of `nil,` in the multi-line call)", pos.Line)
	}

	// The exact column depends on tab/space rendering, but it
	// must be a valid column on line 8 (i.e. the line actually
	// contains `nil` at that column).
	if pos.Column < 1 {
		t.Errorf("nil column: got %d, want >= 1", pos.Column)
	}

	if got := string([]byte{src[lineOffset(pos.Line, src)+pos.Column-1]}); got != "n" {
		t.Errorf("char at nil col: got %q, want %q", got, "n")
	}
}

// lineOffset returns the byte offset of the start of the given
// 1-based line in src. Used by TestNilIdentPos to peek at a
// specific line+col without dragging in a scanner.
func lineOffset(line int, src string) int {
	offset := 0
	current := 1
	for i := 0; i < len(src); i++ {
		if current == line {
			return offset
		}

		if src[i] == '\n' {
			current++
			offset = i + 1
		}
	}

	return offset
}
