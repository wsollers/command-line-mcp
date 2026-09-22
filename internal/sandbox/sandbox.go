// Package sandbox confines every filesystem path and exec working
// directory the server touches to an allow-list of root directories.
//
// Roots come from up to three sources, which can be combined:
//
//   - Config roots: given at launch via -root flags / SHELLMCP_ROOTS env,
//     and permanent for the life of the process. Always present; New
//     requires at least one.
//   - Protocol roots: supplied by the connected MCP client via the
//     standard "roots" capability (roots/list). In practice this is
//     usually empty today — the current MCP protocol version forbids
//     server-initiated roots/list requests during a tool call (SEP-2322),
//     so this only ever populates against an older client. Kept for
//     forward/backward compatibility; see the README.
//   - Runtime roots: added and removed one at a time via the
//     add_allowed_dir/remove_allowed_dir tools, which only exist when the
//     server is started with -allow-runtime-roots. This is the mechanism
//     that actually lets a connected client widen the sandbox today.
//
// A path is allowed if it resolves (after following symlinks) inside ANY
// current root. Config roots can never be removed by a tool call, only
// runtime roots can, and only when explicitly enabled — so the boundary
// can't be widened by a prompt-injected tool argument unless the operator
// opted into that at launch.
package sandbox

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Root is one allowed directory plus where it came from, for
// list_allowed_dirs to report.
type Root struct {
	Path   string `json:"path"`
	Source string `json:"source"` // "config" or "client"
}

type Sandbox struct {
	mu            sync.RWMutex
	configRoots   []string // resolved, absolute, permanent
	protocolRoots []string // resolved, absolute; from the MCP roots protocol, replaced wholesale on each sync
	runtimeRoots  []string // resolved, absolute; added/removed one at a time via add_allowed_dir/remove_allowed_dir
	allowRuntime  bool     // whether add_allowed_dir/remove_allowed_dir are permitted at all
}

// New validates and resolves each configured root directory. allowRuntime
// gates whether AddRuntimeRoot/RemoveRuntimeRoot (and so the
// add_allowed_dir/remove_allowed_dir tools) are permitted to change
// anything — it defaults to false at the call site unless the operator
// opts in, since letting any tool caller widen the sandbox is a real
// relaxation of the boundary.
func New(configRoots []string, allowRuntime bool) (*Sandbox, error) {
	if len(configRoots) == 0 {
		return nil, fmt.Errorf("at least one root directory must be configured (SHELLMCP_ROOTS or -root)")
	}
	resolved := make([]string, 0, len(configRoots))
	for _, r := range configRoots {
		abs, err := resolveExistingDir(r)
		if err != nil {
			return nil, fmt.Errorf("root %q: %w", r, err)
		}
		resolved = append(resolved, abs)
	}
	return &Sandbox{configRoots: dedup(resolved), allowRuntime: allowRuntime}, nil
}

// RuntimeAllowed reports whether add_allowed_dir/remove_allowed_dir are
// enabled for this server instance.
func (s *Sandbox) RuntimeAllowed() bool {
	return s.allowRuntime
}

// AddRuntimeRoot validates and adds one directory to the allow-list at
// runtime. It errors if runtime changes are disabled for this server
// instance, or if the directory doesn't exist.
func (s *Sandbox) AddRuntimeRoot(p string) (string, error) {
	if !s.allowRuntime {
		return "", fmt.Errorf("runtime directory changes are disabled on this server; start it with -allow-runtime-roots to enable add_allowed_dir/remove_allowed_dir")
	}
	abs, err := resolveExistingDir(p)
	if err != nil {
		return "", fmt.Errorf("%q: %w", p, err)
	}
	s.mu.Lock()
	if !contains(s.runtimeRoots, abs) {
		s.runtimeRoots = append(s.runtimeRoots, abs)
	}
	s.mu.Unlock()
	return abs, nil
}

// RemoveRuntimeRoot removes one directory previously added via
// AddRuntimeRoot. It only ever removes from the runtime-added set — a
// config or protocol root can never be removed this way. Returns whether
// a matching entry was found.
func (s *Sandbox) RemoveRuntimeRoot(p string) (bool, error) {
	if !s.allowRuntime {
		return false, fmt.Errorf("runtime directory changes are disabled on this server; start it with -allow-runtime-roots to enable add_allowed_dir/remove_allowed_dir")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return false, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved = abs // best effort; if it no longer exists we still allow removing it by its original path
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.runtimeRoots {
		if r == resolved || r == abs {
			s.runtimeRoots = append(s.runtimeRoots[:i], s.runtimeRoots[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func resolveExistingDir(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	return resolved, nil
}

// SetClientRoots replaces the client-supplied root list with the given
// file:// URIs, validating each first. A root that fails to validate
// (doesn't exist, isn't a directory, isn't a file:// URI) is skipped
// rather than failing the whole update, so one bad entry from the client
// doesn't drop every other one.
func (s *Sandbox) SetClientRoots(uris []string) (accepted []string, skipped map[string]string) {
	skipped = map[string]string{}
	resolved := make([]string, 0, len(uris))
	for _, u := range uris {
		p, err := fileURIToPath(u)
		if err != nil {
			skipped[u] = err.Error()
			continue
		}
		abs, err := resolveExistingDir(p)
		if err != nil {
			skipped[u] = err.Error()
			continue
		}
		resolved = append(resolved, abs)
	}
	resolved = dedup(resolved)

	s.mu.Lock()
	s.protocolRoots = resolved
	s.mu.Unlock()

	return resolved, skipped
}

// Roots returns every currently allowed root, tagged with its source:
// "config" (launch-time flag/env), "mcp-roots" (the client's declared MCP
// roots — only ever populated by clients on a protocol version where
// server-initiated roots/list is still permitted, see README), or
// "runtime" (added via the add_allowed_dir tool).
func (s *Sandbox) Roots() []Root {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Root, 0, len(s.configRoots)+len(s.protocolRoots)+len(s.runtimeRoots))
	for _, r := range s.configRoots {
		out = append(out, Root{Path: r, Source: "config"})
	}
	for _, r := range s.protocolRoots {
		out = append(out, Root{Path: r, Source: "mcp-roots"})
	}
	for _, r := range s.runtimeRoots {
		out = append(out, Root{Path: r, Source: "runtime"})
	}
	return out
}

// primaryRoot is where a relative path argument is anchored: the first
// configured root, so relative paths behave predictably regardless of
// what the client has (or hasn't) declared.
func (s *Sandbox) primaryRoot() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.configRoots[0]
}

// Resolve turns a user-supplied path into an absolute path, and errors
// unless that path (after following symlinks) falls inside at least one
// currently allowed root.
func (s *Sandbox) Resolve(p string) (string, error) {
	if p == "" {
		p = "."
	}

	var joined string
	if filepath.IsAbs(p) {
		joined = filepath.Clean(p)
	} else {
		joined = filepath.Clean(filepath.Join(s.primaryRoot(), p))
	}

	if !s.withinAnyRoot(joined) {
		return "", fmt.Errorf("path %q is outside every allowed directory", p)
	}

	// Resolve the deepest existing ancestor's symlinks too, so a symlink
	// that points outside every root is caught even for a not-yet-existing
	// target path (e.g. write_file creating a new file).
	check := joined
	for {
		if resolved, err := filepath.EvalSymlinks(check); err == nil {
			if !s.withinAnyRoot(resolved) {
				return "", fmt.Errorf("path %q escapes every allowed directory via a symlink", p)
			}
			break
		}
		parent := filepath.Dir(check)
		if parent == check {
			break
		}
		check = parent
	}

	return joined, nil
}

func (s *Sandbox) withinAnyRoot(p string) bool {
	for _, r := range s.Roots() {
		rel, err := filepath.Rel(r.Path, p)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, "..") && rel != "..") {
			return true
		}
	}
	return false
}

func fileURIToPath(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URI: %w", err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("unsupported root scheme %q (only file:// is accepted)", u.Scheme)
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/")
	}
	if p == "" {
		return "", fmt.Errorf("empty path in file URI")
	}
	return p, nil
}

func contains(in []string, v string) bool {
	for _, s := range in {
		if s == v {
			return true
		}
	}
	return false
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
