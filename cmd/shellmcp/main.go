// Command shellmcp is a generic MCP server exposing sandboxed process and
// filesystem primitives: exec (spawn + pipe stdin/stdout/stderr),
// read_file, write_file, mkdir, ls, rm, and list_allowed_dirs.
//
// Every path argument, and exec's working directory, is confined to an
// allow-list of root directories built from two sources:
//
//  1. Launch config: -root flags (repeatable) and/or SHELLMCP_ROOTS (an
//     OS-path-list-separator-delimited string: ':' on Linux/macOS, ';' on
//     Windows). At least one is required.
//  2. The connected client's MCP roots, if it advertises the roots
//     capability: pulled fresh before every tool call (see
//     internal/tools) and merged on top of the config roots — they widen
//     what's allowed to whatever the client's own workspace roots are,
//     they never replace the config roots, and a client that doesn't
//     support roots simply never contributes any.
//
// See internal/sandbox for the actual boundary enforcement.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wsollers/command-line-mcp/internal/sandbox"
	"github.com/wsollers/command-line-mcp/internal/tools"
)

const version = "v0.1.0"

// repeatableFlag collects -root passed multiple times.
type repeatableFlag []string

func (r *repeatableFlag) String() string { return strings.Join(*r, ",") }
func (r *repeatableFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

func main() {
	var roots repeatableFlag
	flag.Var(&roots, "root", "an allowed root directory; repeatable (default: $SHELLMCP_ROOTS)")
	allowRuntime := flag.Bool("allow-runtime-roots", false,
		"expose add_allowed_dir/remove_allowed_dir tools so a connected client can widen/narrow the allow-list at runtime (default: config-only)")
	flag.Parse()

	configRoots := []string(roots)
	if len(configRoots) == 0 {
		if env := os.Getenv("SHELLMCP_ROOTS"); env != "" {
			configRoots = strings.Split(env, string(os.PathListSeparator))
		}
	}
	if len(configRoots) == 0 {
		log.Fatal("shellmcp: no allowed root configured; pass -root (repeatable) or set SHELLMCP_ROOTS")
	}

	sb, err := sandbox.New(configRoots, *allowRuntime)
	if err != nil {
		log.Fatalf("shellmcp: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "shellmcp", Version: version}, nil)
	tools.Register(server, sb)

	for _, r := range sb.Roots() {
		log.Printf("shellmcp: allowed root (%s): %s", r.Source, r.Path)
	}

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("shellmcp: server failed: %v", err)
	}
}
