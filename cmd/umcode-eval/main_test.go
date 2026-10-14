package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestCLINoAccidentalDispatch(t *testing.T) {
	for _, args := range [][]string{{}, {"validate", "--manifest", "missing"}, {"run", "--manifest", "../../internal/evaluation/testdata/deterministic.json", "--out", filepath.Join(t.TempDir(), "campaign"), "--live"}, {"report", "--out", t.TempDir()}} {
		var out, err bytes.Buffer
		if code := run(context.Background(), args, &out, &err); code != 2 {
			t.Fatalf("%v code=%d: %s", args, code, err.String())
		}
	}
}
func TestCLIRoundTrip(t *testing.T) {
	var out, err bytes.Buffer
	manifest := "../../internal/evaluation/testdata/deterministic.json"
	invoke := func(args ...string) {
		t.Helper()
		out.Reset()
		err.Reset()
		if code := run(t.Context(), args, &out, &err); code != 0 {
			t.Fatalf("%v code=%d %s %s", args, code, out.String(), err.String())
		}
	}
	invoke("validate", "--manifest", manifest)
	root := filepath.Join(t.TempDir(), "campaign")
	invoke("run", "--manifest", manifest, "--out", root)
	invoke("run", "--manifest", manifest, "--out", root, "--resume")
	invoke("review-export", "--out", root, "--destination", filepath.Join(t.TempDir(), "review"))
	invoke("report", "--out", root, "--format", "json")
	if !bytes.Contains(out.Bytes(), []byte(`"live_release": "inconclusive"`)) {
		t.Fatal(out.String())
	}
	invoke("report", "--out", root, "--format", "markdown")
	invoke("cleanup", "--out", root, "--campaign", "deterministic")
}
