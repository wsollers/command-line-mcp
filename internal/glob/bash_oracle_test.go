package glob

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestMatchAgainstBash builds a real directory tree and, for a battery of
// patterns, compares this package's Match against actual bash (with
// globstar on and dotglob off — the semantics this package documents
// itself as mimicking) run against that same tree. Skips if bash isn't
// on PATH, so it never fails a platform that simply doesn't have it
// (e.g. a Windows CI runner).
func TestMatchAgainstBash(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found on PATH, skipping cross-check")
	}

	dir := t.TempDir()
	files := []string{
		"main.go",
		"main.ts",
		"README.md",
		"pkg/a.go",
		"pkg/b.go",
		"pkg/sub/c.go",
		"pkg/sub/deep/d.go",
		"pkg/sub/deep/e.ts",
		"other/f.go",
		".hidden.go",
		".hiddendir/g.go",
		"pkg/.hiddensub/h.go",
		"Mixed/Case/File.GO",
		"weird name/x.go",
		"a.1.go",
		"a.2.go",
	}
	for _, f := range files {
		full := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	patterns := []string{
		"*.go",
		"*.{go,ts}",
		"**/*.go",
		"**/*.{go,ts}",
		"pkg/**/*.go",
		"pkg/**",
		"pkg/*/*.go",
		"pkg/sub/**/*.go",
		"[a-z]*.go",
		"[[:upper:]]*/*/*.GO",
		"a.[12].go",
		"a.?.go",
		"*/x.go",
		".*.go",
		"**/.hidden*",
		"other/*",
		"nonexistent/*.go",
		"*.md",
	}

	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			want := bashExpand(t, bashPath, dir, pattern)
			got := ourExpand(t, dir, pattern)
			if !equalSets(want, got) {
				t.Errorf("pattern %q mismatch:\n  bash: %v\n  ours: %v", pattern, want, got)
			}
		})
	}
}

// bashExpand runs pattern through a real bash, with globstar on and
// dotglob off (nullglob on, so a non-matching pattern yields no output
// instead of the literal pattern text — the standard technique for
// making "no matches" unambiguous when comparing expansions).
func bashExpand(t *testing.T, bashPath, dir, pattern string) []string {
	t.Helper()
	// The pattern must be UNQUOTED here — quoting it (single or double)
	// would suppress bash's pathname expansion entirely, which is the
	// exact thing this test needs bash to actually perform. These
	// patterns are fixed test constants, never external input, so
	// interpolating them directly into the script is safe.
	script := "shopt -s globstar nullglob; printf '%s\\n' " + pattern
	cmd := exec.Command(bashPath, "-c", script)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bash failed for pattern %q: %v", pattern, err)
	}
	var results []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		// bash appends a trailing '/' to a directory match as a display
		// convention; this package's Match/Expand don't attach any
		// trailing-slash meaning to paths, so strip it for a fair
		// comparison against our own directory-walk results (which come
		// from filepath.Rel and never carry one).
		line = strings.TrimSuffix(filepath.ToSlash(line), "/")
		results = append(results, line)
	}
	return results
}

// ourExpand walks dir and returns every relative path this package's
// Match considers a match for pattern.
func ourExpand(t *testing.T, dir, pattern string) []string {
	t.Helper()
	var results []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		ok, err := Match(pattern, rel)
		if err != nil {
			t.Fatalf("Match(%q, %q): %v", pattern, rel, err)
		}
		if ok {
			results = append(results, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return results
}

func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
