// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package fs

import (
	"fmt"
	"os"
	"path/filepath"
)

// atomicWriteFile persists data to resolved via a temp-file +
// rename pattern so concurrent readers see either the old or the
// new contents, never a torn write. POSIX rename(2) is atomic for
// files on the same filesystem, so the temp file MUST live in the
// same directory as resolved — the kernel does not atomically
// cross mount points. On any failure before the rename succeeds
// the temp file is best-effort removed so the directory does not
// accumulate .tmp-* cruft.
func atomicWriteFile(resolved string, data []byte) error {
	dir := filepath.Dir(resolved)

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	tmpName := tmp.Name()

	//nolint:errcheck // best-effort cleanup; after a successful rename the path is gone (ENOENT)
	defer func() { _ = os.Remove(tmpName) }()

	chmodErr := tmp.Chmod(fileCreateMode)
	if chmodErr != nil {
		_ = tmp.Close()

		return fmt.Errorf("chmod temp file: %w", chmodErr)
	}

	_, writeErr := tmp.Write(data)
	if writeErr != nil {
		_ = tmp.Close()

		return fmt.Errorf("write temp file: %w", writeErr)
	}

	closeErr := tmp.Close()
	if closeErr != nil {
		return fmt.Errorf("close temp file: %w", closeErr)
	}

	renameErr := os.Rename(tmpName, resolved)
	if renameErr != nil {
		return fmt.Errorf("rename temp file: %w", renameErr)
	}

	return nil
}
