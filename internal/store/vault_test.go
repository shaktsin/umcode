package store

import (
	"context"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func vaultWork(t *testing.T) (*Store, protocol.Work, protocol.WorkNode) {
	t.Helper()
	ctx := context.Background()
	st, th := workFixture(t)
	w, err := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	crit, err := st.AddWorkNode(ctx, protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeCriterion, Title: "unit", Status: "pending", ValidFrom: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return st, w, crit
}

func attemptInput(w protocol.Work, crit protocol.WorkNode, hash string, at time.Time) RecordAttemptInput {
	code := 0
	return RecordAttemptInput{
		Attempt: protocol.VerificationAttempt{WorkID: w.ID, CriterionNodeID: crit.ID, CheckType: "command", Command: "go test ./...",
			Status: protocol.AttemptPassed, ExitCode: &code, StartedAt: at, FinishedAt: at},
		Evidence: protocol.Evidence{WorkID: w.ID, NodeID: crit.ID, Kind: protocol.EvidenceVerificationOutput, Summary: "ok",
			VaultHash: hash, EnvFingerprint: "env1", Availability: protocol.AvailAvailable, ObservedAt: at},
		Object:      &protocol.VaultObjectRow{Hash: hash, Class: "plain", Status: "available", Size: 10, OriginalSize: 10, CreatedAt: at, LastReferencedAt: at},
		Fingerprint: &protocol.Fingerprint{WorkID: w.ID, TurnID: "t1", Kind: protocol.FingerprintVerification, Value: "fp1", Paths: []string{"a.go"}, TakenAt: at},
		Criterion:   &CriterionUpdate{NodeID: crit.ID, Status: protocol.AttemptPassed, Revision: crit.Revision, At: at},
	}
}

func TestMigration0014Additive(t *testing.T) {
	ctx := context.Background()
	st, w, _ := vaultWork(t)
	if _, err := st.AddEvidence(ctx, protocol.Evidence{WorkID: w.ID, Kind: protocol.EvidenceToolError, Summary: "old style"}); err != nil {
		t.Fatal(err)
	}
	d, err := st.GetWorkDetail(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Evidence) != 1 || d.Evidence[0].VaultHash != "" || d.Evidence[0].Availability != protocol.AvailNone {
		t.Fatalf("evidence = %+v", d.Evidence)
	}
	if d.Fingerprints == nil {
		t.Fatal("Fingerprints must be a non-nil slice")
	}
}

func TestRecordAttemptCommitsAtomically(t *testing.T) {
	ctx := context.Background()
	st, w, crit := vaultWork(t)
	at := time.Now().UTC()
	att, err := st.RecordAttempt(ctx, attemptInput(w, crit, hashA, at))
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.GetWorkDetail(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Attempts) != 1 || len(d.Evidence) != 1 || len(d.Fingerprints) != 1 {
		t.Fatalf("detail = %+v", d)
	}
	a, ev, fp := d.Attempts[0], d.Evidence[0], d.Fingerprints[0]
	if a.ID != att.ID || a.EvidenceID != ev.ID || a.FingerprintID != fp.ID {
		t.Fatalf("links: attempt %+v evidence %s fingerprint %s", a, ev.ID, fp.ID)
	}
	if ev.VaultHash != hashA || ev.EnvFingerprint != "env1" || ev.Availability != protocol.AvailAvailable {
		t.Fatalf("evidence = %+v", ev)
	}
	if len(fp.Paths) != 1 || fp.Paths[0] != "a.go" || fp.Kind != protocol.FingerprintVerification {
		t.Fatalf("fingerprint = %+v", fp)
	}
	for _, n := range d.Nodes {
		if n.ID == crit.ID && n.Status != protocol.AttemptPassed {
			t.Fatalf("criterion status = %s", n.Status)
		}
	}
	// A vault object that goes missing turns the evidence unavailable.
	if err := st.SetVaultObjectStatus(ctx, hashA, "missing"); err != nil {
		t.Fatal(err)
	}
	d, _ = st.GetWorkDetail(ctx, w.ID)
	if d.Evidence[0].Availability != protocol.AvailUnavailable {
		t.Fatalf("availability after missing = %s", d.Evidence[0].Availability)
	}
}

func TestRecordAttemptRollsBack(t *testing.T) {
	ctx := context.Background()
	st, w, crit := vaultWork(t)
	at := time.Now().UTC()
	first := attemptInput(w, crit, hashA, at)
	first.Attempt.ID = "vat_fixed"
	if _, err := st.RecordAttempt(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := attemptInput(w, crit, hashB, at.Add(time.Second))
	second.Attempt.ID = "vat_fixed" // primary key conflict inside the transaction
	if _, err := st.RecordAttempt(ctx, second); err == nil {
		t.Fatal("expected a conflict error")
	}
	d, _ := st.GetWorkDetail(ctx, w.ID)
	if len(d.Attempts) != 1 || len(d.Evidence) != 1 || len(d.Fingerprints) != 1 {
		t.Fatalf("rollback left rows: attempts %d evidence %d fingerprints %d", len(d.Attempts), len(d.Evidence), len(d.Fingerprints))
	}
	if _, err := st.GetVaultObject(ctx, hashB); err == nil {
		t.Fatal("vault row from the failed transaction survived")
	}
}

func TestVaultObjectCRUD(t *testing.T) {
	ctx := context.Background()
	st, _, _ := vaultWork(t)
	at := time.Now().UTC()
	row := protocol.VaultObjectRow{Hash: hashA, Class: "redacted", Status: "available", Size: 5, OriginalSize: 9, Truncated: true, CreatedAt: at, LastReferencedAt: at}
	for i := 0; i < 2; i++ { // idempotent
		if err := st.UpsertVaultObject(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.GetVaultObject(ctx, hashA)
	if err != nil || got.Class != "redacted" || !got.Truncated || got.OriginalSize != 9 {
		t.Fatalf("got %+v err %v", got, err)
	}
	if err := st.SetVaultObjectStatus(ctx, hashA, "missing"); err != nil {
		t.Fatal(err)
	}
	later := at.Add(time.Hour)
	if err := st.TouchVaultObject(ctx, hashA, later); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListVaultObjects(ctx)
	if err != nil || len(list) != 1 || list[0].Status != "missing" || !list[0].LastReferencedAt.After(at) {
		t.Fatalf("list %+v err %v", list, err)
	}
	if err := st.DeleteVaultObject(ctx, hashA); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetVaultObject(ctx, hashA); err == nil {
		t.Fatal("row still present")
	}
}

func TestReferencedVaultHashes(t *testing.T) {
	ctx := context.Background()
	st, th := workFixture(t)
	now := time.Now().UTC()
	mk := func(hash string, status string, stale bool) {
		w, _ := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID})
		var staleAt *time.Time
		if stale {
			staleAt = &now
		}
		if _, err := st.AddEvidence(ctx, protocol.Evidence{WorkID: w.ID, Kind: protocol.EvidenceVerificationOutput, VaultHash: hash, StaleAt: staleAt, ObservedAt: now}); err != nil {
			t.Fatal(err)
		}
		if status != protocol.WorkOpen {
			if err := st.CloseWork(ctx, w.ID, status, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk(hashA, protocol.WorkCompleted, true)           // stale on a closed work: unreferenced
	mk(hashB, protocol.WorkOpen, true)                // stale but the work is open: referenced
	mk(hashA[:63]+"c", protocol.WorkCompleted, false) // active on a closed work: referenced
	got, err := st.ReferencedVaultHashes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[hashA] || !got[hashB] || !got[hashA[:63]+"c"] {
		t.Fatalf("referenced = %v", got)
	}
}

func TestMarkEvidenceStale(t *testing.T) {
	ctx := context.Background()
	st, w, crit := vaultWork(t)
	at := time.Now().UTC()
	if _, err := st.RecordAttempt(ctx, attemptInput(w, crit, hashA, at)); err != nil {
		t.Fatal(err)
	}
	d, _ := st.GetWorkDetail(ctx, w.ID)
	if err := st.MarkEvidenceStale(ctx, []string{d.Evidence[0].ID}, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	d, _ = st.GetWorkDetail(ctx, w.ID)
	if d.Evidence[0].StaleAt == nil {
		t.Fatal("stale_at not set")
	}
	if err := st.MarkEvidenceStale(ctx, nil, at); err != nil {
		t.Fatalf("empty list should be a no-op: %v", err)
	}
}
