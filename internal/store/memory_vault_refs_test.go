package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMemoryReferenceBackfillAndSourceDeletion(t *testing.T) {
	st, in := memoryPromotionFixture(t)
	defer st.Close()
	hash := "vault_live_memory"
	_, err := st.DB.Exec(`UPDATE evidence SET vault_hash=? WHERE id=?`, hash, memoryEvidenceIDs(t, in)[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.Exec(`INSERT INTO vault_objects(hash,class,status,size,original_size,truncated,created_at,last_referenced_at) VALUES(?,'plain','available',10,10,0,?,?)`, hash, FormatTime(time.Now().Add(-time.Hour)), FormatTime(time.Now().Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.Exec(`INSERT INTO project_memories(id,project_id,work_id,candidate_node_id,semantic_key,category,target_path,text,text_hash,status,source_revision,evidence_json,created_at,user_owned) VALUES('live',?,?,?,'key','command','UMCODE.md','- fact','hash','active','final',?,?,0)`, in.Memory.ProjectID, in.Memory.WorkID, in.Memory.CandidateNodeID, mustJSON(memoryEvidenceIDs(t, in)), Now())
	if err != nil {
		t.Fatal(err)
	}
	refs, err := st.ReferencedVaultHashes(t.Context())
	if err != nil || !refs[hash] {
		t.Fatalf("backfill refs=%v err=%v", refs, err)
	}
	th, err := st.GetThread(t.Context(), in.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.DeleteThread(t.Context(), th.ID); err != nil {
		t.Fatal(err)
	}
	refs, err = st.ReferencedVaultHashes(t.Context())
	if err != nil || !refs[hash] {
		t.Fatalf("source deletion lost memory pin: %v %v", refs, err)
	}
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func TestRetiredMemoryReleasesOnlyItsReference(t *testing.T) {
	st, in := memoryPromotionFixture(t)
	defer st.Close()
	for _, id := range []string{"mem1", "mem2"} {
		_, err := st.DB.Exec(`INSERT INTO project_memories(id,project_id,work_id,candidate_node_id,semantic_key,category,target_path,text,text_hash,status,source_revision,evidence_json,created_at,user_owned) VALUES(?,?,?,? ,?,'command','UMCODE.md','- fact','hash','active','final',?, ?,0)`, id, in.Memory.ProjectID, in.Memory.WorkID, in.Memory.CandidateNodeID, id, mustJSON(memoryEvidenceIDs(t, in)), Now())
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := st.DB.Exec(`INSERT INTO memory_vault_refs(memory_id,vault_hash) VALUES('mem1','shared'),('mem2','shared')`)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RetireProjectMemory(t.Context(), "mem1", "hash"); err != nil {
		t.Fatal(err)
	}
	refs, err := st.ReferencedVaultHashes(t.Context())
	if err != nil || !refs["shared"] {
		t.Fatalf("other memory pin lost: %v %v", refs, err)
	}
	if err = st.RetireProjectMemory(t.Context(), "mem2", "hash"); err != nil {
		t.Fatal(err)
	}
	refs, err = st.ReferencedVaultHashes(t.Context())
	if err != nil || refs["shared"] {
		t.Fatalf("retired memories still pin unused hash: %v %v", refs, err)
	}
}

func TestPromotionPinsEvidenceBeforeSourceDeletion(t *testing.T) {
	st, in := memoryPromotionFixture(t)
	defer st.Close()
	hash := "vault_promotion_memory"
	if _, err := st.DB.Exec(`UPDATE evidence SET vault_hash=? WHERE id=?`, hash, memoryEvidenceIDs(t, in)[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO vault_objects(hash,class,status,size,original_size,truncated,created_at,last_referenced_at) VALUES(?,'plain','available',10,10,0,?,?)`, hash, Now(), Now()); err != nil {
		t.Fatal(err)
	}
	in.Validate = func(MemoryPromotionSnapshot) error { return nil }
	if _, err := st.PrepareMemoryPromotion(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	refs, err := st.ReferencedVaultHashes(t.Context())
	if err != nil || !refs[hash] {
		t.Fatalf("prepared memory did not pin evidence: %v %v", refs, err)
	}
	th, err := st.GetThread(t.Context(), in.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteThread(t.Context(), th.ID); err != nil {
		t.Fatal(err)
	}
	refs, err = st.ReferencedVaultHashes(t.Context())
	if err != nil || !refs[hash] {
		t.Fatalf("source deletion lost pending-memory pin: %v %v", refs, err)
	}
}
func memoryEvidenceIDs(t *testing.T, in PrepareMemoryPromotion) []string {
	t.Helper()
	var ids []string
	if err := json.Unmarshal([]byte(in.Memory.EvidenceJSON), &ids); err != nil {
		t.Fatal(err)
	}
	return ids
}
