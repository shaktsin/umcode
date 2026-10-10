package projects

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestRecordPromotionStrictAndIdempotent(t *testing.T) {
	s, st, _ := newService(t)
	ctx := t.Context()
	p, err := st.CreateProject(ctx, protocol.Project{Root: t.TempDir(), Name: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(p.Root, "UMCODE.md")
	before := "before\n"
	after := "after\n"
	os.WriteFile(abs, []byte(after), 0644)
	// The operation owns the immutable history snapshots; recording must never
	// reread a path an external writer could swap after the atomic replacement.
	_, err = st.DB.Exec(`INSERT INTO memory_promotion_ops(id,project_id,work_id,candidate_node_id,memory_id,thread_id,turn_id,target_path,state,file_hash_before,file_hash_after,before_bytes,after_bytes,error_class,created_at,updated_at) VALUES('op',?,'work','candidate','memory','thread','turn','UMCODE.md','prepared','before','after',?,?,'','2026-10-09T00:00:00Z','2026-10-09T00:00:00Z')`, p.ID, []byte(before), []byte(after))
	if err != nil {
		t.Fatal(err)
	}
	emitted := 0
	r := s.NewRecorder(p, "thread", "turn", func(protocol.FileChangeData) { emitted++ })
	if _, err := st.DB.Exec(`CREATE TRIGGER reject_promotion BEFORE INSERT ON file_changes BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RecordPromotion(ctx, "op", abs, &before); err == nil || emitted != 0 {
		t.Fatalf("record failure=%v emits=%d", err, emitted)
	}
	st.DB.Exec(`DROP TRIGGER reject_promotion`)
	first, err := r.RecordPromotion(ctx, "op", abs, &before)
	if err != nil || emitted != 1 {
		t.Fatalf("record=%+v err=%v emits=%d", first, err, emitted)
	}
	os.Remove(abs)
	os.Symlink("/unavailable/foreign", abs)
	second, err := r.RecordPromotion(ctx, "op", abs, &before)
	if err != nil || !reflect.DeepEqual(first, second) || emitted != 1 {
		t.Fatalf("repeat=%+v err=%v emits=%d", second, err, emitted)
	}
	changes, err := st.ListFileChanges(ctx, p.ID, "", "", 100)
	if err != nil || len(changes) != 1 || changes[0].PromotionOpID != "op" {
		t.Fatalf("changes=%+v %v", changes, err)
	}
}
