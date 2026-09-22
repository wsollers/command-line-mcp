---
name: shellmcp-write-file
description: Use when you need to create or overwrite a file (text or binary), or append to one, through the shellmcp MCP server's write_file tool.
---

# shellmcp: write_file

Writes (or appends) content to a file.

## Arguments

- `path` (required): relative to the primary allowed root, or absolute inside any allowed root.
- `content` (optional): UTF-8 text to write.
- `content_base64` (optional): base64-encoded raw bytes to write, for binary content.
- `blob_handle` (optional): a handle previously returned by `read_file` (see below) — writes those exact bytes without you ever having to see or touch them.
- `append` (optional, default false): `false` overwrites the file (creating it if it doesn't exist); `true` appends to the end, creating it if it doesn't exist.

Provide exactly one of `content` / `content_base64` / `blob_handle` — the call is rejected (`BOTH_CONTENT_FIELDS_SET`) if more than one is set, and if none are.

**Never hand-author `content_base64` yourself.** Computing base64 encoding of arbitrary content by hand is not something you can do reliably, especially for anything nontrivial — it's a bit-level transform, not a text-generation task, and there's no way to check your own work by eye. The only correct source for a `content_base64` value is a blob you were just handed by `read_file` (with `encoding: "base64"` or `"auto"` on a binary file) — copy it through verbatim, never retype or reconstruct it. If you're inventing new binary content from scratch, generate it with `exec` (e.g. a script or tool that writes the file directly) rather than trying to compose it as base64 yourself.

## Writing from a blob handle

If a prior `read_file` call returned `encoding: "blob"` (which happens automatically once binary content exceeds 256 KiB — see the `shellmcp-read-file` skill), pass its `blob_handle` value straight through in this tool's `blob_handle` argument instead of `content`/`content_base64`. This is the only way to move content that large from a read to a write without routing the actual bytes through your context at all. Handles expire 15 minutes after `read_file` issued them (`BLOB_EXPIRED` if you're too slow, `BLOB_NOT_FOUND` for an unknown or already-consumed one) — use it promptly, and don't hold onto a handle across a long detour before writing it.

## Before you call it

- **The parent directory must already exist.** `write_file` does not create directories for you — call `mkdir` (with `recursive: true` if there are multiple missing levels) first if you're writing into a new path.
- With `append: false` (the default), any existing content at `path` is discarded, not merged. If you need to edit part of an existing text file, `read_file` it first, construct the full new content yourself, and write that back — there is no partial/patch write.
- If the actual goal is duplicating or relocating a file rather than transforming its content, use `copy_file` instead — it never routes the bytes through you, so it has none of the concerns above.
- This tool writes content as given; it doesn't format, validate, or lint text, and doesn't verify base64-decoded or blob bytes form any particular file type. If you're generating code or structured data, get the content right before calling it.

## Errors

On failure, the result is `{"ok": false, "error": {"code", "message", "path", "details"}}` — check `error.code` (e.g. `BOTH_CONTENT_FIELDS_SET`, `BLOB_EXPIRED`, `BLOB_NOT_FOUND`, `PATH_OUTSIDE_ROOT`, `INVALID_BASE64`) rather than pattern-matching the message text if you need to branch on failure kind.

## Sandboxing

Same boundary as every other tool here: `path` must resolve inside a currently allowed root (`list_allowed_dirs` shows what's allowed) or the call is rejected (`PATH_OUTSIDE_ROOT`) before anything is written, including for a brand-new file whose parent directory is itself a symlink pointing outside every root.
