package workbench

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeServiceSelectAndPersist(t *testing.T) {
	home := t.TempDir()
	python := filepath.Join(t.TempDir(), "bin", "python")
	if err := os.MkdirAll(filepath.Dir(python), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(python, []byte("#!/bin/sh\necho '{\"version\":\"3.13\",\"missing\":[]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	svc := NewRuntimeService(home)
	if _, err := svc.Select(context.Background(), "python"); err == nil {
		t.Fatal("relative path accepted")
	}
	state, err := svc.Select(context.Background(), python)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Ready || state.Selected != python {
		t.Fatalf("unexpected state: %+v", state)
	}
	if got := NewRuntimeService(home).Selected(context.Background()); got != python {
		t.Fatalf("selection was not persisted: %q", got)
	}
}

func TestRuntimeServiceReportsMissingPackages(t *testing.T) {
	home := t.TempDir()
	python := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(python, []byte("#!/bin/sh\necho '{\"version\":\"3.13\",\"missing\":[\"torch\",\"numpy\"]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	state, err := NewRuntimeService(home).Select(context.Background(), python)
	if err != nil {
		t.Fatal(err)
	}
	if state.Ready || !strings.Contains(strings.Join(state.Missing, ","), "torch") {
		t.Fatalf("missing packages not reported: %+v", state)
	}
}

func TestProbeRealPythonReportsVersion(t *testing.T) {
	path, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	result := probePython(context.Background(), path, "System")
	if result.Error != "" || result.Version == "" {
		t.Fatalf("probe failed: %+v", result)
	}
}
