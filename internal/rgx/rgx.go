// Package rgx compiles ripgrep-flavored patterns into Go's regexp
// (RE2) engine, so tools in this module can accept the same regex
// syntax and flag semantics a ripgrep/Rust-regex user already knows,
// without vendoring or shelling out to an actual Rust toolchain.
//
// Why this works at all: ripgrep's default (non-PCRE2) engine is the
// Rust `regex` crate, which — like Go's regexp — is a RE2 derivative.
// Both guarantee linear-time matching and, as a direct consequence,
// neither supports backreferences or lookaround. The two syntaxes are
// close enough that the large majority of a pattern passes through to
// Go's regexp completely unchanged. This package's job is narrower
// than "implement a regex engine": it (1) rewrites the handful of
// Perl class shorthands whose default meaning differs between the two
// engines, (2) rejects, with a clear error, the small set of
// Rust-regex constructs Go's engine has no equivalent for at all, and
// (3) layers ripgrep's CLI-level pattern semantics (-i, -S, -F, -w,
// -x, -U) on top, the same way ripgrep itself builds those flags on
// top of the regex crate rather than baking them into its syntax.
//
// # Syntax parity
//
// Everything below is identical between Go's regexp and the Rust
// regex crate (both documented as RE2-syntax derivatives) and passes
// through untouched: literals, `.`, `*` `+` `?` `{n,m}` and their lazy
// `?`-suffixed forms, `|` alternation, `(...)` `(?:...)` `(?P<name>...)`
// / `(?<name>...)` groups, `(?flags)` / `(?flags:...)` inline flag
// toggles for `i` `m` `s` (Go additionally supports `U`; the Rust
// crate's `U` (ungreedy) and `x` (verbose/comments) and `R` (CRLF)
// flags are not translated — verbose mode in particular has no Go
// equivalent, so a pattern using `(?x)` should have its whitespace
// and `#` comments stripped by the caller before it reaches this
// package), `^` `$` `\A` `\z` `\b` `\B`, the octal/hex/`\x{...}` escape
// forms, and the ASCII `[[:alpha:]]`-style POSIX classes.
//
// # Translated: Unicode-mode Perl classes
//
// The Rust regex crate's `\d` `\s` `\w` (and negations) are
// Unicode-aware by default: `\d` is `\p{Nd}`, `\s` is
// `\p{White_Space}`, `\w` is `\p{Alphabetic} + \p{M} + \d + \p{Pc} +
// \p{Join_Control}`. Go's `\d` `\s` `\w` are ASCII-only, always,
// with no flag to change that. When Options.Unicode is true (the
// default, matching ripgrep), this package rewrites bare `\d` `\D`
// `\s` `\S` `\w` `\W` — outside of character classes and outside
// `\Q...\E` literal spans — to Go-expressible equivalents:
//
//	\d  ->  \p{Nd}
//	\D  ->  \P{Nd}
//	\w  ->  [\p{L}\p{M}\p{Nd}\p{Pc}]
//	\W  ->  [^\p{L}\p{M}\p{Nd}\p{Pc}]
//	\s  ->  [\t\n\v\f\r \x{85}\p{Z}]
//	\S  ->  [^\t\n\v\f\r \x{85}\p{Z}]
//
// \d/\D are an exact match (both are defined as Unicode general
// category Nd). \w/\W and \s/\S are close approximations, not exact
// ports: Go's regexp only recognizes Unicode general categories and
// script names via \p{...} (confirmed empirically — see rgx_test.go
// TestGoUnicodePropertySupport), not the boolean Unicode properties
// (White_Space, Alphabetic, Join_Control, ...) the Rust crate's
// definitions are actually stated in terms of. \w above omits the two
// Join_Control codepoints (U+200C/U+200D, ZWNJ/ZWJ) and uses the
// general category L in place of the broader Alphabetic property;
// \s above enumerates the ASCII whitespace controls plus NEL (U+0085)
// alongside the Z (separator) category, which covers ordinary
// Unicode whitespace including NBSP but can diverge from
// White_Space at the margins. This divergence was cross-checked
// empirically against a real build of the Rust regex crate across a
// battery of Unicode strings (ASCII, Latin-1 accents, combining
// marks, CJK, connector punctuation, various whitespace forms); see
// the design notes in the repository for the comparison. When exact
// parity matters more than a close approximation, set Options.Unicode
// to false and write \p{...} classes explicitly in the pattern.
//
// With Options.Unicode false, \d \s \w pass through unchanged as
// Go's native ASCII-only classes — this matches neither engine's
// default but is available for callers who want the classic (and
// faster) ASCII behavior.
//
// # Rejected: no Go equivalent
//
// These Rust-regex constructs are detected and rejected with a
// specific error rather than being silently mishandled or left to
// produce a confusing low-level Go parse error:
//
//   - Character class set operations: intersection `&&`, subtraction
//     `--`, symmetric difference `~~` inside `[...]` (e.g. `[a-y&&xyz]`,
//     `[0-9--4]`, `[a-g~~b-h]`). Go's character classes only support
//     union and negation.
//   - The boundary forms `\b{start}`, `\b{end}`, `\b{start-half}`,
//     `\b{end-half}`, `\<`, `\>`. Go's `\b`/`\B` only express a plain
//     word/non-word transition.
//
// Lookaround and backreferences need no special detection: neither
// engine supports them, so a pattern using `(?=...)`, `(?<=...)`, or
// `\1` fails to parse the same way under both, and the caller gets
// Go's own parse error for those.
package rgx

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Options controls how Compile builds a pattern, mirroring the
// ripgrep flags that affect pattern *construction* (as opposed to
// search execution, like context lines or output format, which are a
// concern of whatever tool consumes the compiled *regexp.Regexp and
// out of scope for this package).
type Options struct {
	// IgnoreCase makes matching case-insensitive (ripgrep -i).
	IgnoreCase bool

	// SmartCase makes matching case-insensitive unless the pattern
	// contains at least one uppercase letter, in which case it is
	// case-sensitive (ripgrep -S). If both IgnoreCase and SmartCase
	// are set, IgnoreCase wins (matches ripgrep: -i overrides -S).
	// SmartCase inspects the pattern as written, before any
	// FixedStrings/WordRegexp/LineRegexp wrapping, same as ripgrep.
	SmartCase bool

	// FixedStrings treats the pattern as literal text rather than a
	// regex (ripgrep -F): every character is escaped before any
	// other option is applied.
	FixedStrings bool

	// WordRegexp requires the match to fall on word boundaries
	// (ripgrep -w), equivalent to wrapping the pattern in \b(?:...)\b.
	WordRegexp bool

	// LineRegexp requires the match to span the entire line (ripgrep
	// -x), equivalent to wrapping the pattern in ^(?:...)$. A caller
	// doing line-at-a-time matching gets this for free from Go's
	// normal ^/$ handling; it is provided here so patterns compiled
	// with Multiline behave the same way at each line.
	LineRegexp bool

	// Multiline makes '.' match newlines and allows a single pattern
	// to match across line boundaries (ripgrep -U), by prefixing the
	// compiled pattern with (?s). It does not by itself change ^/$
	// behavior; combine with the caller's own use of (?m) (or write
	// it directly into the pattern) if per-line anchors are wanted
	// over a multi-line haystack.
	Multiline bool

	// Unicode selects Unicode-aware \d \s \w (and negations), matching
	// the Rust regex crate's and ripgrep's default. Defaults to true
	// via DefaultOptions; set false to leave \d \s \w as Go's native
	// ASCII-only classes.
	Unicode bool
}

// DefaultOptions returns the ripgrep-equivalent defaults: case
// sensitive, not fixed-strings, not word/line-anchored, single-line,
// Unicode-aware classes.
func DefaultOptions() Options {
	return Options{Unicode: true}
}

// UnsupportedSyntaxError reports a Rust-regex construct this package
// recognizes but cannot translate into Go's regexp engine.
type UnsupportedSyntaxError struct {
	Pattern string
	Snippet string
	Reason  string
}

func (e *UnsupportedSyntaxError) Error() string {
	return fmt.Sprintf("unsupported regex syntax %q in pattern %q: %s", e.Snippet, e.Pattern, e.Reason)
}

// Compile builds a Go *regexp.Regexp from a ripgrep-flavored pattern
// and options. The returned error is an *UnsupportedSyntaxError for a
// recognized-but-untranslatable construct, or Go's own *syntax.Error
// (wrapped) for anything else regexp.Compile itself rejects —
// including constructs neither engine supports at all, like
// backreferences or lookaround.
func Compile(pattern string, opts Options) (*regexp.Regexp, error) {
	work := pattern

	if opts.FixedStrings {
		work = regexp.QuoteMeta(work)
	} else {
		if err := checkUnsupported(pattern); err != nil {
			return nil, err
		}
		if opts.Unicode {
			work = translateUnicodeClasses(work)
		}
	}

	// SmartCase and IgnoreCase both look at the *original* pattern
	// text, same as ripgrep, so escaping or class translation above
	// never affects the decision. -i wins over -S, matching ripgrep.
	caseInsensitive := opts.IgnoreCase
	if !opts.IgnoreCase && opts.SmartCase {
		caseInsensitive = !hasUpper(pattern)
	}

	if opts.WordRegexp {
		work = `\b(?:` + work + `)\b`
	}
	if opts.LineRegexp {
		work = `^(?:` + work + `)$`
	}

	var flagPrefix string
	if caseInsensitive {
		flagPrefix += "i"
	}
	if opts.Multiline {
		flagPrefix += "s"
	}
	if flagPrefix != "" {
		work = "(?" + flagPrefix + ")" + work
	}

	re, err := regexp.Compile(work)
	if err != nil {
		return nil, fmt.Errorf("compiling translated pattern %q (from %q): %w", work, pattern, err)
	}
	return re, nil
}

// hasUpper reports whether s contains any uppercase letter, Unicode
// letters included (ripgrep's actual smart-case check is Unicode-
// aware, not ASCII-only).
func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// checkUnsupported scans pattern for Rust-regex constructs this
// package cannot translate, returning an *UnsupportedSyntaxError
// naming the first one found. It is a light lexical scan, not a full
// parse: it tracks bracket-class and escape state just well enough to
// avoid false positives on ordinary uses of '-' (e.g. "[a-z]") while
// still catching the distinctive "&&", "--", "~~" set-operation
// tokens and the \b{...}/\</\> boundary forms.
func checkUnsupported(pattern string) error {
	inClass := false
	classStart := -1
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '\\' && i+1 < len(pattern):
			next := pattern[i+1]
			if !inClass {
				switch {
				case next == 'b' && i+2 < len(pattern) && pattern[i+2] == '{':
					return &UnsupportedSyntaxError{pattern, `\b{...}`, "sub-boundary forms (\\b{start}, \\b{end}, \\b{start-half}, \\b{end-half}) have no Go equivalent; use plain \\b or restructure the pattern"}
				case next == '<' || next == '>':
					return &UnsupportedSyntaxError{pattern, `\` + string(next), "the \\< / \\> word-boundary shorthands have no Go equivalent; use \\b"}
				}
			}
			i++ // skip the escaped character
		case !inClass && c == '[':
			inClass = true
			classStart = i
		case inClass && c == ']' && i > classStart+1:
			inClass = false
		case inClass && (c == '&' || c == '-' || c == '~') && i+1 < len(pattern) && pattern[i+1] == c:
			op := string(c) + string(c)
			return &UnsupportedSyntaxError{pattern, op, "character class set operations (intersection &&, subtraction --, symmetric difference ~~) are not supported by Go's regexp; rewrite the class as an explicit union/negation"}
		}
	}
	return nil
}

// translateUnicodeClasses rewrites bare \d \D \s \S \w \W outside of
// character classes and \Q...\E spans into their Unicode-aware
// equivalents (see the package doc for exact substitutions and known
// approximation gaps). Occurrences already inside a [...] class are
// left untouched: composing a bracket expression around one of these
// substitutions (several of which are themselves bracket expressions)
// is not generally valid, and a caller who wants Unicode classes
// inside their own [...] can write \p{...} directly.
func translateUnicodeClasses(pattern string) string {
	var out strings.Builder
	inClass := false
	classStart := -1
	inQuoteLiteral := false
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]

		if inQuoteLiteral {
			out.WriteByte(c)
			if c == '\\' && i+1 < len(pattern) && pattern[i+1] == 'E' {
				out.WriteByte('E')
				i++
				inQuoteLiteral = false
			}
			continue
		}

		if c == '\\' && i+1 < len(pattern) {
			next := pattern[i+1]
			if !inClass && next == 'Q' {
				out.WriteByte(c)
				out.WriteByte(next)
				i++
				inQuoteLiteral = true
				continue
			}
			if !inClass {
				if repl, ok := unicodeClassReplacement(next); ok {
					out.WriteString(repl)
					i++
					continue
				}
			}
			out.WriteByte(c)
			out.WriteByte(next)
			i++
			continue
		}

		if !inClass && c == '[' {
			inClass = true
			classStart = i
		} else if inClass && c == ']' && i > classStart+1 {
			inClass = false
		}
		out.WriteByte(c)
	}
	return out.String()
}

func unicodeClassReplacement(class byte) (string, bool) {
	switch class {
	case 'd':
		return `\p{Nd}`, true
	case 'D':
		return `\P{Nd}`, true
	case 'w':
		return `[\p{L}\p{M}\p{Nd}\p{Pc}]`, true
	case 'W':
		return `[^\p{L}\p{M}\p{Nd}\p{Pc}]`, true
	case 's':
		return `[\t\n\v\f\r \x{85}\p{Z}]`, true
	case 'S':
		return `[^\t\n\v\f\r \x{85}\p{Z}]`, true
	default:
		return "", false
	}
}
