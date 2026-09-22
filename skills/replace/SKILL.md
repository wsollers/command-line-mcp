---
name: shellmcp-replace
description: Use when you need to find-and-replace text (literal or regex) across every file in a directory tree matching a name pattern, through the shellmcp MCP server's replace tool.
---

# shellmcp: replace

Applies a literal or regex substitution across every file under a directory (optionally filtered by a name glob), without you authoring any predicate/action JSON. Always call with `dry_run: true` first to see what would change before it actually writes anything.

## Arguments

- `root` (required): directory to search, relative to the primary allowed root or absolute inside any allowed root.
- `name_glob` (optional): a bash-like glob pattern (`**` for any depth, e.g. `src/**/*.rs`) applied to each candidate file's path *relative to `root`*. Omit to consider every file under `root`.
- `search` (required): text to find in each matched file — or, if `is_regex` is true, a ripgrep-compatible regex.
- `replace` (required): replacement text. In regex mode this can reference capture groups as `$1`, `${name}`, etc. (Go's `regexp.Expand` syntax — not identical to every other regex flavor's replacement syntax, so double-check group references in `dry_run` output before writing).
- `is_regex` (optional, default false): treat `search` as a regex instead of literal text.
- `dry_run` (optional, default false): **set this to `true` on your first call for any multi-file replace.** Reports exactly what would change — same result shape either way — without writing a single byte.
- `max_results` (optional, default 1000): cap on how many candidate files (post `name_glob` filter) are considered. If the tree has more, `truncated` is `true` in the result — no continuation token yet, so narrow `name_glob`/`root` if you hit this on a call that isn't `dry_run`, since a truncated non-dry-run replace only touches part of the matching set.

## Result

`{"root", "dry_run", "files": [{"path", "replacements"}], "files_changed", "total_replacements", "truncated"}`. `files` only lists files where at least one replacement happened (or would happen, under `dry_run`) — a matched-by-glob file with zero occurrences of `search` doesn't appear.

## Before you call it

- **Always preview first.** Call with `dry_run: true`, look at `files`/`total_replacements`, and only then repeat the call with `dry_run` false (or omitted) once you're sure it's what you want. There's no undo.
- A file that isn't valid UTF-8 is silently skipped, never touched — this tool has no binary-safe replace mode. If you need to change bytes in a binary file, that's outside this tool's scope.
- In regex mode, `search` is compiled the same way `find`'s `content_regex` and `exec`'s ripgrep-compatible matching are — see `internal/rgx`'s documented syntax support (translated `\d`/`\s`/`\w`, some constructs explicitly rejected rather than silently reinterpreted) if a pattern you expect to work doesn't compile.
- This is not atomic across files: if it fails partway through a large replace, files processed before the failure are already changed and the rest aren't. `dry_run` is how you avoid surprises here, not a rollback mechanism.
- If you're just renaming/moving a single file rather than changing its content, this isn't the tool — use `copy_file`/`rm`, or a plain `write_file` for a single known file.

## Errors

`{"ok": false, "error": {"code", "message", "path", "details"}}` on failure. Notable codes: `INVALID_ARGUMENT` (empty `search`), `INVALID_GLOB` (malformed `name_glob`), `INVALID_REGEX` / `UNSUPPORTED_REGEX_SYNTAX` (malformed or untranslatable `search` pattern in regex mode), `NOT_A_DIRECTORY` (`root` resolves to a file), `PATH_OUTSIDE_ROOT`.

## Sandboxing

Same boundary as every other tool here: `root` must resolve inside a currently allowed root (`list_allowed_dirs` shows what's allowed), and every symlink encountered while walking is independently re-validated — one pointing outside every allowed root is skipped rather than followed, so a write can never land outside the sandbox even via a symlinked path discovered mid-walk.
