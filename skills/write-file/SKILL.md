---
name: shellmcp-write-file
description: Use when you need to create or overwrite a text file, or append to one, through the shellmcp MCP server's write_file tool.
---

# shellmcp: write_file

Writes (or appends) text content to a file.

## Arguments

- `path` (required): relative to the primary allowed root, or absolute inside any allowed root.
- `content` (required): the full text to write.
- `append` (optional, default false): `false` overwrites the file (creating it if it doesn't exist); `true` appends to the end, creating it if it doesn't exist.

## Before you call it

- **The parent directory must already exist.** `write_file` does not create directories for you — call `mkdir` (with `recursive: true` if there are multiple missing levels) first if you're writing into a new path.
- With `append: false` (the default), any existing content at `path` is discarded, not merged. If you need to edit part of an existing file, `read_file` it first, construct the full new content yourself, and write that back — there is no partial/patch write.
- This tool writes text as given; it doesn't format, validate, or lint the content. If you're generating code or structured data, get the content right before calling it.

## Sandboxing

Same boundary as every other tool here: `path` must resolve inside a currently allowed root (`list_allowed_dirs` shows what's allowed) or the call is rejected before anything is written, including for a brand-new file whose parent directory is itself a symlink pointing outside every root.
