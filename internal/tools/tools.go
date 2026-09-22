// Package tools registers shellmcp's MCP tools: exec, read_file,
// write_file, mkdir, ls, rm, and list_allowed_dirs.
package tools

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wsollers/command-line-mcp/internal/process"
	"github.com/wsollers/command-line-mcp/internal/sandbox"
)

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
	registerExec(server, sb)
	registerReadFile(server, sb)
	registerWriteFile(server, sb)
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
			return errResult("command must not be empty"), nil, nil
		}

		cwd, err := sb.Resolve(args.Cwd)
		if err != nil {
			return errResult(err.Error()), nil, nil
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
			return errResult(err.Error()), nil, nil
		}
		return jsonResult(res), res, nil
	})
}

// ---------------------------------------------------------------------------
// read_file
// ---------------------------------------------------------------------------

type readFileArgs struct {
	Path string `json:"path" jsonschema:"file path, relative to the primary allowed root or absolute inside any allowed root"`
}

func registerReadFile(server *mcp.Server, sb *sandbox.Sandbox) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_file",
		Description: "Read the full contents of a text file inside an allowed root.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args readFileArgs) (*mcp.CallToolResult, any, error) {
		refreshClientRoots(ctx, req, sb)
		p, err := sb.Resolve(args.Path)
		if err != nil {
			return errResult(err.Error()), nil, nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return errResult(err.Error()), nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
	})
}

// ---------------------------------------------------------------------------
// write_file
// ---------------------------------------------------------------------------

type writeFileArgs struct {
	Path    string `json:"path" jsonschema:"file path, relative to the primary allowed root or absolute inside any allowed root"`
	Content string `json:"content" jsonschema:"text content to write"`
	Append  bool   `json:"append,omitempty" jsonschema:"append instead of overwriting (default: overwrite)"`
}

func registerWriteFile(server *mcp.Server, sb *sandbox.Sandbox) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "write_file",
		Description: "Write (or append to) a text file inside an allowed root. The parent directory must already exist; use mkdir first.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args writeFileArgs) (*mcp.CallToolResult, any, error) {
		refreshClientRoots(ctx, req, sb)
		p, err := sb.Resolve(args.Path)
		if err != nil {
			return errResult(err.Error()), nil, nil
		}
		flags := os.O_CREATE | os.O_WRONLY
		if args.Append {
			flags |= os.O_APPEND
		} else {
			flags |= os.O_TRUNC
		}
		f, err := os.OpenFile(p, flags, 0o644)
		if err != nil {
			return errResult(err.Error()), nil, nil
		}
		defer f.Close()
		if _, err := f.WriteString(args.Content); err != nil {
			return errResult(err.Error()), nil, nil
		}
		out := map[string]any{"path": args.Path, "bytes_written": len(args.Content)}
		return jsonResult(out), out, nil
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
			return errResult(err.Error()), nil, nil
		}
		if args.Recursive {
			err = os.MkdirAll(p, 0o755)
		} else {
			err = os.Mkdir(p, 0o755)
		}
		if err != nil {
			return errResult(err.Error()), nil, nil
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
			return errResult(err.Error()), nil, nil
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return errResult(err.Error()), nil, nil
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
			return errResult(err.Error()), nil, nil
		}
		for _, r := range sb.Roots() {
			if p == r.Path {
				return errResult("refusing to remove an allowed root directory itself"), nil, nil
			}
		}
		if args.Recursive {
			err = os.RemoveAll(p)
		} else {
			err = os.Remove(p)
		}
		if err != nil {
			return errResult(err.Error()), nil, nil
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
			return errResult(err.Error()), nil, nil
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
			return errResult(err.Error()), nil, nil
		}
		out := map[string]any{"path": args.Path, "removed": removed}
		return jsonResult(out), out, nil
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

func jsonResult(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResult(err.Error())
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}
