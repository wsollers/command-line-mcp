// Oracle harness: reads base64(pattern)\tbase64(input) lines from
// stdin, writes true/false/ERROR:<msg> for each, one per line. Both
// fields are base64-encoded specifically so that arbitrary bytes in
// either one (newlines, tabs, anything) can never collide with the
// line- and field-delimiters of this wire protocol itself — an
// earlier plain-text version of this harness had exactly that bug
// (a literal tab or newline inside a test input was indistinguishable
// from the protocol's own delimiters), which produced false
// mismatches that were about this harness, not about the regex
// engines being compared.
use base64::{engine::general_purpose::STANDARD, Engine as _};
use regex::Regex;
use std::io::{self, BufRead, Write};

fn main() {
    let stdin = io::stdin();
    let stdout = io::stdout();
    let mut out = stdout.lock();
    for line in stdin.lock().lines() {
        let line = line.unwrap();
        let mut parts = line.splitn(2, '\t');
        let pattern_b64 = parts.next().unwrap_or("");
        let input_b64 = parts.next().unwrap_or("");

        let pattern = match STANDARD.decode(pattern_b64).ok().and_then(|b| String::from_utf8(b).ok()) {
            Some(p) => p,
            None => {
                writeln!(out, "ERROR:bad base64/utf8 in pattern field").unwrap();
                continue;
            }
        };
        let input = match STANDARD.decode(input_b64).ok().and_then(|b| String::from_utf8(b).ok()) {
            Some(i) => i,
            None => {
                writeln!(out, "ERROR:bad base64/utf8 in input field").unwrap();
                continue;
            }
        };

        match Regex::new(&pattern) {
            Ok(re) => writeln!(out, "{}", re.is_match(&input)).unwrap(),
            Err(e) => writeln!(out, "ERROR:{}", e.to_string().replace('\n', " ")).unwrap(),
        }
    }
}
