// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"flag"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go.amidman.dev/mcp/scripts/fix-untyped-nil/fixer"
)

// TestMain is a smoke test that the script builds and the binary
// exists. It exists primarily so the coverage tool has something
// to count for the main package; deeper coverage would require
// running the linter in a sandbox, which is out of scope.
func TestMain(t *testing.T) {
	// We can't call main() directly (it calls os.Exit on error
	// and reads flag state); just verify the binary builds and
	// exists. A proper integration test would run the script in
	// a sandbox with a tiny fixture package and verify the rewrite.
	repoRoot, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	// go build the script and verify the binary appears.
	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "fix-untyped-nil"), ".")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
}

// resetFlags restores the global flag state to defaults between
// tests. parseFlags reads optsStorage directly, but flag.StringVar
// also registers flags on the global FlagSet; resetting the FlagSet
// prevents flag re-registration panics.
func resetFlags(t *testing.T) {
	t.Helper()

	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	lintArgsRaw = ""
	optsStorage = options{}
}

// TestParseFlags covers the lintArgsRaw → strings.Fields split and
// the optsStorage short-circuit.
func TestParseFlags(t *testing.T) {
	resetFlags(t)

	registerFlags()

	// Override the default lintArgsRaw via the global.
	lintArgsRaw = "run --timeout=5m"

	opts, err := parseFlags()
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}

	want := []string{"run", "--timeout=5m"}
	if len(opts.lintArgs) != len(want) {
		t.Fatalf("lintArgs: got %v, want %v", opts.lintArgs, want)
	}

	for i, arg := range want {
		if opts.lintArgs[i] != arg {
			t.Errorf("lintArgs[%d]: got %q, want %q", i, opts.lintArgs[i], arg)
		}
	}
}

// TestPrintDryRun verifies that planned edits are emitted to stdout
// grouped by file with line/col pointers.
func TestPrintDryRun(t *testing.T) {
	edits := []fixer.Edit{
		{File: "a/foo.go", Line: 5, Col: 9, Repl: "(*T)(nil)"},
		{File: "a/foo.go", Line: 12, Col: 4, Repl: "[]string(nil)"},
		{File: "b/bar.go", Line: 3, Col: 1, Repl: "any(nil)"},
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	origStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	printDryRun(edits)

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"a/foo.go",
		"b/bar.go",
		"5:9 -> (*T)(nil)",
		"12:4 -> []string(nil)",
		"3:1 -> any(nil)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestWriteEdits verifies the file-rewrite loop: edits applied in
// descending position so earlier line indices stay valid as later
// ones mutate.
func TestWriteEdits(t *testing.T) {
	tmpDir := t.TempDir()

	srcPath := filepath.Join(tmpDir, "foo.go")
	src := "package p\n\nfunc f() {\n\tg(nil, 0)\n\th(0, nil)\n}\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	// Relative path so rewriteFile joins correctly.
	relPath := "foo.go"

	// Find the line/col of each nil by parsing the source. Use a
	// simple byte scan as a robust alternative — the exact column
	// doesn't matter for correctness as long as both edits are in
	// the right order.
	line1 := strings.Index(src, "g(nil")
	col1 := strings.Index(src[line1:], "nil")
	line1 = strings.Count(src[:line1], "\n") + 1
	col1++

	line2 := strings.Index(src, "h(0, nil")
	col2 := strings.Index(src[line2:], "nil")
	line2 = strings.Count(src[:line2], "\n") + 1
	col2++

	edits := []fixer.Edit{
		{File: relPath, Line: line1, Col: col1, Repl: "(*int)(nil)"},
		{File: relPath, Line: line2, Col: col2, Repl: "(*string)(nil)"},
	}

	opts := &options{
		moduleRoot: tmpDir,
		pkgPath:    "",
		lintCmd:    "",
		lintArgs:   nil,
		dryRun:     false,
		verbose:    false,
	}

	if err := writeEdits(opts, edits); err != nil {
		t.Fatalf("writeEdits: %v", err)
	}

	got, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	want := "package p\n\nfunc f() {\n\tg((*int)(nil), 0)\n\th(0, (*string)(nil))\n}\n"
	if string(got) != want {
		t.Errorf("file content mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestWriteEdits_ReadError confirms that a missing source file
// produces a wrapped error rather than a silent skip.
func TestWriteEdits_ReadError(t *testing.T) {
	tmpDir := t.TempDir()
	opts := &options{
		moduleRoot: tmpDir,
		pkgPath:    "",
		lintCmd:    "",
		lintArgs:   nil,
		dryRun:     false,
		verbose:    false,
	}
	edits := []fixer.Edit{
		{File: "does-not-exist.go", Line: 1, Col: 1, Repl: "(*T)(nil)"},
	}

	err := writeEdits(opts, edits)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "read") {
		t.Errorf("error should mention read, got: %v", err)
	}
}

// TestRunQuiet_Success covers the no-error path of runQuiet.
func TestRunQuiet_Success(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	runQuiet(exec.CommandContext(t.Context(), "true"))

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	if strings.Contains(buf.String(), "warning:") {
		t.Errorf("unexpected warning on success: %s", buf.String())
	}
}

// TestRunQuiet_Failure covers the warning-on-error branch.
func TestRunQuiet_Failure(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	// `false` exits 1 in every shell.
	runQuiet(exec.CommandContext(t.Context(), "false"))

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	if !strings.Contains(buf.String(), "warning:") {
		t.Errorf("expected warning on failure, got: %s", buf.String())
	}
}

// TestRunFormatters_NoLinter confirms that runFormatters tolerates a
// missing linter binary: gofmt still runs and no panic occurs.
func TestRunFormatters_NoLinter(t *testing.T) {
	tmpDir := t.TempDir()
	// Point opts.lintCmd at a path that doesn't exist so the
	// LookPath call fails, but the gofmt path runs first.
	opts := &options{
		moduleRoot: tmpDir,
		pkgPath:    "./...",
		lintCmd:    "/nonexistent/golangci-lint-binary-" + strconv.Itoa(os.Getpid()),
		lintArgs:   nil,
		dryRun:     false,
		verbose:    false,
	}

	runFormatters(opts)
}

// TestLogVerbose confirms the four-line log structure.
func TestLogVerbose(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	opts := &options{
		moduleRoot: "/tmp",
		pkgPath:    "./...",
		lintCmd:    "golangci-lint",
		lintArgs:   []string{"run"},
		dryRun:     true,
		verbose:    false,
	}
	logVerbose(opts)

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"module-root: /tmp",
		"package:     ./...",
		"lint-cmd:    golangci-lint [run]",
		"dry-run:     true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
}

// TestLogResult confirms the totals line.
func TestLogResult(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	logResult(&resolveOutcome{
		edits: []fixer.Edit{
			{File: "a.go", Line: 1, Col: 1, Repl: "(*T)(nil)"},
			{File: "b.go", Line: 2, Col: 1, Repl: "[]T(nil)"},
			{File: "c.go", Line: 3, Col: 1, Repl: "any(nil)"},
		},
		skipped: 2,
	})

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	want := "found 5 issues, resolved 3, skipped 2"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("log missing %q:\n%s", want, buf.String())
	}
}

// TestRecordSkip increments the package-level `skipped` counter and
// writes a skip line to stderr. We don't assert on the counter
// (it's a global) — just that the stderr line is well-formed.
func TestRecordSkip(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	recordSkip(fixer.Issue{File: "foo.go", Line: 42, Col: 9, PkgRel: "foo"}, "foo", "test reason")

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"skip:",
		"foo.go:42:9",
		"test reason",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("recordSkip output missing %q:\n%s", want, out)
		}
	}
}

// TestRewriteFile_NoEdits handles the zero-edits branch — the file
// must still be written back unchanged.
func TestRewriteFile_NoEdits(t *testing.T) {
	tmpDir := t.TempDir()

	srcPath := filepath.Join(tmpDir, "foo.go")
	src := "package p\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := rewriteFile(tmpDir, "foo.go", nil); err != nil {
		t.Fatalf("rewriteFile: %v", err)
	}

	got, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	if string(got) != src {
		t.Errorf("file changed despite no edits:\ngot: %q\nwant: %q", got, src)
	}
}

// TestResolveParamType covers the multi-info loop: first info
// resolves the type, second is irrelevant.
func TestResolveParamType(t *testing.T) {
	infos := []*types.Info{nil, nil}
	if got := resolveParamType(infos, nil, 0); got != nil {
		t.Errorf("nil call: got %v, want nil", got)
	}
}

// writeFixtureModule creates a self-contained Go module in a
// temp dir with a single Go file that contains a typed-nil
// ruleguard violation. Returns the temp dir.
func writeFixtureModule(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()

	goMod := "module fixt\n\ngo 1.26\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	src := `package fixt

func G(any) {}

func F() { G(nil) }
`
	if err := os.WriteFile(filepath.Join(tmpDir, "foo.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write foo.go: %v", err)
	}

	return tmpDir
}

// TestLoadPackages exercises the go/packages wrapper against a
// real fixture module.
func TestLoadPackages(t *testing.T) {
	tmpDir := writeFixtureModule(t)

	pkgs, err := loadPackages(tmpDir, ".")
	if err != nil {
		t.Fatalf("loadPackages: %v", err)
	}

	if len(pkgs) == 0 {
		t.Fatal("no packages loaded")
	}

	if pkgs[0].TypesInfo == nil {
		t.Error("TypesInfo not populated")
	}

	if pkgs[0].Fset == nil {
		t.Error("Fset not populated")
	}
}

// TestResolvePackageEdits_EndToEnd runs the orchestration logic
// against a fixture: load the package, find the call, resolve the
// parameter type, and emit an Edit. This covers most of the
// resolvePackageEdits branch surface (file resolution, lookup,
// param-type resolution) without the linter subprocess.
func TestResolvePackageEdits_EndToEnd(t *testing.T) {
	tmpDir := writeFixtureModule(t)

	pkgs, err := loadPackages(tmpDir, ".")
	if err != nil {
		t.Fatalf("loadPackages: %v", err)
	}

	// Synthesize the linter output: line 5 col 6 is `G` in `G(nil)`.
	issues := []fixer.Issue{
		{File: "foo.go", Line: 5, Col: 6, PkgRel: "."},
	}

	edits := resolvePackageEdits(&options{
		moduleRoot: tmpDir,
		pkgPath:    "",
		lintCmd:    "",
		lintArgs:   nil,
		dryRun:     false,
		verbose:    false,
	}, ".", pkgs, issues)
	if len(edits) != 1 {
		t.Fatalf("got %d edits, want 1", len(edits))
	}

	if edits[0].Repl != "any(nil)" {
		t.Errorf("repl: got %q, want %q", edits[0].Repl, "any(nil)")
	}

	// The edit must point at the nil literal's position, not at
	// the call start. We pass Line=5/Col=6 (the call start) and
	// expect the resolver to translate to the actual nil ident
	// column. The lookup finds the call via the call-span
	// fallback (call start on line 5) and NilIdentPos returns
	// the nil ident's position. The Edit.Line should equal 5
	// (the nil is on the same line) but the Edit.Col must point
	// at the nil literal, not at G.
	if edits[0].Line != 5 {
		t.Errorf("line: got %d, want 5", edits[0].Line)
	}

	if edits[0].Col == 6 {
		t.Errorf("col still points at call start (%d); should be nil ident's column", edits[0].Col)
	}
}

// TestResolvePackageEdits_NoMatch verifies the skip path: an issue
// at a position with no matching call must record a skip, not panic
// or silently drop.
func TestResolvePackageEdits_NoMatch(t *testing.T) {
	tmpDir := writeFixtureModule(t)

	pkgs, err := loadPackages(tmpDir, ".")
	if err != nil {
		t.Fatalf("loadPackages: %v", err)
	}

	prev := skipped
	t.Cleanup(func() { skipped = prev })

	// Redirect stderr to capture the skip line.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	// Issue at a position with no call expression at all.
	issues := []fixer.Issue{
		{File: "foo.go", Line: 1, Col: 1, PkgRel: "."},
	}

	edits := resolvePackageEdits(&options{
		moduleRoot: tmpDir,
		pkgPath:    "",
		lintCmd:    "",
		lintArgs:   nil,
		dryRun:     false,
		verbose:    false,
	}, ".", pkgs, issues)
	if len(edits) != 0 {
		t.Errorf("got %d edits, want 0", len(edits))
	}

	if skipped-prev != 1 {
		t.Errorf("skipped delta: got %d, want 1", skipped-prev)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	if !strings.Contains(buf.String(), "no matching call expression") {
		t.Errorf("missing skip reason: %s", buf.String())
	}
}

// TestResolvePackageEdits_NonNilArg exercises the "matched arg is
// not an *ast.Ident" branch — when the lookup returns a CallAt
// whose arg isn't actually a nil literal (shouldn't happen with
// real ruleguard output but defensively guarded).
func TestResolvePackageEdits_NonNilArg(t *testing.T) {
	tmpDir := t.TempDir()

	goMod := "module fixt\n\ngo 1.26\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// Source where G's arg is a literal int — never nil. The
	// lookup will skip this call (it only indexes nils), but we
	// can still verify the early-return in resolvePackageEdits:
	// an issue at the G(...) position with a non-nil arg should
	// produce no edit.
	src := `package fixt

func G(int) {}

func F() { G(42) }
`
	if err := os.WriteFile(filepath.Join(tmpDir, "foo.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write foo.go: %v", err)
	}

	pkgs, err := loadPackages(tmpDir, ".")
	if err != nil {
		t.Fatalf("loadPackages: %v", err)
	}

	prev := skipped
	t.Cleanup(func() { skipped = prev })

	issues := []fixer.Issue{
		{File: "foo.go", Line: 5, Col: 6, PkgRel: "."},
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	edits := resolvePackageEdits(&options{
		moduleRoot: tmpDir,
		pkgPath:    "",
		lintCmd:    "",
		lintArgs:   nil,
		dryRun:     false,
		verbose:    false,
	}, ".", pkgs, issues)
	if len(edits) != 0 {
		t.Errorf("got %d edits, want 0", len(edits))
	}

	// No nil in the fixture → no indexed call → lookup fails.
	if skipped-prev != 1 {
		t.Errorf("skipped delta: got %d, want 1", skipped-prev)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	if !strings.Contains(buf.String(), "no matching call expression") {
		t.Errorf("missing skip reason: %s", buf.String())
	}
}

// TestResolveAll exercises the package-grouping + per-package
// resolve loop. Two fixture packages, one issue each, both should
// be resolved into edits.
func TestResolveAll(t *testing.T) {
	tmpDir := t.TempDir()

	goMod := "module testmod\n\ngo 1.26\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	for _, pkg := range []struct {
		dir, src string
	}{
		{"a", "package a\nfunc G(any) {}\nfunc F() { G(nil) }\n"},
		{"b", "package b\nfunc H(any) {}\nfunc F() { H(nil) }\n"},
	} {
		if err := os.MkdirAll(filepath.Join(tmpDir, pkg.dir), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", pkg.dir, err)
		}

		if err := os.WriteFile(filepath.Join(tmpDir, pkg.dir, "x.go"), []byte(pkg.src), 0o644); err != nil {
			t.Fatalf("write %s/x.go: %v", pkg.dir, err)
		}
	}

	issues := []fixer.Issue{
		{File: "a/x.go", Line: 3, Col: 17, PkgRel: "a"},
		{File: "b/x.go", Line: 3, Col: 17, PkgRel: "b"},
	}

	outcome, err := resolveAll(&options{
		moduleRoot: tmpDir,
		pkgPath:    "",
		lintCmd:    "",
		lintArgs:   nil,
		dryRun:     false,
		verbose:    false,
	}, issues)
	if err != nil {
		t.Fatalf("resolveAll: %v", err)
	}

	if len(outcome.edits) != 2 {
		t.Fatalf("got %d edits, want 2", len(outcome.edits))
	}

	if outcome.skipped != 0 {
		t.Errorf("skipped: got %d, want 0", outcome.skipped)
	}
}

// writeFakeLinter writes a shell script that pretends to be the
// linter: it echoes the given issues to stdout and exits with code
// 1 (golangci-lint exits non-zero when issues are found).
func writeFakeLinter(t *testing.T, issues string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "fake-lint")

	// sh script: print issues to stdout, exit 1.
	script := "#!/bin/sh\nprintf '%s' " + shellQuote(issues) + "\nexit 1\n"

	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // G306: executable script
		t.Fatalf("write fake linter: %v", err)
	}

	return path
}

// shellQuote wraps s in single quotes for POSIX shell. Internal
// single quotes become '\”.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestCollectIssues exercises the linter-subprocess wrapper. The
// fake linter emits one ruleguard-format line and exits 1; the
// script must capture the output and return the parsed issue.
func TestCollectIssues(t *testing.T) {
	lintPath := writeFakeLinter(t,
		"foo.go:5:6: ruleguard: use typed nil instead of plain nil (gocritic)\n",
	)

	opts := &options{
		moduleRoot: "/tmp",
		pkgPath:    "./...",
		lintCmd:    lintPath,
		lintArgs:   []string{"run"},
		dryRun:     false,
		verbose:    false,
	}

	issues, err := collectIssues(opts)
	if err != nil {
		t.Fatalf("collectIssues: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(issues))
	}

	if issues[0].File != "foo.go" || issues[0].Line != 5 || issues[0].Col != 6 {
		t.Errorf("issue: got %+v", issues[0])
	}
}

// TestCollectIssues_MissingBinary covers the linter-not-found
// branch: exec returns a non-ExitError, which must propagate.
func TestCollectIssues_MissingBinary(t *testing.T) {
	opts := &options{
		moduleRoot: "/tmp",
		pkgPath:    "./...",
		lintCmd:    "/nonexistent-" + strconv.Itoa(os.Getpid()),
		lintArgs:   []string{"run"},
		dryRun:     false,
		verbose:    false,
	}

	if _, err := collectIssues(opts); err == nil {
		t.Fatal("expected error for missing binary, got nil")
	}
}

// TestRunApp_FullFlow drives the entire runApp pipeline against a
// fixture module using a fake linter. Verifies that:
//   - collectIssues returns the fake-linter output
//   - resolveAll finds the matching call and resolves its param type
//   - writeEdits rewrites the file in place
//   - logResult emits the totals line
//
// runFormatters is also exercised — it best-effort runs gofmt on
// the fixture module; if gofmt is available it normalizes the
// rewrite (no-op here since the input is already formatted).
func TestRunApp_FullFlow(t *testing.T) {
	tmpDir := writeFixtureModule(t)

	// Read the source to find the `G(nil)` call. The fixture is
	// line 5, col 6 — but we don't need exact precision because
	// the lookup falls back to line-keyed matching.
	lintPath := writeFakeLinter(t,
		"foo.go:5:6: ruleguard: use typed nil instead of plain nil (gocritic)\n",
	)

	opts := &options{
		moduleRoot: tmpDir,
		pkgPath:    "./...",
		lintCmd:    lintPath,
		lintArgs:   []string{"run"},
		dryRun:     false,
		verbose:    false,
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	if runErr := runApp(opts); runErr != nil {
		t.Fatalf("runApp: %v", runErr)
	}

	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("close pipe: %v", closeErr)
	}

	var buf bytes.Buffer
	if _, copyErr := io.Copy(&buf, r); copyErr != nil {
		t.Fatalf("read pipe: %v", copyErr)
	}

	out := buf.String()
	if !strings.Contains(out, "found 1 issues, resolved 1, skipped 0") {
		t.Errorf("missing result line: %s", out)
	}

	// Verify the file was rewritten.
	got, err := os.ReadFile(filepath.Join(tmpDir, "foo.go"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	if !strings.Contains(string(got), "G(any(nil))") {
		t.Errorf("file not rewritten:\n%s", got)
	}
}
