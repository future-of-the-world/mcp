// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Command fix-untyped-nil automates the typed-nil fix for golangci-lint
// gocritic / ruleguard "use typed nil instead of plain nil" issues.
//
// Usage:
//
//	fix-untyped-nil [--module-root DIR] [--package PKG] [--lint-cmd CMD]
//	                [--lint-args "ARGS"] [--dry-run] [--verbose]
//
// Defaults:
//
//	--module-root   current directory
//	--package       ./...
//	--lint-cmd      golangci-lint
//	--lint-args     run --timeout=5m
//
// The tool runs the linter, parses its output for ruleguard issues,
// then for each issue resolves the parameter type via go/types and
// rewrites the offending nil literal in place. Output is then run
// through gofmt and golangci-lint fmt to clean up.
//
// Exit codes:
//
//	0  no issues or all issues fixed (skipped issues are reported on stderr)
//	1  unexpected error (linter missing, type-check failed, etc.)

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	"go.amidman.dev/mcp/scripts/fix-untyped-nil/fixer"
)

const exitError = 1

// main is the CLI entry point — kept separate from runApp so the
// project linter's `deep-exit` revive rule (which forbids flag.Parse
// outside main / init) is satisfied.
func main() {
	registerFlags()
	flag.Parse()

	opts, err := parseFlags()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fix-untyped-nil:", err)
		os.Exit(exitError)
	}

	if err := runApp(opts); err != nil {
		fmt.Fprintln(os.Stderr, "fix-untyped-nil:", err)
		os.Exit(exitError)
	}
}

// runApp orchestrates the fix workflow for the given parsed
// options. Taking options as a parameter (instead of reading them
// from package globals) lets tests drive the orchestrator
// without depending on flag state.
func runApp(opts *options) error {
	if opts == nil {
		return errors.New("nil options")
	}

	if opts.verbose {
		logVerbose(opts)
	}

	issues, err := collectIssues(opts)
	if err != nil {
		return fmt.Errorf("lint: %w", err)
	}

	if len(issues) == 0 {
		return nil
	}

	if opts.verbose {
		fmt.Fprintf(os.Stderr, "found %d ruleguard issues\n", len(issues))
	}

	outcome, err := resolveAll(opts, issues)
	if err != nil {
		return fmt.Errorf("resolve: %w", err)
	}

	logResult(outcome)

	if len(outcome.edits) == 0 && outcome.skipped == 0 {
		return fmt.Errorf("%d issues could not be resolved", len(issues))
	}

	if opts.dryRun {
		printDryRun(outcome.edits)
		return nil
	}

	if err := writeEdits(opts, outcome.edits); err != nil {
		return fmt.Errorf("apply: %w", err)
	}

	runFormatters(opts)
	return nil
}

// --- types ---

// options holds the parsed CLI flags.
type options struct {
	moduleRoot string
	pkgPath    string
	lintCmd    string
	lintArgs   []string
	dryRun     bool
	verbose    bool
}

// resolveOutcome bundles the script's outputs.
type resolveOutcome struct {
	edits   []fixer.Edit
	skipped int
}

// optsStorage holds the flag-bound option fields. A package-level
// variable gives flag.StringVar an address that outlives flag.Parse.
var optsStorage options

// lintArgsRaw is the flag.StringVar target for --lint-args.
var lintArgsRaw string

// --- flag parsing ---

func registerFlags() {
	flag.StringVar(&optsStorage.moduleRoot, "module-root", ".", "module root (where go.mod lives)")
	flag.StringVar(
		&optsStorage.pkgPath,
		"package",
		"./...",
		"package selector passed to the linter",
	)
	flag.StringVar(&optsStorage.lintCmd, "lint-cmd", "golangci-lint", "linter executable")
	flag.StringVar(&lintArgsRaw, "lint-args", "run --timeout=5m", "linter args (space-separated)")
	flag.BoolVar(
		&optsStorage.dryRun,
		"dry-run",
		false,
		"print replacements without rewriting files",
	)
	flag.BoolVar(&optsStorage.verbose, "verbose", false, "enable verbose logging on stderr")
}

func parseFlags() (*options, error) {
	opts := &optsStorage

	opts.lintArgs = strings.Fields(lintArgsRaw)
	return opts, nil
}

// --- linter invocation ---

// collectIssues runs the linter and parses its output for ruleguard
// diagnostics.
func collectIssues(opts *options) ([]fixer.Issue, error) {
	args := append([]string{}, opts.lintArgs...)
	args = append(args, opts.pkgPath)

	cmd := exec.CommandContext(context.Background(), opts.lintCmd, args...)
	cmd.Dir = opts.moduleRoot
	cmd.Stderr = os.Stderr

	out, runErr := cmd.Output()

	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		return nil, runErr
	}

	return fixer.ParseLinterOutput(out)
}

// --- resolution ---

// resolveAll groups issues by package, type-checks each package via
// golang.org/x/tools/go/packages, and resolves the typed-nil cast
// for each issue.
func resolveAll(opts *options, issues []fixer.Issue) (*resolveOutcome, error) {
	byPkg := fixer.GroupIssuesByPackage(issues)

	outcome := &resolveOutcome{}

	for pkgRel, pkgIssues := range byPkg {
		pkgs, err := loadPackages(opts.moduleRoot, pkgRel)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", pkgRel, err)
		}

		edits := resolvePackageEdits(opts, pkgRel, pkgs, pkgIssues)
		outcome.edits = append(outcome.edits, edits...)
	}

	return outcome, nil
}

// loadPackages uses go/packages to fully load the named package.
func loadPackages(moduleRoot, pkgRel string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedSyntax,
		Dir:   filepath.Join(moduleRoot, pkgRel),
		Tests: true,
	}

	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, err
	}

	return pkgs, nil
}

// resolvePackageEdits walks every *ast.CallExpr in the package's
// files (across all variants — production, test) and looks up each
// reported issue.
func resolvePackageEdits(
	opts *options,
	pkgRel string,
	pkgs []*packages.Package,
	issues []fixer.Issue,
) []fixer.Edit {
	if len(pkgs) == 0 {
		for range issues {
			recordSkip(fixer.Issue{}, pkgRel, "no package loaded")
		}

		return nil
	}

	var (
		fset     *token.FileSet
		allFiles []*ast.File
		infos    []*types.Info
	)

	for _, pkg := range pkgs {
		if fset == nil && pkg.Fset != nil {
			fset = pkg.Fset
		}

		if pkg.TypesInfo != nil {
			infos = append(infos, pkg.TypesInfo)
		}

		allFiles = append(allFiles, pkg.Syntax...)
	}

	if fset == nil || len(infos) == 0 || len(allFiles) == 0 {
		for range issues {
			recordSkip(fixer.Issue{}, pkgRel, "no type info or syntax")
		}

		return nil
	}

	lookup := fixer.BuildCallLookup(fset, allFiles, opts.moduleRoot)

	if opts.verbose {
		lookup.DumpKeys(os.Stderr)
	}

	var out []fixer.Edit

	for _, is := range issues {
		entry := lookup.LookupCall(is.File, is.Line, is.Col)
		if entry.Call == nil {
			recordSkip(is, pkgRel, "no matching call expression at this position")
			continue
		}

		// Try each Info until we find the parameter type. Production
		// packages can't see symbols that only the _test variant
		// imports, so the first match wins.
		paramType := resolveParamType(infos, entry.Call, entry.ArgIdx)
		if paramType == nil {
			recordSkip(is, pkgRel, "cannot resolve param type")
			continue
		}

		// The linter reports the call's start position, not the nil
		// ident's. For multi-line calls (or any call where the nil
		// isn't on the same line as the function name) using
		// is.Line/is.Col would point ApplyEdit at the function name
		// and the rewrite would silently no-op. Resolve the nil
		// ident's own position from the AST instead.
		nilPos := entry.NilIdentPos(fset)
		if nilPos.Line == 0 {
			recordSkip(is, pkgRel, "matched arg is not an *ast.Ident")
			continue
		}

		out = append(out, fixer.Edit{
			File: is.File,
			Line: nilPos.Line,
			Col:  nilPos.Column,
			Repl: fixer.TypedNilString(paramType),
		})
	}

	return out
}

// resolveParamType tries each Info until one returns a non-nil
// parameter type. Nil Infos are skipped — they typically come from
// the _test variant of a package that didn't load successfully.
func resolveParamType(infos []*types.Info, call *ast.CallExpr, argIdx int) types.Type {
	for _, info := range infos {
		if info == nil {
			continue
		}

		if pt := fixer.ParamTypeAt(info, call, argIdx); pt != nil {
			return pt
		}
	}

	return nil
}

// skipped is a counter of issues the script couldn't auto-resolve.
var skipped int

func recordSkip(is fixer.Issue, pkgRel, reason string) {
	skipped++
	fmt.Fprintf(os.Stderr, "skip: %s:%d:%d %s\n", is.File, is.Line, is.Col, reason)
}

// --- output ---

// printDryRun emits the planned edits to stdout in a stable order.
func printDryRun(edits []fixer.Edit) {
	byFile := fixer.GroupByFile(edits)
	keys := fixer.SortedKeys(byFile)

	for _, file := range keys {
		_, _ = fmt.Fprintln(os.Stdout, file)
		for _, item := range byFile[file] {
			_, _ = fmt.Fprintf(os.Stdout, "  %d:%d -> %s\n", item.Line, item.Col, item.Repl)
		}
	}
}

// writeEdits rewrites each file in-place.
func writeEdits(opts *options, edits []fixer.Edit) error {
	byFile := fixer.GroupByFile(edits)

	for file, fileEdits := range byFile {
		if err := rewriteFile(opts.moduleRoot, file, fileEdits); err != nil {
			return err
		}
	}

	return nil
}

// rewriteFile reads the file, applies all edits in descending
// (line, col) order, and writes the result back.
func rewriteFile(moduleRoot, file string, fileEdits []fixer.Edit) error {
	fullPath := filepath.Join(moduleRoot, file)

	data, readErr := os.ReadFile(fullPath)
	if readErr != nil {
		return fmt.Errorf("read %s: %w", fullPath, readErr)
	}

	lines := strings.Split(string(data), "\n")
	less := fixer.DescendingPos(fileEdits)

	for i := 1; i < len(fileEdits); i++ {
		for j := i; j > 0 && less(j, j-1); j-- {
			fileEdits[j], fileEdits[j-1] = fileEdits[j-1], fileEdits[j]
		}
	}

	for _, item := range fileEdits {
		fixer.ApplyEdit(lines, item)
	}

	writeErr := os.WriteFile(fullPath, []byte(strings.Join(lines, "\n")), 0o644)
	if writeErr != nil {
		return fmt.Errorf("write %s: %w", fullPath, writeErr)
	}

	return nil
}

// runFormatters best-effort runs gofmt and golangci-lint fmt.
func runFormatters(opts *options) {
	gofmtPath, err := exec.LookPath("gofmt")
	if err == nil {
		runQuiet(exec.CommandContext(
			context.Background(), gofmtPath, "-w", opts.moduleRoot))
	}

	lintPath, err := exec.LookPath(opts.lintCmd)
	if err == nil {
		// The `fmt` subcommand has its own flag set; reuse only the
		// package selector and drop run-only flags like --timeout.
		args := []string{"fmt", opts.pkgPath}
		runQuiet(exec.CommandContext(context.Background(), lintPath, args...))
	}
}

// runQuiet runs cmd, surfacing non-zero exits on stderr.
func runQuiet(cmd *exec.Cmd) {
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
}

// --- logging ---

func logVerbose(opts *options) {
	fmt.Fprintf(os.Stderr, "module-root: %s\n", opts.moduleRoot)
	fmt.Fprintf(os.Stderr, "package:     %s\n", opts.pkgPath)
	fmt.Fprintf(os.Stderr, "lint-cmd:    %s %v\n", opts.lintCmd, opts.lintArgs)
	fmt.Fprintf(os.Stderr, "dry-run:     %v\n", opts.dryRun)
}

func logResult(outcome *resolveOutcome) {
	fmt.Fprintf(os.Stderr, "found %d issues, resolved %d, skipped %d\n",
		len(outcome.edits)+outcome.skipped, len(outcome.edits), outcome.skipped)
}
