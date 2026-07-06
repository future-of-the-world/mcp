// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// parentDirRef is the literal string ".."; factored out because
// filepath.Clean's output and string-prefix checks both rely on it
// and the magic string would otherwise trigger revive's add-constant
// rule when used in two places.
const parentDirRef = ".."

// currentDirRef is the literal string "."; same rationale as
// parentDirRef above.
const currentDirRef = "."

// --- Sentinel errors ---

// errAllowedPathsEmpty is returned when the operator configured no
// allowed_paths. The fs source refuses to start with an empty
// allowlist — the principle is "deny by default".
var errAllowedPathsEmpty = errors.New("fs: connect.allowed_paths is required and must be non-empty")

// errPathEmpty is returned when the caller passed an empty path. An
// empty string is never a valid target; surfacing this as a clear
// error prevents the LLM from accidentally deleting or reading the
// process's current working directory.
var errPathEmpty = errors.New("fs: path is empty")

// errPathOutsideAllowed is returned when the requested path resolves
// outside every configured allowed root. The error message names the
// resolved path so the operator can debug a misconfigured allowlist
// without re-running with -v.
var errPathOutsideAllowed = errors.New("fs: path is outside the configured allowed paths")

// errPathContainsParent is returned when the requested path contains
// `..` components that escape the root after Clean+Abs. This is the
// canonical "path traversal" attack pattern; refusing early keeps the
// resolver's contract honest.
var errPathContainsParent = errors.New("fs: path contains parent-directory traversal")

// errSymlinkEscape is returned when a symlink in the chain resolves
// outside the configured allowed root. The default
// (follow_symlinks=false) never follows symlinks at all; the
// permissive mode (follow_symlinks=true) follows them but rejects
// any post-follow real path that exits the allowlist.
var errSymlinkEscape = errors.New("fs: symlink escapes the configured allowed paths")

// resolveAndGuard returns the absolute real path of `requested` if
// and only if it lives under one of the configured `allowedPaths`.
// The returned path is the symlink-resolved real path so downstream
// tools can stat / open it without further normalization.
//
// Behavior:
//   - Empty allowedPaths → errAllowedPathsEmpty
//   - Empty requested → errPathEmpty
//   - `..` components in requested → errPathContainsParent
//   - Path resolves outside every allowed root → errPathOutsideAllowed
//   - When followSymlinks is false: any user-created symlink in
//     the chain is rejected as errSymlinkEscape. System-level
//     mount-point symlinks (e.g. /var → /private/var on macOS) are
//     resolved transparently because they live above the
//     user-controlled namespace.
//   - When followSymlinks is true: the post-follow real path must
//     still be inside the root, otherwise errSymlinkEscape.
//
// The resolver normalizes both root and absPath into the same
// canonical namespace via nameSpaceNormal. On macOS this rewrites
// /var/folders/... → /private/var/folders/...; on Linux the call
// is a no-op. For followSymlinks=false the normalization stops at
// the parent directory so a symlink planted as the final
// component of absPath is preserved for the chain-detection walk.
//
//nolint:revive // flag-parameter: single entry point; policy is read from cfg.FollowSymlinks.
func resolveAndGuard(allowedPaths []string, followSymlinks bool, requested string) (string, error) {
	if len(allowedPaths) == 0 {
		return "", errAllowedPathsEmpty
	}

	absPath, err := preflightPathChecks(requested)
	if err != nil {
		return "", err
	}

	// Normalize absPath into the same canonical namespace as
	// realRoot so the prefix check matches despite macOS-style
	// /var → /private/var mount-point symlinks. The original
	// absPath is preserved for the follow-symlinks branch.
	preCanonical := nameSpaceNormal(absPath)

	for _, root := range allowedPaths {
		realRoot, rootErr := canonicalRoot(root)
		if rootErr != nil {
			// A misconfigured root (e.g. points at a deleted
			// directory) is the operator's problem, not the
			// caller's. Surface it so the dispatcher logs the
			// failed source.
			return "", fmt.Errorf("resolve allowed root %q: %w", root, rootErr)
		}

		if !pathHasPrefix(preCanonical, realRoot) {
			continue
		}

		if followSymlinks {
			return resolveFollowingSymlinks(realRoot, absPath)
		}

		return resolveRejectingSymlinks(realRoot, preCanonical)
	}

	return "", errPathOutsideAllowed
}

// preflightPathChecks performs the input checks that require no I/O:
// rejects an empty input, rejects `..` traversal, and converts the
// cleaned relative form into an absolute path. Returning the abs
// path keeps the caller free of repeated filepath.Abs round-trips.
func preflightPathChecks(requested string) (string, error) {
	if requested == "" {
		return "", errPathEmpty
	}

	cleaned := filepath.Clean(requested)

	if isParentTraversal(cleaned) {
		return "", errPathContainsParent
	}

	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}

	return absPath, nil
}

// resolveFollowingSymlinks is the followSymlinks=true branch. It
// fully collapses every symlink in the chain via canonicalize and
// verifies the post-follow real path still lives under realRoot.
// A symlink that escapes the root triggers errSymlinkEscape.
func resolveFollowingSymlinks(realRoot, absPath string) (string, error) {
	followed, err := canonicalize(absPath)
	if err != nil {
		return "", err
	}

	if !pathHasPrefix(followed, realRoot) {
		return "", errSymlinkEscape
	}

	return followed, nil
}

// resolveRejectingSymlinks is the followSymlinks=false branch. The
// caller passes the namespace-normalized form of absPath
// (preCanonical); the walker then descends from realRoot through
// the relative path. The first user-created symlink triggers
// errSymlinkEscape.
func resolveRejectingSymlinks(realRoot, preCanonical string) (string, error) {
	rel, relErr := filepath.Rel(realRoot, preCanonical)
	if relErr != nil {
		return "", fmt.Errorf("compute relative path: %w", relErr)
	}

	return walkRejectingSymlinks(realRoot, rel)
}

// canonicalRoot returns the absolute, symlink-resolved form of an
// allowed-path root. A non-existent root is taken as-is on the
// operator's behalf — they may have created the directory between
// config-load and first tool call.
func canonicalRoot(root string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return absRoot, nil
		}

		return "", fmt.Errorf("eval symlinks: %w", err)
	}

	return resolved, nil
}

// nameSpaceNormal returns the absolute form of path with the
// longest existing prefix EvalSymlinks-resolved. Unlike
// canonicalize, this never follows a symlink that lives in the
// final component of path itself — the chain-detection walk in
// resolveAndGuard is responsible for catching that.
//
// On macOS this collapses /var/folders/... → /private/var/folders/...
// transparently (the system-level symlink lives above the
// user-controlled namespace). On Linux it is a no-op for paths
// that already live under the real root.
func nameSpaceNormal(path string) string {
	current := path
	tail := ""

	for {
		parent := filepath.Dir(current)
		if parent == current {
			// Reached the filesystem root without finding
			// any existing ancestor — extremely unusual but
			// possible. Fall back to Clean.
			return filepath.Clean(path)
		}

		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			// parent exists; the original path is
			// `parent + basename(current) + tail`. Reattach
			// the components we walked past so the returned
			// path matches the LLM's expectation (sans the
			// namespace-only prefix rewrite).
			return filepath.Join(resolved, filepath.Base(current), tail)
		}

		tail = filepath.Join(filepath.Base(current), tail)

		current = parent
	}
}

// canonicalize returns the absolute, symlink-resolved form of path.
// If the final component does not exist, the longest existing
// ancestor is resolved via EvalSymlinks and the missing tail is
// appended verbatim. This is the chokepoint used by
// followSymlinks=true to fully collapse every symlink in the chain.
func canonicalize(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}

	current := path
	tail := ""

	for {
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve existing ancestor for %q", path)
		}

		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			if tail == "" {
				return resolved, nil
			}

			return filepath.Join(resolved, tail), nil
		}

		tail = filepath.Join(filepath.Base(current), tail)

		current = parent
	}
}

// walkRejectingSymlinks descends from realRoot down `rel`, stat-ing
// each segment. The first symlink triggers errSymlinkEscape. A
// non-existent component short-circuits to success because the
// handler will surface ENOENT when it tries to open the file
// (write_file target, etc.). The full target path is reconstructed
// from realRoot + rel so the returned path is always the
// caller's intended destination, not the partial walk.
//
// rel is the relative path between realRoot and the canonical
// resolved form of absPath; passing it pre-computed avoids a
// second filepath.Rel round-trip inside the loop.
func walkRejectingSymlinks(realRoot, rel string) (string, error) {
	if rel == currentDirRef || rel == "" {
		return realRoot, nil
	}

	if isParentTraversal(rel) {
		return "", errPathOutsideAllowed
	}

	current := realRoot

	for _, segment := range splitPath(rel) {
		next, segErr := advanceRejectingSymlink(realRoot, rel, current, segment)
		if segErr != nil {
			return "", segErr
		}

		current = next
	}

	return current, nil
}

// advanceRejectingSymlink performs one segment of the symlink walk.
// It returns the new current path on success, a sentinel "missing
// tail" indicator (currentDirRef, "") for ENOENT (the caller is
// expected to reconstruct the full target), or a wrapped error.
func advanceRejectingSymlink(realRoot, rel, current, segment string) (string, error) {
	next := filepath.Join(current, segment)

	info, err := os.Lstat(next)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Reconstruct the full target path so the
			// caller gets back exactly what they asked
			// for (minus the namespace-only prefix
			// rewrite).
			return filepath.Join(realRoot, rel), nil
		}

		return "", fmt.Errorf("stat %q: %w", next, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return "", errSymlinkEscape
	}

	return next, nil
}

// pathHasPrefix reports whether child is the same as parent or sits
// strictly inside it. The check is separator-aware so /home/user is
// not a prefix of /home/user2.
func pathHasPrefix(child, parent string) bool {
	if child == parent {
		return true
	}

	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}

	if rel == currentDirRef {
		return true
	}

	return rel != parentDirRef && !strings.HasPrefix(rel, parentDirRef+string(filepath.Separator))
}

// isParentTraversal reports whether p is exactly ".." or starts
// with the "../" separator-prefixed form. It centralizes the
// equality-and-prefix check so revive's add-constant rule does not
// flag the parentDirRef literal at multiple call sites.
func isParentTraversal(p string) bool {
	return p == parentDirRef || strings.HasPrefix(p, parentDirRef+string(filepath.Separator))
}

// splitPath returns the slash-separated segments of rel. The result
// is empty when rel is empty or ".". Used by walkRejectingSymlinks
// to walk a path one component at a time.
func splitPath(rel string) []string {
	if rel == "" || rel == currentDirRef {
		return nil
	}

	return strings.Split(rel, string(filepath.Separator))
}
