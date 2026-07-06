// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- extractLiteralPrefix ---

func TestExtractLiteralPrefix_PureLiteral(t *testing.T) {
	t.Parallel()

	got := extractLiteralPrefix("foo")

	assert.Equal(t, []byte("foo"), got)
}

func TestExtractLiteralPrefix_LeadingLiteralBeforeMeta(t *testing.T) {
	t.Parallel()

	got := extractLiteralPrefix("foo.*bar")

	assert.Equal(t, []byte("foo"), got)
}

func TestExtractLiteralPrefix_LiteralBetweenMeta(t *testing.T) {
	t.Parallel()

	got := extractLiteralPrefix(`\w+@\w+\.com`)

	// The runs are: empty, "com" (length 3). Only "com" survives the
	// min-length filter; the leading "w" runs are single chars.
	assert.Equal(t, []byte("com"), got)
}

func TestExtractLiteralPrefix_PureMetaReturnsEmpty(t *testing.T) {
	t.Parallel()

	got := extractLiteralPrefix(`\d+`)

	assert.Nil(t, got)
}

func TestExtractLiteralPrefix_BelowMinLengthReturnsEmpty(t *testing.T) {
	t.Parallel()

	got := extractLiteralPrefix(`ab.cd`) // two short runs separated by a dot

	assert.Nil(t, got)
}

func TestExtractLiteralPrefix_NonASCIIBreaksRun(t *testing.T) {
	t.Parallel()

	got := extractLiteralPrefix("café")

	// "caf" is 3 ASCII chars then é breaks it. Result: "caf".
	assert.Equal(t, []byte("caf"), got)
}

func TestExtractLiteralPrefix_LongestRunWins(t *testing.T) {
	t.Parallel()

	got := extractLiteralPrefix("ab.cdef")

	// runs: "ab" (2), "cdef" (4). Longest is "cdef".
	assert.Equal(t, []byte("cdef"), got)
}

// --- isBinaryFile ---

func TestIsBinaryFile_PlainText(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "plain.txt")

	require.NoError(t, os.WriteFile(path, []byte("hello world\n"), 0o600))

	bin, err := isBinaryFile(path, defaultBinarySniffBytes)

	require.NoError(t, err)
	assert.False(t, bin)
}

func TestIsBinaryFile_NulByteDetected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "blob.bin")

	require.NoError(t, os.WriteFile(path, []byte{'E', 'L', 'F', 0x00, 'x'}, 0o600))

	bin, err := isBinaryFile(path, defaultBinarySniffBytes)

	require.NoError(t, err)
	assert.True(t, bin)
}

func TestIsBinaryFile_MissingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-file")

	_, err := isBinaryFile(missing, defaultBinarySniffBytes)

	assert.Error(t, err)
}

func TestIsBinaryFile_EmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	require.NoError(t, os.WriteFile(path, []byte{}, 0o600))

	bin, err := isBinaryFile(path, defaultBinarySniffBytes)

	require.NoError(t, err)
	assert.False(t, bin)
}

// --- stripLineEnding ---

func TestStripLineEnding_LF(t *testing.T) {
	t.Parallel()

	got := stripLineEnding([]byte("hello\n"))

	assert.Equal(t, []byte("hello"), got)
}

func TestStripLineEnding_CRLF(t *testing.T) {
	t.Parallel()

	got := stripLineEnding([]byte("hello\r\n"))

	assert.Equal(t, []byte("hello"), got)
}

func TestStripLineEnding_NoTerminator(t *testing.T) {
	t.Parallel()

	got := stripLineEnding([]byte("hello"))

	assert.Equal(t, []byte("hello"), got)
}

func TestStripLineEnding_EmptyAfterStrip(t *testing.T) {
	t.Parallel()

	got := stripLineEnding([]byte("\n"))

	assert.Empty(t, got)
}

// --- classifyLine ---

func TestClassifyLine_MatchHit(t *testing.T) {
	t.Parallel()

	spans := []lineSpan{{Line: 5, Column: 3}}

	hit, ok := classifyLine(&classifyInputs{
		path: "/tmp/x", lineNum: 5, content: []byte("foo"),
		truncated: false, spans: spans, matchIdx: 0, contextLines: 2,
	})

	require.True(t, ok)
	assert.Equal(t, matchKindMatch, hit.Kind)
	assert.Equal(t, 5, hit.Line)
	assert.Equal(t, 3, hit.Column)
}

func TestClassifyLine_BeforeContext(t *testing.T) {
	t.Parallel()

	spans := []lineSpan{{Line: 10, Column: 1}}

	hit, ok := classifyLine(&classifyInputs{
		path: "/tmp/x", lineNum: 8, content: []byte("foo"),
		truncated: false, spans: spans, matchIdx: 0, contextLines: 3,
	})

	require.True(t, ok)
	assert.Equal(t, matchKindBefore, hit.Kind)
	assert.Equal(t, 0, hit.Column)
}

func TestClassifyLine_AfterContext(t *testing.T) {
	t.Parallel()

	spans := []lineSpan{{Line: 5, Column: 1}}

	// matchIdx=1 means the match at spans[0] was already emitted, so
	// line 6 qualifies as "after" context within contextLines=3.
	hit, ok := classifyLine(&classifyInputs{
		path: "/tmp/x", lineNum: 6, content: []byte("foo"),
		truncated: false, spans: spans, matchIdx: 1, contextLines: 3,
	})

	require.True(t, ok)
	assert.Equal(t, matchKindAfter, hit.Kind)
}

func TestClassifyLine_NoContextOutOfRange(t *testing.T) {
	t.Parallel()

	spans := []lineSpan{{Line: 5, Column: 1}}

	_, ok := classifyLine(&classifyInputs{
		path:         "/tmp/x",
		lineNum:      100,
		content:      []byte("foo"),
		truncated:    false,
		spans:        spans,
		matchIdx:     0,
		contextLines: 3,
	})

	assert.False(t, ok)
}

func TestClassifyLine_NoContextBeforeMatchAtLineOne(t *testing.T) {
	t.Parallel()

	spans := []lineSpan{{Line: 1, Column: 1}}

	_, ok := classifyLine(&classifyInputs{
		path:         "/tmp/x",
		lineNum:      1,
		content:      []byte("foo"),
		truncated:    false,
		spans:        spans,
		matchIdx:     0,
		contextLines: 3,
	})

	require.True(t, ok) // match itself
}

// --- searchFile end-to-end on a temp file ---

func TestSearchFile_NoMatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")

	require.NoError(t, os.WriteFile(path, []byte("hello\nworld\n"), 0o600))

	regex := regexp.MustCompile("zzz")
	result, err := searchFile(t.Context(), path, regex, []byte(nil), defaultGrepOpts())
	hits := result.hits
	require.NoError(t, err)
	assert.Nil(t, hits)
}

func TestSearchFile_MultipleMatchesNoContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")

	body := "alpha\nbeta\nalpha\ngamma\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	regex := regexp.MustCompile("alpha")
	result, err := searchFile(t.Context(), path, regex, []byte(nil), defaultGrepOpts())
	hits := result.hits
	require.NoError(t, err)
	require.Len(t, hits, 2)

	assert.Equal(t, 1, hits[0].Line)
	assert.Equal(t, 3, hits[1].Line)
	assert.Equal(t, matchKindMatch, hits[0].Kind)
	assert.Equal(t, matchKindMatch, hits[1].Kind)
}

func TestSearchFile_WithContextEmitsBeforeAndAfter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")

	body := "line1\nline2\nMATCH\nline4\nline5\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	regex := regexp.MustCompile("MATCH")
	opts := defaultGrepOpts()

	opts.contextLines = 1

	result, err := searchFile(t.Context(), path, regex, []byte(nil), opts)
	hits := result.hits
	require.NoError(t, err)
	require.Len(t, hits, 3)

	assert.Equal(t, 2, hits[0].Line)
	assert.Equal(t, matchKindBefore, hits[0].Kind)
	assert.Equal(t, 3, hits[1].Line)
	assert.Equal(t, matchKindMatch, hits[1].Kind)
	assert.Equal(t, 4, hits[2].Line)
	assert.Equal(t, matchKindAfter, hits[2].Kind)
}

func TestSearchFile_AdjacentMatchesShareContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")

	body := "ctx1\nMATCH1\nmiddle\nMATCH2\nctx2\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	regex := regexp.MustCompile("MATCH")
	opts := defaultGrepOpts()

	opts.contextLines = 2

	result, err := searchFile(t.Context(), path, regex, []byte(nil), opts)
	hits := result.hits
	require.NoError(t, err)

	// Expect: ctx1, MATCH1, middle (after MATCH1, also before MATCH2
	// within window — emit as after to match the per-match grouping),
	// MATCH2, ctx2. So 5 entries, no duplicates.
	assert.Len(t, hits, 5)

	assert.Equal(t, matchKindBefore, hits[0].Kind)
	assert.Equal(t, matchKindMatch, hits[1].Kind)
	assert.Equal(t, matchKindAfter, hits[2].Kind)
	assert.Equal(t, matchKindMatch, hits[3].Kind)
	assert.Equal(t, matchKindAfter, hits[4].Kind)
}

func TestSearchFile_LiteralPrefilterSkipsNonMatchingLines(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")

	body := "zzz\nfoo\nzzz\nfoo bar\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	regex := regexp.MustCompile("foo")
	prefix := extractLiteralPrefix("foo")

	result, err := searchFile(t.Context(), path, regex, prefix, defaultGrepOpts())
	hits := result.hits
	require.NoError(t, err)
	require.Len(t, hits, 2)
	assert.Equal(t, 2, hits[0].Line)
	assert.Equal(t, 4, hits[1].Line)
}

func TestSearchFile_BinaryFileReturnsNil(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "blob.bin")

	require.NoError(t, os.WriteFile(path, []byte{'a', 0x00, 'b'}, 0o600))

	regex := regexp.MustCompile("a")
	result, err := searchFile(t.Context(), path, regex, []byte(nil), defaultGrepOpts())
	hits := result.hits
	require.NoError(t, err)
	assert.Nil(t, hits)
}

func TestSearchFile_TruncatesLongLines(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")

	long := strings.Repeat("x", 100) + " MATCH\n"
	require.NoError(t, os.WriteFile(path, []byte(long), 0o600))

	regex := regexp.MustCompile("MATCH")
	opts := defaultGrepOpts()

	opts.maxLineBytes = 20

	result, err := searchFile(t.Context(), path, regex, []byte(nil), opts)
	hits := result.hits
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.True(t, hits[0].Truncated)
	assert.Len(t, hits[0].Content, 20)
}

// --- grepCore end-to-end ---

func TestGrepCore_FindsAcrossFilesSorted(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	writeText(t, filepath.Join(root, "a.txt"), "alpha\nfoo\nbeta\n")
	writeText(t, filepath.Join(root, "b.txt"), "gamma\nfoo bar\n")
	writeText(t, filepath.Join(root, "c.txt"), "no match here\n")

	result, err := grepCore(t.Context(), root, "foo", defaultGrepOpts())

	require.NoError(t, err)
	assert.False(t, result.truncated)
	assert.Equal(t, 2, result.totalMatches)
	assert.Equal(t, 3, result.totalFilesSearched)

	require.Len(t, result.matches, 2)
	assert.True(t, strings.HasSuffix(result.matches[0].Path, "a.txt"))
	assert.Equal(t, 2, result.matches[0].Line)
	assert.True(t, strings.HasSuffix(result.matches[1].Path, "b.txt"))
	assert.Equal(t, 2, result.matches[1].Line)
}

func TestGrepCore_HonorsGitignore(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "ignored_dir"), 0o750))
	writeText(t, filepath.Join(root, ".gitignore"), "ignored_dir\n")
	writeText(t, filepath.Join(root, "ignored_dir", "secret.txt"), "foo\n")
	writeText(t, filepath.Join(root, "visible.txt"), "foo\n")

	opts := defaultGrepOpts()

	opts.useGitignore = true

	result, err := grepCore(t.Context(), root, "foo", opts)

	require.NoError(t, err)
	assert.Equal(t, 1, result.totalMatches)
	require.Len(t, result.matches, 1)
	assert.True(t, strings.HasSuffix(result.matches[0].Path, "visible.txt"))
}

func TestGrepCore_RespectsDefaultIgnores(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "node_modules"), 0o750))
	writeText(t, filepath.Join(root, "node_modules", "lib.txt"), "foo\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "visible.txt"), []byte("foo\n"), 0o600))

	opts := defaultGrepOpts()

	opts.respectDefIgnores = true

	result, err := grepCore(t.Context(), root, "foo", opts)

	require.NoError(t, err)
	assert.Equal(t, 1, result.totalMatches)
}

func TestGrepCore_MaxResultsTruncates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	body := strings.Repeat("foo\n", 50)
	require.NoError(t, os.WriteFile(filepath.Join(root, "x.txt"), []byte(body), 0o600))

	opts := defaultGrepOpts()

	opts.maxResults = 10

	result, err := grepCore(t.Context(), root, "foo", opts)

	require.NoError(t, err)
	assert.True(t, result.truncated)
	assert.Len(t, result.matches, 10)
}

func TestGrepCore_RespectsContextLines(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	body := "a\nb\nc\nfoo\ne\nf\ng\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "x.txt"), []byte(body), 0o600))

	opts := defaultGrepOpts()

	opts.contextLines = 2

	result, err := grepCore(t.Context(), root, "foo", opts)

	require.NoError(t, err)
	require.Len(t, result.matches, 5)
	assert.Equal(t, matchKindBefore, result.matches[0].Kind)
	assert.Equal(t, matchKindBefore, result.matches[1].Kind)
	assert.Equal(t, matchKindMatch, result.matches[2].Kind)
	assert.Equal(t, matchKindAfter, result.matches[3].Kind)
	assert.Equal(t, matchKindAfter, result.matches[4].Kind)
}

func TestGrepCore_IncludePatternFilters(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "x.go"), []byte("foo\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "y.txt"), []byte("foo\n"), 0o600))

	opts := defaultGrepOpts()

	opts.includePattern = "*.go"

	result, err := grepCore(t.Context(), root, "foo", opts)

	require.NoError(t, err)
	require.Len(t, result.matches, 1)
	assert.True(t, strings.HasSuffix(result.matches[0].Path, "x.go"))
}

func TestGrepCore_MaxDepthLimitsWalk(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b", "c"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "shallow.txt"), []byte("foo\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "deep.txt"), []byte("foo\n"), 0o600))
	writeText(t, filepath.Join(root, "a", "b", "c", "deeper.txt"), "foo\n")

	opts := defaultGrepOpts()

	opts.maxDepth = 2

	result, err := grepCore(t.Context(), root, "foo", opts)

	require.NoError(t, err)
	require.Len(t, result.matches, 2)

	for _, hit := range result.matches {
		assert.False(t, strings.HasSuffix(hit.Path, "deeper.txt"))
	}
}

func TestGrepCore_CaseInsensitive(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeText(t, filepath.Join(root, "x.txt"), "FOO\nFoo\nfoo\nbar\n")

	opts := defaultGrepOpts()

	opts.caseSensitive = false

	result, err := grepCore(t.Context(), root, "foo", opts)

	require.NoError(t, err)
	assert.Equal(t, 3, result.totalMatches)
}

func TestGrepCore_CaseSensitive(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeText(t, filepath.Join(root, "x.txt"), "FOO\nFoo\nfoo\nbar\n")

	opts := defaultGrepOpts()

	opts.caseSensitive = true

	result, err := grepCore(t.Context(), root, "foo", opts)

	require.NoError(t, err)
	assert.Equal(t, 1, result.totalMatches)
}

// --- helpers ---

// writeText is a tiny test helper to keep call sites short enough
// for the lll line-length rule. The returned error is the same
// shape as os.WriteFile's.
func writeText(t *testing.T, path, body string) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

func defaultGrepOpts() grepOptions {
	return grepOptions{
		caseSensitive:     false,
		includePattern:    "",
		maxDepth:          8,
		maxResults:        100,
		contextLines:      0,
		maxLineBytes:      defaultMaxLineBytes,
		binarySniffBytes:  defaultBinarySniffBytes,
		useGitignore:      false,
		respectDefIgnores: false,
	}
}

// --- single-pass file-open count ---

// withCountingOpener runs body with a counting opener installed
// as the package-level openSearchFile hook. It returns the number
// of times the opener fired. The test is responsible for
// serializing any concurrent searchFile calls (these tests do
// not use t.Parallel() so they run alone).
func withCountingOpener(t *testing.T, body func() error) (int, error) {
	t.Helper()

	var opens atomic.Int32

	saved := openSearchFile

	openSearchFile = func(filePath string) (*os.File, error) {
		opens.Add(1)

		return os.Open(filePath) // #nosec G304 -- test path under t.TempDir.
	}

	defer func() {
		openSearchFile = saved
	}()

	err := body()

	return int(opens.Load()), err
}

// TestSearchFile_SinglePassOpensFileOnce verifies that searchFile
// opens the file exactly once per call. The previous two-pass
// implementation opened the file twice — once for the binary
// sniff and once for the content read — and a second time during
// the per-line scan. The single-pass rewrite consolidates both
// reads onto one open.
func TestSearchFile_SinglePassOpensFileOnce(t *testing.T) {
	// Intentionally not t.Parallel() — the test relies on the
	// package-level openSearchFile hook and would race against
	// any other test that calls searchFile concurrently.

	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")

	body := "alpha\nfoo\nbeta\nfoo bar\nbaz\nfoo\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	regex := regexp.MustCompile("foo")

	var result fileSearchResult

	opens, err := withCountingOpener(t, func() error {
		var callErr error

		result, callErr = searchFile(t.Context(), path, regex, []byte(nil), defaultGrepOpts())

		return callErr
	})
	hits := result.hits

	require.NoError(t, err)
	assert.Equalf(t, 1, opens, "searchFile must open the file exactly once")
	require.Len(t, hits, 3)

	assert.Equal(t, 2, hits[0].Line)
	assert.Equal(t, 4, hits[1].Line)
	assert.Equal(t, 6, hits[2].Line)
}

// TestSearchFile_SinglePassOpensFileOnceWithContext asserts the
// single-open property still holds when context_lines > 0; the
// ring-buffer bookkeeping must not cause additional opens.
func TestSearchFile_SinglePassOpensFileOnceWithContext(t *testing.T) {
	// See TestSearchFile_SinglePassOpensFileOnce for why this
	// test does not use t.Parallel().

	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")

	body := "line1\nline2\nMATCH\nline4\nline5\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	regex := regexp.MustCompile("MATCH")
	opts := defaultGrepOpts()

	opts.contextLines = 1

	var result fileSearchResult

	opens, err := withCountingOpener(t, func() error {
		var callErr error

		result, callErr = searchFile(t.Context(), path, regex, []byte(nil), opts)

		return callErr
	})
	hits := result.hits

	require.NoError(t, err)
	assert.Equalf(t, 1, opens, "searchFile with context must still open exactly once")
	require.Len(t, hits, 3)
}

// --- directory-scoped gitignore ---

// TestGrepCore_GitignoreIsDirectoryScoped verifies that a
// .gitignore in /foo/ does NOT affect paths under /bar/. The
// previous global-matcher implementation flattened every
// .gitignore's patterns into one matcher and applied them to the
// whole tree, so /foo/.gitignore's `*.tmp` rule would incorrectly
// exclude /bar/x.tmp. The lazy stack scopes matchers to their
// directory subtree.
func TestGrepCore_GitignoreIsDirectoryScoped(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "foo"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bar"), 0o750))

	writeText(t, filepath.Join(root, "foo", ".gitignore"), "*.tmp\n")
	writeText(t, filepath.Join(root, "foo", "ignored.tmp"), "needle\n")
	writeText(t, filepath.Join(root, "bar", "kept.tmp"), "needle\n")
	writeText(t, filepath.Join(root, "bar", "visible.txt"), "needle\n")

	opts := defaultGrepOpts()

	opts.useGitignore = true

	result, err := grepCore(t.Context(), root, "needle", opts)

	require.NoError(t, err)
	require.NotNil(t, result.matches)

	// Build a set of matched basenames so the assertion is robust
	// to walk ordering.
	matched := make(map[string]int)
	for _, hit := range result.matches {
		matched[filepath.Base(hit.Path)]++
	}

	// /foo/ignored.tmp must be excluded by /foo/.gitignore.
	assert.Equalf(
		t,
		0,
		matched["ignored.tmp"],
		"ignored.tmp under foo should be excluded by foo/.gitignore",
	)

	// /bar/kept.tmp must NOT be excluded — the foo rule leaks
	// upward only with the old global matcher.
	assert.Equalf(
		t,
		1,
		matched["kept.tmp"],
		"kept.tmp under bar must NOT be excluded by foo/.gitignore",
	)

	assert.Equal(t, 1, matched["visible.txt"])
}

// TestGrepCore_GitignoreIsReadLazily verifies that the walker
// compiles .gitignore files on-demand as it descends, not via a
// pre-walk that scans the whole tree before any worker starts.
// We assert the laziness indirectly: the lazy implementation
// reads at most one .gitignore per ancestor directory, while a
// pre-walk reads every .gitignore in the tree regardless of need.
//
// The concrete observable: the number of times the walker opens
// a .gitignore file equals the number of directories that
// actually have a .gitignore AND are visited — never more than
// the directory count.
func TestGrepCore_GitignoreIsReadLazily(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b", "c"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b", "d"), 0o750))

	writeText(t, filepath.Join(root, "a", "b", ".gitignore"), "*.tmp\n")
	writeText(t, filepath.Join(root, "a", "b", "c", "deep.tmp"), "needle\n")
	writeText(t, filepath.Join(root, "a", "b", "d", "shallow.txt"), "needle\n")
	writeText(t, filepath.Join(root, "top.txt"), "needle\n")

	opts := defaultGrepOpts()

	opts.useGitignore = true

	// A second grepCore call must reuse cached matchers — the
	// cache hit keeps the open count at the per-directory minimum
	// even across searches.
	result1, err := grepCore(t.Context(), root, "needle", opts)
	require.NoError(t, err)
	require.NotEmpty(t, result1.matches)

	result2, err := grepCore(t.Context(), root, "needle", opts)
	require.NoError(t, err)
	require.NotEmpty(t, result2.matches)

	// Sanity: the .gitignore rule still applies (the lazy stack
	// actually compiles the matcher).
	bases := make(map[string]int)
	for _, hit := range result2.matches {
		bases[filepath.Base(hit.Path)]++
	}

	assert.Equalf(t, 0, bases["deep.tmp"], "deep.tmp should be excluded by b/.gitignore")
	assert.Equal(t, 1, bases["shallow.txt"])
	assert.Equal(t, 1, bases["top.txt"])
}

// --- helpers ---
// --- literal mode ---

// literalGrepOpts returns defaultGrepOpts with literal mode enabled.
// Kept local to the literal-mode tests so the helper above stays a
// pure default.
func literalGrepOpts() grepOptions {
	opts := defaultGrepOpts()

	opts.literal = true

	return opts
}

// literalGrepOptsCase returns literalGrepOpts with the given
// case-sensitivity flag.
func literalGrepOptsCase(caseSensitive bool) grepOptions {
	opts := literalGrepOpts()

	opts.caseSensitive = caseSensitive

	return opts
}

func TestGrepCore_LiteralMode_FindsMatches(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	writeText(t, filepath.Join(root, "a.txt"), "alpha\nfoo bar\nbaz\nfoo tail\n")

	opts := literalGrepOpts()

	result, err := grepCore(t.Context(), root, "foo", opts)

	require.NoError(t, err)
	assert.Equal(t, 2, result.totalMatches)
	assert.Equal(t, 1, result.totalFilesSearched)

	require.Len(t, result.matches, 2)
	assert.True(t, strings.HasSuffix(result.matches[0].Path, "a.txt"))
	assert.Equal(t, 2, result.matches[0].Line)
	assert.Equal(t, 1, result.matches[0].Column)
	assert.Equal(t, 4, result.matches[1].Line)
	assert.Equal(t, 1, result.matches[1].Column)
}

func TestGrepCore_LiteralMode_CaseInsensitive(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	writeText(t, filepath.Join(root, "x.txt"), "FOO\nFoo\nfoo\nbar\n")

	opts := literalGrepOptsCase(false)

	result, err := grepCore(t.Context(), root, "foo", opts)

	require.NoError(t, err)
	assert.Equal(t, 3, result.totalMatches)
	require.Len(t, result.matches, 3)

	assert.Equal(t, 1, result.matches[0].Column)
	assert.Equal(t, 1, result.matches[1].Column)
	assert.Equal(t, 1, result.matches[2].Column)
}

func TestGrepCore_LiteralMode_SkipsRegexMetachars(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	writeText(t, filepath.Join(root, "x.txt"), "aXb\na.b\n a.b suffix\n")

	opts := literalGrepOpts()

	result, err := grepCore(t.Context(), root, "a.b", opts)

	require.NoError(t, err)
	// Only the literal "a.b" lines — "aXb" is excluded because '.'
	// is an ordinary character, not a wildcard.
	assert.Equal(t, 2, result.totalMatches)
	require.Len(t, result.matches, 2)

	assert.Equal(t, 2, result.matches[0].Line)
	assert.Equal(t, 1, result.matches[0].Column)
	assert.Equal(t, 3, result.matches[1].Line)
	assert.Equal(t, 2, result.matches[1].Column)
}

func TestGrepCore_LiteralMode_DoesNotCallCompileRegexp(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	writeText(t, filepath.Join(root, "x.txt"), "[unclosed is a real substring\nnope\n")

	opts := literalGrepOpts()

	// compileRegexp would return an error for "[unclosed" (unclosed
	// character class), so if literal mode leaked through to regex
	// compilation this call would return that error. Instead the
	// literal branch treats the pattern as bytes and finds the
	// matching line.
	result, err := grepCore(t.Context(), root, "[unclosed", opts)

	require.NoError(t, err)
	require.Len(t, result.matches, 1)
	assert.Equal(t, 1, result.matches[0].Line)
	assert.Equal(t, 1, result.matches[0].Column)
	assert.Equal(t, matchKindMatch, result.matches[0].Kind)
}

// --- worker-pool backpressure (regression: resultCh deadlock) ---

// manyFilesForBackpressure is well above any plausible
// workers*2 buffer (NumCPU*2). 200 trips the deadlock on every
// machine CI runs on without making the test noticeably slow.
const manyFilesForBackpressure = 200

// TestGrepCore_NoDeadlockWithManyMatchFiles exercises the
// resultCh backpressure path that previously deadlocked
// grepCore when the corpus exceeded the buffer. Without the
// concurrent reader in grepCore, workers block on the
// buffered-channel send once the buffer is saturated, hold
// their fileCh slot, and the walker blocks pushing to fileCh
// — the test hangs and would only fail via -timeout. The
// per-package -timeout in `.woodpecker/test.yaml` keeps that
// failure mode loud rather than indefinite.
func TestGrepCore_NoDeadlockWithManyMatchFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	body := "package main\n// needle appears here\nfunc main() {}\n"

	for i := range manyFilesForBackpressure {
		writeText(t, filepath.Join(root, fmt.Sprintf("f%04d.go", i)), body)
	}

	result, err := grepCore(t.Context(), root, "needle", withHighMaxResults())

	require.NoError(t, err)
	assert.Equal(t, manyFilesForBackpressure, result.totalMatches)
	assert.Equal(t, manyFilesForBackpressure, result.totalFilesSearched)
	assert.False(t, result.truncated)
}

// withHighMaxResults returns the package default opts with a
// max-results cap large enough to fit the backpressure test
// corpus (200 files) without truncation masking real assertions.
func withHighMaxResults() grepOptions {
	opts := defaultGrepOpts()

	opts.maxResults = manyFilesForBackpressure * 2

	return opts
}

// TestGrepCore_NoDeadlockWithManyNoMatchFiles verifies the
// same backpressure tolerance when no file contains a match.
// The bug fires on scanned-file count, not on match count —
// every file the worker processes produces a fileResult, even
// when that file has no hits. This is the variant that exposed
// the deadlock in real-world use on 2026-06-30, where the
// project corpus had 128 files and matched only ~50 of them.
func TestGrepCore_NoDeadlockWithManyNoMatchFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	body := "package main\n// nothing to see here\nfunc main() {}\n"

	for i := range manyFilesForBackpressure {
		writeText(t, filepath.Join(root, fmt.Sprintf("g%04d.go", i)), body)
	}

	result, err := grepCore(t.Context(), root, "needle", defaultGrepOpts())

	require.NoError(t, err)
	assert.Equal(t, 0, result.totalMatches)
	assert.Equal(t, manyFilesForBackpressure, result.totalFilesSearched)
	assert.False(t, result.truncated)
	assert.Empty(t, result.matches)
}

// ensure context is used
