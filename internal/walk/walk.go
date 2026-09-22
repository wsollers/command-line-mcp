// Package walk is the internal Layer-1 recursive treewalker engine
// described in docs/api-spec.md §8: a predicate evaluated against every
// filesystem entry under a root, with matches collected for a caller to
// act on. It is not exposed as an MCP tool on its own (see §8.0) — only
// through the narrow Layer 2 tools (find, replace; internal/tools) that
// build a predicate here from a handful of flat, named arguments rather
// than accepting a predicate tree from a caller.
//
// This is a working subset of the full predicate language §8.2
// describes, scoped to exactly what find and replace need today: the
// is_file predicate, path_glob (internal/glob), content_regex
// (internal/rgx), combined with an all-of-these-match combinator. The
// rest of §8.2/§8.3's vocabulary (any/not beyond what All already gives
// for free, size/mtime/json predicates, mutating actions other than
// replace's own literal/regex substitution, and the generic Layer 3
// tool that would expose this package's shape as JSON input) is adopted
// but not yet built — extending this package's Predicate set is how
// that happens later, not a rewrite of it.
//
// include_hidden (§8.1) is not yet exposed as an option: Walk always
// skips dot-prefixed entries, matching internal/glob's own dotfile rule
// and ripgrep/fd's default behavior.
package walk

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/wsollers/command-line-mcp/internal/glob"
	"github.com/wsollers/command-line-mcp/internal/sandbox"
)

// Entry describes one filesystem entry visited during a walk.
type Entry struct {
	// Path is '/'-separated and relative to the walk's root — never
	// the root itself, which Walk does not evaluate or report.
	Path string
	// Abs is the entry's resolved absolute path on disk (a symlink's
	// target, once sandbox-validated, rather than the link itself).
	Abs string
	// IsDir reports whether the entry (after resolving a symlink, if
	// it is one) is a directory.
	IsDir bool
	// Size is the entry's size in bytes; 0 for directories.
	Size int64
}

// Predicate decides whether an Entry matches. readContent lazily reads
// (and, within one Eval call tree, caches) the entry's content — a
// predicate that never calls it, like a bare PathGlob, never pays for a
// read.
type Predicate interface {
	Eval(e Entry, readContent func() ([]byte, error)) (bool, error)
}

// All matches when every one of its predicates matches. An empty All
// matches everything — this is what a find/replace call with no filters
// at all builds.
type All []Predicate

func (a All) Eval(e Entry, readContent func() ([]byte, error)) (bool, error) {
	for _, p := range a {
		ok, err := p.Eval(e, readContent)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// IsFile matches regular files (or a symlink resolving to one), never
// directories. Spec name: is_file.
type IsFile struct{}

func (IsFile) Eval(e Entry, _ func() ([]byte, error)) (bool, error) {
	return !e.IsDir, nil
}

// PathGlob matches when the entry's Path matches any of Patterns, via
// internal/glob (bash-like matching, '**' globstar, dotfile rule
// included). Spec name: path_glob.
type PathGlob struct{ Patterns []string }

func (p PathGlob) Eval(e Entry, _ func() ([]byte, error)) (bool, error) {
	for _, pat := range p.Patterns {
		ok, err := glob.Match(pat, e.Path)
		if err != nil {
			return false, fmt.Errorf("invalid glob pattern %q: %w", pat, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// ContentRegex matches a file whose content contains at least one match
// of Re. Always false for a directory or for content that isn't valid
// UTF-8 — silently, not an error, since binary content simply has no
// "text" for a text pattern to match. Spec name: content_regex.
type ContentRegex struct{ Re *regexp.Regexp }

func (c ContentRegex) Eval(e Entry, readContent func() ([]byte, error)) (bool, error) {
	if e.IsDir {
		return false, nil
	}
	data, err := readContent()
	if err != nil {
		return false, err
	}
	if !utf8.Valid(data) {
		return false, nil
	}
	return c.Re.Match(data), nil
}

// DefaultMaxResults is used when Options.MaxResults is 0.
const DefaultMaxResults = 1000

// Options controls one walk.
type Options struct {
	// RootAbs is the resolved, sandbox-validated absolute directory to
	// walk — from sandbox.Sandbox.Resolve, never raw caller input.
	RootAbs string
	// Sandbox re-validates every symlink encountered mid-walk, so one
	// pointing outside every allowed root is skipped rather than
	// followed. Required.
	Sandbox *sandbox.Sandbox
	// Predicate is evaluated against every non-hidden entry under the
	// root; nil matches everything.
	Predicate Predicate
	// MaxResults caps how many matching entries are collected; once
	// reached, the walk stops early and Result.Truncated is set. 0
	// means DefaultMaxResults.
	MaxResults int
}

// Match is one entry the walk's predicate accepted.
type Match struct {
	Entry
}

// Result is the outcome of Walk.
type Result struct {
	Matches []Match
	// Truncated is true if MaxResults was reached before the tree
	// under Options.RootAbs was fully visited — see docs/api-spec.md
	// §13; there is no continuation token yet, only this flag.
	Truncated bool
}

// Walk traverses opts.RootAbs, evaluating opts.Predicate against every
// non-hidden file and directory beneath it (never the root itself), in
// deterministic (lexicographic) order within each directory. It never
// descends into a symlinked directory (matching docs/api-spec.md §11),
// and silently skips any symlink whose resolved target falls outside
// every currently allowed sandbox root, or is broken. Walk performs no
// mutation — it is the read-only "find" half of the engine; a caller
// that wants to change matched files (replace) re-reads and rewrites
// them itself using the paths Walk returns.
func Walk(opts Options) (Result, error) {
	if opts.Sandbox == nil {
		return Result{}, errors.New("walk: Options.Sandbox is required")
	}
	if opts.RootAbs == "" {
		return Result{}, errors.New("walk: Options.RootAbs is required")
	}
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}
	pred := opts.Predicate
	if pred == nil {
		pred = All{}
	}

	var res Result

	var walkDir func(dirAbs, relPrefix string) error
	walkDir = func(dirAbs, relPrefix string) error {
		entries, err := os.ReadDir(dirAbs)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

		for _, de := range entries {
			if len(res.Matches) >= maxResults {
				res.Truncated = true
				return nil
			}

			name := de.Name()
			if strings.HasPrefix(name, ".") {
				continue // dotfile rule — see package doc
			}

			absPath := filepath.Join(dirAbs, name)
			relPath := name
			if relPrefix != "" {
				relPath = relPrefix + "/" + name
			}

			isSymlink := de.Type()&fs.ModeSymlink != 0
			isDir := de.IsDir()
			var size int64

			if isSymlink {
				// Re-validate through the sandbox rather than following
				// blindly: a symlink discovered mid-walk can point
				// anywhere on disk, including outside every allowed
				// root, and Resolve is the same check every other tool
				// in this server already relies on.
				resolved, rerr := opts.Sandbox.Resolve(absPath)
				if rerr != nil {
					continue // outside the sandbox: skip, don't report, don't descend
				}
				fi, serr := os.Stat(resolved) // follows the now-validated link
				if serr != nil {
					continue // broken symlink: skip
				}
				isDir = fi.IsDir()
				size = fi.Size()
				absPath = resolved
			} else if !isDir {
				info, ierr := de.Info()
				if ierr != nil {
					continue
				}
				size = info.Size()
			}

			entry := Entry{Path: relPath, Abs: absPath, IsDir: isDir, Size: size}

			var content []byte
			var contentRead bool
			var contentErr error
			readContent := func() ([]byte, error) {
				if !contentRead {
					content, contentErr = os.ReadFile(entry.Abs)
					contentRead = true
				}
				return content, contentErr
			}

			ok, perr := pred.Eval(entry, readContent)
			if perr != nil {
				return perr
			}
			if ok {
				res.Matches = append(res.Matches, Match{entry})
			}

			if isDir {
				if isSymlink {
					continue // never descend into a symlinked directory
				}
				if err := walkDir(absPath, relPath); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err := walkDir(opts.RootAbs, ""); err != nil {
		return Result{}, err
	}
	return res, nil
}
