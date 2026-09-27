package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editCtx(t *testing.T) (context.Context, string, *[]string) {
	t.Helper()
	root := t.TempDir()
	var recorded []string
	scope := &Scope{ProjectID: "p", ProjectName: "demo", Root: root,
		Record: func(_ context.Context, abs string, before *string, deleted bool) {
			b := "<new>"
			if before != nil {
				b = *before
			}
			recorded = append(recorded, filepath.Base(abs)+"|"+b)
		}}
	return WithScope(context.Background(), scope), root, &recorded
}

func callEdit(ctx context.Context, path, old, repl string, all bool) (string, error) {
	args, _ := json.Marshal(editArgs{Path: path, OldString: old, NewString: repl, ReplaceAll: all})
	return (&fileEdit{}).Call(ctx, args)
}

func TestFileEditReplacesExactlyOnce(t *testing.T) {
	ctx, root, rec := editCtx(t)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("one\ntwo\nthree\n"), 0o640)
	out, err := callEdit(ctx, "a.go", "two", "2", false)
	if err != nil || !strings.Contains(out, "replaced 1") {
		t.Fatalf("edit: %v %s", err, out)
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.go"))
	if string(got) != "one\n2\nthree\n" {
		t.Fatalf("content = %q", got)
	}
	if st, _ := os.Stat(filepath.Join(root, "a.go")); st.Mode().Perm() != 0o640 {
		t.Errorf("mode changed: %v", st.Mode())
	}
	if len(*rec) != 1 || (*rec)[0] != "a.go|one\ntwo\nthree\n" {
		t.Errorf("recorded = %v", *rec)
	}
}

func TestFileEditErrors(t *testing.T) {
	ctx, root, _ := editCtx(t)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("x\nx\ny\n"), 0o644)
	cases := []struct{ name, path, old, repl, want string }{
		{"missing text", "a.txt", "zzz", "q", "not found"},
		{"ambiguous", "a.txt", "x", "q", "matches 2 places"},
		{"identical", "a.txt", "y", "y", "identical"},
		{"empty old on existing file", "a.txt", "", "q", "empty"},
		{"missing file", "nope.txt", "a", "b", "does not exist"},
		{"outside project", "../escape.txt", "a", "b", "outside"},
	}
	for _, c := range cases {
		_, err := callEdit(ctx, c.path, c.old, c.repl, false)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(got) != "x\nx\ny\n" {
		t.Errorf("file changed by a failed edit: %q", got)
	}
}

func TestFileEditReplaceAllCreateAndCRLF(t *testing.T) {
	ctx, root, rec := editCtx(t)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("x\nx\ny\n"), 0o644)
	if out, err := callEdit(ctx, "a.txt", "x", "z", true); err != nil || !strings.Contains(out, "replaced 2") {
		t.Fatalf("replace_all: %v %s", err, out)
	}
	if out, err := callEdit(ctx, "sub/new.txt", "", "hello", false); err != nil || !strings.Contains(out, "created") {
		t.Fatalf("create: %v %s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "sub", "new.txt")); string(got) != "hello" {
		t.Fatalf("created content = %q", got)
	}
	if last := (*rec)[len(*rec)-1]; last != "new.txt|<new>" {
		t.Errorf("create recorded as %q", last)
	}
	os.WriteFile(filepath.Join(root, "win.txt"), []byte("a\r\nb\r\nc\r\n"), 0o644)
	if _, err := callEdit(ctx, "win.txt", "a\nb", "a\nB", false); err != nil {
		t.Fatalf("crlf edit: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "win.txt")); string(got) != "a\r\nB\r\nc\r\n" {
		t.Fatalf("crlf content = %q", got)
	}
}

func TestFileEditRefusesBinaryLargeAndNoProject(t *testing.T) {
	ctx, root, _ := editCtx(t)
	os.WriteFile(filepath.Join(root, "bin"), []byte{0xff, 0xfe, 0x00}, 0o644)
	if _, err := callEdit(ctx, "bin", "a", "b", false); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("binary: %v", err)
	}
	os.WriteFile(filepath.Join(root, "big"), []byte(strings.Repeat("a", maxEditSize+1)), 0o644)
	if _, err := callEdit(ctx, "big", "a", "b", false); err == nil || !strings.Contains(err.Error(), "1 MB") {
		t.Errorf("large: %v", err)
	}
	if _, err := callEdit(context.Background(), "x", "a", "b", false); err != ErrNoProject {
		t.Errorf("no project: %v", err)
	}
}
