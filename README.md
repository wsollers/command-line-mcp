# command-line-mcp (shellmcp)

A generic MCP server, in Go, exposing sandboxed process and filesystem
primitives: `exec`, `read_file`, `write_file`, `copy_file`, `release_blob`,
`find`, `replace`, `mkdir`, `ls`, `rm`, plus `list_allowed_dirs` and
(optionally) `add_allowed_dir`/`remove_allowed_dir`.

Built on the [official Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk)
(`github.com/modelcontextprotocol/go-sdk`, maintained with Google).

## Why Go

Compiles to a single **static binary** with no runtime dependency — no
Node, no Python interpreter, no shared libraries — and cross-compiles to
Linux, Windows, and macOS from one machine (`GOOS`/`GOARCH`,
`CGO_ENABLED=0`). Verified: the Linux binary reports "not a dynamic
executable" under `ldd`. Deployment is copying one file.

## Repo layout

```
cmd/shellmcp/          entrypoint: flag/env parsing, wires sandbox + tools
internal/sandbox/       the allow-list boundary every path is checked against
internal/tools/         the MCP tool definitions, plus errors.go (structured error codes)
internal/blob/          in-memory TTL-expiring store backing read_file/write_file blob handles
internal/walk/          Layer-1 treewalker engine (a working subset) behind find/replace
internal/glob/          bash-like pathname matching, used by find/replace's name_glob
internal/rgx/           ripgrep-compatible regex, used by find/replace's content_regex/search
internal/process/       argv-only process spawn/pipe/timeout, used by exec
skills/<tool>/SKILL.md  one Claude Skill per tool, for agents that use this server
examples/               example MCP client configs
.github/workflows/      CI: builds + tests + (on a version tag) a GitHub Release
```

## Tools

| Tool                 | What it does                                                                 |
|----------------------|-------------------------------------------------------------------------------|
| `exec`                | Spawn a process as an argv array (no shell), pipe stdin, capture stdout/stderr/exit code, with a timeout |
| `read_file`           | Read a file; returns text directly, base64 for smaller binary content, or a `blob_handle` (see below) once the content exceeds 256 KiB |
| `write_file`          | Write or append a file, from text (`content`), raw bytes (`content_base64`), or a previously issued `blob_handle` |
| `copy_file`           | Copy a file server-side — the content never passes through the caller           |
| `release_blob`        | Explicitly discard a blob handle before its TTL expires, freeing its memory early |
| `find`                | Search a directory tree by name glob and/or content regex — no predicate JSON to author |
| `replace`              | Find-and-replace (literal or regex) across every file under a directory matching a name glob, with `dry_run` preview |
| `mkdir`               | Create a directory (optionally `-p` style)                                     |
| `ls`                  | List a directory's entries (name, type, size)                                  |
| `rm`                  | Remove a file, or a directory recursively                                      |
| `list_allowed_dirs`   | Always available. Reports every currently allowed root and its source          |
| `add_allowed_dir`     | Only registered if started with `-allow-runtime-roots`. Widens the sandbox     |
| `remove_allowed_dir`  | Only registered if started with `-allow-runtime-roots`. Narrows it back        |

Each tool's own `skills/<tool>/SKILL.md` has the argument-level detail and
usage guidance; this README covers the server as a whole.

## Blob handles

`read_file` inlines small content directly (`text` or `content_base64`).
Once the bytes exceed 256 KiB, it instead stores them server-side and
returns a `blob_handle` — an opaque, random, TTL-expiring (15 minutes)
reference — so a large file never has to round-trip through the caller's
context just to move from one tool call to the next. `write_file` accepts
`blob_handle` in place of `content`/`content_base64` to write those bytes
back out, and `release_blob` frees a handle early instead of waiting for
its TTL. See `docs/api-spec.md` §3.2 and §18 for the full design, and
`internal/blob/blob.go` for the implementation.

## Structured error codes

Every tool failure returns `{"ok": false, "error": {"code", "message",
"path", "details"}}` as structured content (in addition to a human-readable
message in the text content), with `code` drawn from a fixed vocabulary
(`PATH_NOT_FOUND`, `PATH_ALREADY_EXISTS`, `NOT_A_FILE`,
`DIRECTORY_NOT_EMPTY`, `PERMISSION_DENIED`, `PATH_OUTSIDE_ROOT`,
`INVALID_BASE64`, `BOTH_CONTENT_FIELDS_SET`, `NOT_UTF8_TEXT`,
`BLOB_NOT_FOUND`, `BLOB_EXPIRED`, `INVALID_ARGUMENT`,
`PROCESS_START_FAILED`, `IO_ERROR`) so a calling agent can branch on
failure kind programmatically instead of pattern-matching a message
string. See `docs/api-spec.md` §4 and `internal/tools/errors.go`.

## `find` / `replace`

Both are a thin, flat-argument surface over `internal/walk`, an internal
recursive treewalker — the caller fills in `root` plus a `name_glob`
and/or `content_regex`/`search`, and the tool builds the predicate tree
server-side. There is deliberately no tool that accepts a predicate tree
directly: composing correctly-nested, correctly-discriminated JSON by
hand is a much easier way to get something that parses but doesn't mean
what was intended than filling in a handful of named fields is. See
`docs/api-spec.md` §8–§9 for the full two-layer design (an internal
engine plus narrow named tools now; a generic, operator-gated escape
hatch is deferred).

Both tools skip hidden (dot-prefixed) files and directories unconditionally
and never descend into a symlinked directory; a symlink whose resolved
target falls outside every allowed root is skipped rather than followed.
`replace` skips any file that isn't valid UTF-8 rather than risk
corrupting binary content, and always call it with `dry_run: true` first
to preview a multi-file change before writing anything.

## Design decisions

- **`exec` uses an argv array, never a shell.** `command` is the binary,
  `args` is a `[]string` passed straight to `exec.Command`. There's no
  shell parsing of `|`, `&&`, `;`, `$()`, backticks, etc. A caller can
  still explicitly run `sh -c "..."` as its own program invocation (same
  as calling any other binary) — shellmcp itself never builds a shell
  command line from string concatenation, so there's no injection surface
  from untrusted argument text.
- **Everything is confined to an allow-list of root directories** — see
  below for how that list is configured.

This intentionally does **not** sandbox what a spawned process itself can
read/write/connect to once it's running (that's OS-level sandboxing —
containers, seccomp, a restricted user — layered underneath, not
something an MCP server can enforce from userspace), and it does not
implement an approval/confirmation flow for destructive calls like
`rm` — whether a human is asked to confirm is entirely up to the MCP
client you register it with.

## Configuring the allowed directory

There are three ways an allowed root gets into the sandbox, and they can
be combined. In order of how much you should actually rely on each:

### 1. Launch-time config (the one you should use)

```bash
shellmcp -root /path/one -root /path/two   # repeatable flag
# or
SHELLMCP_ROOTS=/path/one:/path/two shellmcp   # ':' on Linux/macOS, ';' on Windows
```

At least one root is required; the server refuses to start without one.
This is set by whoever configures the MCP host (e.g. the `args`/`env` in
`claude_desktop_config.json` — see `examples/`), so it's under the
operator's control, not the model's. These roots are permanent for the
life of the process — no tool call can ever remove them.

### 2. The standard MCP "roots" protocol — included, but mostly inert today

MCP has a real, spec-defined mechanism for exactly this: a client can
advertise a `roots` capability and hand the server a list of workspace
directories (`roots/list`), with updates pushed via
`notifications/roots/list_changed`. `shellmcp` implements the server side
of this (`internal/tools.refreshClientRoots`, checked before every
path-touching call) and merges anything it gets on top of the config
roots, tagged `"mcp-roots"` in `list_allowed_dirs`.

**In practice, expect this to do nothing against a current client.** Two
things converged during this project's build-and-verify pass, not just
theory:

- The MCP spec deprecated the roots feature as of protocol version
  `2026-07-28` (SEP-2577) in favor of passing paths via tool parameters or
  configuration — which is exactly what launch-time config and
  `add_allowed_dir` already do.
- More concretely, the *current* protocol version actively **forbids**
  the server-initiated `roots/list` request this relies on. Calling it
  from a tool handler against the official Go SDK's own client (which
  negotiates the newest protocol by default) fails with:

  ```
  "roots/list" cannot be sent while serving a request on protocol version 2026-07-28:
  return an InputRequests map instead (multi round-trip requests, SEP-2322)
  ```

  `shellmcp` swallows that error silently and just carries on with
  whatever roots it already has — so this isn't a crash, it's a no-op.
  It'll only ever populate `list_allowed_dirs` with an `"mcp-roots"`
  entry against an older client still negotiating a pre-2026-07-28
  session. The code is kept in for that case and because it costs one
  cheap local round trip per call, but don't design a deployment around
  it working.

### 3. Runtime tools — the mechanism that actually works today

Start the server with `-allow-runtime-roots` and two more tools appear:
`add_allowed_dir` (validates the path exists, adds it, tagged
`"runtime"`) and `remove_allowed_dir` (removes a runtime-added entry
only — it's a no-op, not an error, against a config root; config roots
can never be removed this way). This is an ordinary `tools/call`, so it
works regardless of protocol version or what the client does or doesn't
implement — it's the answer to "can the client set the allowed directory
itself" that actually functions.

It's opt-in and off by default on purpose: exposing it means any caller
who can invoke tools on this server can widen what `exec`/`rm`/etc. can
touch. Turn it on when the workflow genuinely needs the model to open a
new directory mid-session (e.g. a coding agent that needs to follow the
user into a different project folder); leave it off for a fixed,
single-purpose deployment.

```bash
shellmcp -root /path/one -allow-runtime-roots
```

## Build

```bash
go build -o shellmcp ./cmd/shellmcp     # current OS/arch
```

Cross-compile manually:

```bash
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w" -o shellmcp-linux-amd64   ./cmd/shellmcp
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o shellmcp-windows-amd64.exe ./cmd/shellmcp
```

Or don't build locally at all — see the next section.

## Downloading a prebuilt binary

`.github/workflows/build.yml` builds `linux/amd64` and `windows/amd64` on
every push to `main` and every pull request, and uploads them as workflow
artifacts (Actions tab → a run → Artifacts). Push a tag like `v0.1.0` and
the same workflow also attaches both binaries to a GitHub Release, so
`https://github.com/wsollers/command-line-mcp/releases/latest` always has
a direct download link — no Go toolchain required on the machine that
just wants to run it.

## Run standalone / test with the Inspector

```bash
SHELLMCP_ROOTS=/some/safe/dir ./shellmcp
# or
npx @modelcontextprotocol/inspector ./shellmcp -root /some/safe/dir
```

## Register with an MCP client

See `examples/claude_desktop_config.json` (single fixed root) and
`examples/claude_desktop_config.runtime-roots.json` (multiple roots +
`-allow-runtime-roots`). Minimal form:

```json
{
  "mcpServers": {
    "shellmcp": {
      "command": "/absolute/path/to/shellmcp",
      "args": ["-root", "/absolute/path/to/sandbox/dir"]
    }
  }
}
```

Because it's a static binary, this is the entire deployment: point
`command` at the downloaded/built binary, done.

## Skills

`skills/<tool>/SKILL.md` — one per tool (`exec`, `read-file`, `write-file`,
`copy-file`, `release-blob`, `find`, `replace`, `mkdir`, `ls`, `rm`) —
documents argument shapes, gotchas (no shell in `exec`, no parent-dir
creation in `write_file`, no recursion in `ls`, no undo in `rm`, the
blob-handle threshold and TTL for `read_file`/`write_file`/`release_blob`,
always previewing `replace` with `dry_run` first), and the sandboxing
behavior common to all of them, written for an agent deciding how to use
this server rather than for a human reading API docs.

## Extending it

Each tool is registered in `internal/tools/tools.go` via
`mcp.AddTool(server, &mcp.Tool{...}, handler)`, where the handler's third
parameter is a plain Go struct whose `json`/`jsonschema` tags become the
tool's input schema automatically. Adding a new primitive (`mv`, `stat`,
`chmod`, a streaming variant of `exec` for long-running processes) means
adding one more struct + handler + `registerX` call following the
existing pattern, routing any new path argument through
`sandbox.Sandbox.Resolve()`, and adding a matching `skills/<tool>/SKILL.md`.
