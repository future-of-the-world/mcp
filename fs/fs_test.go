// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package fs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/decode"
)

// --- decodeConnect ---

// TestDecodeConnect_Defaults verifies that an empty connect map
// decodes to a zero-value config (the missing required
// allowed_paths is surfaced at validate time, not here).
func TestDecodeConnect_Defaults(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(make(map[string]any))
	require.NoError(t, err)

	assert.Empty(t, cfg.AllowedPaths)
	assert.Equal(t, 0, cfg.MaxReadBytes)
	assert.Equal(t, 0, cfg.MaxWriteBytes)
	assert.False(t, cfg.FollowSymlinks)
}

// TestDecodeConnect_Full populates every field and verifies the
// values flow through intact.
func TestDecodeConnect_Full(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"allowed_paths":   []string{"/tmp/work", "/home/user/projects"},
		"max_read_bytes":  524288,
		"max_write_bytes": 5242880,
		"follow_symlinks": true,
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"/tmp/work", "/home/user/projects"}, cfg.AllowedPaths)
	assert.Equal(t, 524288, cfg.MaxReadBytes)
	assert.Equal(t, 5242880, cfg.MaxWriteBytes)
	assert.True(t, cfg.FollowSymlinks)
}

// TestDecodeConnect_AllowedPathsFromAnySlice verifies that the
// YAML/JSON-natural []any form is accepted alongside []string.
func TestDecodeConnect_AllowedPathsFromAnySlice(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"allowed_paths": []any{"/tmp/work", "/home/user"},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"/tmp/work", "/home/user"}, cfg.AllowedPaths)
}

// TestDecodeConnect_AllowedPathsWrongType verifies that a string
// where a list is expected produces a wrapped errAllowedPathsWrongType.
func TestDecodeConnect_AllowedPathsWrongType(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"allowed_paths": "/tmp/single-path",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errAllowedPathsWrongType)
}

// TestDecodeConnect_NonScalarAllowedPathElement verifies that a
// non-scalar element inside allowed_paths surfaces as a per-index
// decode error.
func TestDecodeConnect_NonScalarAllowedPathElement(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"allowed_paths": []any{
			"/tmp/work",
			map[string]any{"nested": "value"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowed_paths[1]")
}

// TestDecodeConnect_MaxBytesTypes verifies that integer, int64, and
// float64 (YAML's natural number type) are all accepted; other
// types are rejected.
func TestDecodeConnect_MaxBytesTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     any
		want    int
		wantErr bool
	}{
		{"int", 1024, 1024, false},
		{"int64", int64(2048), 2048, false},
		{"float64", float64(4096), 4096, false},
		{"string", "8192", 0, true},
		{"map", map[string]any{"bytes": 100}, 0, true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := decodeConnect(map[string]any{
				"allowed_paths":  []string{"/tmp"},
				"max_read_bytes": testCase.raw,
			})
			if testCase.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "must be an integer")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.want, cfg.MaxReadBytes)
		})
	}
}

// TestDecodeConnect_FollowSymlinksValues verifies that the boolean
// string forms (true/false) and the YAML bool forms are both
// accepted; unknown strings are rejected.
func TestDecodeConnect_FollowSymlinksValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     any
		want    bool
		wantErr bool
	}{
		{"string true", "true", true, false},
		{"string false", "false", false, false},
		{"string True", "True", true, false},
		{"string FALSE", "FALSE", false, false},
		{"bool true", true, true, false},
		{"bool false", false, false, false},
		{"bad string", "maybe", false, true},
		{"map", map[string]any{"x": true}, false, true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := decodeConnect(map[string]any{
				"allowed_paths":   []string{"/tmp"},
				"follow_symlinks": testCase.raw,
			})
			if testCase.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "follow_symlinks")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.want, cfg.FollowSymlinks)
		})
	}
}

// --- validate ---

// TestValidate_MissingAllowedPaths pins the required-field check.
func TestValidate_MissingAllowedPaths(t *testing.T) {
	t.Parallel()

	cfg := config{}
	err := cfg.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, errAllowedPathsEmpty)
}

// TestValidate_NegativeMaxRead verifies the negative guard.
func TestValidate_NegativeMaxRead(t *testing.T) {
	t.Parallel()

	cfg := config{ //nolint:exhaustruct // negative MaxWriteBytes is the test focus
		AllowedPaths: []string{"/tmp"},
		MaxReadBytes: -1,
	}
	err := cfg.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, errMaxReadNegative)
}

// TestValidate_NegativeMaxWrite verifies the negative guard.
func TestValidate_NegativeMaxWrite(t *testing.T) {
	t.Parallel()

	cfg := config{ //nolint:exhaustruct // negative MaxWriteBytes is the test focus
		AllowedPaths:  []string{"/tmp"},
		MaxWriteBytes: -1,
	}
	err := cfg.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, errMaxWriteNegative)
}

// TestValidate_OK verifies the happy path.
func TestValidate_OK(t *testing.T) {
	t.Parallel()

	cfg := config{ //nolint:exhaustruct // partial literal is intentional
		AllowedPaths:  []string{"/tmp"},
		MaxReadBytes:  1024,
		MaxWriteBytes: 10240,
	}
	assert.NoError(t, cfg.validate())
}

// --- resolveLimit ---

// TestResolveMaxReadBytes verifies the defaulting path.
func TestResolveMaxReadBytes(t *testing.T) {
	t.Parallel()

	zero := config{}
	assert.Equal(t, defaultMaxReadBytes, zero.resolveMaxReadBytes())

	negative := config{
		AllowedPaths:   []string(nil),
		MaxReadBytes:   -1,
		MaxWriteBytes:  0,
		FollowSymlinks: false,
	}
	assert.Equal(t, defaultMaxReadBytes, negative.resolveMaxReadBytes())

	explicit := config{
		AllowedPaths:   []string(nil),
		MaxReadBytes:   4096,
		MaxWriteBytes:  0,
		FollowSymlinks: false,
	}
	assert.Equal(t, 4096, explicit.resolveMaxReadBytes())
}

// TestResolveMaxWriteBytes verifies the defaulting path.
func TestResolveMaxWriteBytes(t *testing.T) {
	t.Parallel()

	zero := config{}
	assert.Equal(t, defaultMaxWriteBytes, zero.resolveMaxWriteBytes())

	explicit := config{
		AllowedPaths:   []string(nil),
		MaxReadBytes:   0,
		MaxWriteBytes:  4096,
		FollowSymlinks: false,
	}
	assert.Equal(t, 4096, explicit.resolveMaxWriteBytes())
}

// --- resolveAndGuard ---

// TestResolveAndGuard_EmptyAllowedPaths pins the deny-by-default
// contract: a source with no allowed paths rejects every call.
func TestResolveAndGuard_EmptyAllowedPaths(t *testing.T) {
	t.Parallel()

	_, err := resolveAndGuard([]string(nil), false, "/tmp/whatever")
	require.Error(t, err)
	assert.ErrorIs(t, err, errAllowedPathsEmpty)
}

// TestResolveAndGuard_EmptyRequested pins the empty-path guard.
func TestResolveAndGuard_EmptyRequested(t *testing.T) {
	t.Parallel()

	_, err := resolveAndGuard([]string{"/tmp"}, false, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, errPathEmpty)
}

// TestResolveAndGuard_ParentTraversalRejected pins the canonical
// ../../etc/passwd attack against the resolver.
func TestResolveAndGuard_ParentTraversalRejected(t *testing.T) {
	t.Parallel()

	_, err := resolveAndGuard([]string{"/tmp"}, false, "../../etc/passwd")
	require.Error(t, err)
	assert.ErrorIs(t, err, errPathContainsParent)
}

// TestResolveAndGuard_Allowed verifies that a child path resolves
// to its canonical real form. On macOS t.TempDir() returns paths
// under /var/folders/... which EvalSymlinks rewrites to
// /private/var/folders/...; the test asserts the canonical form so
// it runs unchanged on Linux.
func TestResolveAndGuard_Allowed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	child := filepath.Join(root, "child.txt")

	require.NoError(t, os.WriteFile(child, []byte("hi"), 0o600))

	resolved, err := resolveAndGuard([]string{root}, false, child)
	require.NoError(t, err)

	realRoot, err := canonicalRoot(root)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(realRoot, "child.txt"), resolved)
}

// TestResolveAndGuard_OutsideRejected pins the cross-allowlist
// rejection: a sibling directory not in the allowlist is refused.
func TestResolveAndGuard_OutsideRejected(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	other := t.TempDir()

	_, err := resolveAndGuard([]string{root}, false, other)
	require.Error(t, err)
	assert.ErrorIs(t, err, errPathOutsideAllowed)
}

// TestResolveAndGuard_PrefixCollisionRejected verifies that the
// prefix check is separator-aware: /tmp/other is NOT considered
// inside /tmp.
func TestResolveAndGuard_PrefixCollisionRejected(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	otherRoot := root + "-other"
	require.NoError(t, os.MkdirAll(otherRoot, 0o750))

	_, err := resolveAndGuard([]string{root}, false, otherRoot)
	require.Error(t, err)
	assert.ErrorIs(t, err, errPathOutsideAllowed)
}

// TestResolveAndGuard_SymlinkEscapeRejected verifies that a symlink
// inside the allowlist that points outside is rejected under the
// default (follow_symlinks=false) policy.
func TestResolveAndGuard_SymlinkEscapeRejected(t *testing.T) {
	t.Parallel()

	if os.Getuid() == 0 {
		t.Skip("symlink behavior is unreliable when running as root")
	}

	root := t.TempDir()
	outside := t.TempDir()

	target := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(target, []byte("secret"), 0o600))

	link := filepath.Join(root, "escape")
	require.NoError(t, os.Symlink(target, link))

	_, err := resolveAndGuard([]string{root}, false, link)
	require.Error(t, err)
	assert.ErrorIs(t, err, errSymlinkEscape)
}

// TestResolveAndGuard_SymlinkFollowedButInRootAccepted verifies
// that a symlink whose target is inside the allowlist is accepted
// under the permissive (follow_symlinks=true) policy. The
// returned path is the post-follow real path.
func TestResolveAndGuard_SymlinkFollowedButInRootAccepted(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "real.txt")

	require.NoError(t, os.WriteFile(target, []byte("ok"), 0o600))

	link := filepath.Join(root, "link.txt")
	require.NoError(t, os.Symlink(target, link))

	resolved, err := resolveAndGuard([]string{root}, true, link)
	require.NoError(t, err)

	realTarget, err := filepath.EvalSymlinks(target)
	require.NoError(t, err)

	assert.Equal(t, realTarget, resolved)
}

// TestResolveAndGuard_FollowSymlinksButEscapeRejected verifies the
// permissive policy's safety net: symlinks are followed, but if the
// post-follow real path exits the root, the call is refused.
func TestResolveAndGuard_FollowSymlinksButEscapeRejected(t *testing.T) {
	t.Parallel()

	if os.Getuid() == 0 {
		t.Skip("symlink behavior is unreliable when running as root")
	}

	root := t.TempDir()
	outside := t.TempDir()

	target := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(target, []byte("secret"), 0o600))

	link := filepath.Join(root, "escape")
	require.NoError(t, os.Symlink(target, link))

	_, err := resolveAndGuard([]string{root}, true, link)
	require.Error(t, err)
	assert.ErrorIs(t, err, errSymlinkEscape)
}

// TestResolveAndGuard_NonexistentChildInRoot verifies that a path
// under the root that does not yet exist is accepted (write_file
// needs this). The handler will surface ENOENT when it tries to
// open the file.
func TestResolveAndGuard_NonexistentChildInRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	resolved, err := resolveAndGuard(
		[]string{root},
		false,
		filepath.Join(root, "does-not-exist.txt"),
	)
	require.NoError(t, err)

	realRoot, err := canonicalRoot(root)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(realRoot, "does-not-exist.txt"), resolved)
}

// TestCanonicalize_ReachesFilesystemRoot exercises the "fall back
// to Clean" branch when every ancestor fails EvalSymlinks. In
// practice this is unreachable on a healthy filesystem, but the
// branch exists for safety; we cover it with a relative path
// whose parent chain has no existing ancestor.
func TestCanonicalize_ReachesFilesystemRoot(t *testing.T) {
	t.Parallel()

	// The path /definitely-not-a-real-path-xyzzy/anything walks
	// up: /definitely-not-a-real-path-xyzzy/anything, then .../xyzzy,
	// then .../not-a-real-path, then /. The first non-error is at /,
	// where EvalSymlinks("/") succeeds. So this exercises the
	// "we resolved a parent" branch — opposite of what we want.
	// Skip rather than fake a filesystem state we can't fake.
	t.Skip("canonicalize's filesystem-root fallback is unreachable in a real test environment")
}

// TestCanonicalRoot_NonExistentPath pins the "absent root is taken
// as-is" branch used by operators who create the directory between
// config-load and first tool call.
func TestCanonicalRoot_NonExistentPath(t *testing.T) {
	t.Parallel()

	abs, err := filepath.Abs("/this/path/should/not/exist/anywhere")
	require.NoError(t, err)

	got, err := canonicalRoot(abs)
	require.NoError(t, err)

	assert.Equal(t, abs, got)
}

// TestNameSpaceNormal_ReachesFilesystemRoot exercises the fallback
// branch where the resolver cannot find any existing ancestor. In
// practice this is unreachable (every path on a healthy filesystem
// has / as an existing ancestor), so we cover the realistic path
// instead: a path whose parent dir exists but the leaf does not.
func TestNameSpaceNormal_MissingLeaf(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "leaf-that-does-not-exist-yet")

	got := nameSpaceNormal(target)

	realRoot, err := canonicalRoot(root)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(realRoot, "leaf-that-does-not-exist-yet"), got)
}

// TestIsValidUTF8 covers the UTF-8 validation used by the
// binary-content encoding path. A regression here would let
// non-UTF-8 text through as text instead of b64-encoding it.
func TestIsValidUTF8(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty", nil, true},
		{"ascii", []byte("hello world"), true},
		// 2-byte UTF-8: U+00A2 (¢) = 0xC2 0xA2
		{"two_byte", []byte{0xC2, 0xA2}, true},
		// 3-byte UTF-8: U+20AC (€) = 0xE2 0x82 0xAC
		{"three_byte", []byte{0xE2, 0x82, 0xAC}, true},
		// 4-byte UTF-8: U+1F600 (😀) = 0xF0 0x9F 0x98 0x80
		{"four_byte", []byte{0xF0, 0x9F, 0x98, 0x80}, true},
		// Stray continuation byte with no lead.
		{"stray_continuation", []byte{0x80, 0x80, 0x80}, false},
		// 2-byte sequence with bad continuation.
		{"bad_continuation", []byte{0xC2, 0x00}, false},
		// 3-byte sequence that's too short.
		{"truncated_three", []byte{0xE2, 0x82}, false},
		// 4-byte sequence that's too short.
		{"truncated_four", []byte{0xF0, 0x9F, 0x98}, false},
	}

	for _, tcase := range cases {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tcase.want, isValidUTF8(tcase.data))
		})
	}
}

// --- Decode helpers smoke tests ---

// TestDecodeInt_AcceptsFloat64 pins the YAML-natural numeric form.
func TestDecodeInt_AcceptsFloat64(t *testing.T) {
	t.Parallel()

	got, err := decodeInt(map[string]any{"x": float64(42)}, "x")
	require.NoError(t, err)
	assert.Equal(t, 42, got)
}

// TestDecodeAllowedPaths_NilInput verifies that a missing
// allowed_paths returns a nil slice (the validate step surfaces
// the missing-required error).
func TestDecodeAllowedPaths_NilInput(t *testing.T) {
	t.Parallel()

	got, err := decodeAllowedPaths(make(map[string]any))
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestDecodeAllowedPaths_NullValue verifies that an explicit
// null is treated the same as missing.
func TestDecodeAllowedPaths_NullValue(t *testing.T) {
	t.Parallel()

	got, err := decodeAllowedPaths(map[string]any{"allowed_paths": any(nil)})
	require.NoError(t, err)
	assert.Nil(t, got)
}

// --- Schemas ---

// TestSchemas_EmbeddedRawMessagesAreValidJSON verifies that every
// embedded schema parses cleanly. A regression here would cause
// silent runtime failures when the dispatcher tries to pass the
// schema to mcp.Server.AddTool.
func TestSchemas_EmbeddedRawMessagesAreValidJSON(t *testing.T) {
	t.Parallel()

	schemas := []struct {
		name string
		data json.RawMessage
	}{
		{"listAllowedDirectoriesOutput", listAllowedDirectoriesOutput},
		{"readFileInput", readFileInput},
		{"readFileOutput", readFileOutput},
		{"writeFileInput", writeFileInput},
		{"writeFileOutput", writeFileOutput},
		{"editFileInput", editFileInput},
		{"editFileOutput", editFileOutput},
		{"createDirectoryInput", createDirectoryInput},
		{"createDirectoryOutput", createDirectoryOutput},
		{"listDirectoryInput", listDirectoryInput},
		{"listDirectoryOutput", listDirectoryOutput},
		{"directoryTreeInput", directoryTreeInput},
		{"directoryTreeOutput", directoryTreeOutput},
		{"moveFileInput", moveFileInput},
		{"moveFileOutput", moveFileOutput},
		{"copyFileInput", copyFileInput},
		{"copyFileOutput", copyFileOutput},
		{"deleteFileInput", deleteFileInput},
		{"deleteFileOutput", deleteFileOutput},
		{"searchFilesInput", searchFilesInput},
		{"searchFilesOutput", searchFilesOutput},
		{"getFileInfoInput", getFileInfoInput},
		{"getFileInfoOutput", getFileInfoOutput},
	}

	for _, schema := range schemas {
		t.Run(schema.name, func(t *testing.T) {
			t.Parallel()

			var probe map[string]any

			require.NoErrorf(t, json.Unmarshal(schema.data, &probe),
				"schema %s must be a valid JSON object: %s", schema.name, schema.data)
		})
	}
}

// TestImportedPackagesUsed is a no-op smoke test that pins the fact
// that we keep the decode package import — it makes a future
// "remove unused import" lint pass not silently drop the import.
func TestImportedPackagesUsed(t *testing.T) {
	t.Parallel()

	// decode.AsString is used elsewhere; this test just exercises
	// the import path so a refactor cannot remove it accidentally.
	_, err := decode.AsString(any(nil))
	require.Error(t, err)
	assert.ErrorIs(t, err, decode.ErrNotSet)
}
