package workbench

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportedRuntimeMissingBundleIsNotReady(t *testing.T) {
	root, id, created, _, _ := runtimeBundleFixture(t)
	runtime := NewImportedRuntime(t.TempDir())
	defer runtime.Close()
	state, err := runtime.Status(BundleTarget{root, id, created})
	if err != nil || state.Ready || state.Reason == "" {
		t.Fatalf("expected blocked runtime, got %+v, %v", state, err)
	}
}

func TestImportedCheckpointPublicationRejectsReplacedModel(t *testing.T) {
	project, id, created, _, assets := runtimeBundleFixture(t)
	b := BundleTarget{project, id, created}
	output := t.TempDir()
	for name, content := range assets {
		if err := os.WriteFile(filepath.Join(output, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	b.CreatedAt = "2000-01-01T00:00:00Z"
	if err := publishImportedCheckpoint(b, output, "checkpoint-test"); err == nil {
		t.Fatal("published into replacement model")
	}
	if _, err := os.Stat(filepath.Join(project, "models", id, "import-checkpoints")); !os.IsNotExist(err) {
		t.Fatalf("stale publication wrote files: %v", err)
	}
}
