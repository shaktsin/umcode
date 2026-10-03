package vault

import (
	"bytes"
	"regexp"
)

const mask = "[REDACTED]"

// secretRules are applied in order. Each rule replaces the secret part of a
// match with the mask; surrounding text is preserved byte for byte.
var secretRules = []struct {
	re   *regexp.Regexp
	repl string
}{
	// A key block whose END line was cut off (clipped output) is masked to the end.
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`), mask},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), mask},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`), mask},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), mask},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), mask},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`), mask},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), mask},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), mask},
	{regexp.MustCompile(`(://[^/\s:@]+:)[^@\s/]+@`), "${1}" + mask + "@"},
	{regexp.MustCompile(`(\s-u\s+[^\s:]+:)\S+`), "${1}" + mask},
	{regexp.MustCompile(`(?i)\b(Authorization:\s*(?:(?:Bearer|Basic|Token)\s+)?)\S+`), "${1}" + mask},
	{regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9._~+/=-]{8,}`), "${1}" + mask},
	{regexp.MustCompile(`(?i)(--(?:password|passwd|pass|token|secret|api[-_]?key)(?:=|\s+))(?:"[^"]*"|'[^']*'|\S+)`), "${1}" + mask},
	{regexp.MustCompile(`(?i)(\b[A-Z0-9_]*(?:KEY|TOKEN|SECRET|PASSWORD|PASSWD|PASS)[A-Z0-9_]*["']?\s*[=:]\s*)(?:"[^"]*"|'[^']*'|\S+)`), "${1}" + mask},
}

// Redact masks secrets in s and reports whether anything changed. The input
// is never modified.
func Redact(s []byte) (out []byte, changed bool) {
	out = s
	for _, r := range secretRules {
		next := r.re.ReplaceAll(out, []byte(r.repl))
		if !bytes.Equal(next, out) {
			changed = true
			out = next
		}
	}
	if !changed {
		return s, false
	}
	return out, true
}
