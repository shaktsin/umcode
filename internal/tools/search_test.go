package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func searchProject(t *testing.T) (context.Context, string) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write("main.go", "package main\n\nfunc main() {\n\thelloWorld()\n}\n")
	write("src/util/util.go", "package util\n\nfunc HelloWorld() {}\nfunc other() {}\n")
	write("src/app/app.ts", "export const hello = 1;\n")
	write("node_modules/dep/index.js", "helloWorld()\n")
	write(".git/config", "helloWorld\n")
	write("bin.dat", "helloWorld\x00\x01")
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("helloWorld secret\n"), 0o644)
	os.Symlink(outside, filepath.Join(root, "link"))
	return WithScope(context.Background(), &Scope{ProjectID: "p", ProjectName: "demo", Root: root}), root
}

func callSearch(ctx context.Context, a searchArgs) (string, error) {
	args, _ := json.Marshal(a)
	return (&fileSearch{}).Call(ctx, args)
}

func TestFileSearchContent(t *testing.T) {
	ctx, _ := searchProject(t)
	out, err := callSearch(ctx, searchArgs{Pattern: "helloWorld", CaseInsensitive: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"main.go:4:", "src/util/util.go:3:"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, bad := range []string{"node_modules", ".git", "bin.dat", "secret", "link/"} {
		if strings.Contains(out, bad) {
			t.Errorf("unexpected %q in:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "2 match(es) in 2 file(s)") {
		t.Errorf("summary wrong:\n%s", out)
	}
}

func TestFileSearchGlobLiteralAndContext(t *testing.T) {
	ctx, _ := searchProject(t)
	out, _ := callSearch(ctx, searchArgs{Pattern: "hello", Glob: "*.ts", CaseInsensitive: true})
	if !strings.Contains(out, "src/app/app.ts:1:") || strings.Contains(out, ".go") {
		t.Errorf("name glob:\n%s", out)
	}
	out, _ = callSearch(ctx, searchArgs{Pattern: "hello", Glob: "src/**/*.go", CaseInsensitive: true})
	if !strings.Contains(out, "src/util/util.go") || strings.Contains(out, "main.go") {
		t.Errorf("path glob:\n%s", out)
	}
	// Literal text: "(" would be an invalid regexp otherwise.
	out, err := callSearch(ctx, searchArgs{Pattern: "helloWorld()", Literal: true})
	if err != nil || !strings.Contains(out, "main.go:4:") {
		t.Errorf("literal: %v\n%s", err, out)
	}
	out, _ = callSearch(ctx, searchArgs{Pattern: "func main", Context: 1})
	if !strings.Contains(out, "main.go-2-") || !strings.Contains(out, "main.go:3:") || !strings.Contains(out, "main.go-4-") {
		t.Errorf("context lines:\n%s", out)
	}
}

func TestFileSearchListFilesLimitsAndErrors(t *testing.T) {
	ctx, _ := searchProject(t)
	out, _ := callSearch(ctx, searchArgs{Glob: "**/*.go"})
	if !strings.Contains(out, "main.go\n") || !strings.Contains(out, "src/util/util.go\n") || !strings.Contains(out, "2 file(s)") {
		t.Errorf("list:\n%s", out)
	}
	out, _ = callSearch(ctx, searchArgs{Glob: "**/*.go", MaxResults: 1})
	if !strings.Contains(out, "stopped at 1") {
		t.Errorf("limit:\n%s", out)
	}
	if out, _ := callSearch(ctx, searchArgs{Pattern: "zzzzz"}); out != "no matches" {
		t.Errorf("no match = %q", out)
	}
	if _, err := callSearch(ctx, searchArgs{}); err == nil {
		t.Error("empty search accepted")
	}
	if _, err := callSearch(ctx, searchArgs{Pattern: "("}); err == nil || !strings.Contains(err.Error(), "invalid pattern") {
		t.Errorf("bad regexp: %v", err)
	}
	if _, err := callSearch(ctx, searchArgs{Pattern: "a", Path: "../.."}); err == nil {
		t.Error("path outside the project accepted")
	}
	// Searching inside a normally skipped folder is allowed.
	if out, _ := callSearch(ctx, searchArgs{Pattern: "helloWorld", Path: "node_modules"}); !strings.Contains(out, "node_modules/dep/index.js:1:") {
		t.Errorf("explicit node_modules:\n%s", out)
	}
}

func TestGlobRegexp(t *testing.T) {
	cases := map[string][]string{
		"*.go":         {"a.go", "!a/b.go"},
		"src/**/*.ts":  {"src/a.ts", "src/x/y/a.ts", "!other/a.ts"},
		"**/test_*.py": {"test_a.py", "pkg/test_a.py"},
		"*.{js,ts}":    {"a.js", "b.ts", "!c.go"},
		"file?.txt":    {"file1.txt", "!file12.txt"},
		"docs/**":      {"docs/a/b.md"},
	}
	for glob, paths := range cases {
		re, err := globRegexp(glob)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			want := !strings.HasPrefix(p, "!")
			p = strings.TrimPrefix(p, "!")
			if re.MatchString(p) != want {
				t.Errorf("glob %q on %q = %v, want %v", glob, p, !want, want)
			}
		}
	}
	if _, err := globRegexp("{a,b"); err == nil {
		t.Error("unbalanced brace accepted")
	}
}
