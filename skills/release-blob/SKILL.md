---
name: shellmcp-release-blob
description: Use when you're done with a blob_handle returned by read_file and want to free it early, through the shellmcp MCP server's release_blob tool.
---

# shellmcp: release_blob

Explicitly discards a blob handle before its TTL elapses, freeing the bytes held for it server-side. This is optional housekeeping, not a required step — every handle expires on its own 15 minutes after `read_file` issued it — but call this when you know you're finished with a handle (e.g. you already passed it to `write_file`, or decided not to use it after all) rather than leaving it to expire.

## Arguments

- `handle` (required): a blob handle string previously returned by `read_file` (in a result with `encoding: "blob"`).

## Result

`{"handle": "...", "released": true|false}`. `released` is `true` only if a live, not-yet-expired entry was actually removed by this call. It is **not an error** to release a handle that's already gone — an unknown handle, one already released, or one that expired on its own — those all just report `released: false`. Don't treat `released: false` as a failure to branch on or retry; it's informational.

## When to use it

- Right after `write_file` consumes a `blob_handle` — the handle is spent either way (a handle is single-use in the sense that once its file is written you're done with it), so releasing it frees the memory a few minutes early instead of waiting out the TTL. Not required, but tidy in a long-running session that reads many large files.
- If you read a large file expecting to write it somewhere and then decide not to (plans changed, wrong file, etc.) — release the handle instead of just abandoning it.
- There's no need to call this for every read — small files never produce a handle at all (see `shellmcp-read-file`), and letting a handle simply expire is perfectly fine if you're not sure whether you're done with it yet.

## Sandboxing

Not applicable — this tool takes no filesystem path, only an opaque handle string, so there's nothing to check against the allowed-directory list.
