package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestPlanSourceImmutable(t *testing.T) {
	s, task := runtimeFixture(t)
	plan := protocol.AgentPlan{ID: "source-plan", ThreadID: task.ThreadID, State: protocol.PlanProposed, Agents: []protocol.PlanAgent{{Name: "worker", Purpose: "test"}}, CreatedAt: time.Now()}
	if err := s.SaveAgentPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	source := AgentPlanSource{PlanID: plan.ID, SourceRoot: "/selected", Manifest: json.RawMessage(`{"head":"abc"}`)}
	if err := s.SaveAgentPlanSource(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	got, err := s.AgentPlanSource(t.Context(), plan.ID)
	if err != nil || got.SourceRoot != source.SourceRoot || string(got.Manifest) != string(source.Manifest) {
		t.Fatalf("source=%+v, %v", got, err)
	}
	if err := s.SaveAgentPlanSource(t.Context(), source); err != nil {
		t.Fatalf("replay: %v", err)
	}
	changed := source
	changed.Manifest = json.RawMessage(`{"head":"different"}`)
	if err := s.SaveAgentPlanSource(t.Context(), changed); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("changed source: %v", err)
	}
	if _, err := s.AgentPlanSource(t.Context(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing source: %v", err)
	}
}
