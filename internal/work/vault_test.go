package work

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/fingerprint"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/vault"
)

// withVault attaches a vault in a temp dir and a scripted workspace fingerprint.
func (f fixture) withVault(t *testing.T) (*vault.Vault, *string) {
	t.Helper()
	v := &vault.Vault{Dir: filepath.Join(t.TempDir(), "vault")}
	f.svc.Vault = v
	f.svc.VaultDir = v.Dir
	value := "ws1"
	f.svc.Workspace = func(ctx context.Context, root string) (fingerprint.Workspace, bool) {
		if value == "" {
			return fingerprint.Workspace{}, false
		}
		return fingerprint.Workspace{Value: value, Paths: []string{"a.go"}}, true
	}
	return v, &value
}

func (f fixture) end(t *testing.T) {
	t.Helper()
	if _, err := f.svc.End(context.Background(), f.th.ID, protocol.TurnCompleted, false, "/r"); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) passAll(t *testing.T) {
	t.Helper()
	f.observe(t, Observation{Tool: "verification.plan", Output: planOut, Root: "/r"})
	f.observe(t, Observation{Tool: "verification.run", Output: runOut("passed", "passed"), Root: "/r"})
}

func critStatus(d protocol.WorkDetail) map[string]string {
	m := map[string]string{}
	for _, n := range kinds(d, protocol.NodeCriterion) {
		m[n.Title] = n.Status
	}
	return m
}

func TestTruncatedRunStillRecordsAttempts(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	f.observe(t, Observation{Tool: "verification.plan", Output: planOut})
	f.observe(t, Observation{Tool: "verification.run", Output: `{"status":"passed","results":[{"label":"unit","comm…[truncated]`, Raw: runOut("passed", "passed")})
	if got := len(f.detail(t).Attempts); got != 2 {
		t.Fatalf("attempts = %d, want 2 recorded from Raw", got)
	}
}

func TestSummaryIsTailAndRedacted(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "g")
	out := strings.Repeat("noise line\n", 600) + "API_TOKEN=supersecretvalue123\nFAIL: the verdict\n"
	raw, _ := json.Marshal(map[string]any{"status": "failed", "results": []map[string]any{{"label": "unit", "command": "go test ./...", "status": "failed", "exit_code": 1, "output": out}}})
	f.observe(t, Observation{Tool: "verification.run", Output: string(raw)})
	ev := f.detail(t).Evidence
	s := ev[len(ev)-1].Summary
	if !strings.Contains(s, "FAIL: the verdict") || strings.Contains(s, "supersecretvalue123") || len(s) > summaryLimit+64 {
		t.Fatalf("summary = %q (len %d)", s, len(s))
	}
}

func TestAttemptLinksVaultObject(t *testing.T) {
	f := newFixture(t)
	v, _ := f.withVault(t)
	f.begin(t, "g")
	f.observe(t, Observation{Tool: "verification.run", Root: "/r", Output: `{"results":[{"command":"go test ./...","status":"passed","exit_code":0,"output":"ok pkg\nTOKEN=abcdef123456\n"}]}`})
	d := f.detail(t)
	e := d.Evidence[len(d.Evidence)-1]
	if e.VaultHash == "" || e.Availability != protocol.AvailAvailable {
		t.Fatalf("evidence = %+v", e)
	}
	b, err := v.Get(e.VaultHash)
	if err != nil || !strings.Contains(string(b), "ok pkg") || strings.Contains(string(b), "abcdef123456") {
		t.Fatalf("vault object = %q err = %v", b, err)
	}
	if e.EnvFingerprint == "" || d.Attempts[0].FingerprintID == "" {
		t.Fatalf("env %q, fingerprint %q", e.EnvFingerprint, d.Attempts[0].FingerprintID)
	}
}

func TestVaultFailureStillRecords(t *testing.T) {
	f := newFixture(t)
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, []byte("x"), 0o600)
	f.svc.Vault = &vault.Vault{Dir: filepath.Join(blocker, "sub")}
	f.begin(t, "g")
	f.observe(t, Observation{Tool: "verification.run", Output: runOut("passed", "passed")})
	d := f.detail(t)
	e := d.Evidence[0] // the unit check carries output
	if len(d.Attempts) != 2 || e.VaultHash != "" || e.Availability != protocol.AvailNone || !strings.Contains(e.Summary, "not retained") {
		t.Fatalf("attempts %d evidence %+v", len(d.Attempts), e)
	}
	if f.svc.Failures.Load() == 0 {
		t.Fatal("vault failure not counted")
	}
}

func TestShellEditMakesCriterionStale(t *testing.T) {
	f := newFixture(t)
	_, ws := f.withVault(t)
	f.begin(t, "g")
	f.passAll(t)
	*ws = "ws2" // sed -i through the shell
	f.end(t)
	d := f.detail(t)
	if d.Work.Status != protocol.WorkOpen {
		t.Fatalf("work status = %s, want open", d.Work.Status)
	}
	for title, st := range critStatus(d) {
		if st != protocol.StatusStale {
			t.Fatalf("criterion %s = %s, want stale", title, st)
		}
	}
	found := false
	for _, e := range d.Evidence {
		if e.Kind == protocol.EvidenceFileChange && e.Summary == "workspace" && e.SourceURI == "a.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no workspace file_change evidence: %+v", d.Evidence)
	}
	if got := len(Unresolved(d)); got != 2 {
		t.Fatalf("unresolved = %d", got)
	}
}

func TestUnchangedFingerprintKeepsPass(t *testing.T) {
	f := newFixture(t)
	f.withVault(t)
	f.begin(t, "g")
	f.passAll(t)
	f.end(t)
	d := f.detail(t)
	if d.Work.Status != protocol.WorkCompleted {
		t.Fatalf("status = %s", d.Work.Status)
	}
	if n := len(d.Fingerprints); n != 3 {
		t.Fatalf("fingerprints = %d, want 2 verification + 1 turn_end", n)
	}
}

func TestHeadOnlyChangeRecordsWorkspaceWide(t *testing.T) {
	f := newFixture(t)
	f.withVault(t)
	value := "head1"
	f.svc.Workspace = func(ctx context.Context, root string) (fingerprint.Workspace, bool) {
		return fingerprint.Workspace{Value: value}, true // no dirty paths: only HEAD can move
	}
	f.begin(t, "g")
	f.passAll(t)
	value = "head2"
	f.end(t)
	d := f.detail(t)
	ok := false
	for _, e := range d.Evidence {
		ok = ok || (e.Kind == protocol.EvidenceFileChange && e.SourceURI == ".")
	}
	if !ok || d.Work.Status != protocol.WorkOpen {
		t.Fatalf("status %s evidence %+v", d.Work.Status, d.Evidence)
	}
}

func TestFingerprintTimeoutInvalidatesNothing(t *testing.T) {
	f := newFixture(t)
	_, ws := f.withVault(t)
	f.begin(t, "g")
	f.passAll(t)
	*ws = "" // fingerprint unavailable at turn end
	f.end(t)
	d := f.detail(t)
	if d.Work.Status != protocol.WorkCompleted {
		t.Fatalf("status = %s, want completed", d.Work.Status)
	}
}

func TestEnvironmentChangeMakesPassStale(t *testing.T) {
	f := newFixture(t)
	f.withVault(t)
	version := "go1.24"
	f.svc.ToolVersion = func(context.Context) string { return version }
	f.begin(t, "g")
	f.passAll(t)
	version = "go1.25"
	f.end(t)
	d := f.detail(t)
	if d.Work.Status != protocol.WorkOpen || critStatus(d)["unit"] != protocol.StatusStale {
		t.Fatalf("status %s crit %v", d.Work.Status, critStatus(d))
	}
}

func TestNewerAttemptSupersedesEvidence(t *testing.T) {
	f := newFixture(t)
	f.withVault(t)
	f.begin(t, "g")
	f.passAll(t)
	f.observe(t, Observation{Tool: "verification.run", Output: runOut("passed", "passed"), Root: "/r"})
	f.end(t)
	d := f.detail(t)
	stale := 0
	for _, e := range d.Evidence {
		if e.Kind == protocol.EvidenceVerificationOutput && e.StaleAt != nil {
			stale++
		}
	}
	if stale != 2 { // the two older attempts' evidence
		t.Fatalf("stale evidence = %d, want 2", stale)
	}
	if d.Work.Status != protocol.WorkCompleted {
		t.Fatalf("status = %s", d.Work.Status)
	}
}

func TestActiveEvidenceExcludesStaleSupersededUnavailable(t *testing.T) {
	now := protocol.WorkDetail{}.Work.CreatedAt
	stale := now
	d := protocol.WorkDetail{
		Nodes: []protocol.WorkNode{
			{ID: "c1", Kind: protocol.NodeCriterion, Status: "passed"},
			{ID: "c2", Kind: protocol.NodeCriterion, Status: StatusSuperseded},
		},
		Evidence: []protocol.Evidence{
			{ID: "ok", NodeID: "c1", Availability: protocol.AvailAvailable},
			{ID: "none", NodeID: "c1", Availability: protocol.AvailNone},
			{ID: "stale", NodeID: "c1", StaleAt: &stale, Availability: protocol.AvailNone},
			{ID: "gone", NodeID: "c1", Availability: protocol.AvailUnavailable},
			{ID: "sup", NodeID: "c2", Availability: protocol.AvailNone},
		},
	}
	var ids []string
	for _, e := range ActiveEvidence(d) {
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, ",") != "ok,none" {
		t.Fatalf("active = %v", ids)
	}
}

func TestExecEscalatesToGuided(t *testing.T) {
	for _, tool := range []string{"exec.start", "exec.write"} {
		if !EscalatesToGuided(tool, "yellow") || EscalatesToGuided(tool, "green") {
			t.Fatalf("%s escalation wrong", tool)
		}
	}
}

func TestNoApprovalOrBlocking(t *testing.T) {
	f := newFixture(t)
	f.svc.Workspace = func(ctx context.Context, root string) (fingerprint.Workspace, bool) {
		<-ctx.Done()
		return fingerprint.Workspace{}, false
	}
	f.svc.Vault = &vault.Vault{Dir: t.TempDir()}
	old := fingerprintBudget
	fingerprintBudget = 50 * time.Millisecond
	defer func() { fingerprintBudget = old }()
	f.begin(t, "g")
	f.passAll(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := f.svc.End(ctx, f.th.ID, protocol.TurnCompleted, false, "/r"); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("End blocked on an unavailable fingerprint")
	}
}

func TestSecretsInCommandsAndGoalNotPersisted(t *testing.T) {
	f := newFixture(t)
	f.begin(t, "deploy with API_TOKEN=goalsecret12345 please")
	plan := `{"checks":[{"label":"x","command":"API_TOKEN=cmdsecret12345 npm test","reason":"r"}]}`
	run := `{"results":[{"command":"API_TOKEN=cmdsecret12345 npm test","status":"passed","exit_code":0,"output":"ok"}]}`
	f.observe(t, Observation{Tool: "verification.plan", Output: plan})
	f.observe(t, Observation{Tool: "verification.run", Output: run})
	d := f.detail(t)
	blob, _ := json.Marshal(d)
	if strings.Contains(string(blob), "cmdsecret12345") || strings.Contains(string(blob), "goalsecret12345") {
		t.Fatalf("secret persisted: %s", blob)
	}
	if len(d.Attempts) != 1 || d.Attempts[0].CriterionNodeID == "" {
		t.Fatalf("redacted command must still match its criterion: %+v", d.Attempts)
	}
}

func TestTurnEndPathsAreCapped(t *testing.T) {
	f := newFixture(t)
	value := "v1"
	var paths []string
	for i := 0; i < 300; i++ {
		paths = append(paths, fmt.Sprintf("f%d.go", i))
	}
	f.svc.Workspace = func(ctx context.Context, root string) (fingerprint.Workspace, bool) {
		return fingerprint.Workspace{Value: value, Paths: paths}, true
	}
	f.begin(t, "g")
	f.passAll(t)
	value = "v2"
	f.end(t)
	n := 0
	for _, e := range f.detail(t).Evidence {
		if e.Kind == protocol.EvidenceFileChange {
			n++
		}
	}
	if n == 0 || n > maxWorkspacePaths+1 {
		t.Fatalf("file_change rows = %d, want 1..%d", n, maxWorkspacePaths+1)
	}
}

func TestSkippedVerificationFingerprintDoesNotInvalidate(t *testing.T) {
	f := newFixture(t)
	value := "turn1"
	f.svc.Workspace = func(ctx context.Context, root string) (fingerprint.Workspace, bool) {
		if value == "" {
			return fingerprint.Workspace{}, false
		}
		return fingerprint.Workspace{Value: value}, true
	}
	f.begin(t, "g")
	f.observe(t, Observation{Tool: "verification.plan", Output: planOut, Root: "/r"})
	f.observe(t, Observation{Tool: "verification.run", Output: runOut("failed", "failed"), Root: "/r"})
	f.end(t) // turn 1 ends with fingerprint "turn1"
	value = ""
	f.observe(t, Observation{Tool: "verification.run", Output: runOut("passed", "passed"), Root: "/r"}) // fingerprint skipped
	value = "turn2"
	f.end(t)
	if d := f.detail(t); d.Work.Status != protocol.WorkCompleted {
		t.Fatalf("status = %s: a pass whose fingerprint was skipped must not be invalidated by an older turn-end fingerprint", d.Work.Status)
	}
}

func TestOneFingerprintPerObservation(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.svc.Workspace = func(ctx context.Context, root string) (fingerprint.Workspace, bool) {
		calls++
		return fingerprint.Workspace{Value: "v"}, true
	}
	f.begin(t, "g")
	f.observe(t, Observation{Tool: "verification.run", Output: runOut("passed", "passed"), Root: "/r"})
	if calls != 1 {
		t.Fatalf("workspace fingerprints for one verification.run = %d, want 1", calls)
	}
}

func TestAttemptRefreshesExistingVaultObject(t *testing.T) {
	f := newFixture(t)
	f.withVault(t)
	f.begin(t, "g")
	run := `{"results":[{"command":"go test ./...","status":"passed","exit_code":0,"output":"identical output"}]}`
	f.observe(t, Observation{Tool: "verification.run", Output: run, Root: "/r"})
	rows, _ := f.st.ListVaultObjects(context.Background())
	old := rows[0].LastReferencedAt
	f.observe(t, Observation{Tool: "verification.run", Output: run, Root: "/r"})
	rows, _ = f.st.ListVaultObjects(context.Background())
	if len(rows) != 1 || !rows[0].LastReferencedAt.After(old) {
		t.Fatalf("a deduplicated put must refresh last_referenced_at: %+v", rows)
	}
}
