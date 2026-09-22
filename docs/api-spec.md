# shellmcp API Specification

**Status:** Working specification for `command-line-mcp` (`shellmcp`)
**Derived from:** an external "MCP Filesystem, Process, and Treewalker API
Specification" proposal, adopted with three additions, one architectural
change, and one deletion (see [§0](#0-provenance-and-changes-from-the-source-proposal))
**Serialization:** JSON-compatible MCP tool arguments and results
**Normative language:** **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT**, and
**MAY** as in RFC 2119.

Each tool/type below is marked with its implementation status:

- **Shipped** — exists in `internal/tools`, `internal/glob`, or `internal/rgx` today, exactly or effectively as specified.
- **Adopted, not yet built** — accepted into the spec this revision; no code yet.
- **Deferred** — intentionally out of scope for now; documented so the shape is decided before it's needed.
- **Rejected** — considered and declined, with the reason kept so it isn't re-litigated from scratch later.

---

## 0. Provenance and changes from the source proposal

This document started from an external spec proposal covering the same
ground: filesystem tools, process execution, glob/regex, and a declarative
treewalker. Three things were added, one architectural decision was changed,
and one feature area was dropped entirely.

**Added** (gaps identified against `shellmcp`'s actual behavior):

1. **Structured error codes** (§4) — every tool error had been free-text (`errResult(msg string)`); this spec formalizes a fixed `code` vocabulary so a calling agent can branch on failure kind, not parse prose.
2. **Optimistic concurrency on writes** (§5.2, §17) — `expected_sha256` / `expected_modified_time` preconditions on `fs.write`, absent from `write_file` today, to prevent silent lost-update races.
3. **Blob handles for large binary content** (§3.2, §18) — above a size threshold, `fs.read` returns a server-side handle instead of inlining base64. Inlining a multi-megabyte base64 string into a tool result burns enormous context for a payload the caller can only relay elsewhere unchanged anyway (see [§18](#18-output-limits-and-continuation)); a handle lets `fs.write`/`fs.copy` consume it without ever re-entering the caller's context.

**Changed** — the treewalker's predicate/action language (§9–§13) is kept in full as an *internal execution engine*, but the source proposal's assumption that it is exposed directly as one generic `tree.walk` tool (the caller hand-authors a recursive JSON predicate tree as a literal tool argument) is not adopted as the primary interface. See [§9.0](#90-two-layer-exposure-model) for the reasoning and the resulting layering.

**Dropped entirely**: auxiliary process I/O streams (the source proposal's §7.4, `aux: [...]` fd/named-pipe bindings beyond stdin/stdout/stderr). No concrete tool this project needs requires a fourth or fifth file descriptor; the protocol surface (binding kinds, collision detection, capture-mode duplication across N streams) is substantial for a need that hasn't materialized. If a real case shows up, it can be re-added as a targeted extension to `exec.run` rather than kept speculatively. `exec.run`'s result shape and capability declaration below have no `aux` field.

---

## 1. Goals

A compact MCP tool surface for:

- reading and writing files, including binary content, without corrupting or misrepresenting bytes that aren't valid UTF-8;
- copying, moving, and deleting filesystem objects, server-side, without routing bytes through the caller when the caller doesn't need to see them;
- literal and regex replacement in files;
- glob expansion, ripgrep-syntax-compatible;
- command execution as an argv array, never a shell;
- recursive tree traversal against declarative predicates and actions, exposed through interfaces sized to what a model can reliably compose;
- structured, machine-actionable errors.

Explicitly *not* a goal: exposing shell parsing, arbitrary code execution as a predicate/action, or a fourth I/O stream nobody has asked for yet.

---

## 2. Design principles

1. **Structured operations over shell commands.** File operations are exposed as structured MCP tools, not `cp`/`mv`/`rm`/`mkdir` strings for `exec` to parse. *(Shipped: `exec` is argv-only, `internal/process`.)*
2. **Bytes at the OS boundary; typed values at the MCP boundary.** Files and process streams are bytes internally; MCP arguments/results distinguish text, JSON, and binary forms. *(Shipped for text/binary via `content`/`content_base64`; JSON as a first-class kind is adopted, not yet built — §3.2.)*
3. **No implicit encoding guesses for writes; self-describing results for reads.** A caller writing data must say what kind it is (which field they populate). A caller reading data is told what kind came back, so no request-side guess is needed for the common case — this mirrors how MCP's own `ImageContent`/`TextContent`/resource `text`/`blob` fields work, and how this project's own `Read` tooling (multimodal image blocks, not base64-as-text) works. *(Shipped: `read_file`'s `encoding: "auto"` self-describes via the response's `encoding` field; `write_file` requires exactly one of `content`/`content_base64`.)*
4. **Globbing is server-side**, ripgrep/bash-glob-compatible (globstar always on, dotfiles excluded by default, brace expansion). *(Shipped: `internal/glob`, cross-validated against real bash.)*
5. **Regex language is ripgrep-compatible**, meaning: the same RE2-derived syntax as Rust's `regex` crate, passed through to Go's `regexp` engine where the syntax is identical, translated where it differs (Unicode `\d`/`\s`/`\w`), and rejected with a specific error — never silently reinterpreted — where no translation exists (character-class set operations, `\b{start}`/`\b{end}`/`\<`/`\>`). *(Shipped: `internal/rgx`, cross-validated against a real build of the Rust `regex` crate.)*
6. **Treewalking is declarative.** Recursive predicates and actions are JSON data interpreted by the server, never executable code — but *declarative* does not by itself mean *the caller hand-authors the JSON*. See §9.0.
7. **Dry-run support** on destructive or bulk mutation operations.
8. **Stable, structured error responses.** Every tool failure carries a `code` from a fixed vocabulary, not only a human-readable message.
9. **No feature for a need that doesn't exist yet.** Auxiliary process streams are the concrete example this revision removed; the same standard applies going forward — a capability earns its place in the spec by having a tool that needs it.

---

## 3. Common types

### 3.1 Path — *Shipped*

```json
{ "path": "src/main.rs" }
```

Interpreted relative to the primary allowed root, or absolute inside any
currently allowed root (`internal/sandbox`). `.`/`..` are normalized and
symlinks are resolved before the sandbox boundary check, including for a
not-yet-existing path (checked against the deepest existing ancestor) — a
symlinked parent directory pointing outside every allowed root is rejected
the same as a direct escape. A path outside every currently allowed root is
rejected with `PATH_OUTSIDE_ROOT` before anything is touched.

### 3.2 DataValue

A value crossing the MCP boundary uses one of four representations.

#### Text — *Shipped*

```json
{ "kind": "text", "text": "hello\n" }
```

Always UTF-8; this project does not support alternate text encodings (no
`encoding` sub-field) — a file that isn't valid UTF-8 is binary as far as
this API is concerned, never "text in some other charset." This is a
deliberate simplification versus the source proposal, which allowed an
arbitrary `encoding` value on text; in practice UTF-8-or-binary is the
distinction that actually matters for an MCP caller, and every non-UTF-8
"text encoding" case is already served correctly by the binary/blob path.

#### JSON — *Adopted, not yet built*

```json
{ "kind": "json", "value": { "name": "example", "count": 3 } }
```

The server serializes/deserializes JSON on the caller's behalf. Optional
serialization controls on write:

```json
{ "kind": "json", "value": {"a": 1}, "format": {"pretty": true, "indent": 2, "trailing_newline": true} }
```

Lower priority than the other two additions: models are already reliable at
producing/consuming JSON text directly via the existing `text` kind, so this
is a convenience (skip one layer of stringify/parse), not a reliability fix.

#### Binary — *Shipped, with an addition*

```json
{ "kind": "binary", "data": "AAECAwQF" }
```

Base64, decoded/encoded entirely server-side — see the base64 discussion in
§3.2.1. *(Shipped as `content_base64` on `write_file`, `content_base64` in
`read_file`'s result.)*

**Addition, adopted but not yet built** — above a size threshold (default
suggestion: 256 KiB), `fs.read` returns a handle instead of inlining base64:

```json
{ "kind": "blob", "handle": "blob_01JXYZ...", "size": 8388608, "media_type": "application/octet-stream" }
```

A `blob` handle is accepted anywhere a `binary` value is accepted as write
input (`fs.write`, `fs.copy`'s conceptual equivalent), so a large file can be
read once and written elsewhere without ever inlining its bytes into the
caller's context — see §18 for why this matters and how it composes with
`copy_file`. A handle expires after a server-defined TTL; a write against an
expired handle fails with `BLOB_EXPIRED`. `blob.release` (optional) lets a
caller free one early.

#### 3.2.1 On base64 specifically

Base64 fields in this spec are **always produced and consumed by the
server**, never hand-authored by the calling model. This is a hard
constraint, not a style preference: base64 is a bit-level transform a model
cannot reliably compute by generating tokens, unlike picking an enum value
or writing plain text (see the design discussion this section is drawn
from). Every `content_base64`-shaped field in this API exists to be *copied
through* a value the caller was just handed by another tool call — `read_file`
→ `write_file`, or now `read_file` → a `blob` handle → `write_file` — never
to be composed from scratch. Where the goal is purely "duplicate these bytes
elsewhere in the sandbox" and the caller never needs to see them, `copy_file`
remains strictly preferable to any base64 round trip at all.

### 3.3 FileWriteMode — *Shipped*

`"truncate"` (create if absent, else replace from offset 0) or `"append"`
(create if absent, else append). Positional writes are a separate,
*deferred* operation (`fs.write_range`) rather than overloaded onto
append/truncate — not built, no current need.

### 3.4 GlobOptions — *Shipped, plus one addition*

```json
{
  "patterns": ["src/**/*.rs", "!src/generated/**"],
  "include_hidden": false,
  "case_sensitive": true,
  "max_results": 10000
}
```

Per-pattern matching (`*`, `?`, `[...]` with POSIX classes and `!`/`^`
negation, always-on globstar, brace expansion, dotfile exclusion by default)
is `internal/glob.Match`, cross-validated against real bash. **Not yet
built**: the `patterns`-list-with-leading-`!`-exclusion evaluator shown
above. This is a genuinely different feature from single-pattern matching —
later patterns in the list cancel earlier matches, evaluated in order,
`.gitignore`-style — layered on top of `Match`, not part of it. Needed for
`fs.glob` and the `find`/`replace` tools' target-selection below.

### 3.5 RegexSpec — *Shipped*

```json
{ "pattern": "foo\\s+bar", "case_sensitive": true, "multiline": false }
```

This is `internal/rgx.Options` plus the pattern string. `case_sensitive:
false` maps to `IgnoreCase`; there is no separate `dotall` field in the
shipped implementation — use `Multiline` (this project's `Multiline` means
"dot matches newline," i.e. ripgrep's `-U`, matching the field name used
throughout `internal/rgx`, not the source proposal's `multiline`/`dotall`
split). A pattern using a construct `internal/rgx` can't translate
(character-class set operations, `\b{start}`/`\b{end}`, `\<`/`\>`) fails
validation with `UNSUPPORTED_REGEX_SYNTAX`, naming the construct, rather
than being silently reinterpreted or passed through to a different engine.

### 3.6 FileMetadata — *Shipped, via `ls`; standalone `fs.stat` adopted, not built*

```json
{ "path": "src/main.rs", "type": "file", "size": 4096, "modified_time": "2026-09-22T12:00:00Z", "symlink": false }
```

`type` is one of `file | directory | symlink | other`. `ls` returns this
shape per entry today (`lsEntry`, minus `modified_time`, which is adopted
but not yet added to `lsEntry`/a standalone `fs.stat`).

### 3.7 ConcurrencyPrecondition — *Adopted, not yet built*

```json
{ "expected_sha256": "..." }
```
or
```json
{ "expected_modified_time": "2026-09-22T12:00:00Z" }
```

Optional on `fs.write`. A mismatch fails with `PRECONDITION_FAILED` and
performs no write. See §17.

---

## 4. Error model — *Adopted (structured codes), not yet built*

Every tool failure — where the MCP transport itself succeeded but the
operation didn't — returns:

```json
{
  "ok": false,
  "error": { "code": "PATH_NOT_FOUND", "message": "Path does not exist", "path": "src/missing.rs", "details": {} }
}
```

`errResult(msg string)` today only sets `IsError: true` and a free-text
`TextContent`; this is a real, adoptable gap, not a stylistic nice-to-have —
a `code` field lets a caller distinguish "retry with a different path" from
"stop, this is a permissions problem" without parsing English.

Error codes:

```text
PATH_NOT_FOUND
PATH_ALREADY_EXISTS
NOT_A_FILE
NOT_A_DIRECTORY
DIRECTORY_NOT_EMPTY
PERMISSION_DENIED
PATH_OUTSIDE_ROOT
INVALID_GLOB
INVALID_REGEX
UNSUPPORTED_REGEX_SYNTAX
INVALID_BASE64
BOTH_CONTENT_FIELDS_SET
NEITHER_CONTENT_FIELD_SET
NOT_UTF8_TEXT
BLOB_NOT_FOUND
BLOB_EXPIRED
PROCESS_NOT_FOUND
PROCESS_TIMEOUT
PROCESS_START_FAILED
OUTPUT_LIMIT_EXCEEDED
PREDICATE_INVALID
ACTION_INVALID
PRECONDITION_FAILED
CONFLICT
IO_ERROR
```

`UNSUPPORTED_REGEX_SYNTAX` is distinct from `INVALID_REGEX`: the former is
`internal/rgx`'s `*UnsupportedSyntaxError` (a recognized-but-untranslatable
Rust-regex construct), the latter is any other pattern Go's `regexp` itself
rejects (malformed syntax under either engine). `NOT_UTF8_TEXT` is what
`read_file` returns today for `encoding: "text"` against non-UTF-8 content,
currently as a plain error string; this table gives it a code.
`BOTH_CONTENT_FIELDS_SET` / `NEITHER_CONTENT_FIELD_SET` name `write_file`'s
existing validation (today: a free-text message) precisely.

---

# 5. Filesystem tools

## 5.1 `read_file` — *Shipped, spec name `fs.read`*

### Input

```json
{ "path": "config.json", "encoding": "auto", "offset": 0, "length": null }
```

`encoding`: `"auto"` (default) | `"text"` | `"base64"`. This is a flag the
caller *sets* (one of three fixed words) — never content the caller
constructs; see §3.2.1. `"auto"` returns text if the file is valid UTF-8,
base64 otherwise, and always reports which one it used. `"text"` forces
text and fails with `NOT_UTF8_TEXT` otherwise, letting a caller assert an
expectation and fail fast rather than silently branch on the response later.
*(`offset`/`length` byte-range reads: adopted, not yet built — today `ls`/
`read_file` read whole files.)*

**Size-based blob handle**: when the resolved content (post-range, if
`offset`/`length` given) exceeds the size threshold, the result carries
`{"kind": "blob", ...}` instead of inline base64, under both `"auto"` and
`"base64"` — *adopted, not yet built*.

### Result

```json
{ "path": "config.json", "encoding": "text", "size_bytes": 19, "text": "..." }
```
or
```json
{ "path": "img.bin", "encoding": "base64", "size_bytes": 11, "content_base64": "..." }
```

---

## 5.2 `write_file` — *Shipped, spec name `fs.write`, plus two additions*

### Input (shipped)

```json
{ "path": "notes.txt", "content": "new line\n", "append": true }
```
or
```json
{ "path": "img.bin", "content_base64": "...", "append": false }
```

Exactly one of `content` / `content_base64`; both or neither is rejected
(`BOTH_CONTENT_FIELDS_SET` / `NEITHER_CONTENT_FIELD_SET`). Parent directory
must already exist (`mkdir` first).

### Additions — *adopted, not yet built*

```json
{
  "path": "notes.txt",
  "content": "new line\n",
  "expected_sha256": null,
  "expected_modified_time": null
}
```

A mismatch against either precondition fails with `PRECONDITION_FAILED`
before any bytes are written. Also accepts a `blob` handle in place of
`content`/`content_base64` (see §3.2), so a value obtained from `read_file`
never has to round-trip through inline base64 to be written elsewhere.

### Result

```json
{ "path": "notes.txt", "bytes_written": 9, "sha256": "..." }
```

`sha256` on the result is new (adopted, not built) — lets a caller capture
it for a future `expected_sha256` precondition without a separate `stat`
call.

---

## 5.3 `copy_file` — *Shipped, spec name `fs.copy`, narrower than proposed*

### Input (shipped)

```json
{ "src": "src.txt", "dst": "dst.txt", "overwrite": false }
```

Entirely server-side — bytes never pass through the caller, so there's no
size limit, no encoding to pick, no risk of a partial/corrupted copy. `src`
must be a regular file; `dst`'s parent must already exist.

**Not adopted from the source proposal**: `recursive` (directory-tree copy)
and `preserve_metadata`/`follow_symlinks` options. A single-file copy tool
covers the common "duplicate/relocate a binary I don't need to see" case
cleanly; recursive tree copy is a materially different operation (partial-
failure semantics across many files, symlink policy per-entry) better
served by `exec` with `cp -r` today, or by a dedicated tool later if single-
file `copy_file` turns out to be a frequent enough building block for tree
copies that the repetition is worth removing.

---

## 5.4 `move_file` — *Deferred*

Spec name `fs.move`. Not built; no current caller need distinct from
`copy_file` + `rm`. Would use an atomic rename when source/destination share
a filesystem.

---

## 5.5 `rm` — *Shipped, spec names `fs.delete` + `fs.rmdir` unified, plus one addition*

### Input (shipped)

```json
{ "path": "tmp/cache", "recursive": true }
```

One tool for both files and directories (the source proposal splits
`fs.delete`/`fs.rmdir`; a single tool with `recursive` covers both without
meaningfully different semantics to justify two names). Refuses to remove an
allowed root itself, regardless of `recursive`. Non-recursive on a
non-empty directory fails with `DIRECTORY_NOT_EMPTY`.

### Addition — *adopted, not yet built*

```json
{ "path": "tmp/cache", "recursive": true, "dry_run": true }
```

`dry_run: true` reports what would be removed (paths, count) without
touching the filesystem — consistent with principle 7 and with the
treewalker's own dry-run semantics (§13), for the same reason: a recursive
delete is the highest-stakes call in this tool surface and currently has no
preview.

---

## 5.6 `mkdir` — *Shipped*

```json
{ "path": "build/output", "recursive": true }
```

---

## 5.7 `ls` — *Shipped, spec name `fs.list`*

```json
{ "path": "src" }
```

Returns `{"entries": [FileMetadata, ...]}` (object-wrapped — see §18 on why
a bare array is rejected by a spec-conformant client). *Not yet built*:
`max_results`/pagination — today unbounded, a real gap for a very large
directory (§18).

---

## 5.8 `fs.stat` — *Adopted, not yet built*

Single-path metadata (§3.6), for when a caller wants one file's size/type/
mtime without listing its whole parent directory.

---

## 5.9 `fs.glob` — *Adopted, not yet built*

```json
{ "root": ".", "glob": { "patterns": ["**/*.lean", "!build/**"] } }
```

Exclusion-list glob evaluation (§3.4) as its own tool, independent of the
treewalker, for the common "just give me the matching paths" case that
doesn't need content predicates or actions.

---

# 6. File replacement — *Shipped, exposed narrowly (see §9.0); see §9.2 for status*

`literal_replace` and `regex_replace` exist in this spec only as **actions**
inside the internal treewalker engine (§10.5, §10.6), invoked through the
narrow `replace` tool (§9.2), never as standalone `fs.replace`/
`fs.regex_replace` tools taking a raw glob-pattern list directly. This is
the §9.0 layering applied to the source proposal's §6 — folding what were
two separate top-level tools into one narrow tool over the shared engine,
rather than three different surfaces (two flat replace tools plus the
walker) doing overlapping things.

---

# 7. Process execution

## 7.1 `exec` — *Shipped, spec name `exec.run`*

### Input (shipped)

```json
{
  "command": "rg",
  "args": ["UpperBound", "src"],
  "cwd": ".",
  "env": { "RUST_BACKTRACE": "1" },
  "stdin": "text to pipe in",
  "timeout_seconds": 30
}
```

Argv array, `os/exec.CommandContext` — never shell-interpreted (`internal/
process`). `stdin` is a plain string today (text only). Working directory
confined to the allowed roots.

### Not yet built, adopted from the source proposal (minus aux streams — §0)

- Typed `stdout`/`stderr` capture modes (`text | json | binary | file |
  discard`) — today, output is always captured as text. `binary` capture
  (base64, or a blob handle past the size threshold — same mechanism as
  `read_file`) matters for a command that legitimately emits binary on
  stdout (e.g. a compressor); `file` capture writes output straight to a
  sandboxed path instead of returning it at all, useful for a large or
  uninteresting stream.
- Typed `stdin` kinds (`none | text | json | binary | file`), matching the
  same `DataValue` union used elsewhere, instead of a bare string today.

### Explicitly not adopted (§0)

Auxiliary streams (`aux: [...]`, arbitrary fd/named-pipe bindings). No tool
this project targets needs a fourth stream; `stdin`/`stdout`/`stderr` cover
every real case so far. Re-added only against a concrete need.

### Result (shipped shape, no `aux` field)

```json
{
  "ok": true,
  "exit_code": 0,
  "timed_out": false,
  "stdout": "match\n",
  "stderr": "",
  "duration_ms": 27
}
```

A nonzero exit code is not itself an MCP transport error — same as the
source proposal.

---

## 7.2 Persistent execution — *Deferred*

`exec_start` / `exec_write` / `exec_read` / `exec_status` / `exec_terminate`
for long-lived subprocesses, via explicit handles (same handle pattern as
blobs — opaque, server-issued, bounded lifetime). Not required for the
current tool surface; kept here only so the shape is decided in advance if
it's ever needed, per principle 9.

---

# 8. Recursive treewalker

## 8.0 Two-layer exposure model

This is the one place this revision changes the source proposal's
architecture rather than adding to or trimming it, so it gets its own
section instead of an inline note.

The source proposal's principle 7 says predicates/actions must be
declarative JSON, not executable code, and its worked examples (its §9,
§21.7–21.8) show the *caller* — in this project's case, an LLM — hand-
authoring a full recursive predicate tree (nested `all`/`any`/`not`, typed
predicate objects with discriminated `type` fields) as a literal tool
argument, submitted to one generic tool.

The "declarative, not code" half of that is right and is kept in full —
§8.2 onward, unchanged. The "therefore the model composes the tree
directly" half is not adopted as the primary interface, for the same reason
base64-by-hand was rejected in §3.2.1: constructing correctly-nested,
correctly-discriminated JSON from scratch is a different, harder task than
filling in a small set of named fields, and it's exactly the kind of task
where a model can produce something that *parses* but doesn't mean what the
model intended — a wrong `all`/`any`, a predicate `type` typo, an off-by-one
in `context_before` — with no feedback until the walk runs.

So the engine is layered:

- **Layer 1 — internal engine** (§8.1–§8.6 below): the full predicate/
  action interpreter, exactly as specified, living in `internal/walk`. Not
  exposed as an MCP tool by itself.
- **Layer 2 — narrow tools** (§9.1–§9.3): `find` and `replace`, each with a
  flat, small, purpose-built argument schema. Each one *constructs* a Layer-1
  predicate/action tree server-side from those flat arguments — the model
  never sees or writes the JSON DSL. This is the primary, recommended
  interface and covers the workflows in §21.
- **Layer 3 — generic escape hatch** (§9.4, *deferred*): a `tree_walk` tool
  taking the full Layer-1 predicate/action tree directly as its argument,
  for a workflow Layer 2's named tools genuinely can't express. Off by
  default, gated the same way `add_allowed_dir` is gated behind
  `-allow-runtime-roots` — an operator opt-in, not assumed available — both
  because it's a strictly more powerful/dangerous surface and because it's
  the one place this API asks a model to author a nested predicate tree by
  hand, which should be an explicit, deliberate choice by whoever's running
  the server, not the default shape of the API.

## 8.1 `tree_walk` engine — *Layer 1, internal; shipped as a working subset*

Recursively traverses a subtree, evaluates a predicate per entry, applies
actions where it's true. Never executes arbitrary embedded code.

`internal/walk` implements exactly the subset `find`/`replace` need today:
predicates `is_file`, `path_glob`, `content_regex`, combined with an `All`
combinator; no separate `report` action object (Walk itself is read-only
and returns matches; `find`/`replace` apply their own action to what it
returns). The rest of §8.2/§8.3's predicate and action vocabulary, the
`any`/`not` combinators, `max_depth`, `order`, `include_hidden` as a
caller-set option (hidden entries are always skipped, unconditionally —
see `internal/walk`'s package doc), and `follow_symlinks: true` are all
still *adopted, not yet built* — extending `internal/walk`'s `Predicate`
set is how each of those gets added later, not a rewrite of the package.

```json
{
  "root": ".",
  "follow_symlinks": false,
  "include_hidden": false,
  "max_depth": null,
  "order": "preorder",
  "predicate": { "all": [ {"type": "is_file"}, {"type": "path_glob", "patterns": ["**/*.lean"]} ] },
  "actions": [ {"type": "report"} ],
  "dry_run": true,
  "max_results": 10000
}
```

## 8.2 Predicate language — *unchanged from the source proposal*

Boolean combinators `all` / `any` / `not`. Primitive predicates:
`is_file` / `is_directory` / `is_symlink`; `name_equals`; `path_glob`
(`internal/glob`, plus the §3.4 exclusion-list evaluator); `path_regex` /
`content_regex` (`internal/rgx` — ripgrep-compatible, per §3.5, not a
separate regex language); `extension`; `size_gt` / `size_lt` /
`size_between`; `modified_after` / `modified_before`; `content_contains`
(literal); `json_pointer_exists` / `json_pointer_equals`.

## 8.3 Actions — modified: `exec` gated, everything else unchanged

`report` (with optional `include_matches`/`context_before`/`context_after`),
`delete`, `move`, `copy`, `literal_replace`, `regex_replace`, `write_json`,
`json_set` — all as specified in the source proposal, all Layer-1-only
(reachable through Layer 2's `replace`/`find`, or Layer 3 if enabled).

`exec` as a treewalker action (run a program per matched entry) is **only
reachable through Layer 3**, never through a Layer 2 tool, and Layer 3
itself must be separately enabled beyond the base `-allow-runtime-roots`-
style flag — this is strictly more dangerous than a single sandboxed `exec`
call (it's `exec`, per matched file, across a whole subtree, driven by a
model-authored predicate), and the source proposal's own text agrees
("significantly more powerful... SHOULD be disabled by default").

## 8.4 Evaluation semantics, ordering, dry-run — *unchanged*

Metadata predicates evaluated before path predicates before content read
before content predicates, short-circuited where possible so content isn't
read for entries that can't match on path/metadata alone.
`order: preorder | postorder` (postorder for destructive operations, so
children are processed before parents), deterministic (lexicographic)
traversal within a directory, symlink-cycle detection when
`follow_symlinks: true`. `dry_run: true` evaluates predicates and reports
planned actions with no mutation — same shape as the source proposal's §13.

---

# 9. Layer 2: narrow tools over the treewalker

## 9.1 `find` — *Shipped, minus `max_results` continuation (see §13)*

```json
{
  "root": ".",
  "name_glob": "**/*.lean",
  "content_regex": "\\bsorry\\b",
  "max_results": 1000
}
```

Flat arguments only — no nested predicate object. The tool builds
`{"all": [{"type":"path_glob",...}, {"type":"content_regex",...}]}`
server-side and runs it through Layer 1 with a single `report` action.
Every field the caller might plausibly want (extension, size bounds, mtime
bounds) gets its own named, flat, optional argument here rather than a
generic predicate slot — the list of fields can grow; the DSL doesn't need
to be exposed to grow it.

## 9.2 `replace` — *Shipped, minus atomicity/preconditions (§12) and `max_results` continuation (§13)*

```json
{
  "root": ".",
  "name_glob": "src/**/*.rs",
  "search": "OldName",
  "replace": "NewName",
  "is_regex": false,
  "dry_run": true
}
```

Builds a Layer-1 walk with a `path_glob` predicate and a
`literal_replace`/`regex_replace` action (chosen by `is_regex`) server-side.
`dry_run: true` (recommended default at the call site, not the schema — the
tool itself has no opinion, but callers should default to previewing a
multi-file replace) returns the same preview shape as §8.4 without touching
any file. Regex mode uses `internal/rgx` (§3.5) directly, so the same
translated-Unicode-class and rejected-construct behavior applies here as
everywhere else regex appears in this API.

## 9.3 (room for more) — *not yet designed*

A narrow `rm_matching` (glob/predicate-driven bulk delete, dry-run-first) is
the obvious next Layer-2 candidate if bulk deletion by pattern turns out to
be a recurring need beyond single-path `rm`. Not designed yet — added only
against an actual workflow, per principle 9, not speculatively.

## 9.4 `tree_walk` — *Layer 3, deferred, operator-gated*

The full Layer-1 predicate/action tree, taken directly as the tool's input
schema, for a caller that needs boolean nesting or a predicate/action
combination no named Layer-2 tool covers. Requires an explicit server flag
beyond the base runtime-roots gate (name TBD, e.g.
`-allow-generic-treewalk`) before it's even registered — mirroring how
`add_allowed_dir`/`remove_allowed_dir` are only registered when
`-allow-runtime-roots` is set. Not built; the interface shape is §8.1
verbatim once it is.

---

# 10. Ripgrep regex compatibility — see `internal/rgx`

Superseded by the concrete, implemented, empirically-validated behavior in
`internal/rgx`'s package documentation rather than restated here: exact-
parity syntax (literals, quantifiers, groups, anchors, POSIX classes, flags
`i`/`m`/`s`), translated syntax (`\d`/`\s`/`\w` and negations, Unicode-aware
by default, with documented approximation gaps for `\w`/`\s` versus the
Rust crate's boolean-property definitions), and rejected syntax (character-
class set operations, `\b{start}`/`\b{end}`, `\<`/`\>`) with a specific
`UnsupportedSyntaxError` rather than silent reinterpretation. Cross-
validated against a real build of the Rust `regex` crate
(`internal/rgx/oracle`, gated behind `-tags oracle`, not part of default
CI — see that package's README for why).

---

# 11. Symlink semantics — *Shipped (sandbox), adopted for the walker*

Listing a symlink reports the symlink itself; deleting a symlink deletes the
link, not the target; traversal does not follow symlinked directories
unless `follow_symlinks: true`, which then requires cycle detection. This
already matches `internal/sandbox.Resolve`'s symlink handling for ordinary
path operations; the treewalker (§8) inherits the same policy rather than
defining a separate one.

---

# 12. Atomicity and concurrency — *adopted, not yet built (see §5.2, §3.7)*

Single-file writes should use write-temp-then-rename where practical for
atomicity. `expected_sha256` / `expected_modified_time` preconditions (§3.7)
on `write_file` give optimistic concurrency; a failed precondition prevents
the write and returns `PRECONDITION_FAILED`. Bulk tree operations (the
walker's mutating actions) are explicitly not atomic as a whole — a failure
partway through a multi-file `replace` leaves earlier files changed and
later ones not; `dry_run` is the mitigation, not an atomicity guarantee.

---

# 13. Output limits and continuation

Two distinct large-payload problems, with two distinct mechanisms — worth
stating explicitly since they're easy to conflate:

**Many small results** (a directory listing, a glob expansion, a `find`
match set) — bounded by `max_results`, with a continuation token for the
remainder. *Adopted, not yet built*: `ls`, `fs.glob`, and `find` are
currently unbounded.

**One large payload** (a single file's content, a single command's stdout)
— this is what blob handles (§3.2) solve, and continuation tokens don't:
there's nothing to paginate through *pages of results*, there's one giant
value, and the fix is not returning it inline at all rather than returning
it in smaller pages. `read_file` past the size threshold, and `exec`'s
binary stdout capture, both use the blob mechanism, not `max_results`.

---

# 14. Capability declaration — *adopted, not yet built*

```json
{
  "filesystem": {
    "read": true, "write": true, "binary": true, "json": false,
    "glob": true, "regex": "ripgrep-compatible-re2",
    "symlink_support": true, "atomic_replace": false,
    "blob_handles": false, "optimistic_concurrency": false
  },
  "execution": { "run": true, "persistent": false, "shell": false },
  "treewalker": {
    "narrow_tools": false, "generic_tree_walk": false, "exec_action": false
  }
}
```

No `aux_fd` field (§0). `regex` names the actual engine relationship
(`internal/rgx` over Go's RE2-based `regexp`), not a bare `"ripgrep-
default"` string, since this project's compatibility is translated/
documented, not literally ripgrep's engine.

---

# 15. Recommended MCP tool set

Mapped against the source proposal's `fs.*`/`exec.*` naming, since that
naming is referenced throughout this document:

| This project | Source proposal equivalent | Status |
|---|---|---|
| `exec` | `exec.run` | Shipped (aux streams dropped, typed I/O kinds adopted) |
| `read_file` | `fs.read` | Shipped (blob handles adopted) |
| `write_file` | `fs.write` | Shipped (concurrency preconditions adopted) |
| `copy_file` | `fs.copy` | Shipped (narrower: no recursive) |
| `mkdir` | `fs.mkdir` | Shipped |
| `ls` | `fs.list` | Shipped (pagination adopted) |
| `rm` | `fs.delete` + `fs.rmdir` | Shipped (dry_run adopted) |
| `list_allowed_dirs` | — (no equivalent) | Shipped |
| `add_allowed_dir` / `remove_allowed_dir` | — (no equivalent) | Shipped |
| `fs.stat` | `fs.stat` | Adopted, not built |
| `fs.glob` | `fs.glob` | Adopted, not built |
| `find` | (narrowed from `fs.replace`-style flat tools + walker) | Shipped (`max_results` continuation adopted, not built) |
| `replace` | `fs.replace` + `fs.regex_replace` (merged, narrowed) | Shipped (atomicity/preconditions, `max_results` continuation adopted, not built) |
| `tree_walk` | `tree.walk` | Deferred, operator-gated |
| `move_file` | `fs.move` | Deferred |
| `fs.write_range` | `fs.write_range` | Deferred |
| `exec_start`/`exec_write`/`exec_read`/`exec_status`/`exec_terminate` | same | Deferred |
| — | `aux` streams on `exec.run` | **Rejected** (§0) |

---

# 16. Security requirements — *unchanged except aux-stream bullet removed*

Policy controls for: allowed workspace roots; allowed/denied executable
programs; maximum process runtime; maximum stdout/stderr capture size;
maximum file read/write size; maximum recursion depth; maximum files
visited per walk; symlink traversal; whether absolute paths are permitted;
whether the treewalker's `exec` action is permitted (§8.3); whether Layer 3
(`tree_walk`) is enabled at all (§9.4); whether shell execution exists at
all (it does not, by construction — §2.1). Path policy is applied after
normalization and symlink resolution (`internal/sandbox`, already true
today). Mutation and execution events should be audit-logged — *adopted,
not yet built*.

---

# 17. Example workflows

## 17.1 Read a config file, then write a sibling file — *shipped shape*

```json
{ "path": "config.json" }
```
→
```json
{ "path": "config.json", "encoding": "text", "size_bytes": 42, "text": "{\"enabled\": true}" }
```

## 17.2 Duplicate a binary asset — *shipped*

```json
{ "src": "assets/logo.png", "dst": "backup/logo.png" }
```

via `copy_file` — never via `read_file` + `write_file`, per §3.2.1.

## 17.3 Find Lean files still containing `sorry` — *Layer 2, adopted*

```json
{ "root": ".", "name_glob": "**/*.lean", "content_regex": "\\bsorry\\b" }
```

via `find` — no nested predicate JSON authored by the caller; internally
this becomes exactly the Layer-1 tree shown in §8.1.

## 17.4 Rename a project identifier across docs — *Layer 2, adopted*

```json
{ "root": "docs", "name_glob": "**/*.md", "search": "OldProjectName", "replace": "NewProjectName", "dry_run": true }
```

via `replace`, previewed before the real run.

## 17.5 A workflow Layer 2 can't express — *Layer 3, deferred, for reference*

The one case worth keeping an example of: a caller needs `any` of two
different extension groups *and* a size bound *and* wants a `move` action
with a template destination — no realistic named Layer-2 tool covers this
combination without growing into the DSL itself. This is what Layer 3
(§9.4) is for, once enabled:

```json
{
  "root": "incoming",
  "predicate": { "all": [ {"type": "is_file"}, {"any": [{"type":"extension","values":[".c",".h"]}, {"type":"extension","values":[".cpp",".hpp"]}]}, {"type": "size_lt", "bytes": 1048576} ] },
  "actions": [ {"type": "move", "destination_template": "sorted/{extension}/{name}"} ],
  "dry_run": false
}
```

---

# 18. Summary model

```text
MCP client (an LLM, almost always)
   |
   +-- read_file / write_file / copy_file / rm / mkdir / ls
   |      |
   |      +-- text (self-describing: response says which encoding)
   |      +-- binary (base64, server-authored only — never hand-composed)
   |      +-- blob handle (above a size threshold — never inlined)
   |      +-- JSON (adopted, convenience only)
   |
   +-- fs.glob / find / replace          <- Layer 2: flat args, no DSL exposure
   |      |
   |      +-- internal/glob (path patterns, exclusion lists)
   |      +-- internal/rgx (ripgrep-compatible regex, exact-parity + translated + rejected)
   |
   +-- exec
   |      |
   |      +-- argv only, never a shell
   |      +-- stdin/stdout/stderr: text | json | binary | file | discard
   |      (no aux streams)
   |
   +-- tree_walk (Layer 3, operator-gated, deferred)
          |
          +-- the full predicate/action DSL, Layer 2's actual implementation,
              exposed directly only when an operator has opted in
```
