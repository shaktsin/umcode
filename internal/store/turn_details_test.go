package store

import (
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/protocol"
	"path/filepath"
	"testing"
	"time"
)

func detailsFixture(t *testing.T) (*Store, protocol.TurnDetailsParams, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "details.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	th, err := s.CreateThread(t.Context(), protocol.Thread{Title: "inspection"})
	if err != nil {
		t.Fatal(err)
	}
	p := protocol.TurnDetailsParams{ThreadID: th.ID, TurnID: "turn_details"}
	if err = s.CreateTurn(t.Context(), protocol.Turn{ID: p.TurnID, ThreadID: p.ThreadID, Status: protocol.TurnCompleted, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return s, p, path
}

// Removing persistence, policy snapshotting or nullable measurement encoding must fail this test.
func TestTurnDetailsRoundTrip(t *testing.T) {
	s, p, path := detailsFixture(t)
	zero := int64(0)
	d := protocol.TurnDiagnostics{Version: 1, Complete: true, Policy: protocol.OptimizationSnapshot{Source: "stored", ContextCompiler: true}, Requests: []protocol.RequestDiagnostics{{Ordinal: 1, Phase: "chat", Model: "model", InputTokens: &zero, MeasurementOrigin: "provider", CostState: "unavailable"}}}
	if err := s.SaveTurnDiagnostics(t.Context(), p.TurnID, d); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.ReadTurnDetails(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Availability != "recorded" || got.Diagnostics == nil || !got.Diagnostics.Policy.ContextCompiler || got.Diagnostics.Policy.Source != "stored" {
		t.Fatalf("snapshot=%+v", got)
	}
	r := got.Diagnostics.Requests[0]
	if r.InputTokens == nil || *r.InputTokens != 0 || r.OutputTokens != nil || r.CostUSD != nil {
		t.Fatalf("measurements=%+v", r)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	json.Unmarshal(b, &fields)
	if fields["inputTokens"] != float64(0) || fields["outputTokens"] != nil {
		t.Fatalf("json=%s", b)
	}
	if err := s.SaveTurnDiagnostics(t.Context(), p.TurnID, protocol.TurnDiagnostics{Version: 2}); err == nil {
		t.Fatal("unsupported version accepted")
	}
}
func TestTurnDetailsLegacy(t *testing.T) {
	s, p, _ := detailsFixture(t)
	if err := s.InsertUsage(t.Context(), UsageRecord{ThreadID: p.ThreadID, TurnID: p.TurnID, Usage: protocol.UsageTotals{InputTokens: 12, OutputTokens: 3}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadTurnDetails(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Diagnostics != nil || got.Availability != "not_recorded" || got.Turn.Usage.InputTokens != 12 {
		t.Fatalf("legacy=%+v", got)
	}
}
func TestTurnDetailsWrongThread(t *testing.T) {
	s, p, _ := detailsFixture(t)
	p.ThreadID = "other"
	if _, err := s.ReadTurnDetails(t.Context(), p); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-thread err=%v", err)
	}
}
func TestTurnDetailsBounds(t *testing.T) {
	s, p, _ := detailsFixture(t)
	sources := make([]protocol.ContextSourceRef, 130)
	tools := make([]string, 131)
	for i := range sources {
		sources[i] = protocol.ContextSourceRef{ID: "evidence", Kind: "evidence"}
	}
	for i := range tools {
		tools[i] = "file.read"
	}
	requests := make([]protocol.RequestDiagnostics, 129)
	requests[0] = protocol.RequestDiagnostics{SelectedSources: sources, SubmittedSources: sources, ToolNames: tools}
	if err := s.SaveTurnDiagnostics(t.Context(), p.TurnID, protocol.TurnDiagnostics{Version: 1, Complete: true, Requests: requests}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadTurnDetails(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	d := got.Diagnostics
	r := d.Requests[0]
	if len(d.Requests) != 128 || d.OmittedRequests != 1 || len(r.SelectedSources) != 128 || r.OmittedSelectedSources != 2 || len(r.SubmittedSources) != 128 || r.OmittedSubmittedSources != 2 || len(r.ToolNames) != 128 || r.OmittedTools != 3 || got.Availability != "partial" {
		t.Fatalf("bounds=%+v request=%+v", d, r)
	}
	if len(requests[0].ToolNames) != 131 {
		t.Fatal("mutated caller")
	}
}
