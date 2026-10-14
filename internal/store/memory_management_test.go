package store

import (
	"github.com/shaktsin/umcode/internal/protocol"
	"testing"
	"time"
)

func managedMemoryFixture(t *testing.T) (*Store, protocol.Project) {
	t.Helper()
	st, _ := workFixture(t)
	p, err := st.CreateProject(t.Context(), protocol.Project{Root: t.TempDir(), Name: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, p
}
func TestMemoryReviewPagination(t *testing.T) {
	st, p := managedMemoryFixture(t)
	a := protocol.ProjectMemory{ID: "m1", ProjectID: p.ID, SemanticKey: "a", Category: "convention", TargetPath: "UMCODE.md", Text: "- A", TextHash: "ha", Status: protocol.MemoryStatusActive, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	b := a
	b.ID = "m2"
	b.SemanticKey = "b"
	b.Text = "- B"
	b.TextHash = "hb"
	insertTestMemory(t, st, a)
	insertTestMemory(t, st, b)
	page, err := st.ListMemoryReview(t.Context(), protocol.MemoryListParams{ProjectID: p.ID, Limit: 1})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].ID != "m1" || page.NextCursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	next, err := st.ListMemoryReview(t.Context(), protocol.MemoryListParams{ProjectID: p.ID, Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(next.Entries) != 1 || next.Entries[0].ID != "m2" {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}
func TestMemoryReviewProjectIsolation(t *testing.T) {
	st, _ := managedMemoryFixture(t)
	if _, err := st.ListMemoryReview(t.Context(), protocol.MemoryListParams{ProjectID: "unknown"}); err != nil {
		t.Fatal(err)
	}
}
func TestMemoryRetirementLineage(t *testing.T) {
	st, p := managedMemoryFixture(t)
	if err := st.RecordMemoryRetirement(t.Context(), p.ID, "semantic", "rev1", "hash1"); err != nil {
		t.Fatal(err)
	}
	suppressed, err := st.MemoryCandidateRetired(t.Context(), p.ID, "semantic", "rev1", "hash1")
	if err != nil || !suppressed {
		t.Fatalf("suppressed=%t err=%v", suppressed, err)
	}
	fresh, err := st.MemoryCandidateRetired(t.Context(), p.ID, "semantic", "rev2", "hash2")
	if err != nil || fresh {
		t.Fatalf("fresh suppressed=%t err=%v", fresh, err)
	}
}
