package tools

import (
	"regexp"
	"strings"
)

// This file classifies shell commands so that approvals can be command-aware:
// known read-only commands run without asking, obviously destructive ones are
// refused outright, and everything else asks. Classification only ever
// relaxes approval for commands it fully understands; anything it cannot parse
// (command substitution, redirection to files, variable expansion, ...) is
// treated as unknown.

// splitShell splits a command line into pipeline segments of words. ok is
// false when the command uses shell features this parser does not model.
func splitShell(cmd string) (segs [][]string, ok bool) {
	var (
		cur    []string
		word   strings.Builder
		inWord bool
		flush  = func() {
			if inWord {
				cur = append(cur, word.String())
				word.Reset()
				inWord = false
			}
		}
		endSeg = func() {
			flush()
			if len(cur) > 0 {
				segs = append(segs, cur)
				cur = nil
			}
		}
	)
	r := []rune(cmd)
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch c {
		case '\'':
			inWord = true
			i++
			for i < len(r) && r[i] != '\'' {
				word.WriteRune(r[i])
				i++
			}
			if i >= len(r) {
				return nil, false
			}
		case '"':
			inWord = true
			i++
			for i < len(r) && r[i] != '"' {
				if r[i] == '$' || r[i] == '`' {
					return nil, false
				}
				if r[i] == '\\' && i+1 < len(r) && strings.ContainsRune(`"\$`+"`", r[i+1]) {
					i++
				}
				word.WriteRune(r[i])
				i++
			}
			if i >= len(r) {
				return nil, false
			}
		case '\\':
			if i+1 >= len(r) {
				return nil, false
			}
			i++
			if r[i] == '\n' {
				continue // line continuation
			}
			inWord = true
			word.WriteRune(r[i])
		case '$', '`', '(', ')', '{', '}', '<':
			if c == '{' || c == '}' {
				// Braces inside a word (brace expansion) are harmless; a
				// stand-alone brace is a command group.
				if inWord {
					word.WriteRune(c)
					continue
				}
			}
			return nil, false
		case '>':
			// Only redirections that discard or duplicate output are allowed.
			flush()
			rest := string(r[i:])
			m := redirectRe.FindString(rest)
			if m == "" {
				return nil, false
			}
			// A leading file-descriptor number was already read as a word.
			if n := len(cur); n > 0 && (cur[n-1] == "1" || cur[n-1] == "2") {
				cur = cur[:n-1]
			}
			i += len([]rune(m)) - 1
		case ';', '\n':
			endSeg()
		case '|':
			endSeg()
			if i+1 < len(r) && r[i+1] == '|' {
				i++
			}
		case '&':
			if i+1 < len(r) && r[i+1] == '&' {
				endSeg()
				i++
				continue
			}
			return nil, false // background job or fd duplication we did not consume
		case ' ', '\t', '\r':
			flush()
		default:
			inWord = true
			word.WriteRune(c)
		}
	}
	endSeg()
	if len(segs) == 0 {
		return nil, false
	}
	for _, s := range segs {
		if isAssignment(s[0]) {
			return nil, false
		}
	}
	return segs, true
}

var (
	redirectRe = regexp.MustCompile(`^>{1,2}\s*(?:/dev/null|&[12])(?:\s|$)`)
	assignRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

func isAssignment(w string) bool { return assignRe.MatchString(w) }

// Class is the classification of a shell command.
type Class int

const (
	ClassUnknown   Class = iota // ask, as before
	ClassSafe                   // read-only commands that can run without asking
	ClassForbidden              // refused without asking
)

// ClassifyShell classifies a command. For ClassForbidden the string is the
// reason. extraForbid are user-configured command prefixes ("git push").
func ClassifyShell(cmd string, extraForbid []string) (Class, string) {
	segs, parsed := splitShell(cmd)
	if reason := forbiddenReason(cmd, segs, parsed, extraForbid); reason != "" {
		return ClassForbidden, reason
	}
	if !parsed {
		return ClassUnknown, ""
	}
	for _, s := range segs {
		if !safeSegment(s) {
			return ClassUnknown, ""
		}
	}
	return ClassSafe, ""
}

// AllSegmentsMatch reports whether the command parses and every segment starts
// with one of the given prefixes, compared word by word (so "git status" does
// not match "git status-x", and "ls" does not match "ls; rm x" as a whole).
func AllSegmentsMatch(cmd string, prefixes []string) bool {
	segs, ok := splitShell(cmd)
	if !ok || len(prefixes) == 0 {
		return false
	}
	for _, s := range segs {
		if !segmentMatches(s, prefixes) {
			return false
		}
	}
	return true
}

func segmentMatches(seg []string, prefixes []string) bool {
	for _, p := range prefixes {
		words := strings.Fields(p)
		if len(words) == 0 || len(words) > len(seg) {
			continue
		}
		match := true
		for i, w := range words {
			if seg[i] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

var (
	pipeToShellRe = regexp.MustCompile(`(?i)\b(curl|wget|fetch)\b[^|;&]*\|\s*(?:sudo\s+)?(?:ba|z|da|k)?sh\b`)
	forkBombRe    = regexp.MustCompile(`:\s*\(\s*\)\s*\{`)
	rootWords     = map[string]bool{"/": true, "/*": true, "~": true, "~/": true, "~/*": true, "$HOME": true, "$HOME/": true, "$HOME/*": true, "${HOME}": true}
	deniedTools   = map[string]bool{
		"sudo": true, "su": true, "doas": true, "shutdown": true, "reboot": true, "halt": true, "poweroff": true,
		"mkfs": true, "fdisk": true, "diskutil": true,
	}
)

// forbiddenReason returns why a command must never run, or "".
func forbiddenReason(cmd string, segs [][]string, parsed bool, extraForbid []string) string {
	if forkBombRe.MatchString(cmd) {
		return "it looks like a fork bomb"
	}
	if pipeToShellRe.MatchString(cmd) {
		return "piping a download into a shell runs unreviewed code"
	}
	if !parsed {
		// Best effort on unparsed commands: look at each word.
		segs = nil
		for _, part := range regexp.MustCompile(`[;&|\n]+`).Split(cmd, -1) {
			if f := strings.Fields(part); len(f) > 0 {
				segs = append(segs, f)
			}
		}
	}
	for _, s := range segs {
		name := baseName(s[0])
		if strings.HasPrefix(name, "mkfs") || deniedTools[name] {
			return name + " is not allowed"
		}
		if name == "rm" && rmTargetsRoot(s[1:]) {
			return "it would delete a root or home directory"
		}
		if name == "dd" {
			for _, a := range s[1:] {
				if strings.HasPrefix(a, "of=/dev/") {
					return "it would overwrite a device"
				}
			}
		}
		if name == "chmod" || name == "chown" {
			recursive := false
			for _, a := range s[1:] {
				if a == "-R" || a == "--recursive" {
					recursive = true
				}
				if recursive && rootWords[a] {
					return name + " -R on a root or home directory is not allowed"
				}
			}
		}
		if segmentMatches(s, extraForbid) {
			return "blocked by policy.shell_forbid_commands"
		}
	}
	return ""
}

func rmTargetsRoot(args []string) bool {
	recursive := false
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			recursive = recursive || a == "--recursive"
			continue
		}
		if strings.HasPrefix(a, "-") {
			recursive = recursive || strings.ContainsAny(a, "rR")
			continue
		}
		if recursive && rootWords[a] {
			return true
		}
	}
	return false
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// readOnlyCommands run without approval when their arguments stay inside the
// project (see argsStayLocal) and none is a flag that writes or executes.
var readOnlyCommands = map[string][]string{ // command → rejected flags
	"ls": nil, "pwd": nil, "cat": nil, "head": nil, "tail": nil, "wc": nil, "stat": nil, "file": nil,
	"du": nil, "tree": {"-o"}, "nl": nil, "tac": nil, "rev": nil, "fold": nil, "column": nil,
	"basename": nil, "dirname": nil, "realpath": nil, "readlink": nil, "which": nil, "whoami": nil,
	"uname": nil, "date": {"-s", "--set"}, "echo": nil, "printf": nil, "true": nil, "false": nil,
	"seq": nil, "id": nil, "diff": nil, "cmp": nil, "cut": nil, "sort": {"-o", "--output"},
	"md5sum": nil, "sha1sum": nil, "sha256sum": nil, "shasum": nil, "cksum": nil, "jq": nil,
	"grep": nil, "egrep": nil, "fgrep": nil,
	"rg":   {"--pre", "--pre-glob", "-z", "--search-zip", "--hostname-bin"},
	"find": {"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls"},
}

// versionCommands are safe with just a version flag.
var versionCommands = map[string]bool{
	"node": true, "python": true, "python3": true, "npm": true, "pnpm": true, "yarn": true, "go": true,
	"cargo": true, "rustc": true, "java": true, "ruby": true, "php": true, "uv": true, "pip": true, "pip3": true, "git": true,
}

var gitReadOnly = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "blame": true, "rev-parse": true,
	"ls-files": true, "ls-tree": true, "describe": true, "shortlog": true, "grep": true,
	"cat-file": true, "rev-list": true, "name-rev": true, "reflog": true, "diff-tree": true,
}

var gitRejectedFlags = []string{"--output", "-o", "--ext-diff", "--textconv", "--exec", "-O", "--open-files-in-pager", "--upload-pack", "--no-index"}

func safeSegment(seg []string) bool {
	name := baseName(seg[0])
	if strings.Contains(seg[0], "/") && !strings.HasPrefix(seg[0], "./") && !strings.HasPrefix(seg[0], "/usr/bin/") && !strings.HasPrefix(seg[0], "/bin/") {
		return false // a path to some other program
	}
	args := seg[1:]
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v" || args[0] == "version") && versionCommands[name] {
		return true
	}
	if !argsStayLocal(args) {
		return false
	}
	switch name {
	case "git":
		return safeGit(args)
	case "go":
		return len(args) >= 1 && (args[0] == "version" || (args[0] == "env" && !hasAny(args, "-w", "-u")) || args[0] == "doc")
	}
	rejected, ok := readOnlyCommands[name]
	if !ok {
		return false
	}
	return !hasAny(args, rejected...)
}

func safeGit(args []string) bool {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return false // global flags such as -c or --exec-path can run programs
	}
	sub, rest := args[0], args[1:]
	if hasAny(rest, gitRejectedFlags...) || hasPrefixAny(rest, "--output=", "--exec=") {
		return false
	}
	switch {
	case gitReadOnly[sub]:
		return true
	case sub == "branch" || sub == "tag":
		for _, a := range rest {
			switch a {
			case "-a", "-r", "-v", "-vv", "--all", "--remotes", "--list", "-l", "--show-current", "--verbose", "-n":
			default:
				return false
			}
		}
		return true
	case sub == "remote":
		return len(rest) == 0 || (len(rest) == 1 && (rest[0] == "-v" || rest[0] == "--verbose"))
	case sub == "stash":
		return len(rest) >= 1 && rest[0] == "list"
	}
	return false
}

// argsStayLocal reports whether no argument points outside the working
// directory by absolute path, "~" or "..". It is lexical: it cannot see
// symlinks, which the host sandbox and project checks handle.
func argsStayLocal(args []string) bool {
	for _, a := range args {
		v := a
		if strings.HasPrefix(v, "-") {
			eq := strings.Index(v, "=")
			if eq < 0 {
				continue
			}
			v = v[eq+1:]
		}
		if v == "/dev/null" {
			continue
		}
		if strings.HasPrefix(v, "/") || strings.HasPrefix(v, "~") {
			return false
		}
		for _, part := range strings.Split(v, "/") {
			if part == ".." {
				return false
			}
		}
	}
	return true
}

func hasAny(args []string, flags ...string) bool {
	for _, a := range args {
		for _, f := range flags {
			if a == f {
				return true
			}
		}
	}
	return false
}

func hasPrefixAny(args []string, prefixes ...string) bool {
	for _, a := range args {
		for _, p := range prefixes {
			if strings.HasPrefix(a, p) {
				return true
			}
		}
	}
	return false
}
