package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// maxEditSize is the largest file file.edit will change: the same limit as the
// snapshot used for diffs and undo.
const maxEditSize = 1 << 20

// ---- file.edit ----

type fileEdit struct{ ws *Workspaces }

func (*fileEdit) Name() string { return "file.edit" }
func (*fileEdit) Description() string {
	return "Change part of an existing text file in the open project by replacing an exact piece of text. " +
		"old_string must match the file exactly (including whitespace) and be unique unless replace_all is true; " +
		"include a few surrounding lines to make it unique. Prefer this over file.write for changes to existing files. " +
		"If old_string is empty and the file does not exist, the file is created with new_string. The change is recorded and can be undone."
}
func (*fileEdit) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string","description":"Exact text to replace; empty only to create a new file"},"new_string":{"type":"string","description":"Replacement text"},"replace_all":{"type":"boolean","description":"Replace every occurrence instead of requiring exactly one"},"workspace":{"type":"string"}},"required":["path","old_string","new_string"]}`)
}
func (*fileEdit) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct{ Path string }](args)
	return RiskYellow, "Edit " + a.Path
}

type editArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
	Workspace  string `json:"workspace"`
}

func (t *fileEdit) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[editArgs](args)
	if err != nil {
		return "", err
	}
	scope := ScopeFrom(ctx)
	if scope == nil {
		return "", ErrNoProject
	}
	p, err := resolvePath(ctx, t.ws, a.Workspace, a.Path, OpWrite)
	if err != nil {
		return "", err
	}
	st, statErr := os.Stat(p)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	if statErr != nil { // the file does not exist
		if a.OldString != "" {
			return "", fmt.Errorf("%s does not exist; to create a file pass an empty old_string", scope.Rel(p))
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(a.NewString), 0o644); err != nil {
			return "", err
		}
		if scope.Record != nil {
			scope.Record(ctx, p, nil, false)
		}
		return fmt.Sprintf("created %s (%d bytes)", scope.Rel(p), len(a.NewString)), nil
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a directory", scope.Rel(p))
	}
	if st.Size() > maxEditSize {
		return "", fmt.Errorf("%s is larger than 1 MB; edit it with a command instead", scope.Rel(p))
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", errors.New("file is not UTF-8 text")
	}
	updated, n, err := applyEdit(string(data), a.OldString, a.NewString, a.ReplaceAll)
	if err != nil {
		return "", fmt.Errorf("%s: %w", scope.Rel(p), err)
	}
	before := Snapshot(p)
	if err := os.WriteFile(p, []byte(updated), st.Mode().Perm()); err != nil {
		return "", err
	}
	if scope.Record != nil {
		scope.Record(ctx, p, before, false)
	}
	return fmt.Sprintf("edited %s: replaced %d occurrence(s)", scope.Rel(p), n), nil
}

// applyEdit replaces old with repl in content. It requires exactly one match
// unless all is true. A file that uses CRLF line endings is matched with the
// model's LF text by converting the arguments to CRLF.
func applyEdit(content, old, repl string, all bool) (string, int, error) {
	if old == "" {
		return "", 0, errors.New("old_string is empty; use file.write to replace a whole file")
	}
	if old == repl {
		return "", 0, errors.New("old_string and new_string are identical; nothing to change")
	}
	if strings.Contains(content, "\r\n") && !strings.Contains(old, "\r\n") {
		old = strings.ReplaceAll(old, "\n", "\r\n")
		repl = strings.ReplaceAll(strings.ReplaceAll(repl, "\r\n", "\n"), "\n", "\r\n")
	}
	n := strings.Count(content, old)
	switch {
	case n == 0:
		return "", 0, errors.New("old_string was not found; re-read the file and copy the text exactly, including whitespace and indentation")
	case n > 1 && !all:
		return "", 0, fmt.Errorf("old_string matches %d places; add surrounding lines to make it unique, or set replace_all to change every occurrence", n)
	}
	if all {
		return strings.ReplaceAll(content, old, repl), n, nil
	}
	return strings.Replace(content, old, repl, 1), 1, nil
}
