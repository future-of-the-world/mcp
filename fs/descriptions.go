// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package fs

// Per-tool description suffixes. Kept as named consts (rather than
// inline string literals) so the lll line-length rule is happy and
// the per-tool phrasing is editable in one place. The shell-source
// pattern uses a single multiline string per tool; the woodpecker
// pattern concatenates a shared narrative with a per-tool suffix —
// the fs source uses the per-tool-only style because there is no
// shared narrative across the twelve tools (each stands on its own
// contract).
const (
	listAllowedDirectoriesDescription = "\n\nList the directories the " +
		"fs source is configured to allow. Call this first when you " +
		"do not know the operator's allowed roots — every other tool " +
		"rejects paths outside this set."

	readFileDescription = "\n\nRead the contents of a file. The " +
		"return value is a UTF-8 string for text files, and a 'b64:" +
		"<base64>' string for binary content. Files larger than " +
		"connect.max_read_bytes are rejected with a clear error " +
		"rather than silently truncated." +
		"\n\nBoth input and output support optional line-range " +
		"fields on UTF-8 text files: pass `start_line` (1-indexed, " +
		"inclusive) and / or `end_line` (1-indexed, inclusive) to " +
		"fetch a contiguous slice of lines. When both are omitted " +
		"the whole file is returned. If `end_line` exceeds the " +
		"file's line count it is silently clamped to the last " +
		"line; if `start_line` exceeds the line count the call " +
		"fails with a clear error. Text-file output adds " +
		"`returned_bytes`, `total_lines`, and `returned_lines` so " +
		"the caller knows how much came back; binary files keep " +
		"their existing `{content, size_bytes, is_binary}` shape " +
		"byte-for-byte and reject any line-range argument." +
		"\n\nThe server serializes concurrent calls against the " +
		"same path, but the server cannot know that two " +
		"disjoint-looking inputs in one tool-call turn remain " +
		"disjoint once earlier calls land — serialize batching " +
		"at the call site."

	writeFileDescription = "\n\nCreate or overwrite a file. Parent " +
		"directories are created on demand. Set encoding to 'base64' " +
		"to write binary content; otherwise the content is treated as " +
		"UTF-8 text." +
		"\n\nThe server serializes concurrent calls against the " +
		"same path, but the server cannot know that two " +
		"disjoint-looking inputs in one tool-call turn remain " +
		"disjoint once earlier calls land — serialize batching " +
		"at the call site."

	editFileDescription = "\n\nApply a single find/replace edit to a " +
		"text file. The request is rejected if old_text is not present " +
		"exactly once — silent partial edits are a worse failure mode " +
		"than a refused call." +
		"\n\nThe server serializes concurrent calls against the " +
		"same path, but the server cannot know that two " +
		"disjoint-looking inputs in one tool-call turn remain " +
		"disjoint once earlier calls land — serialize batching " +
		"at the call site."

	createDirectoryDescription = "\n\nCreate a directory (mkdir -p " +
		"semantics: intermediate directories are created on demand). " +
		"Refuses to overwrite an existing regular file at the target " +
		"path; directories that already exist are accepted as no-ops." +
		"\n\nThe server serializes concurrent calls against the " +
		"same path, but the server cannot know that two " +
		"disjoint-looking inputs in one tool-call turn remain " +
		"disjoint once earlier calls land — serialize batching " +
		"at the call site."

	listDirectoryDescription = "\n\nList the immediate children of a " +
		"directory. For a recursive view, use directory_tree instead."

	directoryTreeDescription = "\n\nRecursively describe a directory " +
		"as a JSON tree. Depth is bounded by max_depth (default 8) so " +
		"the JSON envelope cannot be blown by a huge repo."

	moveFileDescription = "\n\nRename or move a file or directory. " +
		"Both source and destination must live inside the configured " +
		"allowed_paths. Cross-root moves are rejected; use copy + " +
		"delete when you genuinely need to cross roots." +
		"\n\nThe server serializes concurrent calls against the " +
		"same path, but the server cannot know that two " +
		"disjoint-looking inputs in one tool-call turn remain " +
		"disjoint once earlier calls land — serialize batching " +
		"at the call site."

	copyFileDescription = "\n\nCopy a regular file. Both source and " +
		"destination must live inside the configured allowed_paths. " +
		"Refuses to copy a directory — make the destination directory " +
		"first, then copy individual files into it." +
		"\n\nThe server serializes concurrent calls against the " +
		"same path, but the server cannot know that two " +
		"disjoint-looking inputs in one tool-call turn remain " +
		"disjoint once earlier calls land — serialize batching " +
		"at the call site."

	deleteFileDescription = "\n\nRemove a file or empty directory. " +
		"Refuses to delete non-empty directories — walk the tree " +
		"first to enumerate and remove contents one entry at a time. " +
		"This is intentional: a recursive delete is a destructive " +
		"operation the LLM should walk deliberately." +
		"\n\nThe server serializes concurrent calls against the " +
		"same path, but the server cannot know that two " +
		"disjoint-looking inputs in one tool-call turn remain " +
		"disjoint once earlier calls land — serialize batching " +
		"at the call site."

	searchFilesDescription = "\n\nRecursive glob-style search under a " +
		"directory. The pattern is matched against each path relative " +
		"to root using filepath.Match semantics: '*' matches any " +
		"sequence, '?' matches a single character."

	getFileInfoDescription = "\n\nStat a path and return its size, " +
		"mode bits, last-modification time, and directory-or-file kind."

	grepDescription = "\n\nRegex content search over files reachable " +
		"from the configured allowed_paths. Patterns use Go RE2 syntax; " +
		"the longest required literal substring is extracted as a fast " +
		"prefilter, so patterns like 'foo.*bar' are searched at " +
		"near-literal speed. Set literal=true to skip regex compilation " +
		"entirely — the whole pattern is then matched as a literal " +
		"substring (regex metacharacters like '.', '*', '[' are treated " +
		"as ordinary characters). Surrounding context lines can be " +
		"requested via context_lines. Matches are returned in " +
		"(path, line) order; a line that is both a match and context " +
		"is emitted once with type='match'. By default, .gitignore " +
		"files in root and parents are honored, and a hardcoded list " +
		"of noise directories (.git, node_modules, vendor, etc.) plus " +
		"hidden files are skipped — set " +
		"respect_default_ignores=false to search them anyway."
)
