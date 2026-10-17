package engine

import (
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"testing"
)

func TestExplicitTaskRequirements(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Review my design before implementation")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	proposal := protocol.RequirementProposal{TaskID: snap.Task.ID, ExpectedRevision: snap.Task.Revision, Origin: "user", OriginRef: user.ID, Scope: json.RawMessage(`{"tools":["file.write","file.edit","shell.run"]}`), Condition: json.RawMessage(`{"type":"approval_before_actions"}`)}
	req, err := e.validateTaskRequirement(t.Context(), th, proposal)
	if err != nil || req.Status != "active" || req.OriginRef != user.ID {
		t.Fatalf("requirement=%+v %v", req, err)
	}
	requirements, err := e.Store.TaskRequirements(t.Context(), snap.Task.ID)
	if err != nil || len(requirements) != 1 {
		t.Fatalf("requirements=%+v %v", requirements, err)
	}
	for _, origin := range []string{"assistant", "skill_output", "policy"} {
		proposal.Origin = origin
		if _, err := e.validateTaskRequirement(t.Context(), th, proposal); err == nil {
			t.Fatalf("untrusted origin accepted: %s", origin)
		}
	}
	proposal.Origin = "user"
	proposal.Condition = json.RawMessage(`{"type":"approved"}`)
	if _, err := e.validateTaskRequirement(t.Context(), th, proposal); err == nil {
		t.Fatal("self approval accepted")
	}
	next := add("retry")
	if _, err := e.resolveTask(t.Context(), th, next); err != nil {
		t.Fatal(err)
	}
	proposal.OriginRef = user.ID
	proposal.Condition = json.RawMessage(`{"type":"no_actions"}`)
	if _, err := e.validateTaskRequirement(t.Context(), th, proposal); err == nil {
		t.Fatal("historical user reference accepted as current authority")
	}
}

func TestLegacyTaskRecovery(t *testing.T) {
	for _, material := range []bool{false, true} {
		t.Run(map[bool]string{false: "inferred", true: "unknown_gate"}[material], func(t *testing.T) {
			e, th, add := taskContextFixture(t)
			original := add("Update notes.txt with corrections")
			w, err := e.Store.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, Goal: original.Text, WorkflowDepth: protocol.DepthDesigned})
			if err != nil {
				t.Fatal(err)
			}
			if material {
				if _, err := e.Store.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: "decision", Title: "Review solution", Status: "proposed", Content: json.RawMessage(`{"required":true,"gate_kind":"security"}`)}); err != nil {
					t.Fatal(err)
				}
			}
			current := add("retry")
			snap, err := e.recoverLegacyTask(t.Context(), th, current)
			if err != nil {
				t.Fatal(err)
			}
			if snap.Task.OriginItemID != original.ID || snap.Task.Objective != original.Text {
				t.Fatalf("legacy origin=%+v", snap.Task)
			}
			if material && len(snap.Requirements) != 1 {
				t.Fatal("unknown legacy gate lost")
			}
			if !material && len(snap.Requirements) != 0 {
				t.Fatal("inferred depth became a requirement")
			}
			again, err := e.recoverLegacyTask(t.Context(), th, current)
			if err != nil || again.Task.ID != snap.Task.ID || len(again.Requirements) != len(snap.Requirements) {
				t.Fatalf("legacy recovery duplicated task/requirements: %+v %v", again, err)
			}
			unchanged, err := e.Store.GetWorkDetail(t.Context(), w.ID)
			if err != nil || unchanged.Work.Revision != w.Revision {
				t.Fatal("migration rewrote graph", err)
			}
		})
	}
}

func TestLegacyTaskRecoveryAtomic(t *testing.T) {
	e, th, add := taskContextFixture(t)
	original := add("Edit notes.txt")
	w, err := e.Store.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, Goal: original.Text, WorkflowDepth: protocol.DepthDesigned})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: "decision", Status: "proposed", Content: json.RawMessage(`{"required":true,"gate_kind":"security"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.ExecContext(t.Context(), `CREATE TRIGGER fail_requirement BEFORE INSERT ON task_requirements BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	_, err = e.recoverLegacyTask(t.Context(), th, add("retry"))
	if err == nil {
		t.Fatal("injected failure ignored")
	}
	if _, err := e.Store.ActiveExecutionTask(t.Context(), th.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("partial migration left a task without its requirements", err)
	}
}
func TestExplicitConfiguredRequirement(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Edit notes.txt")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.Cfg = &config.Config{Policy: config.PolicyConfig{RequireWorkflowApproval: true}}
	proposal := protocol.RequirementProposal{TaskID: snap.Task.ID, ExpectedRevision: snap.Task.Revision, Origin: "policy", OriginRef: "policy.require_workflow_approval", Scope: json.RawMessage(`{}`), Condition: json.RawMessage(`{"type":"approval_before_actions"}`)}
	req, err := e.validateTaskRequirement(t.Context(), th, proposal)
	if err != nil || req.Origin != "policy" {
		t.Fatalf("configured=%+v %v", req, err)
	}
}
