//go:build oracle

// This file cross-validates rgx's translations against a real build
// of the Rust regex crate, the same empirical methodology
// internal/glob's bash_oracle_test.go uses against real bash. It is
// gated behind the "oracle" build tag rather than running as part of
// the normal `go test ./...` (and therefore CI): unlike bash, a Rust
// toolchain plus network access to crates.io is not a safe thing to
// assume every environment running this module's tests has. Run it
// explicitly when touching rgx's translation logic:
//
//	cd internal/rgx/oracle && cargo build --release
//	RGX_ORACLE_BIN=$(pwd)/target/release/rgx_oracle \
//	  go test -tags oracle ./internal/rgx/... -run TestAgainstRustOracle -v
package rgx

import (
	"bufio"
	"encoding/base64"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// oracle wraps a running instance of the compiled rgx_oracle Rust
// binary (see the package doc above), communicating over its
// stdin/stdout one query per line.
type oracle struct {
	cmd    *exec.Cmd
	stdin  *bufio.Writer
	stdout *bufio.Reader
}

func newOracle(t *testing.T) *oracle {
	t.Helper()
	bin := os.Getenv("RGX_ORACLE_BIN")
	if bin == "" {
		t.Skip("RGX_ORACLE_BIN not set; skipping Rust regex crate cross-validation (see package doc for how to build and run it)")
	}
	cmd := exec.Command(bin)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting oracle binary %q: %v", bin, err)
	}
	o := &oracle{cmd: cmd, stdin: bufio.NewWriter(stdinPipe), stdout: bufio.NewReader(stdoutPipe)}
	t.Cleanup(func() {
		stdinPipe.Close()
		_ = cmd.Wait()
	})
	return o
}

// query sends pattern/input to the oracle and reports whether Rust's
// regex crate considers pattern to match input. ok is false if the
// pattern failed to compile in Rust (message describes why). Both
// fields travel base64-encoded (see oracle/src/main.rs) so that no
// byte either one contains can ever be confused with this protocol's
// own tab/newline delimiters.
func (o *oracle) query(t *testing.T, pattern, input string) (matched, ok bool, message string) {
	t.Helper()
	line := base64.StdEncoding.EncodeToString([]byte(pattern)) + "\t" + base64.StdEncoding.EncodeToString([]byte(input)) + "\n"
	if _, err := o.stdin.WriteString(line); err != nil {
		t.Fatalf("writing to oracle: %v", err)
	}
	if err := o.stdin.Flush(); err != nil {
		t.Fatalf("flushing oracle stdin: %v", err)
	}
	resp, err := o.stdout.ReadString('\n')
	if err != nil {
		t.Fatalf("reading oracle response: %v", err)
	}
	resp = strings.TrimSuffix(resp, "\n")
	if strings.HasPrefix(resp, "ERROR:") {
		return false, false, strings.TrimPrefix(resp, "ERROR:")
	}
	return resp == "true", true, ""
}

// TestAgainstRustOracle cross-checks two things against the real
// Rust regex crate: (1) exact-parity syntax — literals, classes,
// quantifiers, groups, alternation, anchors, flags — must behave
// identically between Go's regexp and Rust's regex crate with no
// translation involved; (2) rgx's Unicode-class translations
// (\d \s \w and negations), which are documented as approximations
// for \s/\w, are compared directly against what the untranslated
// pattern does in real Rust, over a battery of Unicode strings.
func TestAgainstRustOracle(t *testing.T) {
	o := newOracle(t)

	inputs := []string{
		"hello", "HELLO", "Hello123", "", "   ", "\t\n",
		"café", "naïve", "日本語", "Привет", "Ελληνικά",
		"a_b-c.d", "foo_bar", "42", "٤٢", "০৯",
		"á", // 'a' + combining acute accent
		" ",  // NBSP
		" ",  // line separator (Zl, not Z-space but Unicode "space-like")
		"user@example.com", "ZWJ‍here",
	}

	t.Run("exact-parity syntax", func(t *testing.T) {
		patterns := []string{
			`foo`, `f.o`, `f?o`, `fo*`, `fo+`, `fo{2,3}`,
			`[abc]+`, `[^abc]+`, `[a-z]+`, `[[:alpha:]]+`, `[[:digit:]]+`,
			`(foo|bar)`, `(?:foo|bar)`, `(?P<name>foo)bar`,
			`(?i)FOO`, `^foo$`, `\Afoo\z`, `foo\b`, `\Bfoo`,
			`a\d+b`, // untranslated: exercises Go's native ASCII \d against Rust's Unicode \d directly
		}
		for _, pattern := range patterns {
			for _, input := range inputs {
				goRe, goErr := Compile(pattern, Options{Unicode: false})
				wantMatch, wantOK, wantMsg := o.query(t, pattern, input)

				if goErr != nil || !wantOK {
					// Both engines reject some of the same constructs
					// (e.g. none of these patterns should actually be
					// rejected by either, given the list above) — if
					// either side errors, just make sure the other
					// side didn't silently disagree by matching.
					if goErr == nil && !wantOK {
						t.Errorf("pattern %q input %q: Go compiled but Rust rejected it: %s", pattern, input, wantMsg)
					}
					if goErr != nil && wantOK {
						t.Errorf("pattern %q input %q: Rust compiled but Go rejected it: %v", pattern, input, goErr)
					}
					continue
				}

				gotMatch := goRe.MatchString(input)
				if gotMatch != wantMatch {
					t.Errorf("pattern %q input %q: Go=%v Rust=%v", pattern, input, gotMatch, wantMatch)
				}
			}
		}
	})

	t.Run("translated \\d exact parity", func(t *testing.T) {
		pattern := `^\d+$`
		for _, input := range inputs {
			goRe, err := Compile(pattern, DefaultOptions())
			if err != nil {
				t.Fatalf("Compile(%q): %v", pattern, err)
			}
			wantMatch, wantOK, wantMsg := o.query(t, pattern, input)
			if !wantOK {
				t.Fatalf("oracle rejected %q: %s", pattern, wantMsg)
			}
			gotMatch := goRe.MatchString(input)
			if gotMatch != wantMatch {
				t.Errorf(`\d input %q: Go=%v Rust=%v`, input, gotMatch, wantMatch)
			}
		}
	})

	t.Run("translated \\w approximation (documented divergence allowed)", func(t *testing.T) {
		pattern := `^\w+$`
		var divergences []string
		for _, input := range inputs {
			goRe, err := Compile(pattern, DefaultOptions())
			if err != nil {
				t.Fatalf("Compile(%q): %v", pattern, err)
			}
			wantMatch, wantOK, wantMsg := o.query(t, pattern, input)
			if !wantOK {
				t.Fatalf("oracle rejected %q: %s", pattern, wantMsg)
			}
			gotMatch := goRe.MatchString(input)
			if gotMatch != wantMatch {
				divergences = append(divergences, input)
			}
		}
		if len(divergences) > 0 {
			t.Logf(`\w approximation diverges from Rust on: %v (expected — see rgx.go package doc)`, divergences)
		}
	})

	t.Run("translated \\s approximation (documented divergence allowed)", func(t *testing.T) {
		pattern := `^\s+$`
		var divergences []string
		for _, input := range inputs {
			goRe, err := Compile(pattern, DefaultOptions())
			if err != nil {
				t.Fatalf("Compile(%q): %v", pattern, err)
			}
			wantMatch, wantOK, wantMsg := o.query(t, pattern, input)
			if !wantOK {
				t.Fatalf("oracle rejected %q: %s", pattern, wantMsg)
			}
			gotMatch := goRe.MatchString(input)
			if gotMatch != wantMatch {
				divergences = append(divergences, input)
			}
		}
		if len(divergences) > 0 {
			t.Logf(`\s approximation diverges from Rust on: %v (expected — see rgx.go package doc)`, divergences)
		}
	})
}
