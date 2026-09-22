# rgx_oracle

A tiny standalone Rust binary used only for testing: it wraps the real
`regex` crate behind a line-oriented stdin/stdout protocol so
`internal/rgx`'s Go tests can cross-validate translations against
actual Rust-regex behavior, not just against a from-memory
understanding of its documented semantics.

It is not part of `command-line-mcp` itself and is never built,
vendored, or shipped as part of the `shellmcp` binary or its release
artifacts — nothing in the main Go module or build depends on a Rust
toolchain being present. It exists purely so a developer touching
`internal/rgx`'s translation logic can verify it empirically, the same
way `internal/glob`'s `bash_oracle_test.go` verifies glob matching
against real bash.

## Protocol

Reads `pattern\tinput\n` lines from stdin. For each, writes one line
to stdout:

- `true` / `false` — whether `Regex::new(pattern)` matched `input`
- `ERROR:<message>` — if the pattern failed to compile in Rust

## Usage

```sh
cargo build --release
RGX_ORACLE_BIN=$(pwd)/target/release/rgx_oracle \
  go test -tags oracle ../../internal/rgx/... -run TestAgainstRustOracle -v
```

Requires a Rust toolchain (`cargo`) and network access to crates.io
the first time (to fetch the `regex` crate). Neither is required to
build, test, or run `shellmcp` itself — this is a developer-only
verification tool for the `internal/rgx` translation layer.
