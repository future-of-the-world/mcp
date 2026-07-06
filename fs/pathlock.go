// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package fs

import "sync"

// pathMu is the package-level registry of per-path locks. Keys are
// the canonical (symlink-resolved, namespace-normalised) paths the
// handlers operate on; values are *sync.RWMutex instances shared by
// every handler that touches the same path. A path that has never
// been touched before is lazy-initialized on first access via
// LoadOrStore. Entries are retained for the life of the process:
// the map stays small relative to the filesystem, so cleanup would
// add complexity without meaningful memory savings.
var pathMu sync.Map // resolved path -> *sync.RWMutex

// pathLockFor returns the *sync.RWMutex stored at resolved,
// creating one if the path has never been touched before. The
// invariant "every value ever stored is *sync.RWMutex" is held
// by every call site in this file, so the comma-ok form's ok
// return can be discarded without a panic risk.
func pathLockFor(resolved string) *sync.RWMutex {
	stored, _ := pathMu.LoadOrStore(resolved, &sync.RWMutex{})
	//nolint:revive // invariant: every value stored in pathMu is *sync.RWMutex
	lock, _ := stored.(*sync.RWMutex)

	return lock
}

// withPathLock acquires the write side of the per-path RWMutex
// keyed by resolved, runs fn, and releases the lock. The lock is
// keyed by the canonical (symlink-resolved, namespace-normalised)
// path so two different inputs that resolve to the same file share
// the same mutex instance.
//
// fn runs while the lock is held. The error fn returns is
// forwarded to the caller verbatim. Handlers that need to lock
// two paths at once (e.g. move_file's source + destination) must
// go through withPathTwoLocks so the order is deterministic and
// deadlock-free; nesting two withPathLock calls inline can
// deadlock when two parallel callers swap the order in which they
// pass the paths.
func withPathLock(resolved string, runFn func() error) error {
	pathLock := pathLockFor(resolved)

	pathLock.Lock()
	defer pathLock.Unlock()

	return runFn()
}

// withPathRLock acquires the read side of the per-path RWMutex
// keyed by resolved. Multiple readers can hold the RLock
// concurrently; a writer (withPathLock) blocks until every reader
// releases.
//
// Reads dominate real workloads — the LLM calls read_file far
// more often than the mutating tools — so the RWMutex split keeps
// the hot path concurrent while still serializing against any
// in-flight write.
func withPathRLock(resolved string, runFn func() error) error {
	pathLock := pathLockFor(resolved)

	pathLock.RLock()
	defer pathLock.RUnlock()

	return runFn()
}

// withPathTwoLocks acquires the write side of the per-path lock
// for both first and second in lexicographic order, then runs fn
// while holding both. The deterministic order prevents deadlock
// when two parallel callers swap which argument is "first" — the
// canonical example is move_file with A→B racing B→A: without
// the ordering, one goroutine holds A waiting for B while the
// other holds B waiting for A.
//
// When first == second the lock is acquired only once; sync.RWMutex
// is not re-entrant, so the obvious nested call would self-deadlock
// on the same path.
func withPathTwoLocks(first, second string, runFn func() error) error {
	switch {
	case first == second:
		return withPathLock(first, runFn)

	case first < second:
		firstLock := pathLockFor(first)
		secondLock := pathLockFor(second)

		firstLock.Lock()
		defer firstLock.Unlock()

		secondLock.Lock()
		defer secondLock.Unlock()

		return runFn()

	default: // second < first
		firstLock := pathLockFor(first)
		secondLock := pathLockFor(second)

		secondLock.Lock()
		defer secondLock.Unlock()

		firstLock.Lock()
		defer firstLock.Unlock()

		return runFn()
	}
}
