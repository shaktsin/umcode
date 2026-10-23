package engine

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

func intentProposal(task protocol.ExecutionTask, user protocol.Item, kind, predicate string) protocol.TaskIntentProposal {
	resource := json.RawMessage(`{}`)
	if kind != "explanation" {
		resource = json.RawMessage(`{"type":"file","path":"notes.txt"}`)
	}
	return protocol.TaskIntentProposal{TaskID: task.ID, ExpectedRevision: task.Revision, OriginItemID: user.ID, Relation: "continue", Expectations: []protocol.TaskExpectationProposal{{Source: protocol.TaskSourceSpan{ItemID: user.ID, Start: 0, End: utf8.RuneCountInString(user.Text)}, Kind: kind, Predicate: predicate, Resource: resource}}}
}

// Catches accepting non-user sources or treating a proposal as permission.
func TestTaskIntentScopeValidation(t *testing.T) {
	for _, mode := range []string{"valid_save", "valid_inspection", "valid_explanation", "foreign_origin", "bad_span", "path_escape", "stale", "unknown_predicate", "forged_binding", "silent_replacement"} {
		t.Run(mode, func(t *testing.T) {
			e, th, add := taskContextFixture(t)
			user := add("Save notes.txt")
			if _, err := e.resolveTask(t.Context(), th, user); err != nil {
				t.Fatal(err)
			}
			snap, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
			if err != nil {
				t.Fatal(err)
			}
			ctx := tools.WithScope(t.Context(), &tools.Scope{Root: t.TempDir()})
			p := intentProposal(snap.Task, user, "mutation", "file_content")
			switch mode {
			case "valid_inspection":
				p = intentProposal(snap.Task, user, "inspection", "exists")
			case "valid_explanation":
				p = intentProposal(snap.Task, user, "explanation", "answer")
			case "foreign_origin":
				p.OriginItemID = "absent"
			case "bad_span":
				p.Expectations[0].Source.End = 999
			case "path_escape":
				p.Expectations[0].Resource = json.RawMessage(`{"type":"file","path":"../escape"}`)
			case "stale":
				p.ExpectedRevision = 0
			case "forged_binding":
				p.Expectations[0].Resource = json.RawMessage(`{"type":"file","path":"notes.txt","requestedPath":"other.txt"}`)
			case "unknown_predicate":
				p.Expectations[0].Predicate = "approved"
			case "silent_replacement":
				if _, err := e.Store.SetTaskExpectations(ctx, snap.Task.ID, snap.Task.Revision, user.ID, user.TurnID, p.Expectations); err != nil {
					t.Fatal(err)
				}
				snap, _ = e.Store.ActiveExecutionTask(ctx, th.ID)
				p = intentProposal(snap.Task, user, "explanation", "answer")
			}
			got, err := e.validateTaskIntent(ctx, th, protocol.Turn{ID: user.TurnID, ThreadID: th.ID}, p)
			valid := mode == "valid_save" || mode == "valid_inspection" || mode == "valid_explanation"
			if valid {
				if err != nil || len(got) != 1 {
					t.Fatalf("proposal=%+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("untrusted intent accepted")
			}
			if len(snap.Requirements) != 0 {
				t.Fatal("intent created authority")
			}
		})
	}
}
func TestTaskIntentResolution(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Save notes.txt")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	p := intentProposal(snap.Task, user, "mutation", "file_content")
	ctx := tools.WithScope(t.Context(), &tools.Scope{Root: t.TempDir()})
	got, err := e.resolveTaskExpectations(ctx, ctx, th, protocol.Turn{ID: user.TurnID, ThreadID: th.ID}, user, nil, nil, &p)
	if err != nil || len(got.Expectations) != 1 || got.Expectations[0].Kind != "mutation" {
		t.Fatalf("contract=%+v %v", got, err)
	}
	next := add("retry")
	got, err = e.resolveTaskExpectations(ctx, ctx, th, protocol.Turn{ID: next.TurnID, ThreadID: th.ID}, next, nil, nil, nil)
	if err != nil || got.Expectations[0].OriginItemID != user.ID {
		t.Fatalf("continuity=%+v %v", got, err)
	}
}

func TestTaskIntentRetryCannotDowngrade(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Save notes.txt")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	p := intentProposal(snap.Task, user, "mutation", "file_content")
	if _, err := e.Store.SetTaskExpectations(t.Context(), snap.Task.ID, snap.Task.Revision, user.ID, user.TurnID, p.Expectations); err != nil {
		t.Fatal(err)
	}
	retry := add("retry")
	snap, _ = e.Store.ActiveExecutionTask(t.Context(), th.ID)
	p = intentProposal(snap.Task, retry, "explanation", "answer")
	if _, err := e.validateTaskIntent(t.Context(), th, protocol.Turn{ID: retry.TurnID, ThreadID: th.ID}, p); err == nil {
		t.Fatal("retry erased original requested action")
	}
}
func TestTaskIntentLegacyOriginalCannotBeSkipped(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Save notes.txt")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	current := add("retry")
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	p := intentProposal(snap.Task, current, "explanation", "answer")
	if _, err := e.validateTaskIntent(t.Context(), th, protocol.Turn{ID: current.TurnID, ThreadID: th.ID}, p); err == nil {
		t.Fatal("legacy original intent skipped")
	}
}

func TestTaskIntentExplicitNewRequestBinding(t *testing.T) {
	e, th, add := taskContextFixture(t)
	user := add("Save notes.txt")
	if _, err := e.resolveTask(t.Context(), th, user); err != nil {
		t.Fatal(err)
	}
	p := intentProposal(protocol.ExecutionTask{}, user, "mutation", "file_content")
	p.OriginItemID = ""
	p.Expectations[0].Source.ItemID = ""
	ctx := tools.WithScope(t.Context(), &tools.Scope{Root: t.TempDir()})
	got, err := e.resolveTaskExpectations(ctx, ctx, th, protocol.Turn{ID: user.TurnID, ThreadID: th.ID}, user, nil, nil, &p)
	if err != nil || len(got.Expectations) != 1 || got.Expectations[0].OriginItemID != user.ID {
		t.Fatalf("structured new request cannot bind engine IDs: %+v %v", got, err)
	}
}
