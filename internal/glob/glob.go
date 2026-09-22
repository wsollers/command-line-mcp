// Package glob implements bash-like pathname matching, independent of
// any filesystem access — it operates purely on strings, so it can be
// used both to test one candidate path and, later, as the core
// predicate inside a tree walker.
//
// Supported syntax, matching bash's default interactive behavior (i.e.
// as if `shopt -s globstar` were always on and `shopt -s dotglob` were
// always off, since those are the two shopts a tool's caller has no way
// to set):
//
//   - '*'  matches any sequence of characters within one path segment
//   - '?'  matches any single character within one path segment
//   - '[...]' a POSIX bracket expression: literal characters, ranges
//     ('a-z'), negation with a leading '^' or '!', and named classes
//     ('[:alpha:]', '[:digit:]', '[:alnum:]', '[:upper:]', '[:lower:]',
//     '[:space:]', '[:blank:]', '[:punct:]', '[:cntrl:]', '[:graph:]',
//     '[:print:]', '[:xdigit:]' — matched against the ASCII/C locale;
//     this package is not locale-aware)
//   - '**' as an entire path segment matches zero or more path segments
//     — e.g. "a/**/b" matches "a/b", "a/x/b", "a/x/y/b", ... This is
//     always enabled; there is no way to ask for bash's non-globstar
//     '**' (which behaves the same as a single '*' within one segment).
//     A '**' that is not the entire segment (e.g. "a**b") is treated as
//     an ordinary run of '*' within that one segment, same as bash.
//   - '{a,b,c}' brace expansion, including nesting (e.g. "*.{go,{ts,tsx}}").
//     A brace group is only expanded if it contains at least one
//     top-level comma — "{foo}" is left as the literal text "{foo}",
//     matching bash. Numeric/alpha range expansion ("{1..5}", "{a..z}")
//     is not implemented.
//   - '\\' escapes the next character, making it literal.
//
// Dotfile rule: a name segment that starts with '.' is only matched by
// a pattern segment that itself starts with a literal '.' — this is
// bash's default (dotglob off) behavior, and applies to '**' as well
// (globstar does not descend into a segment starting with '.' unless
// the surrounding pattern explicitly asks for it).
package glob

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Match reports whether name matches pattern. Both are '/'-separated;
// leading, trailing, and repeated slashes are ignored in both. Match
// does not touch the filesystem.
func Match(pattern, name string) (bool, error) {
	nameSegs := splitSegments(name)
	var lastErr error
	for _, alt := range Expand(pattern) {
		patSegs := splitSegments(alt)
		ok, err := matchSegs(patSegs, nameSegs, newMemo(len(patSegs), len(nameSegs)))
		if err != nil {
			lastErr = err
			continue
		}
		if ok {
			return true, nil
		}
	}
	return false, lastErr
}

func splitSegments(p string) []string {
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// memo avoids the exponential blowup that a naive recursive '**'
// matcher can hit when a pattern contains several globstars: each
// (pattern-index, name-index) pair is resolved at most once.
type memo struct {
	seen   map[[2]int]bool
	result map[[2]int]bool
}

func newMemo(patLen, nameLen int) *memo {
	return &memo{seen: make(map[[2]int]bool, patLen*nameLen), result: make(map[[2]int]bool, patLen*nameLen)}
}

func matchSegs(pat, name []string, m *memo) (bool, error) {
	key := [2]int{len(pat), len(name)}
	if m.seen[key] {
		return m.result[key], nil
	}

	ok, err := matchSegsUncached(pat, name, m)
	if err == nil {
		m.seen[key] = true
		m.result[key] = ok
	}
	return ok, err
}

func matchSegsUncached(pat, name []string, m *memo) (bool, error) {
	if len(pat) == 0 {
		return len(name) == 0, nil
	}

	if pat[0] == "**" {
		// Try consuming zero segments under the globstar first.
		if ok, err := matchSegs(pat[1:], name, m); err != nil || ok {
			return ok, err
		}
		if len(name) == 0 {
			return false, nil
		}
		// Globstar does not descend into a dotfile/dot-directory by
		// default, same as any other unmatched leading '.' segment.
		if strings.HasPrefix(name[0], ".") {
			return false, nil
		}
		return matchSegs(pat, name[1:], m)
	}

	if len(name) == 0 {
		return false, nil
	}

	ok, err := matchSegment(pat[0], name[0])
	if err != nil || !ok {
		return false, err
	}
	return matchSegs(pat[1:], name[1:], m)
}

// matchSegment matches a single path segment (no '/') against a single
// pattern segment (no '/', and not the literal "**").
func matchSegment(pattern, name string) (bool, error) {
	if name == "." || name == ".." {
		return false, nil
	}
	if strings.HasPrefix(name, ".") && !strings.HasPrefix(pattern, ".") {
		return false, nil
	}
	translated, err := translate(pattern)
	if err != nil {
		return false, err
	}
	ok, err := filepath.Match(translated, name)
	if err != nil {
		return false, fmt.Errorf("bad pattern %q: %w", pattern, err)
	}
	return ok, nil
}

// translate rewrites bracket-expression syntax that bash accepts but
// path/filepath.Match does not: '!' as a negation marker (Match only
// understands '^'), and POSIX named classes ('[:alpha:]' and friends).
// Everything outside brackets, and the '*'/'?'/'\\' operators
// path/filepath.Match already implements identically to bash within a
// single path segment, is passed through unchanged.
func translate(pattern string) (string, error) {
	var out strings.Builder
	i := 0
	for i < len(pattern) {
		c := pattern[i]
		switch c {
		case '\\':
			out.WriteByte(c)
			i++
			if i < len(pattern) {
				out.WriteByte(pattern[i])
				i++
			}
		case '[':
			end, translated, err := translateBracket(pattern, i)
			if err != nil {
				return "", err
			}
			out.WriteString(translated)
			i = end
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String(), nil
}

// translateBracket translates the bracket expression starting at
// pattern[start] (which must be '['), returning the index just past its
// closing ']' and the translated text (including the surrounding '['
// and ']').
func translateBracket(pattern string, start int) (int, string, error) {
	i := start + 1
	var body strings.Builder
	body.WriteByte('[')

	if i < len(pattern) && (pattern[i] == '!' || pattern[i] == '^') {
		body.WriteByte('^')
		i++
	}
	// A ']' immediately after '[' or '[!'/'[^' is a literal ']', per
	// POSIX bracket expression rules, not the closing bracket.
	if i < len(pattern) && pattern[i] == ']' {
		body.WriteString(`\]`)
		i++
	}

	closed := false
	for i < len(pattern) {
		if pattern[i] == ']' {
			closed = true
			i++
			break
		}
		if strings.HasPrefix(pattern[i:], "[:") {
			if end, ok := writePosixClass(pattern, i, &body); ok {
				i = end
				continue
			}
		}
		if pattern[i] == '\\' && i+1 < len(pattern) {
			body.WriteByte('\\')
			body.WriteByte(pattern[i+1])
			i += 2
			continue
		}
		// filepath.Match treats '\\' specially even inside a bracket
		// expression; escape any literal backslash from the input so
		// it isn't misread as the start of an escape sequence.
		if pattern[i] == '\\' {
			body.WriteString(`\\`)
			i++
			continue
		}
		body.WriteByte(pattern[i])
		i++
	}
	if !closed {
		return 0, "", fmt.Errorf("bad pattern %q: unterminated bracket expression", pattern)
	}
	body.WriteByte(']')
	return i, body.String(), nil
}

var posixClasses = map[string]string{
	"alpha":  "a-zA-Z",
	"digit":  "0-9",
	"alnum":  "a-zA-Z0-9",
	"upper":  "A-Z",
	"lower":  "a-z",
	"xdigit": "0-9a-fA-F",
	"space":  "\\ \\\t\\\n\\\r\\\f\\\v",
	"blank":  "\\ \\\t",
	"cntrl":  "\x01-\x1f\\\x7f",
	"graph":  "\x21-\x7e",
	"print":  "\x20-\x7e",
	"punct":  "\\!\\\"\\#\\$\\%\\&\\'\\(\\)\\*\\+\\,\\-\\.\\/\\:\\;\\<\\=\\>\\?\\@\\[\\\\\\]\\^\\_\\`\\{\\|\\}\\~",
}

// writePosixClass writes the translation of a "[:name:]" class found at
// pattern[i:] (i pointing at the leading '['), if it is one. It reports
// the index just past the closing ']' and whether a class was actually
// recognized there (a plain "[:" that isn't a real class falls through
// to ordinary character handling).
func writePosixClass(pattern string, i int, body *strings.Builder) (int, bool) {
	end := strings.Index(pattern[i:], ":]")
	if end < 0 {
		return 0, false
	}
	name := pattern[i+2 : i+end]
	expansion, ok := posixClasses[name]
	if !ok {
		return 0, false
	}
	body.WriteString(expansion)
	return i + end + 2, true
}

// Expand performs brace expansion only (no other glob semantics),
// returning every literal alternative pattern produces. A pattern with
// no brace groups returns a single-element slice containing it
// unchanged.
func Expand(pattern string) []string {
	open := findUnescaped(pattern, '{', 0)
	if open < 0 {
		return []string{pattern}
	}
	closeIdx, alts := splitBraceGroup(pattern, open)

	if closeIdx < 0 {
		// Unterminated: no matching '}' was ever found, so bash treats
		// everything from '{' to the end of the string as literal text
		// (there is nothing valid left to keep scanning for — any
		// '{'/'}' pair inside was already absorbed into the failed
		// nesting search).
		return []string{pattern}
	}

	if len(alts) < 2 {
		// A well-formed but comma-less group, e.g. "{foo}": bash leaves
		// it as literal text (including its braces), but expansion
		// continues on whatever follows it.
		prefix := pattern[:closeIdx+1]
		out := make([]string, 0, 1)
		for _, r := range Expand(pattern[closeIdx+1:]) {
			out = append(out, prefix+r)
		}
		return out
	}

	prefix := pattern[:open]
	suffix := pattern[closeIdx+1:]
	suffixExpansions := Expand(suffix)

	var out []string
	for _, alt := range alts {
		for _, altExpanded := range Expand(alt) {
			for _, suf := range suffixExpansions {
				out = append(out, prefix+altExpanded+suf)
			}
		}
	}
	return out
}

// splitBraceGroup, given the index of an unescaped '{' in pattern,
// returns the index of its matching unescaped '}' (-1 if none) and the
// top-level comma-separated alternatives between them (nil if the group
// contains no top-level comma).
func splitBraceGroup(pattern string, open int) (int, []string) {
	depth := 0
	var alts []string
	last := open + 1
	i := open
	for i < len(pattern) {
		switch pattern[i] {
		case '\\':
			i++ // skip the escaped character too
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				alts = append(alts, pattern[last:i])
				return i, alts
			}
		case ',':
			if depth == 1 {
				alts = append(alts, pattern[last:i])
				last = i + 1
			}
		}
		i++
	}
	return -1, nil
}

func findUnescaped(s string, c byte, from int) int {
	for i := from; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == c {
			return i
		}
	}
	return -1
}
