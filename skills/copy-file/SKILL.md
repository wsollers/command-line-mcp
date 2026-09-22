---
name: shellmcp-copy-file
description: Use when you need to duplicate or relocate a file (text or binary) through the shellmcp MCP server's copy_file tool, without routing its content through you.
---

# shellmcp: copy_file

Copies one file to another path, entirely server-side. The bytes never pass through you (no `content`/`content_base64` field to fill in), so there's no size limit, no encoding to think about, and no risk of a partial or corrupted copy — this is the right tool any time the actual goal is "duplicate this file" or "move this file," as opposed to "read it and do something with the content."

## Arguments

- `src` (required): source file, relative to the primary allowed root or absolute inside any allowed root.
- `dst` (required): destination file, same path rules.
- `overwrite` (optional, default false): `false` refuses if `dst` already exists; `true` replaces it.

## Before you call it

- Prefer this over `read_file` + `write_file` whenever you're not actually transforming the content — that pattern is slower, burns context on a payload you don't need to see, and for binary files means round-tripping base64 you didn't need to touch at all.
- `src` must be a regular file — copying a directory isn't supported (there's no recursive mode). For copying a directory tree, use `exec` with `cp -r` (or equivalent) instead.
- `dst`'s parent directory must already exist, same as `write_file` — use `mkdir` first if it doesn't.
- Without `overwrite: true`, an existing `dst` causes the call to fail cleanly rather than silently replacing something — check with `ls` first if you're not sure whether it exists and whether replacing it is intended.

## Sandboxing

Both `src` and `dst` must resolve inside a currently allowed root (`list_allowed_dirs` shows what's allowed); the call is rejected before anything is read or written if either one falls outside every root.
