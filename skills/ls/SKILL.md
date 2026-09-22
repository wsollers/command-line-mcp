---
name: shellmcp-ls
description: Use when you need to list a directory's contents through the shellmcp MCP server's ls tool.
---

# shellmcp: ls

Lists the immediate contents of one directory (not recursive).

## Arguments

- `path` (optional): relative to the primary allowed root, or absolute inside any allowed root. Defaults to the primary allowed root itself if omitted.

## Result shape

An array of `{name, type, size_bytes}`, where `type` is `"file"`, `"dir"`, `"symlink"`, or `"other"`. `size_bytes` is only meaningful for `type: "file"` (it's `0` for directories and symlinks).

## When to use it

- To check whether a path exists / what's in a directory before `read_file`, `write_file`, or `rm`ing something in it.
- To discover files matching a pattern yourself, since `exec` has no shell globbing available — list the directory with `ls`, filter the `name` fields in the result, then act on the specific paths.
- It is not recursive. To walk a subtree, call it again on each subdirectory you find, or use `exec` with `find`/an equivalent tool if you need a deep listing in one call.

## Sandboxing

`path` must resolve inside a currently allowed root; listing `list_allowed_dirs` itself (a separate tool) tells you which roots those are right now.
