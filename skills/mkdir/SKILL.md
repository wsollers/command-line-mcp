---
name: shellmcp-mkdir
description: Use when you need to create a directory (optionally with missing parents) through the shellmcp MCP server's mkdir tool.
---

# shellmcp: mkdir

Creates a directory.

## Arguments

- `path` (required): relative to the primary allowed root, or absolute inside any allowed root.
- `recursive` (optional, default false): `true` behaves like `mkdir -p` — creates any missing parent directories and doesn't error if `path` already exists. `false` behaves like plain `mkdir` — errors if the parent doesn't exist, or if `path` already exists.

## When to use it

Before `write_file`ing into a directory that might not exist yet — `write_file` doesn't create parent directories itself. If you're not sure whether the target directory already exists, just call `mkdir` with `recursive: true`; it's a no-op success if it's already there.

## Sandboxing

`path` must resolve inside a currently allowed root (`list_allowed_dirs`), same as every other tool — this is the only way to add structure inside the sandbox, it can't be used to create anything outside it.
