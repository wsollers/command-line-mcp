---
name: shellmcp-read-file
description: Use when you need to read the contents of a text file through the shellmcp MCP server's read_file tool.
---

# shellmcp: read_file

Reads the full contents of one text file and returns it as the tool result text. There's no offset/limit/line-range support — it reads the whole file every call.

## Arguments

- `path` (required): relative to the server's primary allowed root, or an absolute path — either way it must resolve inside one of the currently allowed roots (see `list_allowed_dirs`).

## Before you call it

- Prefer `ls` first if you're not sure the file exists or you need to see what's in a directory — `read_file` on a missing path just returns an error, it won't list nearby files for you.
- This is for text. It reads the raw bytes and returns them as a string; a binary file will come back as garbled/unprintable content rather than an error, so don't use it to "peek" at binaries.
- Large files come back in full — there's no pagination. For a very large file, use `exec` with `head`/`tail`/`grep`-style tools instead (see the `shellmcp-exec` skill) to pull just the part you need, rather than reading megabytes into context.

## Sandboxing

A `path` outside every currently allowed root — via `..` traversal or a symlink pointing outside — is rejected with an error before the file is touched. If you get an "outside every allowed directory" error and you believe the path should be reachable, check `list_allowed_dirs` for what's actually allowed right now; if `add_allowed_dir` is available (server started with `-allow-runtime-roots`), you can widen access, but that's a deliberate choice the operator has to have opted into — don't assume it's available.
