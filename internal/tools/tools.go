// Package tools registers shellmcp's MCP tools: exec, read_file,
// write_file, mkdir, ls, rm, and list_allowed_dirs.
package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wsollers/command-line-mcp/internal/blob"
	"github.com/wsollers/command-line-mcp/internal/process"
	"github.com/wsollers/command-line-mcp/internal/sandbox"
)

// blobThresholdBytes is the read_file content size above which a result is
// returned as a blob handle instead of inline base64 — see
// docs/api-spec.md §3.2/§18. Inlining a multi-megabyte base64 string into
// a tool result burns enormous context for a payload the caller can only
// relay elsewhere unchanged anyway.
const blobThresholdBytes = 256 * 1024

// blobTTL is how long a read_file blob handle stays valid before it must
// be re-read. Deliberately short: a handle is meant to be consumed
// promptly within one task (read, then write or release), not held
// indefinitely.
const blobTTL = 15 * time.Minute

// refreshClientRoots asks the connected client (via req.Session) for its
// current MCP roots and merges them into sb, before every path-touching
// tool call. This is deliberately done per call rather than once at
// connect time: the obvious hook for a one-time pull, ServerOptions.
// InitializedHandler, never fires for clients that use the newer
// stateless server/discover negotiation instead of the legacy
// initialize/initialized handshake (verified against this SDK's own
// client, which defaults to discover). Pulling fresh each call is a
// single extra local round trip and guarantees the roots in effect are
// never stale; it is a silent no-op for any client that doesn't
// implement roots (ListRoots just errors, which is ignored).
func refreshClientRoots(ctx context.Context, req *mcp.CallToolRequest, sb *sandbox.Sandbox) {
	if req == nil || req.Session == nil {
		return
	}
	res, err := req.Session.ListRoots(ctx, nil)
	if err != nil {
		// Expected and silent for almost every real client today: the
		// current MCP protocol version (2026-07-28+, SEP-2322/2577)
		// forbids server-initiated roots/list requests entirely, and any
		// older client may simply not implement roots. See the README's
		// "roots protocol" section — add_allowed_dir/remove_allowed_dir
		// (gated by -allow-runtime-roots) is the mechanism that actually
		// works today.
		return
	}
	uris := make([]string, 0, len(res.Roots))
	for _, r := range res.Roots {
		uris = append(uris, r.URI)
	}
	sb.SetClientRoots(uris)
}

// Register adds every tool to server, enforcing sb on all path and cwd
// arguments.
func Register(server *mcp.Server, sb *sandbox.Sandbox) {
	bs := blob.New(blobTTL)
	registerExec(server, sb)
	registerReadFile(server, sb, bs)
	registerWriteFile(server, sb, bs)
	registerCopyFile(server, sb)
	registerReleaseBlob(server, bs)
	registerMkdir(server, sb)
	registerLs(server, sb)
	registerRm(server, sb)
	registerListAllowedDirs(server, sb)
	if sb.RuntimeAllowed() {
		registerAddAllowedDir(server, sb)
		registerRemoveAllowedDir(server, sb)
	}
}

// ---------------------------------------------------------------------------
// exec
// ---------------------------------------------------------------------------

type execArgs struct {
	Command        string            `json:"command" jsonschema:"the executable to run, e.g. \"ls\" or \"/usr/bin/git\""`
	Args           []string          `json:"args,omitempty" jsonschema:"arguments to pass to the command, one element per argv entry"`
	Cwd            string            `json:"cwd,omitempty" jsonschema:"working directory, relative to the primary allowed root (default: that root)"`
	Stdin          string            `json:"stdin,omitempty" jsonschema:"text to write to the process's stdin before closing it"`
	Env            map[string]string `json:"env,omitempty" jsonschema:"extra environment variables to set for the process"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty" jsonschema:"kill the process if it hasn't exited after this many seconds (default 30)"`
}

func registerExec(server *mcp.Server, sb *sandbox.Sandbox) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "exec",
		Description: "Spawn a process as an argv array (no shell interpretation of pipes, &&, ;, $(), etc.), " +
			"optionally piping text to its stdin, and return stdout, stderr, and the exit code. " +
			"The working directory is confined to the allowed roots (see list_allowed_dirs).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args execArgs) (*mcp.CallToolResult, any, error) {
		refreshClientRoots(ctx, req, sb)
		if args.Command == "" {
			return codedError(codeInvalidArgument, "", "command must not be empty", nil), nil, nil
		}

		cwd, err := sb.Resolve(args.Cwd)
		if err != nil {
			return errFromErr(err, args.Cwd), nil, nil
		}

		timeout := 30 * time.Second
		if args.TimeoutSeconds > 0 {
			timeout = time.Duration(args.TimeoutSeconds) * time.Second
		}

		res, err := process.Run(ctx, process.Spec{
			Command: args.Command,
			Args:    args.Args,
			Dir:     cwd,
			Stdin:   args.Stdin,
			Env:     args.Env,
			Timeout: timeout,
		})
		if err != nil {
			return codedError(codeProcessStartFailed, "", err.Error(), map[string]any{"command": args.Command}), nil, nil
		}
		return jsonResult(res), res, nil
	})
}

// ---------------------------------------------------------------------------
// read_file
// ---------------------------------------------------------------------------

type readFileArgs struct {
	Path string `json:"path" jsonschema:"file path, relative to the primary allowed root or absolute inside any allowed root"`
	// Encoding controls how content comes back. It is a flag the
	// caller SETS (one of three fixed words), never content the
	// caller has to CONSTRUCT — the base64 payload itself, when one
	// is produced, is always built by this server, never typed out
	// by an MCP client. See the package doc for why that distinction
	// matters.
	Encoding string `json:"encoding,omitempty" jsonschema:"how to return the content: one of \"auto\" (default: returns text if the file is valid UTF-8, base64 otherwise), \"text\" (forces UTF-8 text and errors if the file isn't valid UTF-8), or \"base64\" (always base64-encodes the raw bytes)"`
}

// readFileResult always reports which encoding was actually used —
// under "auto" the caller doesn't choose, so it needs telling — and
// carries exactly one of Text/ContentBase64/BlobHandle depending on that
// outcome.
type readFileResult struct {
	Path          string `json:"path"`
	Encoding      string `json:"encoding"` // "text", "base64", or "blob": what was actually used
	SizeBytes     int64  `json:"size_bytes"`
	Text          string `json:"text,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	BlobHandle    string `json:"blob_handle,omitempty"`
	MediaType     string `json:"media_type,omitempty"`
}

func registerReadFile(server *mcp.Server, sb *sandbox.Sandbox, bs *blob.Store) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "read_file",
		Description: "Read the full contents of a file inside an allowed root. Returns UTF-8 text directly by default; " +
			"a file that isn't valid UTF-8 (e.g. a binary file) is automatically returned base64-encoded instead — " +
			"pass encoding=\"base64\" to always get base64, or encoding=\"text\" to require text and error otherwise. " +
			"A binary result larger than the inline threshold comes back as a blob_handle instead of inline base64 — " +
			"pass that handle straight to write_file's blob_handle argument rather than trying to read or reconstruct it.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args readFileArgs) (*mcp.CallToolResult, any, error) {
		refreshClientRoots(ctx, req, sb)
		p, err := sb.Resolve(args.Path)
		if err != nil {
			return errFromErr(err, args.Path), nil, nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return errFromErr(err, args.Path), nil, nil
		}

		requested := args.Encoding
		if requested == "" {
			requested = "auto"
		}

		var actual string
		switch requested {
		case "auto":
			if utf8.Valid(data) {
				actual = "text"
			} else {
				actual = "base64"
			}
		case "text":
			if !utf8.Valid(data) {
				return codedError(codeNotUTF8Text, args.Path, fmt.Sprintf(
					"%s is not valid UTF-8 text; retry with encoding=\"base64\" or encoding=\"auto\"", args.Path,
				), nil), nil, nil
			}
			actual = "text"
		case "base64":
			actual = "base64"
		default:
			return codedError(codeInvalidArgument, args.Path, fmt.Sprintf(
				`unknown encoding %q: must be "auto", "text", or "base64"`, args.Encoding,
			), nil), nil, nil
		}

		out := readFileResult{Path: args.Path, SizeBytes: int64(len(data))}

		if actual == "base64" && len(data) > blobThresholdBytes {
			mediaType := http.DetectContentType(data)
			handle, err := bs.Put(data, mediaType)
			if err != nil {
				return codedError(codeIOError, args.Path, err.Error(), nil), nil, nil
			}
			out.Encoding = "blob"
			out.BlobHandle = handle
			out.MediaType = mediaType
			note := fmt.Sprintf(
				"%s is %d bytes of binary content, over the %d-byte inline threshold; returned as blob handle %s "+
					"(pass this straight to write_file's blob_handle argument, or release_blob to discard it early)",
				args.Path, len(data), blobThresholdBytes, handle,
			)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: note}}}, out, nil
		}

		var wireText string
		if actual == "text" {
			out.Encoding = "text"
			out.Text = string(data)
			wireText = out.Text
		} else {
			out.Encoding = "base64"
			out.ContentBase64 = base64.StdEncoding.EncodeToString(data)
			wireText = out.ContentBase64
		}
		// Content carries the raw payload directly (text, or the
		// base64 string) so it's immediately usable without unwrapping
		// JSON; StructuredContent (the returned out value) carries the
		// same payload plus the path/encoding/size metadata for a
		// caller that wants to branch on it programmatically.
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: wireText}}}, out, nil
	})
}

// ---------------------------------------------------------------------------
// write_file
// ---------------------------------------------------------------------------

type writeFileArgs struct {
	Path string `json:"path" jsonschema:"file path, relative to the primary allowed root or absolute inside any allowed root"`
	// Exactly one of Content / ContentBase64 / BlobHandle may be set.
	// ContentBase64 and BlobHandle both exist for round-tripping a value
	// a prior read_file call (or, for BlobHandle, the blob-handle path
	// specifically) already handed the caller — never for a caller to
	// hand-author fresh base64 text; see the package doc.
	Content       string `json:"content,omitempty" jsonschema:"UTF-8 text content to write"`
	ContentBase64 string `json:"content_base64,omitempty" jsonschema:"base64-encoded raw bytes to write, for binary content (e.g. a blob returned by read_file with encoding=\"base64\"); mutually exclusive with content and blob_handle"`
	BlobHandle    string `json:"blob_handle,omitempty" jsonschema:"a handle previously returned by read_file when its content exceeded the inline size threshold; mutually exclusive with content and content_base64"`
	Append        bool   `json:"append,omitempty" jsonschema:"append instead of overwriting (default: overwrite)"`
}

func registerWriteFile(server *mcp.Server, sb *sandbox.Sandbox, bs *blob.Store) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "write_file",
		Description: "Write (or append to) a file inside an allowed root. The parent directory must already exist; use mkdir first. " +
			"Provide exactly one of content (UTF-8 text), content_base64 (raw bytes, base64-encoded), or blob_handle " +
			"(a handle read_file returned for a large binary file).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args writeFileArgs) (*mcp.CallToolResult, any, error) {
		refreshClientRoots(ctx, req, sb)

		fieldsSet := 0
		if args.Content != "" {
			fieldsSet++
		}
		if args.ContentBase64 != "" {
			fieldsSet++
		}
		if args.BlobHandle != "" {
			fieldsSet++
		}
		if fieldsSet > 1 {
			return codedError(codeBothContentFields, args.Path,
				"provide only one of content, content_base64, or blob_handle", nil), nil, nil
		}

		var data []byte
		switch {
		case args.BlobHandle != "":
			d, _, err := bs.Get(args.BlobHandle)
			if err != nil {
				return codedError(blobErrCode(err), args.Path, err.Error(),
					map[string]any{"handle": args.BlobHandle}), nil, nil
			}
			data = d
		case args.ContentBase64 != "":
			decoded, err := base64.StdEncoding.DecodeString(args.ContentBase64)
			if err != nil {
				return codedError(codeInvalidBase64, args.Path,
					fmt.Sprintf("content_base64 is not valid base64: %v", err), nil), nil, nil
			}
			data = decoded
		default:
			data = []byte(args.Content)
		}

		p, err := sb.Resolve(args.Path)
		if err != nil {
			return errFromErr(err, args.Path), nil, nil
		}
		flags := os.O_CREATE | os.O_WRONLY
		if args.Append {
			flags |= os.O_APPEND
		} else {
			flags |= os.O_TRUNC
		}
		f, err := os.OpenFile(p, flags, 0o644)
		if err != nil {
			return errFromErr(err, args.Path), nil, nil
		}
		defer f.Close()
		if _, err := f.Write(data); err != nil {
			return errFromErr(err, args.Path), nil, nil
		}
		out := map[string]any{"path": args.Path, "bytes_written": len(data)}
		return jsonResult(out), out, nil
	})
}

// ---------------------------------------------------------------------------
// release_blob
// ---------------------------------------------------------------------------

type releaseBlobArgs struct {
	Handle string `json:"handle" jsonschema:"a blob handle previously returned by read_file"`
}

func registerReleaseBlob(server *mcp.Server, bs *blob.Store) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "release_blob",
		Description: "Release a blob handle returned by read_file before it expires on its own, freeing the " +
			"server-held bytes early. Not required — handles expire automatically after a fixed TTL — but worth " +
			"calling once a caller is done relaying a large binary elsewhere within the same task.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args releaseBlobArgs) (*mcp.CallToolResult, any, error) {
		released := bs.Release(args.Handle)
		out := map[string]any{"handle": args.Handle, "released": released}
		return jsonResult(out), out, nil
	})
}

// ---------------------------------------------------------------------------
// copy_file
// ---------------------------------------------------------------------------

type copyFileArgs struct {
	Src       string `json:"src" jsonschema:"source file path, relative to the primary allowed root or absolute inside any allowed root"`
	Dst       string `json:"dst" jsonschema:"destination file path, relative to the primary allowed root or absolute inside any allowed root"`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"overwrite dst if it already exists (default: refuse)"`
}

func registerCopyFile(server *mcp.Server, sb *sandbox.Sandbox) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "copy_file",
		Description: "Copy a file inside an allowed root, entirely server-side. Prefer this over reading a file and " +
			"writing it back out for duplicating or moving binary content: the bytes never pass through the caller, " +
			"so there's no size or encoding concern. Source and destination must both be regular files (no directories) " +
			"inside an allowed root; the destination's parent directory must already exist.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args copyFileArgs) (*mcp.CallToolResult, any, error) {
		refreshClientRoots(ctx, req, sb)

		srcPath, err := sb.Resolve(args.Src)
		if err != nil {
			return errFromErr(err, args.Src), nil, nil
		}
		dstPath, err := sb.Resolve(args.Dst)
		if err != nil {
			return errFromErr(err, args.Dst), nil, nil
		}

		srcInfo, err := os.Stat(srcPath)
		if err != nil {
			return errFromErr(err, args.Src), nil, nil
		}
		if !srcInfo.Mode().IsRegular() {
			return codedError(codeNotAFile, args.Src, fmt.Sprintf("%s is not a regular file", args.Src), nil), nil, nil
		}

		if !args.Overwrite {
			if _, err := os.Stat(dstPath); err == nil {
				return codedError(codePathAlreadyExists, args.Dst,
					fmt.Sprintf("%s already exists; pass overwrite=true to replace it", args.Dst), nil), nil, nil
			} else if !os.IsNotExist(err) {
				return errFromErr(err, args.Dst), nil, nil
			}
		}

		in, err := os.Open(srcPath)
		if err != nil {
			return errFromErr(err, args.Src), nil, nil
		}
		defer in.Close()

		out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode().Perm())
		if err != nil {
			return errFromErr(err, args.Dst), nil, nil
		}
		written, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return errFromErr(copyErr, args.Dst), nil, nil
		}
		if closeErr != nil {
			return errFromErr(closeErr, args.Dst), nil, nil
		}

		result := map[string]any{"src": args.Src, "dst": args.Dst, "bytes_written": written}
		return jsonResult(result), result, nil
	})
}

// ---------------------------------------------------------------------------
// mkdir
// ---------------------------------------------------------------------------

type mkdirArgs struct {
	Path      string `json:"path" jsonschema:"directory path, relative to the primary allowed root or absolute inside any allowed root"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"create parent directories as needed (like mkdir -p)"`
}

func registerMkdir(server *mcp.Server, sb *sandbox.Sandbox) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mkdir",
		Description: "Create a directory inside an allowed root.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args mkdirArgs) (*mcp.CallToolResult, any, error) {
		refreshClientRoots(ctx, req, sb)
		p, err := sb.Resolve(args.Path)
		if err != nil {
			return errFromErr(err, args.Path), nil, nil
		}
		if args.Recursive {
			err = os.MkdirAll(p, 0o755)
		} else {
			err = os.Mkdir(p, 0o755)
		}
		if err != nil {
			return errFromErr(err, args.Path), nil, nil
		}
		out := map[string]any{"path": args.Path, "created": true}
		return jsonResult(out), out, nil
	})
}

// ---------------------------------------------------------------------------
// ls
// ---------------------------------------------------------------------------

type lsArgs struct {
	Path string `json:"path,omitempty" jsonschema:"directory path, relative to the primary allowed root (default: that root)"`
}

type lsEntry struct {
	Name      string `json:"name"`
	Type      string `json:"type"` // "file", "dir", "symlink", "other"
	SizeBytes int64  `json:"size_bytes"`
}

// lsResult wraps the entry list in an object because MCP's
// structuredContent must be a JSON object, not a bare array — a client
// that validates against the spec (as the desktop app's MCP client does)
// rejects a top-level array outright.
type lsResult struct {
	Entries []lsEntry `json:"entries"`
}

func registerLs(server *mcp.Server, sb *sandbox.Sandbox) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ls",
		Description: "List the contents of a directory inside an allowed root.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args lsArgs) (*mcp.CallToolResult, any, error) {
		refreshClientRoots(ctx, req, sb)
		p, err := sb.Resolve(args.Path)
		if err != nil {
			return errFromErr(err, args.Path), nil, nil
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return errFromErr(err, args.Path), nil, nil
		}
		out := make([]lsEntry, 0, len(entries))
		for _, e := range entries {
			info, ierr := e.Info()
			var size int64
			typ := "other"
			switch {
			case ierr != nil:
				// leave as "other" / zero size
			case info.Mode()&os.ModeSymlink != 0:
				typ = "symlink"
			case info.IsDir():
				typ = "dir"
			case info.Mode().IsRegular():
				typ = "file"
				size = info.Size()
			}
			out = append(out, lsEntry{Name: e.Name(), Type: typ, SizeBytes: size})
		}
		res := lsResult{Entries: out}
		return jsonResult(res), res, nil
	})
}

// ---------------------------------------------------------------------------
// rm
// ---------------------------------------------------------------------------

type rmArgs struct {
	Path      string `json:"path" jsonschema:"path to remove, relative to the primary allowed root or absolute inside any allowed root"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"remove directories and their contents recursively (like rm -rf)"`
}

func registerRm(server *mcp.Server, sb *sandbox.Sandbox) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "rm",
		Description: "Remove a file or directory inside an allowed root. Refuses to remove an allowed root itself.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args rmArgs) (*mcp.CallToolResult, any, error) {
		refreshClientRoots(ctx, req, sb)
		p, err := sb.Resolve(args.Path)
		if err != nil {
			return errFromErr(err, args.Path), nil, nil
		}
		for _, r := range sb.Roots() {
			if p == r.Path {
				// A policy-level refusal, not an OS error — PERMISSION_DENIED
				// is the closest fit in the fixed vocabulary ("you may not
				// do this," same as an OS permission bit would say).
				return codedError(codePermissionDenied, args.Path,
					"refusing to remove an allowed root directory itself", nil), nil, nil
			}
		}
		if args.Recursive {
			err = os.RemoveAll(p)
		} else {
			err = os.Remove(p)
		}
		if err != nil {
			return errFromErr(err, args.Path), nil, nil
		}
		out := map[string]any{"path": args.Path, "removed": true}
		return jsonResult(out), out, nil
	})
}

// ---------------------------------------------------------------------------
// list_allowed_dirs
// ---------------------------------------------------------------------------

// allowedDirsResult wraps the roots list in an object for the same reason
// as lsResult above: structuredContent must be a JSON object.
type allowedDirsResult struct {
	Roots []sandbox.Root `json:"roots"`
}

func registerListAllowedDirs(server *mcp.Server, sb *sandbox.Sandbox) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_allowed_dirs",
		Description: "List every directory the server currently allows exec/read_file/write_file/mkdir/ls/rm to touch, " +
			"and whether each came from launch config or from the connected client's MCP roots.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		refreshClientRoots(ctx, req, sb)
		res := allowedDirsResult{Roots: sb.Roots()}
		return jsonResult(res), res, nil
	})
}

// ---------------------------------------------------------------------------
// add_allowed_dir / remove_allowed_dir — only registered when the server
// was started with -allow-runtime-roots.
// ---------------------------------------------------------------------------

type addAllowedDirArgs struct {
	Path string `json:"path" jsonschema:"absolute path of a directory to add to the allow-list; must already exist"`
}

func registerAddAllowedDir(server *mcp.Server, sb *sandbox.Sandbox) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "add_allowed_dir",
		Description: "Add a directory to the set of allowed roots for the rest of this session. " +
			"Only available because this server was started with -allow-runtime-roots. " +
			"The directory must already exist; this cannot be used to escape onto a path outside what the operator running this server intended to expose.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args addAllowedDirArgs) (*mcp.CallToolResult, any, error) {
		abs, err := sb.AddRuntimeRoot(args.Path)
		if err != nil {
			return errFromErr(err, args.Path), nil, nil
		}
		out := map[string]any{"path": abs, "added": true}
		return jsonResult(out), out, nil
	})
}

type removeAllowedDirArgs struct {
	Path string `json:"path" jsonschema:"path of a previously runtime-added directory to remove"`
}

func registerRemoveAllowedDir(server *mcp.Server, sb *sandbox.Sandbox) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "remove_allowed_dir",
		Description: "Remove a directory previously added with add_allowed_dir. " +
			"Cannot remove a root that came from launch config — those are permanent for the life of the process.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args removeAllowedDirArgs) (*mcp.CallToolResult, any, error) {
		removed, err := sb.RemoveRuntimeRoot(args.Path)
		if err != nil {
			return errFromErr(err, args.Path), nil, nil
		}
		out := map[string]any{"path": args.Path, "removed": removed}
		return jsonResult(out), out, nil
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func jsonResult(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return codedError(codeIOError, "", err.Error(), nil)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}
