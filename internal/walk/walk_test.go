package walk

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/wsollers/command-line-mcp/internal/sandbox"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func matchPaths(t *testing.T, res Result) []string {
	t.Helper()
	out := make([]string, 0, len(res.Matches))
	for _, m := range res.Matches {
		out = append(out, m.Path)
	}
	sort.Strings(out)
	return out
}

func TestWalkNoPredicateMatchesEverything(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "hello")
	writeFile(t, dir, "sub/b.txt", "world")
	sb, err := sandbox.New([]string{dir}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Walk(Options{RootAbs: dir, Sandbox: sb})
	if err != nil {
		t.Fatal(err)
	}
	got := matchPaths(t, res)
	want := []string{"a.txt", "sub", "sub/b.txt"}
	if len(got) != len(want) {
		t.Fatalf("matches = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("matches[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWalkIsFileExcludesDirectories(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "hello")
	writeFile(t, dir, "sub/b.txt", "world")
	sb, err := sandbox.New([]string{dir}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Walk(Options{RootAbs: dir, Sandbox: sb, Predicate: All{IsFile{}}})
	if err != nil {
		t.Fatal(err)
	}
	got := matchPaths(t, res)
	want := []string{"a.txt", "sub/b.txt"}
	if len(got) != len(want) {
		t.Fatalf("matches = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("matches[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWalkPathGlob(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package a")
	writeFile(t, dir, "b.txt", "not go")
	writeFile(t, dir, "sub/c.go", "package sub")
	sb, err := sandbox.New([]string{dir}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Walk(Options{
		RootAbs:   dir,
		Sandbox:   sb,
		Predicate: All{IsFile{}, PathGlob{Patterns: []string{"**/*.go"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := matchPaths(t, res)
	want := []string{"a.go", "sub/c.go"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("matches = %v, want %v", got, want)
	}
}

func TestWalkContentRegex(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "has.txt", "the quick brown fox")
	writeFile(t, dir, "hasnot.txt", "nothing to see here")
	sb, err := sandbox.New([]string{dir}, false)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\bfox\b`)

	res, err := Walk(Options{
		RootAbs:   dir,
		Sandbox:   sb,
		Predicate: All{IsFile{}, ContentRegex{Re: re}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := matchPaths(t, res)
	if len(got) != 1 || got[0] != "has.txt" {
		t.Errorf("matches = %v, want [has.txt]", got)
	}
}

func TestWalkContentRegexSkipsBinary(t *testing.T) {
	dir := t.TempDir()
	// 0xFF is never valid anywhere in a UTF-8 sequence — see the blob
	// tests' largeBinaryFixture for the same trick, and why an
	// all-zero buffer is the wrong way to construct "invalid UTF-8".
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte{0xff, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.New([]string{dir}, false)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`.`)

	res, err := Walk(Options{
		RootAbs:   dir,
		Sandbox:   sb,
		Predicate: All{IsFile{}, ContentRegex{Re: re}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 0 {
		t.Errorf("matches = %v, want none (binary content should never match content_regex)", matchPaths(t, res))
	}
}

func TestWalkSkipsHiddenEntries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "visible.txt", "x")
	writeFile(t, dir, ".hidden.txt", "x")
	writeFile(t, dir, ".hiddendir/inside.txt", "x")
	sb, err := sandbox.New([]string{dir}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Walk(Options{RootAbs: dir, Sandbox: sb, Predicate: All{IsFile{}}})
	if err != nil {
		t.Fatal(err)
	}
	got := matchPaths(t, res)
	if len(got) != 1 || got[0] != "visible.txt" {
		t.Errorf("matches = %v, want [visible.txt] (hidden files/dirs must be skipped)", got)
	}
}

func TestWalkDoesNotDescendSymlinkedDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "real/inside.txt", "x")
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	sb, err := sandbox.New([]string{dir}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Walk(Options{RootAbs: dir, Sandbox: sb})
	if err != nil {
		t.Fatal(err)
	}
	got := matchPaths(t, res)
	// "link" itself is reported (it resolves inside the sandbox), but
	// nothing under it — "link/inside.txt" must not appear.
	for _, p := range got {
		if p == "link/inside.txt" {
			t.Errorf("matches = %v: walked into a symlinked directory", got)
		}
	}
	foundLink := false
	for _, p := range got {
		if p == "link" {
			foundLink = true
		}
	}
	if !foundLink {
		t.Errorf("matches = %v, want \"link\" itself to be reported", got)
	}
}

func TestWalkSkipsSymlinkOutsideSandbox(t *testing.T) {
	inside := t.TempDir()
	outside := t.TempDir()
	writeFile(t, outside, "secret.txt", "do not read me")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(inside, "escape.txt")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	sb, err := sandbox.New([]string{inside}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Walk(Options{RootAbs: inside, Sandbox: sb, Predicate: All{IsFile{}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 0 {
		t.Errorf("matches = %v, want none (symlink escaping the sandbox must be skipped, not followed)", matchPaths(t, res))
	}
}

func TestWalkMaxResultsTruncates(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeFile(t, dir, filepathFor(i), "x")
	}
	sb, err := sandbox.New([]string{dir}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Walk(Options{RootAbs: dir, Sandbox: sb, Predicate: All{IsFile{}}, MaxResults: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 2 {
		t.Errorf("len(Matches) = %d, want 2", len(res.Matches))
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true")
	}
}

func filepathFor(i int) string {
	return string(rune('a'+i)) + ".txt"
}

func TestWalkRequiresSandbox(t *testing.T) {
	_, err := Walk(Options{RootAbs: t.TempDir()})
	if err == nil {
		t.Error("expected an error when Options.Sandbox is nil")
	}
}
