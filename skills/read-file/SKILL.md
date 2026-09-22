---
name: shellmcp-read-file
description: Use when you need to read the contents of a file (text or binary) through the shellmcp MCP server's read_file tool.
---

# shellmcp: read_file

Reads the full contents of one file and returns it as the tool result. There's no offset/limit/line-range support — it reads the whole file every call.

## Arguments

- `path` (required): relative to the server's primary allowed root, or an absolute path — either way it must resolve inside one of the currently allowed roots (see `list_allowed_dirs`).
- `encoding` (optional, default `"auto"`): one of `"auto"`, `"text"`, `"base64"`. This is a flag you *set* — pick one of the three words — never content you need to construct yourself.
  - `"auto"`: returns UTF-8 text if the file is valid UTF-8, base64 otherwise. Right choice almost always.
  - `"text"`: forces text and returns an error if the file isn't valid UTF-8, instead of silently doing something else.
  - `"base64"`: always base64-encodes the raw bytes, even for a file that happens to be valid UTF-8.

The result always reports which encoding was actually used (`encoding` in the structured result — matters under `"auto"`, since you don't choose it there) along with `size_bytes` and one of `text`, `content_base64`, or (see below) `blob_handle`.

## Large binary content: blob handles

If the resolved encoding is `"base64"` (explicitly, or via `"auto"` on non-UTF-8 content) and the file is larger than 256 KiB, the content is **not** inlined as `content_base64`. Instead it's held server-side and the result carries `encoding: "blob"` plus a `blob_handle` (a string like `blob_...`) and `media_type` (a best-effort sniff, e.g. `image/png`). Nothing else about the call changes — you don't ask for this, it just happens once content crosses the size threshold, so always check `encoding` in the result rather than assuming `content_base64` is populated for a binary read.

Use that `blob_handle` as-is:
- Pass it straight through to `write_file`'s `blob_handle` argument to write those same bytes somewhere else, without ever seeing them yourself.
- It expires after 15 minutes from when `read_file` issued it — consume it promptly rather than holding onto it across a long multi-step task.
- Call `release_blob` on it once you're done, if you're not going to use it (frees the memory early instead of waiting out the TTL); not required, just tidy.
- Small binary content (at or under 256 KiB) still comes back as ordinary inline `content_base64` — the handle mechanism only kicks in above the threshold.

## Before you call it

- Prefer `ls` first if you're not sure the file exists or you need to see what's in a directory — `read_file` on a missing path just returns an error, it won't list nearby files for you.
- Don't hand-author a `content_base64` value yourself for anything — you're not expected to compute base64 encoding by hand, and you'd very likely get it wrong on anything nontrivial. The only base64 you should ever pass to another tool (e.g. `write_file`'s `content_base64`) is a blob a prior `read_file` call already handed you, copied through unchanged. For anything large enough to get a `blob_handle` instead, don't try to extract or re-encode the bytes yourself at all — just pass the handle through.
- If you just want to duplicate or relocate a file — including a binary one — use `copy_file` instead of `read_file` + `write_file`. It never routes the bytes through you at all, so there's no size limit, no encoding to think about, and no risk of a partial/garbled copy.
- Large files come back in full — there's no pagination. For a very large text file, use `exec` with `head`/`tail`/`grep`-style tools instead (see the `shellmcp-exec` skill) to pull just the part you need, rather than reading megabytes into context.

## Errors

On failure, the result is `{"ok": false, "error": {"code", "message", "path", "details"}}` — check `error.code` (e.g. `PATH_NOT_FOUND`, `PATH_OUTSIDE_ROOT`, `NOT_UTF8_TEXT`) rather than pattern-matching the message text if you need to branch on failure kind.

## Sandboxing

A `path` outside every currently allowed root — via `..` traversal or a symlink pointing outside — is rejected with an error before the file is touched. If you get an "outside every allowed directory" error (`PATH_OUTSIDE_ROOT`) and you believe the path should be reachable, check `list_allowed_dirs` for what's actually allowed right now; if `add_allowed_dir` is available (server started with `-allow-runtime-roots`), you can widen access, but that's a deliberate choice the operator has to have opted into — don't assume it's available.
