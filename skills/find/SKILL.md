---
name: shellmcp-find
description: Use when you need to locate files by name pattern and/or content, across a directory tree, through the shellmcp MCP server's find tool.
---

# shellmcp: find

Searches a directory tree for files matching a name pattern and/or containing a regex match, without you having to author any predicate JSON. Only matches files, never directories.

## Arguments

- `root` (required): directory to search, relative to the primary allowed root or absolute inside any allowed root.
- `name_glob` (optional): a bash-like glob pattern (see `internal/glob`'s rules — `**` matches any number of path segments, e.g. `**/*.go` matches at any depth) applied to each candidate file's path *relative to `root`*. Omit to consider every file.
- `content_regex` (optional): a ripgrep-compatible regex (see the `shellmcp-exec`-adjacent `internal/rgx` behavior, or just: standard regex syntax, Unicode-aware `\d`/`\s`/`\w` by default) a file's content must contain at least one match of. A file that isn't valid UTF-8 never matches this — it's silently excluded, not an error.
- `max_results` (optional, default 1000): stop after this many matches. If the tree has more, the result's `truncated` field is `true` — there's no continuation token yet, so narrow `name_glob`/`content_regex` or `root` further if you hit this.

Provide `name_glob`, `content_regex`, both, or neither (neither just lists every non-hidden file under `root`).

## Result

`{"root", "matches": [{"path", "size_bytes"}], "count", "truncated"}`. `path` in each match is relative to `root`, matching what you'd pass to `read_file`/`write_file`/etc. joined onto that same root.

## Before you call it

- This only ever returns files, never directories — you don't need `name_glob` to exclude directories explicitly.
- Hidden (dot-prefixed) files and directories are always skipped, and a symlinked directory is never descended into — there's no option to change either yet.
- If you already know the exact path, use `ls` or `read_file` directly instead — `find` is for "which files match this pattern," not single-file lookups.
- A `content_regex` match requires reading every file it might apply to — for a huge tree, narrow with `name_glob` first if you can, so fewer files need their content checked.

## Errors

`{"ok": false, "error": {"code", "message", "path", "details"}}` on failure. Notable codes: `INVALID_GLOB` (malformed `name_glob`), `INVALID_REGEX` / `UNSUPPORTED_REGEX_SYNTAX` (malformed or untranslatable `content_regex` — see the regex behavior notes in `internal/rgx`'s docs if you hit the latter), `NOT_A_DIRECTORY` (`root` resolves to a file), `PATH_OUTSIDE_ROOT`.

## Sandboxing

`root` must resolve inside a currently allowed root, same as every other tool here (`list_allowed_dirs` shows what's allowed). Any symlink encountered while walking is independently re-validated against the sandbox — one pointing outside every allowed root is skipped, not followed, even if the walk started from a path fully inside the sandbox.
