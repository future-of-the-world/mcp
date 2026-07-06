// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package fs

// --- Entry kinds (returned by list_directory / directory_tree) ---

// entryKindFile marks an entry as a regular file in the JSON output.
const entryKindFile = "file"

// entryKindDirectory marks an entry as a directory in the JSON output.
const entryKindDirectory = "directory"

// --- Match entry kinds (returned by grep) ---

// matchKindMatch marks a line that satisfied the regex.
const matchKindMatch = "match"

// matchKindBefore marks a context line preceding a match.
const matchKindBefore = "before"

// matchKindAfter marks a context line following a match.
const matchKindAfter = "after"

// --- File permissions ---

// dirCreateMode is the permission set used when creating new
// directories on disk. 0o750 — owner rwx, group rx, other none — is
// the typical "private to the operator" choice that matches how
// `os.MkdirAll` is invoked elsewhere in the codebase.
const dirCreateMode = 0o750

// fileCreateMode is the permission set used when writing new files
// to disk. 0o600 — owner rw, group none, other none — keeps the
// contents private to the operator.
const fileCreateMode = 0o600

// --- grep defaults ---

// defaultMaxLineBytes caps the length of an individual line returned
// in grep output. Lines exceeding this are truncated in the per-entry
// `content` field with `truncated: true`. The default (4 KiB) keeps a
// single minified-JS / packed-binary line from blowing up the response
// envelope even when max_results is generous.
const defaultMaxLineBytes = 1 << 12 // 4096

// defaultBinarySniffBytes is how many bytes from the head of a file
// the binary detector scans for a NUL byte. 8 KiB matches ripgrep's
// heuristic and is enough to catch every common binary header
// (PE/ELF/Mach-O, PNG, gzip, etc.) without slowing the search.
const defaultBinarySniffBytes = 1 << 13 // 8192

// defaultMaxResults caps the number of match entries returned by
// grep. When the cap is hit, top-level `truncated` is set to true and
// the caller can re-run with a larger value or a narrower pattern.
const defaultMaxResults = 100

// defaultContextLines is the default number of context lines emitted
// before and after each match. 0 means "matches only" — the cheapest
// output and the right default for code-search callers that already
// know the surrounding context.
const defaultContextLines = 0

// minLiteralPrefixLen is the minimum length of a literal run
// extractLiteralPrefix will return. Shorter prefixes do not pay back
// the prefilter overhead (the regex match is already cheap on lines
// that short).
const minLiteralPrefixLen = 3

// defaultBufioScannerBytes is the maximum line length the bufio.Scanner
// will buffer when reading files for grep. Matches are emitted at this
// size before truncation kicks in, so files with longer lines are
// still scanned (we do not skip them) — we just clip the output.
const defaultBufioScannerBytes = 1 << 16 // 64 KiB

// defaultWorkerMultiplier scales the worker-pool size relative to
// runtime.NumCPU(). One worker per logical CPU is the sweet spot for
// I/O-bound file search; the multiplier exists so tests / future tuning
// can override it without touching the walker code.
const defaultWorkerMultiplier = 1

// --- Default-ignore directory basenames ---

// defaultIgnoreDirs is the hardcoded list of directory basenames
// skipped by the grep walker when respect_default_ignores is true.
// These are the universally-slow directories that almost never contain
// source code the LLM is searching for. The list is intentionally
// short — operators who need finer control use .gitignore.
var defaultIgnoreDirs = map[string]struct{}{
	".git":         {},
	".hg":          {},
	".svn":         {},
	"node_modules": {},
	"vendor":       {},
	"target":       {},
	"dist":         {},
	"build":        {},
	"out":          {},
	".next":        {},
	".turbo":       {},
	".cache":       {},
}
