package rgx

import (
	"regexp"
	"testing"
)

// TestGoUnicodePropertySupport documents, executably, exactly which
// \p{...} names Go's regexp engine accepts. This is the empirical
// basis for translateUnicodeClasses' approximations: Go only resolves
// Unicode general categories (L, N, M, Pc, Nd, Z, ...) and script
// names (Latin, Greek, Han, ...) via \p{...}, never the boolean
// properties (White_Space, Alphabetic, Lowercase, Join_Control, ...)
// the Rust regex crate's \s/\w definitions are actually written in
// terms of. If a future Go release adds support for any of these,
// this test starts failing and translateUnicodeClasses' comment
// (and possibly its substitutions) should be revisited.
func TestGoUnicodePropertySupport(t *testing.T) {
	supported := []string{"L", "N", "M", "Pc", "Nd", "Z", "Latin", "Greek", "Han", "Any"}
	for _, name := range supported {
		if _, err := regexp.Compile(`\p{` + name + `}`); err != nil {
			t.Errorf(`\p{%s} expected to compile, got error: %v`, name, err)
		}
	}

	unsupported := []string{"White_Space", "Alphabetic", "Lowercase", "Uppercase", "Math", "Join_Control", "Emoji", "Word"}
	for _, name := range unsupported {
		if _, err := regexp.Compile(`\p{` + name + `}`); err == nil {
			t.Errorf(`\p{%s} expected to fail to compile (documented Go limitation) but it compiled`, name)
		}
	}
}

func TestCompileBasic(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		opts    Options
		input   string
		want    bool
	}{
		{"literal", "foo", DefaultOptions(), "foobar", true},
		{"literal-no-match", "foo", DefaultOptions(), "bar", false},
		{"case-sensitive-default", "Foo", DefaultOptions(), "foo", false},
		{"ignore-case", "Foo", Options{IgnoreCase: true}, "foo", true},

		{"smart-case-lower-pattern-matches-upper", "foo", Options{SmartCase: true}, "FOO", true},
		{"smart-case-upper-pattern-is-sensitive", "Foo", Options{SmartCase: true}, "foo", false},
		{"smart-case-upper-pattern-matches-exact", "Foo", Options{SmartCase: true}, "Foo", true},
		{"ignore-case-wins-over-smart-case", "Foo", Options{IgnoreCase: true, SmartCase: true}, "foo", true},

		{"fixed-strings-literal-dot", "a.b", Options{FixedStrings: true}, "aXb", false},
		{"fixed-strings-literal-dot-exact", "a.b", Options{FixedStrings: true}, "a.b", true},
		{"fixed-strings-escapes-brackets", "[abc]", Options{FixedStrings: true}, "[abc]", true},

		{"word-regexp-matches-whole-word", "cat", Options{WordRegexp: true}, "a cat sat", true},
		{"word-regexp-rejects-substring", "cat", Options{WordRegexp: true}, "category", false},

		{"line-regexp-matches-whole-line", "abc", Options{LineRegexp: true}, "abc", true},
		{"line-regexp-rejects-partial-line", "abc", Options{LineRegexp: true}, "abcd", false},

		{"multiline-dot-crosses-newline", "a.b", Options{Multiline: true}, "a\nb", true},
		{"non-multiline-dot-stays-in-line", "a.b", Options{}, "a\nb", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			re, err := Compile(c.pattern, c.opts)
			if err != nil {
				t.Fatalf("Compile(%q, %+v) error: %v", c.pattern, c.opts, err)
			}
			got := re.MatchString(c.input)
			if got != c.want {
				t.Errorf("Compile(%q, %+v).MatchString(%q) = %v, want %v", c.pattern, c.opts, c.input, got, c.want)
			}
		})
	}
}

func TestCompileUnicodeClasses(t *testing.T) {
	opts := DefaultOptions() // Unicode: true

	cases := []struct {
		name    string
		pattern string
		input   string
		want    bool
	}{
		{`\d matches ASCII digit`, `\d+`, "42", true},
		{`\d matches non-ASCII digit`, `\d+`, "٤٢", true}, // Arabic-Indic digits, category Nd
		{`\D rejects ASCII digit`, `^\D+$`, "abc", true},
		{`\D rejects non-ASCII digit`, `^\D+$`, "abc٤", false},

		{`\w matches ASCII letter`, `\w+`, "hello", true},
		{`\w matches accented letter`, `\w+`, "café", true},
		{`\w matches CJK`, `\w+`, "日本語", true},
		{`\w matches underscore`, `\w+`, "_foo", true},
		{`\w rejects punctuation-only`, `^\w+$`, "!!!", false},

		{`\s matches space`, `a\sb`, "a b", true},
		{`\s matches tab`, `a\sb`, "a\tb", true},
		{`\s matches NBSP`, "a\\sb", "a b", true},
		{`\S rejects space`, `^\S+$`, "a b", false},
		{`\S matches non-space`, `^\S+$`, "abc", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			re, err := Compile(c.pattern, opts)
			if err != nil {
				t.Fatalf("Compile(%q) error: %v", c.pattern, err)
			}
			got := re.MatchString(c.input)
			if got != c.want {
				t.Errorf("Compile(%q).MatchString(%q) = %v, want %v", c.pattern, c.input, got, c.want)
			}
		})
	}
}

func TestCompileAsciiModeUnchanged(t *testing.T) {
	// With Unicode: false, \w must stay Go's native ASCII-only class
	// and therefore NOT match a CJK character.
	re, err := Compile(`\w+`, Options{Unicode: false})
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	if re.MatchString("日本語") {
		t.Errorf(`ASCII-mode \w unexpectedly matched a CJK string`)
	}
	if !re.MatchString("hello") {
		t.Errorf(`ASCII-mode \w should still match ASCII word chars`)
	}
}

func TestCompileUnsupportedSyntax(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
	}{
		{"class-intersection", "[a-y&&xyz]"},
		{"class-subtraction", "[0-9--4]"},
		{"class-symmetric-difference", "[a-g~~b-h]"},
		{"start-word-boundary", `\b{start}foo`},
		{"end-word-boundary", `foo\b{end}`},
		{"start-half-boundary", `\b{start-half}foo`},
		{"less-than-boundary", `\<foo`},
		{"greater-than-boundary", `foo\>`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Compile(c.pattern, DefaultOptions())
			if err == nil {
				t.Fatalf("Compile(%q): expected an *UnsupportedSyntaxError, got nil", c.pattern)
			}
			var target *UnsupportedSyntaxError
			if !isUnsupportedSyntaxError(err, &target) {
				t.Fatalf("Compile(%q): expected *UnsupportedSyntaxError, got %T: %v", c.pattern, err, err)
			}
		})
	}
}

func isUnsupportedSyntaxError(err error, target **UnsupportedSyntaxError) bool {
	if e, ok := err.(*UnsupportedSyntaxError); ok {
		*target = e
		return true
	}
	return false
}

func TestCompileUnsupportedSyntaxDoesNotFalsePositive(t *testing.T) {
	// Ordinary uses of '-' inside a class, and a class containing a
	// literal '&', '~', or a lone '-' at the edges, must NOT be
	// misdetected as a set operation.
	ok := []string{
		`[a-z]`,
		`[a-z0-9]`,
		`[-abc]`, // literal '-' as first char
		`[abc-]`, // literal '-' as last char
		`[a&b]`,  // single '&', not a set op
		`[a~b]`,  // single '~', not a set op
		`a-b`,    // '-' outside any class at all
	}
	for _, p := range ok {
		if _, err := Compile(p, DefaultOptions()); err != nil {
			t.Errorf("Compile(%q): unexpected error: %v", p, err)
		}
	}
}

func TestCompileFixedStringsSkipsSyntaxCheck(t *testing.T) {
	// A literal search string that happens to contain "&&" must not
	// be rejected when FixedStrings is set — it's not being
	// interpreted as a character class at all.
	re, err := Compile("a[b&&c]d", Options{FixedStrings: true})
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	if !re.MatchString("a[b&&c]d") {
		t.Errorf("fixed-strings pattern failed to match its own literal text")
	}
}
