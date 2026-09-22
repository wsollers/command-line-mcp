---
name: shellmcp-exec
description: Use when you need to run a command-line program (build a project, run a script, invoke git/npm/pip/etc, check a tool's output) through the shellmcp MCP server's exec tool.
---

# shellmcp: exec

`exec` spawns one process as an argv array and returns its stdout, stderr, and exit code. It never goes through a shell.

## When to use it

Any time you'd otherwise reach for a terminal to run a single command: build steps, test runs, git/npm/pip/go invocations, checking a file's type, running a linter, etc. Use `read_file`/`write_file`/`ls`/`mkdir`/`rm` instead for pure filesystem operations — they're cheaper and clearer than shelling out to `cat`, `ls`, `mkdir -p`, or `rm`.

## Arguments

- `command` (required): the executable name or path, e.g. `"git"`, `"npm"`, `"/usr/bin/python3"`.
- `args` (optional): a list of arguments, one array element per argv entry — **never** a single string of space-joined arguments. `{"command": "git", "args": ["status", "--short"]}`, not `{"command": "git status --short"}`.
- `cwd` (optional): working directory, relative to the server's primary allowed root. Defaults to that root.
- `stdin` (optional): text piped to the process's stdin, then stdin is closed.
- `env` (optional): extra environment variables merged into the process's environment.
- `timeout_seconds` (optional, default 30): the process is killed if it hasn't exited by then; check `timed_out` in the result.

## No shell — plan accordingly

There is no shell interpreting the command: no `|`, `&&`, `;`, `$()`, globbing, or `~` expansion. Concretely:

- A pipeline (`grep foo | wc -l`) is two `exec` calls: run the first, take its `stdout`, pass it as `stdin` to the second.
- `&&`/`;` sequencing is just multiple `exec` calls, checking `exit_code` between them if you need to stop on failure.
- Wildcards (`*.go`) don't expand — use `ls` to enumerate files yourself and pass explicit paths in `args`.
- If you genuinely need shell syntax, you can call `exec` with `command: "sh", args: ["-c", "..."]` — that's `shellmcp` invoking the `sh` binary like any other program; it doesn't add its own shell layer around your call. Prefer avoiding this when there's an argv-only equivalent, since it reopens injection risk if any part of the string comes from untrusted input.

## Sandboxing

`cwd` must resolve inside an allowed root (see the `list_allowed_dirs` tool, or the `shellmcp-read-file` skill for what "allowed root" means) — `exec` itself does not otherwise restrict what the spawned process can read or touch once it's running (e.g. `git` can still follow `cd ..` internally, or read files outside the root if given an absolute path as an argument). Don't rely on `exec`'s cwd confinement as a guarantee about what the child process does with args you hand it.

## Reading the result

The result is `{stdout, stderr, exit_code, timed_out}`. A non-zero `exit_code` is not itself an MCP-level error (`isError` stays false) — check it explicitly, the same way you'd check `$?` after running a command yourself.
