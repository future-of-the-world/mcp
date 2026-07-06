// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package fs implements an MCP source that exposes a path-confined
// set of file-operation tools to the LLM. Connect decodes the
// source's `connect:` map (allowed_paths, max_read_bytes,
// max_write_bytes, follow_symlinks) and returns thirteen tools:
//
//	list_allowed_directories  (read-only, idempotent)
//	read_file                  (read-only, idempotent)
//	write_file                 (write, destructive)
//	edit_file                  (write, destructive)
//	create_directory           (write, idempotent)
//	list_directory             (read-only, idempotent)
//	directory_tree             (read-only, idempotent)
//	move_file                  (write, destructive)
//	copy_file                  (write, idempotent)
//	delete_file                (write, destructive)
//	search_files               (read-only, idempotent)
//	get_file_info              (read-only, idempotent)
//	grep                       (read-only, idempotent)
//
// Security model: this source can read, write, and delete files on
// disk. The MVP trusts the operator (same trust model as the postgres
// source for SQL and the shell source for shell execution) but
// narrows the blast radius to a fixed set of paths the operator
// chooses upfront via connect.allowed_paths. The LLM cannot escape
// the allowlist regardless of how it phrases a path: every tool
// argument that names a path is routed through resolveAndGuard,
// which rejects `..` traversal, symlink escapes, and any path that
// does not normalize to a child of one of the configured roots.
//
// Out of scope: arbitrary shell execution (use the shell source),
// HTTP fetch (use the http source), and networked filesystems (S3,
// SSH, NFS). Operators that need those mount them locally and add
// the mount point to allowed_paths.
package fs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "embed"

	"go.amidman.dev/mcp/decode"
	"go.amidman.dev/mcp/tool"
)

// --- Embedded JSON Schemas ---

//go:embed schemas/list_allowed_directories_output.json
var listAllowedDirectoriesOutput json.RawMessage

//go:embed schemas/read_file_input.json
var readFileInput json.RawMessage

//go:embed schemas/read_file_output.json
var readFileOutput json.RawMessage

//go:embed schemas/write_file_input.json
var writeFileInput json.RawMessage

//go:embed schemas/write_file_output.json
var writeFileOutput json.RawMessage

//go:embed schemas/edit_file_input.json
var editFileInput json.RawMessage

//go:embed schemas/edit_file_output.json
var editFileOutput json.RawMessage

//go:embed schemas/create_directory_input.json
var createDirectoryInput json.RawMessage

//go:embed schemas/create_directory_output.json
var createDirectoryOutput json.RawMessage

//go:embed schemas/list_directory_input.json
var listDirectoryInput json.RawMessage

//go:embed schemas/list_directory_output.json
var listDirectoryOutput json.RawMessage

//go:embed schemas/directory_tree_input.json
var directoryTreeInput json.RawMessage

//go:embed schemas/directory_tree_output.json
var directoryTreeOutput json.RawMessage

//go:embed schemas/move_file_input.json
var moveFileInput json.RawMessage

//go:embed schemas/move_file_output.json
var moveFileOutput json.RawMessage

//go:embed schemas/copy_file_input.json
var copyFileInput json.RawMessage

//go:embed schemas/copy_file_output.json
var copyFileOutput json.RawMessage

//go:embed schemas/delete_file_input.json
var deleteFileInput json.RawMessage

//go:embed schemas/delete_file_output.json
var deleteFileOutput json.RawMessage

//go:embed schemas/search_files_input.json
var searchFilesInput json.RawMessage

//go:embed schemas/search_files_output.json
var searchFilesOutput json.RawMessage

//go:embed schemas/get_file_info_input.json
var getFileInfoInput json.RawMessage

//go:embed schemas/get_file_info_output.json
var getFileInfoOutput json.RawMessage

//go:embed schemas/grep_input.json
var grepInput json.RawMessage

//go:embed schemas/grep_output.json
var grepOutput json.RawMessage

// --- Constants and defaults ---

// defaultMaxReadBytes caps the size of files returned by read_file
// when connect.max_read_bytes is absent or invalid.
const defaultMaxReadBytes = 1 << 20 // 1 MiB

// defaultMaxWriteBytes caps the size of content accepted by
// write_file and edit_file when connect.max_write_bytes is absent or
// invalid.
const defaultMaxWriteBytes = 10 << 20 // 10 MiB

// defaultMaxTreeDepth bounds directory_tree and search_files
// recursion so the JSON envelope cannot be blown by a giant repo.
const defaultMaxTreeDepth = 8

// --- Sentinel errors (config / decode) ---

// errMaxReadNegative is returned when connect.max_read_bytes is a
// negative number. Zero is allowed (and means "use the default").
var errMaxReadNegative = errors.New("fs: connect.max_read_bytes must be non-negative")

// errMaxWriteNegative is returned when connect.max_write_bytes is a
// negative number. Zero is allowed (and means "use the default").
var errMaxWriteNegative = errors.New("fs: connect.max_write_bytes must be non-negative")

// errAllowedPathsWrongType is returned when connect.allowed_paths is
// set to a non-list value (e.g. a string).
var errAllowedPathsWrongType = errors.New("fs: connect.allowed_paths must be a list of strings")

// --- Configuration decoding ---

// config holds the decoded `connect:` map for an fs source.
// AllowedPaths is required and must contain at least one absolute
// path. The remaining fields have defaults applied by validate or by
// the handler at call time.
type config struct {
	AllowedPaths   []string
	MaxReadBytes   int
	MaxWriteBytes  int
	FollowSymlinks bool
}

// decodeConnect decodes the source's `connect:` map into a config.
// Scalar string fields are decoded through decode.AsString so
// YAML-natural values (numbers, bools, null) are accepted and
// stringified; non-scalar values produce a wrapped decode.ErrWrongType
// error so genuine config bugs surface as a clear message rather than
// a silent "field is empty" downstream.
func decodeConnect(connect map[string]any) (config, error) {
	var cfg config

	paths, err := decodeAllowedPaths(connect)
	if err != nil {
		return cfg, err
	}

	cfg.AllowedPaths = paths

	maxRead, decodeErr := decodeInt(connect, "max_read_bytes")
	if decodeErr != nil {
		return cfg, fmt.Errorf("connect.max_read_bytes: %w", decodeErr)
	}

	cfg.MaxReadBytes = maxRead

	maxWrite, decodeErr := decodeInt(connect, "max_write_bytes")
	if decodeErr != nil {
		return cfg, fmt.Errorf("connect.max_write_bytes: %w", decodeErr)
	}

	cfg.MaxWriteBytes = maxWrite

	follow, err := decode.AsString(connect["follow_symlinks"])
	switch {
	case err == nil:
		switch follow {
		case "true", "True", "TRUE":
			cfg.FollowSymlinks = true

		case "false", "False", "FALSE":
			cfg.FollowSymlinks = false

		default:
			return cfg, fmt.Errorf("connect.follow_symlinks: must be true or false, got %q", follow)
		}

	case errors.Is(err, decode.ErrNotSet):
		// follow_symlinks is optional; defaults to false.

	default:
		return cfg, fmt.Errorf("connect.follow_symlinks: %w", err)
	}

	return cfg, nil
}

// decodeAllowedPaths decodes the connect.allowed_paths slice. Each
// element is coerced through decode.AsString so YAML-natural values
// (numbers, bools) are accepted and stringified; non-scalar values
// produce a wrapped error so a misconfigured value surfaces as a
// clear message rather than silently dropping the slice.
func decodeAllowedPaths(connect map[string]any) ([]string, error) {
	raw, ok := connect["allowed_paths"]
	if !ok {
		return nil, nil
	}

	switch val := raw.(type) {
	case []string:
		return val, nil

	case []any:
		out := make([]string, 0, len(val))

		for index, item := range val {
			str, err := decode.AsString(item)
			if err != nil {
				return nil, fmt.Errorf("connect.allowed_paths[%d]: %w", index, err)
			}

			out = append(out, str)
		}

		return out, nil

	case nil:
		return nil, nil

	default:
		return nil, fmt.Errorf("%w: got %T", errAllowedPathsWrongType, raw)
	}
}

// decodeInt decodes an integer field from connect. Accepts int, int64,
// and float64 (YAML and JSON both produce float64 for non-typed
// numerics); other types produce an error.
func decodeInt(connect map[string]any, key string) (int, error) {
	raw, ok := connect[key]
	if !ok {
		return 0, nil
	}

	switch val := raw.(type) {
	case int:
		return val, nil

	case int64:
		return int(val), nil

	case float64:
		return int(val), nil

	default:
		return 0, fmt.Errorf("must be an integer, got %T", raw)
	}
}

// validate checks that the decoded config is usable: AllowedPaths must
// contain at least one absolute path; MaxReadBytes and MaxWriteBytes
// must be non-negative. The zero values for the optional fields are
// not flagged here — defaults are applied at call time.
func (c *config) validate() error {
	if len(c.AllowedPaths) == 0 {
		return errAllowedPathsEmpty
	}

	if c.MaxReadBytes < 0 {
		return errMaxReadNegative
	}

	if c.MaxWriteBytes < 0 {
		return errMaxWriteNegative
	}

	return nil
}

// resolveMaxReadBytes returns the configured byte cap, falling back
// to the package default when the operator did not set one.
func (c *config) resolveMaxReadBytes() int {
	if c.MaxReadBytes <= 0 {
		return defaultMaxReadBytes
	}

	return c.MaxReadBytes
}

// resolveMaxWriteBytes returns the configured byte cap for write
// operations, falling back to the package default when the operator
// did not set one.
func (c *config) resolveMaxWriteBytes() int {
	if c.MaxWriteBytes <= 0 {
		return defaultMaxWriteBytes
	}

	return c.MaxWriteBytes
}

// --- Tool annotations ---

// readOnlyAnnotations marks the read-only tools (read_file,
// list_directory, directory_tree, search_files, get_file_info,
// list_allowed_directories). ReadOnlyHint=true is the signal hosts
// use to gate tool invocation on whether the user is in a "view
// only" mode.
func readOnlyAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    true,
		DestructiveHint: nilBoolPtr(),
		IdempotentHint:  true,
		OpenWorldHint:   newFalse(),
	}
}

// destructiveWriteAnnotations marks the write tools that mutate the
// filesystem in ways the LLM cannot undo from outside the source
// (write_file, edit_file, move_file, delete_file).
func destructiveWriteAnnotations() *mcp.ToolAnnotations {
	destructive := true

	return &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    false,
		DestructiveHint: &destructive,
		IdempotentHint:  false,
		OpenWorldHint:   newFalse(),
	}
}

// safeWriteAnnotations marks the write tools that are non-destructive
// from the operator's perspective: create_directory and copy_file
// never delete existing content, so a wrong call can be undone by
// removing the new directory / file.
func safeWriteAnnotations() *mcp.ToolAnnotations {
	destructive := false

	return &mcp.ToolAnnotations{
		Title:           "",
		ReadOnlyHint:    false,
		DestructiveHint: &destructive,
		IdempotentHint:  true,
		OpenWorldHint:   newFalse(),
	}
}

// newFalse returns a pointer to the bool literal false — shorthand
// so the annotation helpers above stay readable.
func newFalse() *bool {
	value := false

	return &value
}

// nilBoolPtr returns a typed-nil *bool. Used for ToolAnnotations
// fields where untyped nil trips revive's ruleguard on "typed nil".
func nilBoolPtr() *bool {
	return (*bool)(nil)
}

// --- Connect entry point ---

// Connect decodes the source's `connect:` map, validates it, and
// returns the twelve file-operation tools. Every tool advertises
// ReadOnlyHint and DestructiveHint so hosts that gate tool invocation
// on these hints treat FS operations correctly. The tools are
// registered in a stable order so dispatcher unit tests can assert
// on the names without depending on map iteration.
func Connect(
	_ context.Context,
	connect map[string]any,
	_ ...tool.Option,
) (tool.Response, error) {
	cfg, err := decodeConnect(connect)
	if err != nil {
		return tool.Response{}, fmt.Errorf("fs: decode: %w", err)
	}

	validateErr := cfg.validate()
	if validateErr != nil {
		return tool.Response{}, fmt.Errorf("fs: validate: %w", validateErr)
	}

	readOnly := readOnlyAnnotations()
	destructive := destructiveWriteAnnotations()
	safe := safeWriteAnnotations()

	return tool.Response{
		Tools: []tool.Tool{
			{
				Tool: &mcp.Tool{
					Name:         "list_allowed_directories",
					Description:  listAllowedDirectoriesDescription,
					InputSchema:  emptyObjectSchema,
					OutputSchema: listAllowedDirectoriesOutput,
					Annotations:  readOnly,
				},
				Handler: handleListAllowedDirectories(&cfg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "read_file",
					Description:  readFileDescription,
					InputSchema:  readFileInput,
					OutputSchema: readFileOutput,
					Annotations:  readOnly,
				},
				Handler: handleReadFile(&cfg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "write_file",
					Description:  writeFileDescription,
					InputSchema:  writeFileInput,
					OutputSchema: writeFileOutput,
					Annotations:  destructive,
				},
				Handler: handleWriteFile(&cfg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "edit_file",
					Description:  editFileDescription,
					InputSchema:  editFileInput,
					OutputSchema: editFileOutput,
					Annotations:  destructive,
				},
				Handler: handleEditFile(&cfg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "create_directory",
					Description:  createDirectoryDescription,
					InputSchema:  createDirectoryInput,
					OutputSchema: createDirectoryOutput,
					Annotations:  safe,
				},
				Handler: handleCreateDirectory(&cfg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "list_directory",
					Description:  listDirectoryDescription,
					InputSchema:  listDirectoryInput,
					OutputSchema: listDirectoryOutput,
					Annotations:  readOnly,
				},
				Handler: handleListDirectory(&cfg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "directory_tree",
					Description:  directoryTreeDescription,
					InputSchema:  directoryTreeInput,
					OutputSchema: directoryTreeOutput,
					Annotations:  readOnly,
				},
				Handler: handleDirectoryTree(&cfg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "move_file",
					Description:  moveFileDescription,
					InputSchema:  moveFileInput,
					OutputSchema: moveFileOutput,
					Annotations:  destructive,
				},
				Handler: handleMoveFile(&cfg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "copy_file",
					Description:  copyFileDescription,
					InputSchema:  copyFileInput,
					OutputSchema: copyFileOutput,
					Annotations:  safe,
				},
				Handler: handleCopyFile(&cfg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "delete_file",
					Description:  deleteFileDescription,
					InputSchema:  deleteFileInput,
					OutputSchema: deleteFileOutput,
					Annotations:  destructive,
				},
				Handler: handleDeleteFile(&cfg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "search_files",
					Description:  searchFilesDescription,
					InputSchema:  searchFilesInput,
					OutputSchema: searchFilesOutput,
					Annotations:  readOnly,
				},
				Handler: handleSearchFiles(&cfg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "get_file_info",
					Description:  getFileInfoDescription,
					InputSchema:  getFileInfoInput,
					OutputSchema: getFileInfoOutput,
					Annotations:  readOnly,
				},
				Handler: handleGetFileInfo(&cfg),
			},
			{
				Tool: &mcp.Tool{
					Name:         "grep",
					Description:  grepDescription,
					InputSchema:  grepInput,
					OutputSchema: grepOutput,
					Annotations:  readOnly,
				},
				Handler: handleGrep(&cfg),
			},
		},
	}, nil
}

// emptyObjectSchema is the input schema for tools that take no
// arguments (list_allowed_directories). Defined here so Connect
// reads as a flat list rather than a per-tool constructor.
var emptyObjectSchema = json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`)
