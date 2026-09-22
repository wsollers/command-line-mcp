package glob

import (
	"reflect"
	"sort"
	"testing"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		// literal
		{"foo.go", "foo.go", true},
		{"foo.go", "bar.go", false},

		// '*' and '?'
		{"*.go", "main.go", true},
		{"*.go", "main.ts", false},
		{"*.go", "sub/main.go", false}, // '*' does not cross '/'
		{"?oo.go", "foo.go", true},
		{"?oo.go", "fooo.go", false},
		{"*", "anything", true},
		{"*", "a/b", false},

		// character classes
		{"[abc].go", "a.go", true},
		{"[abc].go", "d.go", false},
		{"[a-c].go", "b.go", true},
		{"[a-c].go", "d.go", false},
		{"[^a-c].go", "d.go", true},
		{"[^a-c].go", "a.go", false},
		{"[!a-c].go", "d.go", true}, // '!' negation, bash-ism Go doesn't support natively
		{"[!a-c].go", "a.go", false},
		{"[]a].go", "].go", true}, // literal ']' as first char in class
		{"[[:digit:]].go", "5.go", true},
		{"[[:digit:]].go", "a.go", false},
		{"[[:alpha:]][[:digit:]].go", "a5.go", true},
		{"[[:upper:]]*.go", "Foo.go", true},
		{"[[:upper:]]*.go", "foo.go", false},

		// escaping
		{`\*.go`, "*.go", true},
		{`\*.go`, "x.go", false},
		{`a\[b\].go`, "a[b].go", true},

		// dotfile rule
		{"*.go", ".hidden.go", false},
		{".*.go", ".hidden.go", true},
		{"*", ".", false},
		{"*", "..", false},

		// globstar
		{"**", "a", true},
		{"**", "a/b/c", true},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/y/c", false},
		{"**/*.go", "main.go", true},
		{"**/*.go", "pkg/sub/main.go", true},
		{"**/*.go", "pkg/sub/main.ts", false},
		{"pkg/**", "pkg/a/b/c.go", true},
		{"pkg/**", "other/a.go", false},
		{"**/*.go", "pkg/.hidden/main.go", false}, // globstar skips dot-dirs by default
		{"a**b.go", "aXYb.go", true},              // "**" not alone in a segment == ordinary '*'

		// brace expansion
		{"*.{go,ts}", "main.go", true},
		{"*.{go,ts}", "main.ts", true},
		{"*.{go,ts}", "main.py", false},
		{"*.{go,{ts,tsx}}", "main.tsx", true},
		{"{foo}.go", "{foo}.go", true}, // no comma: literal braces
		{"{foo}.go", "foo.go", false},
		{"{a,b}{c,d}", "ac", true},
		{"{a,b}{c,d}", "bd", true},
		{"{a,b}{c,d}", "ad", true},
		{"{a,b}{c,d}", "ae", false},
	}

	for _, c := range cases {
		got, err := Match(c.pattern, c.name)
		if err != nil {
			t.Errorf("Match(%q, %q) unexpected error: %v", c.pattern, c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestExpand(t *testing.T) {
	cases := []struct {
		pattern string
		want    []string
	}{
		{"plain", []string{"plain"}},
		{"*.{go,ts}", []string{"*.go", "*.ts"}},
		{"{foo}", []string{"{foo}"}},
		{"{a,b,c}", []string{"a", "b", "c"}},
		{"{a,{b,c}}", []string{"a", "b", "c"}},
		{"x{a,b}y{c,d}z", []string{"xaycz", "xaydz", "xbycz", "xbydz"}},
		{"{unterminated", []string{"{unterminated"}},
		{`\{a,b\}`, []string{`\{a,b\}`}},
	}

	for _, c := range cases {
		got := Expand(c.pattern)
		sort.Strings(got)
		want := append([]string(nil), c.want...)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Expand(%q) = %v, want %v", c.pattern, got, want)
		}
	}
}

func TestMatchBadPattern(t *testing.T) {
	_, err := Match("[unterminated", "x")
	if err == nil {
		t.Errorf("Match with an unterminated bracket expression: expected an error, got nil")
	}
}
