// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package fs tool handlers.
//
//nolint:revive // file-length-limit: handlers + locked variants exceed 750-line default.
package fs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- Request argument shapes ---

// readFileArgs is the JSON input to the read_file tool. Path is
// required; the handler resolves it through resolveAndGuard before
// any I/O. StartLine and EndLine are optional 1-indexed inclusive
// line bounds (omitted in JSON serialize as 0). Both apply to
// UTF-8 text files only — binary files reject any line-range
// argument with a clear error.
type readFileArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

// writeFileArgs is the JSON input to the write_file tool. Path and
// Content are required; Encoding is optional and defaults to "utf-8".
type writeFileArgs struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
}

// editFileArgs is the JSON input to the edit_file tool. Path,
// OldText, and NewText are all required.
type editFileArgs struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

// pathArgs is the JSON input to tools that take a single path
// argument (create_directory, list_directory, delete_file,
// get_file_info, directory_tree).
type pathArgs struct {
	Path string `json:"path"`
}

// directoryTreeArgs is the JSON input to directory_tree. MaxDepth is
// optional and defaults to defaultMaxTreeDepth.
type directoryTreeArgs struct {
	Path     string `json:"path"`
	MaxDepth int    `json:"max_depth,omitempty"`
}

// moveFileArgs is the JSON input to move_file.
type moveFileArgs struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// copyFileArgs is the JSON input to copy_file.
type copyFileArgs struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// searchFilesArgs is the JSON input to search_files.
type searchFilesArgs struct {
	Root     string `json:"root"`
	Pattern  string `json:"pattern"`
	MaxDepth int    `json:"max_depth,omitempty"`
}

// grepArgs is the JSON input to grep. The optional fields use
// pointer types so the handler can distinguish "omitted" from
// "explicitly false / zero" and apply the package defaults only
// when the caller did not pick a value.
type grepArgs struct {
	Root                  string `json:"root"`
	Pattern               string `json:"pattern"`
	IncludePattern        string `json:"include_pattern,omitempty"`
	CaseSensitive         bool   `json:"case_sensitive,omitempty"`
	MaxDepth              int    `json:"max_depth,omitempty"`
	MaxResults            int    `json:"max_results,omitempty"`
	ContextLines          int    `json:"context_lines,omitempty"`
	MaxLineBytes          int    `json:"max_line_bytes,omitempty"`
	UseGitignore          *bool  `json:"use_gitignore,omitempty"`
	RespectDefaultIgnores *bool  `json:"respect_default_ignores,omitempty"`
	Literal               bool   `json:"literal,omitempty"`
}

// --- Shared helpers ---

// decodeArgs unmarshals the request's arguments JSON into target. It
// is the one place every tool handler does JSON-decode so the
// per-tool handlers stay focused on per-tool argument validation.
//
//nolint:wrapcheck // external json.Unmarshal error returned verbatim for handler parsing failures
func decodeArgs(req *mcp.CallToolRequest, target any) error {
	return json.Unmarshal(req.Params.Arguments, target)
}

// guardPath resolves requested through the source's allowlist and
// returns the absolute real path. The error wrapping prefix is
// "fs:" so dispatcher logs and operator diagnostics immediately
// point at the source type.
func guardPath(cfg *config, requested string) (string, error) {
	return resolveAndGuard(cfg.AllowedPaths, cfg.FollowSymlinks, requested)
}

// dirEntry is the per-entry shape returned by list_directory and
// directory_tree's intermediate nodes. It matches the
// list_directory_output schema.
type dirEntry struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	SizeBytes int64  `json:"size_bytes"`
}

// treeNode is the recursive shape returned by directory_tree. It
// matches the directory_tree_output schema (children is always
// present; for files it is empty).
type treeNode struct {
	Name     string      `json:"name"`
	Type     string      `json:"type"`
	Children []*treeNode `json:"children"`
}

// buildTree walks dir up to maxDepth and returns the matching tree.
// Files are returned as leaf nodes (children: nil marshals to []).
// Directories beyond maxDepth are represented as leaf nodes with
// type "directory" and no children.
func buildTree(dir string, currentDepth, maxDepth int) ([]*treeNode, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read directory %q: %w", dir, err)
	}

	nodes := make([]*treeNode, 0, len(entries))

	for _, entry := range entries {
		node, nodeErr := buildTreeNode(dir, entry, currentDepth, maxDepth)
		if nodeErr != nil {
			return nil, nodeErr
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

// buildTreeNode constructs one node of the directory tree. Files
// become leaf nodes with an empty Children slice; directories at
// the max depth become directory leaf nodes with an empty
// Children slice; directories under the cap recurse. Both empty
// cases use a non-nil empty slice so the JSON marshals to "[]"
// (matching the directory_tree_output schema's `type: "array"`)
// rather than "null".
func buildTreeNode(dir string, entry os.DirEntry, currentDepth, maxDepth int) (*treeNode, error) {
	node := &treeNode{
		Name:     entry.Name(),
		Type:     entryKindFile,
		Children: []*treeNode{},
	}

	if !entry.IsDir() {
		return node, nil
	}

	node.Type = entryKindDirectory

	if currentDepth >= maxDepth {
		node.Children = []*treeNode{}

		return node, nil
	}

	childPath := filepath.Join(dir, entry.Name())

	children, childErr := buildTree(childPath, currentDepth+1, maxDepth)
	if childErr != nil {
		return nil, childErr
	}

	node.Children = children

	return node, nil
}

// matchSearchPattern tests whether path (relative to root) matches the
// glob pattern. The pattern is matched against filepath.Base(relativePath)
// rather than the full relative path: filepath.Match's '*' does not cross
// path separators, so matching "*.py" against "src/calculator.py" would
// silently drop the file. Matching the basename is what the reference
// MCP filesystem server does and what users expect from a "*.ext"
// pattern against a tree.
func matchSearchPattern(relativePath, pattern string) (bool, error) {
	matched, err := filepath.Match(pattern, filepath.Base(relativePath))
	if err != nil {
		return false, fmt.Errorf("match pattern: %w", err)
	}

	return matched, nil
}

// resolveMaxDepth applies the configured or default tree depth.
func resolveMaxDepth(requested int) int {
	if requested <= 0 {
		return defaultMaxTreeDepth
	}

	return requested
}

// decodeWriteContent decodes the write_file input based on the
// encoding hint. utf-8 (the default) returns the content as a byte
// slice; base64 decodes the content first.
func decodeWriteContent(content, encoding string) ([]byte, error) {
	switch encoding {
	case "", "utf-8":
		return []byte(content), nil

	case "base64":
		data, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, fmt.Errorf("decode base64 content: %w", err)
		}

		return data, nil

	default:
		return nil, fmt.Errorf("encoding: must be 'utf-8' or 'base64', got %q", encoding)
	}
}

// --- Tool handlers ---

// handleListAllowedDirectories returns the mcp.ToolHandler for the
// list_allowed_directories tool. The handler ignores its arguments
// (the input schema is empty) and returns the operator-configured
// roots verbatim.
func handleListAllowedDirectories(cfg *config) mcp.ToolHandler {
	return func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return listAllowedDirectories(cfg)
	}
}

// listAllowedDirectories resolves each configured path to its
// absolute, symlink-aware real form so the LLM sees the same paths
// the resolver uses internally. This keeps "where am I allowed to
// write?" and "where will this path resolve?" in lockstep.
func listAllowedDirectories(cfg *config) (*mcp.CallToolResult, error) {
	paths := make([]string, 0, len(cfg.AllowedPaths))

	for _, root := range cfg.AllowedPaths {
		resolved, err := canonicalRoot(root)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", root, err)
		}

		paths = append(paths, resolved)
	}

	return textResult(map[string]any{"allowed_paths": paths})
}

// handleReadFile returns the mcp.ToolHandler for the read_file tool.
func handleReadFile(cfg *config) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return readFile(req, cfg)
	}
}

// readFile parses the request, validates the line-range arguments,
// resolves the path through the allowlist, applies the read-byte
// cap, and returns the file's content (UTF-8 text verbatim or
// sliced, binary content base64-encoded byte-for-byte). The read
// goes through withPathRLock so concurrent readers can run in
// parallel but block on any in-flight writer for the same resolved
// path.
func readFile(req *mcp.CallToolRequest, cfg *config) (*mcp.CallToolResult, error) {
	var args readFileArgs

	err := decodeArgs(req, &args)
	if err != nil {
		return nil, fmt.Errorf("parse read_file args: %w", err)
	}

	validateErr := validateLineRange(args.StartLine, args.EndLine)
	if validateErr != nil {
		return nil, validateErr
	}

	resolved, err := guardPath(cfg, args.Path)
	if err != nil {
		return nil, err
	}

	var result *mcp.CallToolResult

	lockErr := withPathRLock(resolved, func() error {
		var innerErr error

		result, innerErr = readFileLocked(
			resolved, cfg.resolveMaxReadBytes(), args.StartLine, args.EndLine,
		)

		return innerErr
	})
	if lockErr != nil {
		return nil, lockErr
	}

	return result, nil
}

// validateLineRange is the cheap, pre-I/O argument check for the
// line-range fields. Zero values mean "absent" (omitted in JSON);
// non-zero values must be >= 1, and when both are present end
// must be >= start. start_line > total_lines is checked separately
// inside readFileTextResult because total_lines is only known after
// the file is read.
func validateLineRange(startLine, endLine int) error {
	if startLine != 0 && startLine < 1 {
		return fmt.Errorf("read_file: start_line must be >= 1, got %d", startLine)
	}

	if endLine != 0 && endLine < 1 {
		return fmt.Errorf("read_file: end_line must be >= 1, got %d", endLine)
	}

	if startLine != 0 && endLine != 0 && endLine < startLine {
		return fmt.Errorf(
			"read_file: end_line (%d) must be >= start_line (%d)",
			endLine, startLine,
		)
	}

	return nil
}

// readFileLocked is the read_file work that runs under the
// per-path read lock. The caller has already guarded the path,
// resolved the byte cap, and validated the line-range arguments.
// Binary detection happens before any slicing so the binary-file
// rejection can fire before bytes.Split tries to make sense of
// non-text data. Pulled out of the closure to keep readFile
// below gocognit's complexity threshold.
func readFileLocked(
	resolved string, maxBytes, startLine, endLine int,
) (*mcp.CallToolResult, error) {
	data, err := readFileContents(resolved, maxBytes)
	if err != nil {
		return nil, err
	}

	isBinary := !isValidUTF8(data)
	hasLineArgs := startLine > 0 || endLine > 0

	if isBinary && hasLineArgs {
		return nil, fmt.Errorf(
			"read_file: line-range arguments are supported for UTF-8 " +
				"text files only; drop start_line/end_line to fetch the " +
				"whole file as b64:<base64>",
		)
	}

	if isBinary {
		content, _ := encodeBytes(data)

		return textResult(map[string]any{
			"content":    content,
			"size_bytes": int64(len(data)),
			"is_binary":  true,
		})
	}

	return readFileTextResult(data, startLine, endLine)
}

// readFileTextResult is the text-file branch of readFileLocked.
// When no line arguments are present it returns the whole file
// unchanged (including any trailing newline) — the existing
// pre-line-selection contract — and populates the three new
// output fields with values that mirror the whole-file shape
// (returned_bytes == size_bytes, returned_lines == total_lines).
// When line arguments are present it slices the file by line,
// drops the file's terminal newline (per the documented
// `end_line == total_lines` behavior), and surfaces the actual
// slice size via returned_bytes and returned_lines.
func readFileTextResult(data []byte, startLine, endLine int) (*mcp.CallToolResult, error) {
	// bytes.Split on text ending in "\n" produces a trailing empty
	// element that is an artifact of the trailing newline, not a
	// real line. Strip it so total_lines and slice boundaries
	// reflect actual content. An empty file becomes an empty slice
	// with total_lines == 0, which is the right shape for the
	// "start_line past EOF" check below.
	lines := bytes.Split(data, []byte{'\n'})

	if numLines := len(lines); numLines > 0 && len(lines[numLines-1]) == 0 {
		lines = lines[:numLines-1]
	}

	totalLines := len(lines)
	hasLineArgs := startLine > 0 || endLine > 0

	if !hasLineArgs {
		content := string(data)

		return textResult(map[string]any{
			"content":        content,
			"size_bytes":     int64(len(data)),
			"returned_bytes": len(content),
			"total_lines":    totalLines,
			"returned_lines": totalLines,
			"is_binary":      false,
		})
	}

	if startLine > totalLines {
		return nil, fmt.Errorf(
			"read_file: start_line %d exceeds file length of %d lines",
			startLine, totalLines,
		)
	}

	if endLine > totalLines {
		endLine = totalLines
	}

	if startLine == 0 {
		startLine = 1
	}

	if endLine == 0 {
		endLine = totalLines
	}

	slice := lines[startLine-1 : endLine]
	content := string(bytes.Join(slice, []byte{'\n'}))

	return textResult(map[string]any{
		"content":        content,
		"size_bytes":     int64(len(data)),
		"returned_bytes": len(content),
		"total_lines":    totalLines,
		"returned_lines": len(slice),
		"is_binary":      false,
	})
}

// readFileContents reads resolved after asserting it is a regular
// file under the configured read-byte cap. The cap is checked
// against os.Stat (not the bytes read) so a race with a growing file
// still surfaces the cap rather than running away. The returned size
// is len(data) because the contract is "the bytes we returned".
func readFileContents(resolved string, maxBytes int) ([]byte, error) {
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", resolved, err)
	}

	if info.IsDir() {
		return nil, fmt.Errorf("read_file: %q is a directory", resolved)
	}

	if info.Size() > int64(maxBytes) {
		return nil, fmt.Errorf(
			"read_file: %q is %d bytes, exceeds max_read_bytes=%d",
			resolved, info.Size(), maxBytes)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", resolved, err)
	}

	return data, nil
}

// handleWriteFile returns the mcp.ToolHandler for the write_file tool.
func handleWriteFile(cfg *config) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return writeFile(req, cfg)
	}
}

// writeFile parses the request, decodes the content, applies the
// write-byte cap, and persists the file (creating parent directories
// as needed). The path is always guarded by resolveAndGuard before
// any I/O. The actual write goes through atomicWriteFile (temp
// file + rename) and is wrapped in withPathLock so concurrent
// writes against the same path serialize instead of racing.
func writeFile(req *mcp.CallToolRequest, cfg *config) (*mcp.CallToolResult, error) {
	var args writeFileArgs

	err := decodeArgs(req, &args)
	if err != nil {
		return nil, fmt.Errorf("parse write_file args: %w", err)
	}

	resolved, err := guardPath(cfg, args.Path)
	if err != nil {
		return nil, err
	}

	data, err := decodeWriteContent(args.Content, args.Encoding)
	if err != nil {
		return nil, err
	}

	if len(data) > cfg.resolveMaxWriteBytes() {
		return nil, fmt.Errorf(
			"write_file: content is %d bytes, exceeds max_write_bytes=%d",
			len(data), cfg.resolveMaxWriteBytes())
	}

	var result *mcp.CallToolResult

	lockErr := withPathLock(resolved, func() error {
		var innerErr error

		result, innerErr = writeFileLocked(resolved, data)

		return innerErr
	})
	if lockErr != nil {
		return nil, lockErr
	}

	return result, nil
}

// writeFileLocked is the write_file work that runs under the
// per-path write lock. The caller has already guarded the path
// and enforced the write-byte cap; this function mkdirs the
// parent (idempotent on existing directories), writes the data
// atomically, and returns the result envelope. Pulled out of
// the closure to keep writeFile below gocognit's complexity
// threshold.
func writeFileLocked(resolved string, data []byte) (*mcp.CallToolResult, error) {
	mkdirErr := os.MkdirAll(filepath.Dir(resolved), dirCreateMode)
	if mkdirErr != nil {
		return nil, fmt.Errorf("create parent dir: %w", mkdirErr)
	}

	writeErr := atomicWriteFile(resolved, data)
	if writeErr != nil {
		return nil, fmt.Errorf("write %q: %w", resolved, writeErr)
	}

	return textResult(map[string]any{
		"path":          resolved,
		"bytes_written": len(data),
	})
}

// handleEditFile returns the mcp.ToolHandler for the edit_file tool.
func handleEditFile(cfg *config) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return editFile(req, cfg)
	}
}

// editFile applies the single-occurrence replacement of OldText by
// NewText. The old text must occur exactly once (zero or many
// matches are rejected with a clear error). The whole read-modify-
// write is wrapped in withPathLock so concurrent edit_file calls
// against the same path serialize instead of silently losing one
// of the updates; the write itself goes through atomicWriteFile so
// concurrent readers see either the pre-edit or post-edit content,
// never a torn file.
func editFile(req *mcp.CallToolRequest, cfg *config) (*mcp.CallToolResult, error) {
	var args editFileArgs

	err := decodeArgs(req, &args)
	if err != nil {
		return nil, fmt.Errorf("parse edit_file args: %w", err)
	}

	resolved, err := guardPath(cfg, args.Path)
	if err != nil {
		return nil, err
	}

	var result *mcp.CallToolResult

	lockErr := withPathLock(resolved, func() error {
		var innerErr error

		result, innerErr = editFileLocked(
			resolved, args.OldText, args.NewText, cfg.resolveMaxReadBytes(),
		)

		return innerErr
	})
	if lockErr != nil {
		return nil, lockErr
	}

	return result, nil
}

// editFileLocked is the edit_file work that runs under the
// per-path write lock. The caller has already guarded the path
// and resolved the read-byte cap; this function performs the
// read-modify-write (single text replacement + atomic rename)
// and returns the result envelope. Pulled out of the closure
// to keep editFile below gocognit's complexity threshold.
func editFileLocked(resolved, oldText, newText string, maxBytes int) (*mcp.CallToolResult, error) {
	updated, err := applySingleReplacement(resolved, oldText, newText, maxBytes)
	if err != nil {
		return nil, err
	}

	writeErr := atomicWriteFile(resolved, []byte(updated))
	if writeErr != nil {
		return nil, fmt.Errorf("write %q: %w", resolved, writeErr)
	}

	return textResult(map[string]any{
		"path":         resolved,
		"replacements": 1,
	})
}

// applySingleReplacement reads the file under the read-byte cap,
// verifies OldText occurs exactly once, and returns the updated
// content. Zero or many matches is an error so the LLM gets a
// deterministic "this would have been ambiguous" signal.
func applySingleReplacement(resolved, oldText, newText string, maxBytes int) (string, error) {
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", resolved, err)
	}

	if info.Size() > int64(maxBytes) {
		return "", fmt.Errorf(
			"edit_file: %q is %d bytes, exceeds max_read_bytes=%d",
			resolved, info.Size(), maxBytes)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", resolved, err)
	}

	original := string(data)

	count := strings.Count(original, oldText)
	if count == 0 {
		return "", fmt.Errorf("edit_file: old_text not found in %q", resolved)
	}

	if count > 1 {
		return "", fmt.Errorf(
			"edit_file: old_text occurs %d times in %q; must occur exactly once",
			count, resolved)
	}

	return strings.Replace(original, oldText, newText, 1), nil
}

// handleCreateDirectory returns the mcp.ToolHandler for the
// create_directory tool. The handler is mkdir -p: it creates
// intermediate directories on demand and treats an existing
// directory as a no-op.
func handleCreateDirectory(cfg *config) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return createDirectory(req, cfg)
	}
}

// createDirectory parses the request, refuses to clobber an
// existing regular file, and otherwise calls mkdir -p. The work
// goes through withPathLock so a concurrent delete_file of the
// same path (or a concurrent create_directory + write_file race)
// cannot leave the LLM with a "directory exists, but contents
// gone" intermediate state.
func createDirectory(req *mcp.CallToolRequest, cfg *config) (*mcp.CallToolResult, error) {
	var args pathArgs

	err := decodeArgs(req, &args)
	if err != nil {
		return nil, fmt.Errorf("parse create_directory args: %w", err)
	}

	resolved, err := guardPath(cfg, args.Path)
	if err != nil {
		return nil, err
	}

	var result *mcp.CallToolResult

	lockErr := withPathLock(resolved, func() error {
		var innerErr error

		result, innerErr = createDirectoryLocked(resolved)

		return innerErr
	})
	if lockErr != nil {
		return nil, lockErr
	}

	return result, nil
}

// createDirectoryLocked is the create_directory work that runs
// under the per-path write lock. The caller has already guarded
// the path, so the only check needed here is the existing-target
// detection and the mkdir -p itself. Pulled out of the closure
// to keep createDirectory below gocognit's complexity threshold.
func createDirectoryLocked(resolved string) (*mcp.CallToolResult, error) {
	info, statErr := os.Stat(resolved)
	if statErr == nil {
		if info.IsDir() {
			return textResult(map[string]any{"path": resolved})
		}

		return nil, fmt.Errorf("create_directory: %q exists and is not a directory", resolved)
	}

	mkdirErr := os.MkdirAll(resolved, dirCreateMode)
	if mkdirErr != nil {
		return nil, fmt.Errorf("create %q: %w", resolved, mkdirErr)
	}

	return textResult(map[string]any{"path": resolved})
}

// handleListDirectory returns the mcp.ToolHandler for the
// list_directory tool.
func handleListDirectory(cfg *config) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return listDirectory(req, cfg)
	}
}

// listDirectory parses the request, asserts the target is a
// directory, and returns the per-entry JSON shape (name, type,
// size_bytes) for every entry in the directory.
func listDirectory(req *mcp.CallToolRequest, cfg *config) (*mcp.CallToolResult, error) {
	var args pathArgs

	err := decodeArgs(req, &args)
	if err != nil {
		return nil, fmt.Errorf("parse list_directory args: %w", err)
	}

	resolved, err := guardPath(cfg, args.Path)
	if err != nil {
		return nil, err
	}

	entries, err := listDirectoryEntries(resolved)
	if err != nil {
		return nil, err
	}

	return textResult(map[string]any{"entries": entries})
}

// listDirectoryEntries returns the dirEntry slice for resolved.
// The caller must have already asserted resolved is a directory.
func listDirectoryEntries(resolved string) ([]dirEntry, error) {
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", resolved, err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("list_directory: %q is not a directory", resolved)
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", resolved, err)
	}

	out := make([]dirEntry, 0, len(entries))

	for _, entry := range entries {
		item, itemErr := dirEntryFromOsEntry(entry)
		if itemErr != nil {
			return nil, itemErr
		}

		out = append(out, item)
	}

	return out, nil
}

// dirEntryFromOsEntry converts an os.DirEntry into the JSON
// dirEntry shape returned to the LLM. The SizeBytes lookup uses the
// FileInfo returned by DirEntry.Info; surface ENOENT / permission
// errors with the entry's name so the operator knows which file
// broke the listing.
func dirEntryFromOsEntry(entry os.DirEntry) (dirEntry, error) {
	kind := entryKindFile

	if entry.IsDir() {
		kind = entryKindDirectory
	}

	info, err := entry.Info()
	if err != nil {
		return dirEntry{}, fmt.Errorf("stat %q: %w", entry.Name(), err)
	}

	return dirEntry{
		Name:      entry.Name(),
		Type:      kind,
		SizeBytes: info.Size(),
	}, nil
}

// handleDirectoryTree returns the mcp.ToolHandler for the
// directory_tree tool. The output wraps the recursive tree under a
// synthetic root node named after the resolved path so the JSON
// shape is consistent regardless of which directory was requested.
func handleDirectoryTree(cfg *config) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return directoryTree(req, cfg)
	}
}

// directoryTree parses the request, builds the recursive tree, and
// returns it wrapped in a synthetic root node named after the
// resolved path.
func directoryTree(req *mcp.CallToolRequest, cfg *config) (*mcp.CallToolResult, error) {
	var args directoryTreeArgs

	err := decodeArgs(req, &args)
	if err != nil {
		return nil, fmt.Errorf("parse directory_tree args: %w", err)
	}

	resolved, err := guardPath(cfg, args.Path)
	if err != nil {
		return nil, err
	}

	info, statErr := os.Stat(resolved)
	if statErr != nil {
		return nil, fmt.Errorf("stat %q: %w", resolved, statErr)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("directory_tree: %q is not a directory", resolved)
	}

	maxDepth := resolveMaxDepth(args.MaxDepth)

	children, buildErr := buildTree(resolved, 1, maxDepth)
	if buildErr != nil {
		return nil, buildErr
	}

	root := &treeNode{
		Name:     filepath.Base(resolved),
		Type:     entryKindDirectory,
		Children: children,
	}

	return textResult(root)
}

// handleMoveFile returns the mcp.ToolHandler for the move_file tool.
// Both source and destination are resolved through the allowlist;
// the source and destination must live inside the same allowed root
// to prevent cross-root surprises (see issue Implementation Notes).
func handleMoveFile(cfg *config) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return moveFile(req, cfg)
	}
}

// moveFile parses the request, resolves both paths through the
// allowlist, and renames source onto destination. Both source and
// destination are locked via withPathTwoLocks, which acquires them
// in lexicographic order so two parallel move_file calls whose
// source and destination cross (move A→B racing B→A) cannot
// deadlock on the pair of per-path locks. When source and dest
// resolve to the same path only one lock is acquired; sync.RWMutex
// is not re-entrant, so the naive nested call would self-deadlock.
func moveFile(req *mcp.CallToolRequest, cfg *config) (*mcp.CallToolResult, error) {
	var args moveFileArgs

	err := decodeArgs(req, &args)
	if err != nil {
		return nil, fmt.Errorf("parse move_file args: %w", err)
	}

	source, err := guardPath(cfg, args.Source)
	if err != nil {
		return nil, err
	}

	dest, err := guardPath(cfg, args.Destination)
	if err != nil {
		return nil, err
	}

	var result *mcp.CallToolResult

	lockErr := withPathTwoLocks(source, dest, func() error {
		var innerErr error

		result, innerErr = moveFileLocked(source, dest)

		return innerErr
	})
	if lockErr != nil {
		return nil, lockErr
	}

	return result, nil
}

// moveFileLocked is the move_file work that runs under the two
// per-path write locks. The caller has already guarded both
// paths; this function performs the rename and returns the
// result envelope. Pulled out of the closure to keep moveFile
// below gocognit's complexity threshold.
func moveFileLocked(source, dest string) (*mcp.CallToolResult, error) {
	renameErr := os.Rename(source, dest)
	if renameErr != nil {
		return nil, fmt.Errorf("move %q → %q: %w", source, dest, renameErr)
	}

	return textResult(map[string]any{
		"source":      source,
		"destination": dest,
	})
}

// handleCopyFile returns the mcp.ToolHandler for the copy_file tool.
func handleCopyFile(cfg *config) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return copyFile(req, cfg)
	}
}

// copyFile parses the request, asserts source is a regular file
// (directories are out of scope), and copies source onto dest.
// The destination is wrapped in withPathLock so two parallel
// copies to the same target serialize — otherwise the last
// writer wins with a silent half-written file. The source is
// intentionally not locked: a parallel copy_file from the same
// source reads from the same bytes, so they cannot race each
// other; the only race worth preventing is the destination-side
// last-writer-wins, which is what withPathLock covers.
func copyFile(req *mcp.CallToolRequest, cfg *config) (*mcp.CallToolResult, error) {
	var args copyFileArgs

	err := decodeArgs(req, &args)
	if err != nil {
		return nil, fmt.Errorf("parse copy_file args: %w", err)
	}

	source, err := guardPath(cfg, args.Source)
	if err != nil {
		return nil, err
	}

	dest, err := guardPath(cfg, args.Destination)
	if err != nil {
		return nil, err
	}

	var result *mcp.CallToolResult

	lockErr := withPathLock(dest, func() error {
		var innerErr error

		result, innerErr = copyFileLocked(source, dest)

		return innerErr
	})
	if lockErr != nil {
		return nil, lockErr
	}

	return result, nil
}

// copyFileLocked is the copy_file work that runs under the
// per-path write lock on the destination. The caller has already
// guarded both paths; this function performs the copy and returns
// the result envelope. Pulled out of the closure to keep copyFile
// below gocognit's complexity threshold.
func copyFileLocked(source, dest string) (*mcp.CallToolResult, error) {
	bytesCopied, copyErr := copyRegularFile(source, dest)
	if copyErr != nil {
		return nil, copyErr
	}

	return textResult(map[string]any{
		"source":       source,
		"destination":  dest,
		"bytes_copied": bytesCopied,
	})
}

// copyRegularFile copies src to dst via io.Copy + a buffered writer.
// The file mode is preserved (best-effort, masked to the writable
// owner bits so the copy is never more permissive than the source).
func copyRegularFile(src, dst string) (int64, error) {
	srcFile, err := os.Open(src)
	if err != nil {
		return 0, fmt.Errorf("open source: %w", err)
	}

	defer func() { _ = srcFile.Close() }()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat source: %w", err)
	}

	if srcInfo.IsDir() {
		return 0, fmt.Errorf(
			"copy_file: %q is a directory; copy_file copies regular files only",
			src,
		)
	}

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcInfo.Mode()&0o777)
	if err != nil {
		return 0, fmt.Errorf("open destination: %w", err)
	}

	defer func() { _ = dstFile.Close() }()

	buf := bytes.NewBuffer([]byte(nil))

	copied, copyErr := io.Copy(buf, srcFile)
	if copyErr != nil {
		return 0, fmt.Errorf("copy: %w", copyErr)
	}

	written, writeErr := dstFile.Write(buf.Bytes())
	if writeErr != nil {
		return int64(written), fmt.Errorf("write destination: %w", writeErr)
	}

	if int64(written) != copied {
		return int64(written), fmt.Errorf(
			"copy: wrote %d bytes, source yielded %d", written, copied,
		)
	}

	return int64(written), nil
}

// handleDeleteFile returns the mcp.ToolHandler for the delete_file
// tool. The handler refuses to delete non-empty directories — the
// LLM must walk the tree first and delete contents one entry at a
// time.
func handleDeleteFile(cfg *config) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return deleteFile(req, cfg)
	}
}

// deleteFile parses the request, refuses to delete non-empty
// directories, and otherwise removes the path. The whole decision
// tree runs under withPathLock so a parallel create_directory or
// write_file against the same path cannot interleave between the
// stat and the remove (which would otherwise leave the LLM with
// a stat-driven "no, this path does not exist" error followed by
// a successful creation on the next call).
func deleteFile(req *mcp.CallToolRequest, cfg *config) (*mcp.CallToolResult, error) {
	var args pathArgs

	err := decodeArgs(req, &args)
	if err != nil {
		return nil, fmt.Errorf("parse delete_file args: %w", err)
	}

	resolved, err := guardPath(cfg, args.Path)
	if err != nil {
		return nil, err
	}

	var result *mcp.CallToolResult

	lockErr := withPathLock(resolved, func() error {
		var innerErr error

		result, innerErr = deleteFileLocked(resolved)

		return innerErr
	})
	if lockErr != nil {
		return nil, lockErr
	}

	return result, nil
}

// deleteFileLocked is the delete_file work that runs under the
// per-path write lock. The caller has already guarded the path;
// the only check needed here is the directory-vs-leaf split and
// the existing-target refusal. Pulled out of the closure to keep
// deleteFile below gocognit's complexity threshold.
func deleteFileLocked(resolved string) (*mcp.CallToolResult, error) {
	info, statErr := os.Stat(resolved)
	if statErr != nil {
		return nil, fmt.Errorf("stat %q: %w", resolved, statErr)
	}

	if info.IsDir() {
		return deleteDirectoryIfEmpty(resolved)
	}

	return removePath(resolved)
}

// deleteDirectoryIfEmpty refuses to remove a directory that still
// has children. The LLM must walk the tree first and delete
// contents one entry at a time; this is the safety contract that
// prevents accidental recursive deletion.
func deleteDirectoryIfEmpty(resolved string) (*mcp.CallToolResult, error) {
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", resolved, err)
	}

	if len(entries) > 0 {
		return nil, fmt.Errorf(
			"delete_file: %q is a non-empty directory (%d entries); remove contents first",
			resolved, len(entries))
	}

	return removePath(resolved)
}

// removePath is the shared tail for the leaf/directory-empty cases.
func removePath(resolved string) (*mcp.CallToolResult, error) {
	rmErr := os.Remove(resolved)
	if rmErr != nil {
		return nil, fmt.Errorf("remove %q: %w", resolved, rmErr)
	}

	return textResult(map[string]any{"path": resolved})
}

// handleSearchFiles returns the mcp.ToolHandler for the search_files
// tool. The handler walks root up to maxDepth and collects every
// path whose relative form matches the glob pattern.
func handleSearchFiles(cfg *config) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return searchFiles(req, cfg)
	}
}

// searchFiles parses the request, asserts the root is a directory,
// and walks it up to maxDepth returning matching paths.
func searchFiles(req *mcp.CallToolRequest, cfg *config) (*mcp.CallToolResult, error) {
	var args searchFilesArgs

	err := decodeArgs(req, &args)
	if err != nil {
		return nil, fmt.Errorf("parse search_files args: %w", err)
	}

	resolved, err := guardPath(cfg, args.Root)
	if err != nil {
		return nil, err
	}

	info, statErr := os.Stat(resolved)
	if statErr != nil {
		return nil, fmt.Errorf("stat %q: %w", resolved, statErr)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("search_files: %q is not a directory", resolved)
	}

	maxDepth := resolveMaxDepth(args.MaxDepth)

	matches, walkErr := walkAndMatch(resolved, args.Pattern, maxDepth)
	if walkErr != nil {
		return nil, walkErr
	}

	return textResult(map[string]any{"matches": matches})
}

// matchResult is the per-match shape returned by search_files.
type matchResult struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

// walkAndMatch walks dir up to maxDepth and collects every entry
// whose basename matches the glob pattern.
func walkAndMatch(dir, pattern string, maxDepth int) ([]matchResult, error) {
	// matches is initialized as a non-nil empty slice so the JSON
	// envelope for "no matches" is [] (an empty array), not null.
	// json.Marshal of a typed-nil slice serializes as null, which the
	// LLM cannot distinguish from an internal error.
	state := &walkState{
		dir:      dir,
		pattern:  pattern,
		maxDepth: maxDepth,
		matches:  []matchResult{},
	}

	walkErr := filepath.WalkDir(dir, state.visit)
	if walkErr != nil {
		return nil, fmt.Errorf("walk %q: %w", dir, walkErr)
	}

	return state.matches, nil
}

// walkState carries the per-walk context so the WalkDir visitor
// stays small enough to keep walkAndMatch's cognitive complexity
// below the lint threshold.
type walkState struct {
	dir      string
	pattern  string
	maxDepth int
	matches  []matchResult
}

// visit processes one filepath.WalkDir entry. The boolean return
// tells the caller whether to SkipDir; the error is propagated
// straight through to WalkDir.
func (w *walkState) visit(path string, entry os.DirEntry, err error) error {
	if err != nil {
		return err
	}

	rel, relErr := filepath.Rel(w.dir, path)
	if relErr != nil {
		return fmt.Errorf("relative path: %w", relErr)
	}

	if rel != currentDirRef {
		appended, matchErr := w.appendIfMatches(rel, entry, path)
		if matchErr != nil {
			return matchErr
		}

		w.matches = appended
	}

	if w.shouldSkipDir(entry, rel) {
		return filepath.SkipDir
	}

	return nil
}

// appendIfMatches appends a matchResult when rel matches pattern.
// The returned slice may be the same as w.matches when the entry
// does not match.
func (w *walkState) appendIfMatches(
	rel string,
	entry os.DirEntry,
	path string,
) ([]matchResult, error) {
	matched, err := matchSearchPattern(rel, w.pattern)
	if err != nil {
		return w.matches, err
	}

	if !matched {
		return w.matches, nil
	}

	info, infoErr := entry.Info()
	if infoErr != nil {
		return w.matches, fmt.Errorf("entry info: %w", infoErr)
	}

	return append(w.matches, matchResult{
		Path:      path,
		SizeBytes: info.Size(),
	}), nil
}

// shouldSkipDir tells filepath.WalkDir to stop descending into a
// directory once the depth cap is reached. The caller passes the
// already-computed relative path so this helper does not duplicate
// filepath.Rel.
func (w *walkState) shouldSkipDir(entry os.DirEntry, rel string) bool {
	if !entry.IsDir() {
		return false
	}

	if rel == currentDirRef {
		return false
	}

	depth := strings.Count(rel, string(filepath.Separator)) + 1

	return depth >= w.maxDepth
}

// handleGetFileInfo returns the mcp.ToolHandler for the get_file_info
// tool.
func handleGetFileInfo(cfg *config) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return getFileInfo(req, cfg)
	}
}

// getFileInfo parses the request and returns a JSON-friendly
// description of the path (size, mode, modification time, is_dir).
func getFileInfo(req *mcp.CallToolRequest, cfg *config) (*mcp.CallToolResult, error) {
	var args pathArgs

	err := decodeArgs(req, &args)
	if err != nil {
		return nil, fmt.Errorf("parse get_file_info args: %w", err)
	}

	resolved, err := guardPath(cfg, args.Path)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", resolved, err)
	}

	return textResult(map[string]any{
		"path":        resolved,
		"size_bytes":  info.Size(),
		"mode":        fmt.Sprintf("%04o", info.Mode().Perm()),
		"modified_at": info.ModTime().UTC().Format(time.RFC3339),
		"is_dir":      info.IsDir(),
	})
}

// handleGrep returns the mcp.ToolHandler for the grep tool. The
// handler decodes args, runs the parallel regex search under the
// resolved root, and returns matches with optional context lines.
func handleGrep(cfg *config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return grepSearch(ctx, req, cfg)
	}
}

// grepSearch is the body of the grep handler. Kept separate from
// handleGrep so the per-call arg-parsing can be tested directly
// without a wrapping closure.
func grepSearch(
	ctx context.Context, req *mcp.CallToolRequest, cfg *config,
) (*mcp.CallToolResult, error) {
	var args grepArgs

	err := decodeArgs(req, &args)
	if err != nil {
		return nil, fmt.Errorf("parse grep args: %w", err)
	}

	resolved, err := guardPath(cfg, args.Root)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", resolved, err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("grep: %q is not a directory", resolved)
	}

	opts := buildGrepOptions(&args)

	result, err := grepCore(ctx, resolved, args.Pattern, opts)
	if err != nil {
		return nil, fmt.Errorf("grep: %w", err)
	}

	return textResult(map[string]any{
		"matches":              result.matches,
		"truncated":            result.truncated,
		"total_files_searched": result.totalFilesSearched,
		"total_matches":        result.totalMatches,
	})
}

// buildGrepOptions translates the request args into the internal
// grepOptions struct, applying package defaults for fields the
// caller did not set.
func buildGrepOptions(args *grepArgs) grepOptions {
	return grepOptions{
		caseSensitive:     args.CaseSensitive,
		includePattern:    args.IncludePattern,
		maxDepth:          resolveMaxDepth(args.MaxDepth),
		maxResults:        resolveMaxResults(args.MaxResults),
		contextLines:      resolveContextLines(args.ContextLines),
		maxLineBytes:      resolveMaxLineBytes(args.MaxLineBytes),
		binarySniffBytes:  defaultBinarySniffBytes,
		useGitignore:      derefBool(args.UseGitignore, true),
		respectDefIgnores: derefBool(args.RespectDefaultIgnores, true),
		literal:           args.Literal,
	}
}

// resolveMaxResults applies the default cap when the caller did
// not supply a value, and clamps negative inputs to the default.
func resolveMaxResults(requested int) int {
	if requested <= 0 {
		return defaultMaxResults
	}

	return requested
}

// resolveContextLines returns the caller's value, defaulting to 0
// (matches only) when not set.
func resolveContextLines(requested int) int {
	if requested < 0 {
		return defaultContextLines
	}

	return requested
}

// resolveMaxLineBytes returns the per-line cap, defaulting when
// the caller did not set one or set a non-positive value.
func resolveMaxLineBytes(requested int) int {
	if requested <= 0 {
		return defaultMaxLineBytes
	}

	return requested
}

// derefBool returns *p when non-nil, fallback otherwise. Used to
// default the pointer-typed args to true when the caller omits the
// field.
func derefBool(ptr *bool, fallback bool) bool {
	if ptr == nil {
		return fallback
	}

	return *ptr
}
