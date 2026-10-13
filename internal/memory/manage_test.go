package memory

import (
	"bytes"
	"github.com/shaktsin/umcode/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManageMemoryRetiresOnlyExactGeneratedBullet(t *testing.T) {
	before := []byte("User rule\n\n## Verified project memory\n<!-- umcode:generated -->\n- Keep go test passing\n- Run go vet\n")
	after, found, ok := removeGeneratedMemory(before, "Keep go test passing")
	if !ok || !found {
		t.Fatalf("found=%t ok=%t", found, ok)
	}
	got := string(after)
	if !strings.Contains(got, "User rule") || !strings.Contains(got, "- Run go vet") || strings.Contains(got, "- Keep go test passing") {
		t.Fatalf("file=%s", got)
	}
}
func TestManageMemoryPreservesEditedBullet(t *testing.T) {
	before := []byte("## Verified project memory\n<!-- umcode:generated -->\n- Keep go test passing (edited)\n")
	_, found, ok := removeGeneratedMemory(before, "Keep go test passing")
	if !ok || found {
		t.Fatalf("edited item claimed as generated: found=%t ok=%t", found, ok)
	}
}
func TestManageMemoryValidationRejectsMultiline(t *testing.T) {
	if validCorrectionText("safe\n- injected") || validCorrectionText(" ") {
		t.Fatal("unsafe correction accepted")
	}
}

func TestManageMemoryRetireGenerated(t *testing.T) {
	svc, st, req := serviceFixture(t)
	svc.PromoteCompleted(t.Context(), req)
	rows, err := st.ListProjectMemories(t.Context(), req.Project.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	before := readTarget(t, req.Project)
	result, err := svc.Retire(t.Context(), protocol.MemoryChangeParams{ProjectID: req.Project.ID, MemoryID: rows[0].ID, ExpectedTextHash: rows[0].TextHash, ExpectedTargetHash: rows[0].FileHashAfter})
	if err != nil {
		t.Fatal(err)
	}
	if result.InstructionRemains || strings.Contains(readTarget(t, req.Project), "Use go test") || !strings.Contains(readTarget(t, req.Project), "## Verified project memory") {
		t.Fatalf("result=%+v file=%s", result, readTarget(t, req.Project))
	}
	if before == readTarget(t, req.Project) {
		t.Fatal("file not edited")
	}
}
func TestManageMemoryExternalEdit(t *testing.T) {
	svc, st, req := serviceFixture(t)
	svc.PromoteCompleted(t.Context(), req)
	rows, _ := st.ListProjectMemories(t.Context(), req.Project.ID)
	path := filepath.Join(req.Project.Root, "UMCODE.md")
	raw, _ := os.ReadFile(path)
	_ = os.WriteFile(path, bytes.Replace(raw, []byte("Use go test ./... for this repository"), []byte("Use go test ./... for this repository, edited"), 1), 0644)
	result, err := svc.Retire(t.Context(), protocol.MemoryChangeParams{ProjectID: req.Project.ID, MemoryID: rows[0].ID, ExpectedTextHash: rows[0].TextHash, ExpectedTargetHash: memoryHash(bytes.Replace(raw, []byte("Use go test ./... for this repository"), []byte("Use go test ./... for this repository, edited"), 1))})
	if err != nil {
		t.Fatal(err)
	}
	if !result.InstructionRemains || !strings.Contains(readTarget(t, req.Project), "Use go test") || !strings.Contains(readTarget(t, req.Project), "edited") {
		t.Fatalf("result=%+v file=%s", result, readTarget(t, req.Project))
	}
}
