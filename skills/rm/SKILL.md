---
name: shellmcp-rm
description: Use when you need to delete a file or directory through the shellmcp MCP server's rm tool. Treat this as a destructive, hard-to-undo action.
---

# shellmcp: rm

Removes a file or directory. **There is no trash/recycle bin and no confirmation step at the protocol level** — whether a human is asked to confirm depends entirely on the MCP client you're running in, not on this tool. Treat every call as irreversible.

## Arguments

- `path` (required): relative to the primary allowed root, or absolute inside any allowed root.
- `recursive` (optional, default false): `false` removes a single file (or an empty directory) and errors on a non-empty directory. `true` behaves like `rm -rf` — removes a directory and everything inside it, no per-item confirmation.

## Before you call it

- Prefer `ls` first to confirm exactly what's at `path` and, for a directory, what's inside it — especially before a `recursive: true` call, since there's no dry-run mode.
- Double-check the path isn't broader than you intend. A relative path is resolved against the primary allowed root, so a typo'd or missing leading segment can point somewhere you didn't expect within that root.
- If you're deleting as part of a larger task (e.g. "clean up the build output"), be as specific as possible about which paths you remove rather than reasoning about a whole directory tree from memory — re-`ls` if there's any doubt.

## Sandboxing

`path` must resolve inside a currently allowed root, and the tool additionally refuses outright to remove an allowed root directory itself (config, mcp-roots, or runtime-added) — that call always errors, by design, regardless of `recursive`.
