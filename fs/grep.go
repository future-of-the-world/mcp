// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package fs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	gitignore "github.com/sabhiram/go-gitignore"
)

// asciiContinuationOrLead is the first byte of a UTF-8 multi-byte
// sequence. Bytes at or above this value are not part of the
// ASCII regex meta / literal alphabet, so extractLiteralPrefix
// treats them as run separators.
const asciiContinuationOrLead = 0x80

// nulByte is the byte the binary detector scans for in a file's
// head buffer. isBinaryFile / isBinaryFileReader return true on
// the first NUL.
const nulByte = 0x00

// --- Types ---

type grepOptions struct {
	caseSensitive     bool
	literal           bool
	includePattern    string
	maxDepth          int
	maxResults        int
	contextLines      int
	maxLineBytes      int
	binarySniffBytes  int
	useGitignore      bool
	respectDefIgnores bool
}

type grepResult struct {
	matches            []grepHit
	truncated          bool
	totalFilesSearched int
	totalMatches       int
}

type grepHit struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	Kind      string `json:"type"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

type lineSpan struct {
	Line   int
	Column int
}

type fileResult struct {
	path    string
	hits    []grepHit
	scanned bool
}

type fileSearchResult struct {
	hits    []grepHit
	scanned bool
}

// --- Literal prefix extraction ---

func isRegexMeta(r rune) bool {
	switch r {
	case '.', '+', '*', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$', '\\':
		return true
	}

	return false
}

func extractLiteralPrefix(pattern string) []byte {
	var best, cur []byte

	promote := func() {
		if len(cur) > len(best) {
			best = append([]byte(nil), cur...)
		}

		cur = cur[:0]
	}

	for i := 0; i < len(pattern); i++ {
		chByte := pattern[i]
		if chByte >= asciiContinuationOrLead {
			promote()

			continue
		}

		if isRegexMeta(rune(chByte)) {
			promote()

			continue
		}

		cur = append(cur, chByte)
	}

	promote()

	if len(best) < minLiteralPrefixLen {
		return nil
	}

	return best
}

// --- Binary detection ---

// isBinaryFileReader inspects the first sniffBytes of r for a NUL
// byte and returns true on the first hit. Used by searchFile's
// single-pass reader so the file is only opened once.
func isBinaryFileReader(r io.Reader, sniffBytes int) (bool, error) {
	buf := make([]byte, sniffBytes)

	bytesRead, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, fmt.Errorf("sniff read: %w", err)
	}

	return bytes.IndexByte(buf[:bytesRead], nulByte) >= 0, nil
}

// isBinaryFile opens the file at path and inspects its head for a
// NUL byte. Retained for direct-call tests; the production search
// path uses isBinaryFileReader on an already-opened handle to avoid
// a second open per file.
//
//nolint:unparam // tests always pass defaultBinarySniffBytes; the param stays for symmetry.
func isBinaryFile(path string, sniffBytes int) (bool, error) {
	file, err := os.Open(path) // #nosec G304 -- path is guarded by resolveAndGuard.
	if err != nil {
		return false, fmt.Errorf("open for sniff %q: %w", path, err)
	}
	defer file.Close()

	return isBinaryFileReader(file, sniffBytes)
}

// --- Strip line endings ---

func stripLineEnding(lineBytes []byte) []byte {
	if n := len(lineBytes); n > 0 && lineBytes[n-1] == '\n' {
		lineBytes = lineBytes[:n-1]
	}

	if n := len(lineBytes); n > 0 && lineBytes[n-1] == '\r' {
		lineBytes = lineBytes[:n-1]
	}

	return lineBytes
}

// --- Matcher (regex + literal prefilter) ---

// matchInputs carries the per-line data tryMatchLine needs. The
// caseSens + literal bool pair is declared adjacent so the two
// bytes share a single 8-byte alignment slot; otherwise each bool
// rounds up to a full word and the struct crosses gocritic's
// 80-byte hugeParam threshold.
type matchInputs struct {
	content    []byte
	prefix     []byte
	caseSens   bool
	literal    bool
	re         *regexp.Regexp
	scratchBuf *[]byte
}

// prepareNeedle returns the byte slice to scan against. When the
// search is case-insensitive and a prefix exists, the content is
// lowercased into the caller's scratch buffer so the byte-equality
// compare against the lowercased prefix succeeds. Otherwise the
// original content is returned verbatim — the case-sensitive branch
// never needs to fold, and the prefix-less regex path doesn't
// benefit from a lowered scratch.
func prepareNeedle(input matchInputs) []byte {
	if input.caseSens || input.prefix == nil {
		return input.content
	}

	if cap(*input.scratchBuf) < len(input.content) {
		*input.scratchBuf = make([]byte, len(input.content))
	} else {
		*input.scratchBuf = (*input.scratchBuf)[:len(input.content)]
	}

	copy(*input.scratchBuf, input.content)

	return bytes.ToLower(*input.scratchBuf)
}

// tryMatchLine runs the literal prefilter (if any) and then the
// regex matcher on the line content. It returns the column of the
// match (1-indexed) and whether the line matched. The literal-mode
// sub-agent owns the match-logic side of this function; do not
// refactor beyond what's strictly required for the single-pass
// merge without coordinating with that branch.
func tryMatchLine(input matchInputs) (lineSpan, bool) {
	needle := prepareNeedle(input)

	if input.literal {
		idx := bytes.Index(needle, input.prefix)
		if idx < 0 {
			return lineSpan{}, false
		}

		return lineSpan{Line: 0, Column: idx + 1}, true
	}

	if input.prefix != nil && !bytes.Contains(needle, input.prefix) {
		return lineSpan{}, false
	}

	loc := input.re.FindIndex(input.content)
	if loc == nil {
		return lineSpan{}, false
	}

	return lineSpan{Line: 0, Column: loc[0] + 1}, true
}

// classifyLine is retained for the per-line classification tests
// that exercise the before/match/after decision table directly.
func classifyLine(input *classifyInputs) (grepHit, bool) {
	if input.matchIdx < len(input.spans) && input.lineNum == input.spans[input.matchIdx].Line {
		return grepHit{
			Path:      input.path,
			Line:      input.lineNum,
			Column:    input.spans[input.matchIdx].Column,
			Kind:      matchKindMatch,
			Content:   string(input.content),
			Truncated: input.truncated,
		}, true
	}

	if input.matchIdx > 0 {
		prev := input.spans[input.matchIdx-1]
		if input.lineNum > prev.Line && input.lineNum-prev.Line <= input.contextLines {
			return grepHit{
				Path:      input.path,
				Line:      input.lineNum,
				Column:    0,
				Kind:      matchKindAfter,
				Content:   string(input.content),
				Truncated: input.truncated,
			}, true
		}
	}

	if input.matchIdx < len(input.spans) {
		next := input.spans[input.matchIdx]
		if input.lineNum < next.Line && next.Line-input.lineNum <= input.contextLines {
			return grepHit{
				Path:      input.path,
				Line:      input.lineNum,
				Column:    0,
				Kind:      matchKindBefore,
				Content:   string(input.content),
				Truncated: input.truncated,
			}, true
		}
	}

	return grepHit{}, false
}

type classifyInputs struct {
	path         string
	lineNum      int
	content      []byte
	truncated    bool
	spans        []lineSpan
	matchIdx     int
	contextLines int
}

// --- Single-pass searchFile ---

// openSearchFile is the package-level file opener used by
// searchFile. Tests override it to count os.Open calls without
// resorting to global monkey-patching. Access is guarded by
// openSearchMu so tests can install/restore the hook
// concurrently.
var (
	openSearchFile = os.Open
	openSearchMu   sync.RWMutex
)

// withOpenSearchFile returns a copy of openSearchFile under a
// read lock so a caller can invoke it without holding the lock.
func withOpenSearchFile() func(string) (*os.File, error) {
	openSearchMu.RLock()
	defer openSearchMu.RUnlock()

	return openSearchFile
}

// ringEntry stores a recent line in the bounded context ring. The
// content slice is owned by the entry (a copy of the bufio.Reader's
// internal slice, which becomes invalid on the next ReadBytes).
type ringEntry struct {
	lineNum   int
	content   []byte
	truncated bool
}

// singlePassState carries the per-file scan state. It owns the
// bounded ring buffer of recent non-match lines (potential before
// context for upcoming matches), the dedup cursor (lastEmittedLine),
// and the after-context anchor (lastMatchLine).
type singlePassState struct {
	path            string
	re              *regexp.Regexp
	prefix          []byte
	caseSensitive   bool
	opts            grepOptions
	hits            []grepHit
	lineNum         int
	lastEmittedLine int
	lastMatchLine   int
	buffer          []ringEntry
	bufferLen       int
	bufferStart     int
	scratchBuf      []byte
}

// contextRingMultiplier is the buffer-size factor per context
// line. The ring holds contextLines of before-context plus a
// current match line and contextLines of post-context room,
// giving 2*contextLines+1 entries — the upper bound any single
// match needs to look back.
const contextRingMultiplier = 2

// newSinglePassState allocates the bounded ring buffer. The buffer
// holds the last 2*contextLines+1 lines (per the issue spec) so
// that emitBeforeContext can look back far enough to find any
// un-emitted before-context for an upcoming match.
func newSinglePassState(
	path string,
	regex *regexp.Regexp,
	prefix []byte,
	opts grepOptions,
) *singlePassState {
	bufSize := max(contextRingMultiplier*opts.contextLines+1, 1)

	return &singlePassState{
		path:            path,
		re:              regex,
		prefix:          prefix,
		caseSensitive:   opts.caseSensitive,
		opts:            opts,
		hits:            []grepHit(nil),
		lineNum:         0,
		lastEmittedLine: 0,
		lastMatchLine:   0,
		buffer:          make([]ringEntry, bufSize),
		bufferLen:       0,
		bufferStart:     0,
		scratchBuf:      []byte(nil),
	}
}

func (s *singlePassState) processLine(lineBytes []byte) {
	fullContent := stripLineEnding(lineBytes)

	// Match against the full (un-truncated) content. A match past
	// maxLineBytes still counts; only the emitted content is
	// clipped. The match column is therefore relative to the full
	// line.
	span, ok := tryMatchLine(matchInputs{
		content:    fullContent,
		prefix:     s.prefix,
		caseSens:   s.caseSensitive,
		re:         s.re,
		scratchBuf: &s.scratchBuf,
		literal:    s.opts.literal,
	})

	truncated := len(fullContent) > s.opts.maxLineBytes

	emitContent := fullContent

	if truncated {
		emitContent = fullContent[:s.opts.maxLineBytes]
	}

	if ok {
		span.Line = s.lineNum
		s.emitBeforeContext(span.Line)
		s.emitMatch(span.Line, span.Column, emitContent, truncated)

		s.lastMatchLine = span.Line

		return
	}

	s.emitAfterContext(emitContent, truncated)

	if s.lineNum > s.lastEmittedLine {
		// Copy: the bufio.Reader's slice will be invalidated on the
		// next ReadBytes, so the ring buffer must own its content.
		// Store the emit-side (truncated) content so before-context
		// hits respect maxLineBytes too.
		owned := append([]byte(nil), emitContent...)
		s.pushRing(ringEntry{
			lineNum:   s.lineNum,
			content:   owned,
			truncated: truncated,
		})
	}
}

// emitBeforeContext walks the ring buffer from oldest to newest,
// emitting any entries that are within contextLines of matchLine
// and have not already been emitted (e.g., as after-context for a
// previous match). Iterating in lineNum order is required so each
// emission strictly advances lastEmittedLine; a reverse scan would
// emit the closest candidate first and then skip the rest via the
// dedup check.
func (s *singlePassState) emitBeforeContext(matchLine int) {
	if s.opts.contextLines == 0 || s.bufferLen == 0 {
		return
	}

	bufCap := len(s.buffer)

	for i := 0; i < s.bufferLen; i++ {
		idx := (s.bufferStart + i) % bufCap
		entry := &s.buffer[idx]

		if entry.lineNum >= matchLine {
			continue
		}

		if matchLine-entry.lineNum > s.opts.contextLines {
			continue
		}

		if entry.lineNum <= s.lastEmittedLine {
			// Already emitted as after-context for a previous match.
			continue
		}

		s.hits = append(s.hits, grepHit{
			Path:      s.path,
			Line:      entry.lineNum,
			Column:    0,
			Kind:      matchKindBefore,
			Content:   string(entry.content),
			Truncated: entry.truncated,
		})
		s.lastEmittedLine = entry.lineNum
	}
}

func (s *singlePassState) emitMatch(line, column int, content []byte, truncated bool) {
	s.hits = append(s.hits, grepHit{
		Path:      s.path,
		Line:      line,
		Column:    column,
		Kind:      matchKindMatch,
		Content:   string(content),
		Truncated: truncated,
	})
	s.lastEmittedLine = line
}

func (s *singlePassState) emitAfterContext(content []byte, truncated bool) {
	if s.opts.contextLines == 0 || s.lastMatchLine == 0 {
		return
	}

	if s.lineNum-s.lastMatchLine > s.opts.contextLines {
		return
	}

	if s.lineNum <= s.lastEmittedLine {
		return
	}

	s.hits = append(s.hits, grepHit{
		Path:      s.path,
		Line:      s.lineNum,
		Column:    0,
		Kind:      matchKindAfter,
		Content:   string(content),
		Truncated: truncated,
	})
	s.lastEmittedLine = s.lineNum
}

// pushRing adds a new entry to the bounded ring, evicting the
// oldest when the ring is full.
func (s *singlePassState) pushRing(entry ringEntry) {
	bufCap := len(s.buffer)

	idx := (s.bufferStart + s.bufferLen) % bufCap
	if s.bufferLen < bufCap {
		s.bufferLen++
	} else {
		// Evict oldest. The evicted slice is now unreachable and
		// will be GC'd.
		s.bufferStart = (s.bufferStart + 1) % bufCap
	}

	s.buffer[idx] = entry
}

// searchFile scans a single file for matches of regex (with an
// optional literal-byte prefilter from extractLiteralPrefix),
// emitting each match plus its surrounding context in a single
// pass over the file. The file is opened exactly once; context
// emission uses a bounded ring buffer of 2*contextLines+1 lines so
// the implementation runs in O(file size) regardless of how many
// matches are present.
//
//nolint:revive // argument-limit: shape mirrors the worker's per-call grepOptions.
func searchFile(
	ctx context.Context,
	path string,
	regex *regexp.Regexp,
	prefix []byte,
	opts grepOptions,
) (fileSearchResult, error) {
	opener := withOpenSearchFile()

	file, err := opener(path) // #nosec G304 -- path is guarded by resolveAndGuard.
	if err != nil {
		return fileSearchResult{}, err
	}

	defer file.Close()

	return scanFile(ctx, file, path, regex, prefix, opts)
}

// scanFile is the per-file loop. It assumes file has just been
// opened; it sniffs the head, seeks back, and walks every line
// through the single-pass state. A non-nil error is returned only
// for ctx-cancellation mid-scan — binary and seek errors are
// reported as a non-fatal skip result so the caller can keep
// counting files.
//
//nolint:revive // argument-limit mirrors searchFile's signature; the helper is the single caller.
func scanFile(
	ctx context.Context,
	file *os.File,
	path string,
	regex *regexp.Regexp,
	prefix []byte,
	opts grepOptions,
) (fileSearchResult, error) {
	bin, binErr := isBinaryFileReader(file, opts.binarySniffBytes)
	if binErr != nil || bin {
		//nolint:nilerr // binary / unreadable head is a non-fatal skip
		return fileSearchResult{hits: []grepHit(nil), scanned: false}, nil
	}

	_, seekErr := file.Seek(0, io.SeekStart)
	if seekErr != nil {
		//nolint:nilerr // seek failure becomes a silent empty result
		return fileSearchResult{hits: []grepHit(nil), scanned: true}, nil
	}

	reader := bufio.NewReaderSize(file, defaultBufioScannerBytes)
	state := newSinglePassState(path, regex, prefix, opts)

	for {
		cause := context.Cause(ctx)
		if cause != nil {
			//nolint:wrapcheck // context error is returned as-is
			return fileSearchResult{}, cause
		}

		lineBytes, readErr := reader.ReadBytes('\n')
		if len(lineBytes) > 0 {
			state.lineNum++
			state.processLine(lineBytes)
		}

		if readErr != nil {
			break
		}
	}

	//nolint:nilerr // loop exited cleanly via readErr; cause was checked
	return fileSearchResult{hits: state.hits, scanned: true}, nil
}

// --- compileRegexp ---

//nolint:revive // flag-parameter: bool is the only natural shape here
func compileRegexp(pattern string, caseSensitive bool) (*regexp.Regexp, error) {
	if caseSensitive {
		return regexp.Compile(pattern) //nolint:wrapcheck // bubbled to grepCore with %w
	}

	return regexp.Compile("(?i)" + pattern) //nolint:wrapcheck // bubbled to grepCore with %w
}

// --- Lazy gitignore stack ---

// dotGitignore is the basename of the per-directory ignore file
// the walker reads on descent.
const dotGitignore = ".gitignore"

// dirMatcher is one entry in the walker-maintained stack. The dir
// is the directory whose .gitignore produced matcher (matcher is
// nil when the directory has no .gitignore or the file is empty).
type dirMatcher struct {
	dir     string
	matcher *gitignore.GitIgnore
}

// grepWalkerState carries per-grep walker state: depth/include
// predicates plus the lazily-built gitignore stack and its
// directory-keyed cache.
type grepWalkerState struct {
	maxDepth          int
	includePattern    string
	respectDefIgnores bool
	root              string
	gitIgnoreStack    []dirMatcher
	gitIgnoreCache    map[string]*gitignore.GitIgnore
}

// loadGitignoreForDir compiles and caches the matcher for dir.
// The cache avoids recompilation when a directory is revisited
// (e.g., via a symlink). Nil values are cached too so a missing
// or empty .gitignore is a single read.
func (s *grepWalkerState) loadGitignoreForDir(dir string) *gitignore.GitIgnore {
	if cached, ok := s.gitIgnoreCache[dir]; ok {
		return cached
	}

	giPath := filepath.Join(dir, dotGitignore)

	// #nosec G304 -- dir comes from filepath.WalkDir under resolveAndGuard.
	data, readErr := os.ReadFile(giPath)

	var matcher *gitignore.GitIgnore

	if readErr == nil {
		var lines []string

		for line := range strings.SplitSeq(string(data), "\n") {
			lines = append(lines, line)
		}

		if len(lines) > 0 {
			matcher = gitignore.CompileIgnoreLines(lines...)
		}
	}

	s.gitIgnoreCache[dir] = matcher

	return matcher
}

// matchesGitignore checks the path against the lazily-built stack
// of ancestor-directory gitignore matchers. Only ancestor
// directories contribute matchers, so a .gitignore in /foo/ never
// affects paths under /bar/ (the directory-scoping bug that the
// old global matcher exhibited). The path is computed relative to
// each stack entry's directory because go-gitignore's pattern
// anchoring (e.g., "/build") is rooted at the .gitignore's
// directory.
func (s *grepWalkerState) matchesGitignore(path string) bool {
	for _, entry := range s.gitIgnoreStack {
		if entry.matcher == nil {
			continue
		}

		rel, err := filepath.Rel(entry.dir, path)
		if err != nil {
			continue
		}

		if entry.matcher.MatchesPath(rel) {
			return true
		}
	}

	return false
}

// --- Walker predicates ---

func (s *grepWalkerState) shouldSkipDir(name string) bool {
	return shouldIgnoreEntry(name, s.respectDefIgnores)
}

func (s *grepWalkerState) shouldSkipFile(name string) bool {
	return shouldIgnoreEntry(name, s.respectDefIgnores)
}

// default-ignore list controls this; turning it into a struct
// would just be ceremony.
//
//nolint:revive // flag-parameter: only the single source of the
func shouldIgnoreEntry(name string, respectDefaultIgnores bool) bool {
	if !respectDefaultIgnores {
		return false
	}

	if _, ok := defaultIgnoreDirs[name]; ok {
		return true
	}

	if name != parentDirRef && name != currentDirRef && strings.HasPrefix(name, ".") {
		return true
	}

	return false
}

func (s *grepWalkerState) shouldSkipByInclude(name string) bool {
	if s.includePattern == "" {
		return false
	}

	matched, err := filepath.Match(s.includePattern, name)
	if err != nil {
		return true
	}

	return !matched
}

// --- Directory walk ---

func walkCandidateFiles(
	ctx context.Context,
	root string,
	state *grepWalkerState,
	fileCh chan<- string,
) error {
	//nolint:wrapcheck // walker errors are best-effort
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			// Per-entry error (perm denied, broken symlink, etc.);
			// skip and keep walking rather than aborting the grep.
			return nil //nolint:nilerr // best-effort walk
		}

		cause := context.Cause(ctx)
		if cause != nil {
			return cause //nolint:wrapcheck // context error is returned as-is
		}

		return visitWalkEntry(path, entry, root, state, fileCh, ctx)
	})
}

// entryDepth returns the depth of path relative to root: 0 for
// root itself, 1 for its direct children, etc.
func entryDepth(path, root string) int {
	if path == root {
		return 0
	}

	rel, err := filepath.Rel(root, path)
	if err != nil || rel == currentDirRef {
		return 0
	}

	return strings.Count(rel, string(filepath.Separator)) + 1
}

// without obscuring the WalkDir callback contract.
//
//nolint:revive // argument-limit: fileCh + ctx cannot be bundled
func visitWalkEntry(
	path string,
	entry os.DirEntry,
	root string,
	state *grepWalkerState,
	fileCh chan<- string,
	ctx context.Context,
) error {
	depth := entryDepth(path, root)

	// Trim the stack to current depth. We push one entry per
	// descended directory, so len(stack) tracks the walker depth:
	// a file at depth D has D ancestors on the stack (len = D), a
	// directory at depth D has D+1 entries (len = D+1) once we
	// push below.
	if len(state.gitIgnoreStack) > depth {
		state.gitIgnoreStack = state.gitIgnoreStack[:depth]
	}

	if entry.IsDir() {
		matcher := state.loadGitignoreForDir(path)

		state.gitIgnoreStack = append(state.gitIgnoreStack, dirMatcher{
			dir:     path,
			matcher: matcher,
		})

		return visitDir(path, root, state)
	}

	if state.shouldSkipFile(entry.Name()) {
		return nil
	}

	return enqueueCandidate(ctx, path, state, fileCh)
}

func visitDir(path, root string, state *grepWalkerState) error {
	if path != root && state.shouldSkipDir(filepath.Base(path)) {
		return filepath.SkipDir
	}

	if exceedsMaxDepth(root, path, state.maxDepth) {
		return filepath.SkipDir
	}

	return nil
}

// enqueueCandidate pushes path onto fileCh after passing the
// per-file predicates (gitignore stack, include pattern). The
// context cancel path is the only error return.
func enqueueCandidate(
	ctx context.Context,
	path string,
	state *grepWalkerState,
	fileCh chan<- string,
) error {
	if state.matchesGitignore(path) {
		return nil
	}

	if state.shouldSkipByInclude(filepath.Base(path)) {
		return nil
	}

	select {
	case fileCh <- path:
	case <-ctx.Done():
		return context.Cause(ctx) //nolint:wrapcheck // context error returned as-is
	}

	return nil
}

func exceedsMaxDepth(root, path string, maxDepth int) bool {
	if path == root {
		return false
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}

	depth := strings.Count(rel, string(filepath.Separator)) + 1

	return depth > maxDepth
}

// --- Worker pool ---

// params cannot be bundled without hiding the goroutine contract.
//
//nolint:revive // argument-limit: the per-worker channels + search
func runWorker(
	ctx context.Context,
	fileCh <-chan string,
	resultCh chan<- fileResult,
	errCh chan<- error,
	regex *regexp.Regexp,
	prefix []byte,
	opts grepOptions,
) {
	for path := range fileCh {
		cause := context.Cause(ctx)
		if cause != nil {
			return
		}

		result, err := searchFile(ctx, path, regex, prefix, opts)
		if err != nil {
			sendNonBlocking(errCh, err)

			continue
		}

		hits := result.hits
		if hits == nil {
			hits = []grepHit{}
		}

		select {
		case resultCh <- fileResult{path: path, hits: hits, scanned: result.scanned}:
		case <-ctx.Done():
			return
		}
	}
}

func sendNonBlocking(chErr chan<- error, err error) {
	select {
	case chErr <- err:
	default:
	}
}

// --- Top-level orchestration ---

func grepCore(
	ctx context.Context,
	root, pattern string,
	opts grepOptions,
) (grepResult, error) {
	var (
		regex  *regexp.Regexp
		prefix []byte
	)

	if opts.literal {
		// Literal mode: skip regex compilation entirely. The whole
		// pattern is the search needle; regex metacharacters have
		// no special meaning.
		needle := []byte(pattern)
		if !opts.caseSensitive {
			needle = bytes.ToLower(needle)
		}

		prefix = needle
	} else {
		// Pre-declare err inside the else branch so the assignment
		// below uses '=' instead of ':='. The latter would shadow
		// the outer `regex` and leave it nil for the worker pool.
		var err error

		regex, err = compileRegexp(pattern, opts.caseSensitive)
		if err != nil {
			return grepResult{}, fmt.Errorf("compile regex: %w", err)
		}

		prefix = extractLiteralPrefix(pattern)
		if !opts.caseSensitive && prefix != nil {
			prefix = bytes.ToLower(prefix)
		}
	}

	state := &grepWalkerState{
		maxDepth:          opts.maxDepth,
		includePattern:    opts.includePattern,
		respectDefIgnores: opts.respectDefIgnores,
		root:              root,
		gitIgnoreStack:    []dirMatcher(nil),
		gitIgnoreCache:    make(map[string]*gitignore.GitIgnore),
	}

	workers := max(runtime.NumCPU()*defaultWorkerMultiplier, 1)

	fileCh := make(chan string, workers*2)
	resultCh := make(chan fileResult, workers*2)
	errCh := make(chan error, workers*2)

	aggregator := newResultAggregator(resultCh)

	var wg sync.WaitGroup

	for range workers {
		wg.Go(func() {
			runWorker(ctx, fileCh, resultCh, errCh, regex, prefix, opts)
		})
	}

	walkDone := make(chan error, 1)

	go func() {
		walkDone <- walkCandidateFiles(ctx, root, state, fileCh)
		close(fileCh)
	}()

	walkErr := <-walkDone

	wg.Wait()
	close(resultCh)
	<-aggregator.done

	return finalizeGrep(aggregator.hits, aggregator.filesSearched, opts), walkErr
}

// resultAggregator drains resultCh concurrently with workers so
// the buffered channel (size workers*2) never fills at production
// input volume. Without this concurrent drain, workers block on
// resultCh send after the buffer is saturated, hold their fileCh
// slot, and the walker eventually blocks pushing to fileCh — the
// search hangs forever.
//
// The aggregator is the sole writer to hits and filesSearched;
// the main goroutine reads them after Done is closed, which
// provides happens-before via the channel-close synchronization
// so -race stays clean.
type resultAggregator struct {
	hits          []grepHit
	filesSearched int
	done          chan struct{}
}

func newResultAggregator(resultCh <-chan fileResult) *resultAggregator {
	agg := &resultAggregator{
		hits:          []grepHit(nil),
		filesSearched: 0,
		done:          make(chan struct{}),
	}

	go func() {
		defer close(agg.done)

		for res := range resultCh {
			if res.scanned {
				agg.filesSearched++
			}

			agg.hits = append(agg.hits, res.hits...)
		}
	}()

	return agg
}

func finalizeGrep(hits []grepHit, filesSearched int, opts grepOptions) grepResult {
	sort.SliceStable(hits, func(i, j int) bool { return lessHit(hits[i], hits[j]) })

	truncated := false

	if opts.maxResults > 0 && len(hits) > opts.maxResults {
		hits = hits[:opts.maxResults]
		truncated = true
	}

	totalMatches := 0

	for _, h := range hits {
		if h.Kind == matchKindMatch {
			totalMatches++
		}
	}

	return grepResult{
		matches:            hits,
		truncated:          truncated,
		totalFilesSearched: filesSearched,
		totalMatches:       totalMatches,
	}
}

func lessHit(left, right grepHit) bool {
	if left.Path != right.Path {
		return left.Path < right.Path
	}

	return left.Line < right.Line
}
