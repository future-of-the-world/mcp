// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package fixer is the testable core of scripts/fix-untyped-nil.
// It owns the linter-output parser, the AST/type-checker lookup,
// the typed-nil formatter, and the file-rewrite logic. The
// command-line wrapper in ../main.go glues these pieces together
// with the linter driver and the runFormatters step.
package fixer

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Issue represents a single gocritic "use typed nil" diagnostic.
type Issue struct {
	File   string
	Line   int
	Col    int
	PkgRel string
}

// Edit is a single nil literal replacement, tagged with the file path.
type Edit struct {
	File string
	Line int
	Col  int
	Repl string
}

// PackageData bundles the loaded package's AST + type info.
type PackageData struct {
	Files []*ast.File
	Info  *types.Info
}

// CallAt is a *ast.CallExpr plus the index of the nil arg within
// the call's argument list.
type CallAt struct {
	Call   *ast.CallExpr
	ArgIdx int
}

// lintLineRE matches `path:line:col: ruleguard: use typed nil ...`.
// The full message text varies between golangci-lint versions, so
// we match only the prefix we need.
var lintLineRE = regexp.MustCompile(`^([^\s:]+):(\d+):(\d+): ruleguard: use typed nil`)

// ParseLinterOutput extracts issues from golangci-lint's stdout.
func ParseLinterOutput(out []byte) ([]Issue, error) {
	var issues []Issue

	for _, line := range strings.Split(string(out), "\n") {
		match := lintLineRE.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		lineNum, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}

		colNum, err := strconv.Atoi(match[3])
		if err != nil {
			continue
		}

		issues = append(issues, Issue{
			File:   match[1],
			Line:   lineNum,
			Col:    colNum,
			PkgRel: PkgRelFromFile(match[1]),
		})
	}

	return issues, nil
}

// PkgRelFromFile returns the package-relative directory of a
// file path, used to group issues by package.
func PkgRelFromFile(file string) string {
	idx := strings.LastIndex(file, "/")
	if idx < 0 {
		return "."
	}

	return file[:idx]
}

// GroupIssuesByPackage groups issues by their package directory.
func GroupIssuesByPackage(issues []Issue) map[string][]Issue {
	byPkg := make(map[string][]Issue, len(issues))

	for _, item := range issues {
		byPkg[item.PkgRel] = append(byPkg[item.PkgRel], item)
	}

	return byPkg
}

// BuildCallLookup walks every parsed file and indexes every
// *ast.CallExpr that has a nil literal in its args. The lookup is
// keyed by both (file, line, col) of the nil ident and (file, line)
// as a fallback — the linter reports the call's start column, not
// the nil arg's, so most lookups will go through the line fallback.
//
// The optional stripPrefix is used to convert the fset's absolute
// file paths to module-relative paths, matching the format the
// linter emits. The prefix is resolved to an absolute path before
// use so the strip works regardless of whether the caller passed
// "." or "/abs/path". If empty, the fset's filename is used
// as-is.
func BuildCallLookup(fset *token.FileSet, files []*ast.File, stripPrefix string) *CallLookup {
	lookup := &CallLookup{
		byExact:    make(map[string]CallAt),
		byLine:     make(map[string][]CallAt),
		byCallSpan: make(map[string][]callSpanEntry),
	}

	absPrefix := stripPrefix
	if absPrefix != "" && !filepath.IsAbs(absPrefix) {
		if p, err := filepath.Abs(absPrefix); err == nil {
			absPrefix = p
		}
	}

	relFile := func(filename string) string {
		if absPrefix != "" && strings.HasPrefix(filename, absPrefix) {
			filename = strings.TrimPrefix(filename, absPrefix)
			filename = strings.TrimPrefix(filename, "/")
		}

		return filename
	}

	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			callPos := fset.Position(call.Pos())
			callEndPos := fset.Position(call.End())

			for i, arg := range call.Args {
				ident, ok := arg.(*ast.Ident)
				if !ok || ident.Name != nilLiteral {
					continue
				}

				pos := fset.Position(ident.NamePos)
				fileName := relFile(pos.Filename)

				exactKey := fmt.Sprintf("%s:%d:%d", fileName, pos.Line, pos.Column)
				lineKey := fmt.Sprintf("%s:%d", fileName, pos.Line)
				spanKey := fmt.Sprintf("%s:%d", fileName, callPos.Line)

				entry := CallAt{Call: call, ArgIdx: i}
				lookup.byExact[exactKey] = entry
				lookup.byLine[lineKey] = append(lookup.byLine[lineKey], entry)
				lookup.byCallSpan[spanKey] = append(
					lookup.byCallSpan[spanKey],
					callSpanEntry{entry: entry, endLine: callEndPos.Line},
				)
			}

			return true
		})
	}

	return lookup
}

// callSpanEntry is a single (call, argIdx) plus the line on which
// the call expression ends. Used by the call-span lookup to
// distinguish multi-line calls where the nil lives on a line
// other than the call's start line.
type callSpanEntry struct {
	entry   CallAt
	endLine int
}

// CallLookup is the index built by BuildCallLookup. The lookup
// methods take an Issue and return the matching CallAt, or a zero
// value if no call is found.
type CallLookup struct {
	byExact    map[string]CallAt
	byLine     map[string][]CallAt
	byCallSpan map[string][]callSpanEntry
}

// LookupCall returns the CallAt for the given issue. Lookup
// proceeds in three stages:
//
//  1. Exact (file:line:col) match — works when the linter
//     reports the nil ident's position.
//  2. Per-line match — works when the linter reports the nil's
//     line and the call is single-line.
//  3. Per-call-start-line match — works when the linter reports
//     the call's start line but the nil is on a different line
//     (multi-line call expressions).
func (l *CallLookup) LookupCall(file string, line, col int) CallAt {
	if l == nil {
		return CallAt{}
	}

	exactKey := fmt.Sprintf("%s:%d:%d", file, line, col)
	if entry, ok := l.byExact[exactKey]; ok {
		return entry
	}

	lineKey := fmt.Sprintf("%s:%d", file, line)
	if entries, ok := l.byLine[lineKey]; ok && len(entries) > 0 {
		return entries[0]
	}

	// The linter reports the call's start column, not the nil's,
	// and for multi-line call expressions the nil can be on a
	// later line. Search the call-span index for a call whose
	// start line matches the linter's line; then return any of
	// its nil-arg entries (the linter emits one issue per nil).
	spanKey := fmt.Sprintf("%s:%d", file, line)
	if entries, ok := l.byCallSpan[spanKey]; ok && len(entries) > 0 {
		return entries[0].entry
	}

	return CallAt{}
}

// DumpKeys writes every indexed key to w. Useful for debugging
// lookup mismatches — used by tests and by main.go's --verbose
// path. Each key is on its own line.
func (l *CallLookup) DumpKeys(w interface{ Write(p []byte) (int, error) }) {
	if l == nil {
		return
	}

	for k := range l.byLine {
		fmt.Fprintf(w, "byLine: %s\n", k)
	}

	for k := range l.byExact {
		fmt.Fprintf(w, "byExact: %s\n", k)
	}
}

// NilIdentPos returns the token.Position of the nil ident that the
// CallAt refers to. The linter reports the call's start position
// (file:line:col), but ApplyEdit needs the nil literal's own
// position to do an in-place rewrite — for multi-line calls these
// differ in line, and even for single-line calls they usually
// differ in column. Returns a zero Position if the arg isn't an
// *ast.Ident, which only happens for already-typed nils.
func (c CallAt) NilIdentPos(fset *token.FileSet) token.Position {
	ident, ok := c.Call.Args[c.ArgIdx].(*ast.Ident)
	if !ok || ident == nil {
		return token.Position{}
	}

	return fset.Position(ident.NamePos)
}

// ParamTypeAt returns the type of the paramIdx-th parameter of the
// function call `call`. Returns nil if the type can't be resolved.
func ParamTypeAt(info *types.Info, call *ast.CallExpr, paramIdx int) types.Type {
	fnType := CallType(info, call)
	if fnType == nil {
		return nil
	}

	sig, ok := fnType.(*types.Signature)
	if !ok {
		return nil
	}

	if paramIdx >= sig.Params().Len() {
		return nil
	}

	return sig.Params().At(paramIdx).Type()
}

// CallType returns the function type for `call`. Handles both
// `f(args)` (Ident) and `pkg.F(args)` (SelectorExpr) call shapes.
//
// Why the explicit Info.Selections / Info.Uses dance: a SelectorExpr's
// type isn't always present in info.Types (the type-checker records
// it for the call site itself, not for the selector expression).
func CallType(info *types.Info, call *ast.CallExpr) types.Type {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		obj, ok := info.Uses[fun]
		if !ok {
			return nil
		}

		return obj.Type()

	case *ast.SelectorExpr:
		if sel, ok := info.Selections[fun]; ok {
			return sel.Type()
		}

		obj, ok := info.Uses[fun.Sel]
		if !ok {
			return nil
		}

		return obj.Type()
	}

	return nil
}

// TypedNilString formats a typed-nil literal for the given type. The
// output is a single Go expression: `any(nil)`, `(*T)(nil)`,
// `[]T(nil)`, `map[K]V(nil)`, `chan T(nil)`, `(pkg.T)(nil)`.
func TypedNilString(typ types.Type) string {
	// `any` is an alias for the empty interface. The type-checker
	// represents it as *types.Named wrapping *types.Interface{}; we
	// unwrap to detect it.
	if iface, ok := typ.Underlying().(*types.Interface); ok && iface.Empty() {
		return "any(nil)"
	}

	switch tt := typ.(type) {
	case *types.Slice:
		return fmt.Sprintf("[]%s(nil)", types.TypeString(tt.Elem(), typeQualifier))

	case *types.Map:
		return fmt.Sprintf("map[%s]%s(nil)",
			types.TypeString(tt.Key(), typeQualifier),
			types.TypeString(tt.Elem(), typeQualifier))

	case *types.Chan:
		dir := chanDirLabel(tt.Dir())
		elem := types.TypeString(tt.Elem(), typeQualifier)
		return fmt.Sprintf("%schan %s(nil)", dir, elem)

	case *types.Pointer:
		return fmt.Sprintf("(%s)(nil)", types.TypeString(tt, typeQualifier))

	case *types.Named:
		return fmt.Sprintf("(%s)(nil)", types.TypeString(tt, typeQualifier))

	case *types.Interface:
		if tt.Empty() {
			return "any(nil)"
		}

		return fmt.Sprintf("(%s)(nil)", types.TypeString(tt, typeQualifier))

	default:
		return fmt.Sprintf("(%s)(nil)", types.TypeString(typ, typeQualifier))
	}
}

// chanDirLabel renders the directional marker for a channel type.
func chanDirLabel(dir types.ChanDir) string {
	if dir == types.SendOnly {
		return "<-"
	}

	if dir == types.RecvOnly {
		return "<- "
	}

	return ""
}

func typeQualifier(p *types.Package) string {
	if p == nil {
		return ""
	}

	return p.Name()
}

// ApplyEdit mutates lines in place: replaces the literal "nil" at
// (line, col) with the typed-nil replacement. Out-of-range lines
// and missing nil idents are silently skipped.
//
// Line and column numbers are 1-based (matching the linter output).
func ApplyEdit(lines []string, item Edit) {
	const (
		lineOffset = 1
		nilLiteral = "nil"
	)

	idx := item.Line - lineOffset
	if idx < 0 || idx >= len(lines) {
		return
	}

	col := item.Col - lineOffset
	if col < 0 || col > len(lines[idx]) {
		return
	}

	startIdx := strings.Index(lines[idx][col:], nilLiteral)
	if startIdx < 0 {
		return
	}

	absStart := col + startIdx
	absEnd := absStart + len(nilLiteral)
	lines[idx] = lines[idx][:absStart] + item.Repl + lines[idx][absEnd:]
}

// GroupByFile groups edits by their file path.
func GroupByFile(edits []Edit) map[string][]Edit {
	byFile := make(map[string][]Edit, len(edits))

	for _, item := range edits {
		byFile[item.File] = append(byFile[item.File], item)
	}

	return byFile
}

// SortedKeys returns the map's keys in sorted order. Useful for
// deterministic output.
func SortedKeys(byFile map[string][]Edit) []string {
	keys := make([]string, 0, len(byFile))
	for k := range byFile {
		keys = append(keys, k)
	}

	sort.Strings(keys)
	return keys
}

// DescendingPos returns a Less function for sort.Slice that orders
// edits from last to first so earlier line indices stay valid as
// we mutate later ones.
func DescendingPos(edits []Edit) func(i, j int) bool {
	return func(i, j int) bool {
		if edits[i].Line != edits[j].Line {
			return edits[i].Line > edits[j].Line
		}

		return edits[i].Col > edits[j].Col
	}
}

// ParseFileForTest is a convenience wrapper used by tests that
// need an *ast.File plus the file set. It parses src as a Go file
// in the named package.
func ParseFileForTest(name, src string) (*token.FileSet, *ast.File, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.ParseComments)
	if err != nil {
		return nil, nil, err
	}

	return fset, file, nil
}

const nilLiteral = "nil"
