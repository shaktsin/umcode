package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	searchDefaultMax = 100
	searchHardMax    = 500
	searchMaxFile    = 2 << 20
	searchMaxLine    = 300
	searchTimeLimit  = 20 * time.Second
)

// searchSkipDirs are not descended into unless the search starts inside one.
var searchSkipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true, "__pycache__": true, ".venv": true,
}

// ---- file.search ----

type fileSearch struct{ ws *Workspaces }

func (*fileSearch) Name() string { return "file.search" }
func (*fileSearch) Description() string {
	return "Search the open project's files. With a pattern, returns matching lines as path:line: text (regular expression, or literal text with literal=true). " +
		"With only a glob, lists matching file paths. glob filters files, e.g. \"*.go\" or \"src/**/*.ts\". " +
		"Skips .git, node_modules, .venv, binary and very large files. Works without the shell."
}
func (*fileSearch) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"pattern":{"type":"string","description":"Regular expression (Go syntax) to find in file contents; omit to list files by glob"},"literal":{"type":"boolean","description":"Treat pattern as plain text"},"case_insensitive":{"type":"boolean"},"path":{"type":"string","description":"Folder to search, relative to the project root; default the root"},"glob":{"type":"string","description":"Only files matching this glob; without a slash it matches the file name"},"context":{"type":"integer","description":"Lines of context around each match, 0-5"},"max_results":{"type":"integer","description":"Default 100, max 500"},"workspace":{"type":"string"}}}`)
}
func (*fileSearch) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct{ Pattern, Glob string }](args)
	what := a.Pattern
	if what == "" {
		what = a.Glob
	}
	return RiskGreen, "Search for " + what
}

type searchArgs struct {
	Pattern         string `json:"pattern"`
	Literal         bool   `json:"literal"`
	CaseInsensitive bool   `json:"case_insensitive"`
	Path            string `json:"path"`
	Glob            string `json:"glob"`
	Context         int    `json:"context"`
	MaxResults      int    `json:"max_results"`
	Workspace       string `json:"workspace"`
}

func (t *fileSearch) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[searchArgs](args)
	if err != nil {
		return "", err
	}
	if a.Pattern == "" && a.Glob == "" {
		return "", errors.New("give a pattern to search file contents, a glob to list files, or both")
	}
	root, err := resolvePath(ctx, t.ws, a.Workspace, a.Path, OpRead)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s is not a folder", a.Path)
	}
	rel := func(p string) string { return filepath.ToSlash(relTo(root, p)) }
	if scope := ScopeFrom(ctx); scope != nil {
		rel = func(p string) string { return scope.Rel(p) }
	}
	var re *regexp.Regexp
	if a.Pattern != "" {
		expr := a.Pattern
		if a.Literal {
			expr = regexp.QuoteMeta(expr)
		}
		if a.CaseInsensitive {
			expr = "(?i)" + expr
		}
		if re, err = regexp.Compile(expr); err != nil {
			return "", fmt.Errorf("invalid pattern: %w", err)
		}
	}
	var glob *regexp.Regexp
	globByName := false
	if a.Glob != "" {
		if glob, err = globRegexp(a.Glob); err != nil {
			return "", fmt.Errorf("invalid glob: %w", err)
		}
		globByName = !strings.Contains(a.Glob, "/")
	}
	max := a.MaxResults
	if max <= 0 {
		max = searchDefaultMax
	}
	if max > searchHardMax {
		max = searchHardMax
	}
	lines := a.Context
	if lines < 0 {
		lines = 0
	}
	if lines > 5 {
		lines = 5
	}

	deadline := time.Now().Add(searchTimeLimit)
	var out strings.Builder
	results, files := 0, 0
	truncated := ""
	stop := errors.New("stop")
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			truncated = "the search time limit was reached"
			return stop
		}
		if d.IsDir() {
			if p != root && searchSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() { // symlinks, sockets, devices
			return nil
		}
		if glob != nil {
			target := relTo(root, p)
			if globByName {
				target = d.Name()
			}
			if !glob.MatchString(filepath.ToSlash(target)) {
				return nil
			}
		}
		if re == nil { // list files
			fmt.Fprintf(&out, "%s\n", rel(p))
			files++
			results++
			if results >= max {
				truncated = fmt.Sprintf("stopped at %d results", max)
				return stop
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > searchMaxFile {
			return nil
		}
		matches, err := searchFile(p, re, lines)
		if err != nil || len(matches) == 0 {
			return nil
		}
		files++
		for _, m := range matches {
			if m.raw {
				fmt.Fprintf(&out, "%s\n", m.text)
			} else {
				fmt.Fprintf(&out, "%s%s\n", rel(p), m.text)
			}
			if m.hit {
				results++
			}
		}
		if results >= max {
			truncated = fmt.Sprintf("stopped at %d matches", max)
			return stop
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, stop) {
		return "", walkErr
	}
	if results == 0 {
		return "no matches", nil
	}
	summary := fmt.Sprintf("%d match(es) in %d file(s)", results, files)
	if re == nil {
		summary = fmt.Sprintf("%d file(s)", files)
	}
	if truncated != "" {
		summary += "; " + truncated + " — narrow the search with path or glob"
	}
	return clip(out.String() + "\n" + summary), nil
}

type searchLine struct {
	text string // ":<line>: <text>" for a hit, "-<line>- <text>" for context
	hit  bool
	raw  bool // printed as is, without the file path (group separator)
}

// searchFile returns the lines of a text file matching re, with context lines.
// Binary files return nothing.
func searchFile(path string, re *regexp.Regexp, context int) ([]searchLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, 8192)
	n, _ := f.Read(head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return nil, nil
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	var all []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		all = append(all, sc.Text())
	}
	if sc.Err() != nil {
		return nil, nil // a line longer than the buffer: treat the file as not searchable
	}
	var out []searchLine
	last := -1 // last line index already emitted
	for i, line := range all {
		if !re.MatchString(line) {
			continue
		}
		start := i - context
		if start < 0 {
			start = 0
		}
		if start <= last {
			start = last + 1
		} else if last >= 0 && context > 0 {
			out = append(out, searchLine{text: "--", raw: true})
		}
		for j := start; j < i; j++ {
			out = append(out, searchLine{text: fmt.Sprintf("-%d- %s", j+1, clipLine(all[j]))})
		}
		out = append(out, searchLine{text: fmt.Sprintf(":%d: %s", i+1, clipLine(line)), hit: true})
		last = i
		for j := i + 1; j <= i+context && j < len(all); j++ {
			if re.MatchString(all[j]) {
				break // it becomes the next hit
			}
			out = append(out, searchLine{text: fmt.Sprintf("-%d- %s", j+1, clipLine(all[j]))})
			last = j
		}
	}
	return out, nil
}

func clipLine(s string) string {
	if len(s) <= searchMaxLine {
		return s
	}
	return s[:searchMaxLine] + "…"
}

func relTo(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}

// globRegexp converts a glob ("*", "?", "**", "{a,b}") to an anchored regexp
// over slash-separated paths.
func globRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	brace := 0
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '{':
			brace++
			b.WriteString("(?:")
		case '}':
			if brace == 0 {
				b.WriteString(`\}`)
			} else {
				brace--
				b.WriteString(")")
			}
		case ',':
			if brace > 0 {
				b.WriteString("|")
			} else {
				b.WriteString(",")
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if brace != 0 {
		return nil, errors.New("unbalanced {")
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
