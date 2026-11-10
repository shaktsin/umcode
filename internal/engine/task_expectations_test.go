package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

func expectationEngine(t *testing.T, kind, predicate string) (*Engine, protocol.Thread, protocol.Turn, context.Context, string) {
	t.Helper()
	e, th, root, _ := taskRuntimeFixture(t, "write_verified", false)
	snap, err := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	user, err := e.Store.GetItem(t.Context(), snap.Task.OriginItemID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithScope(t.Context(), &tools.Scope{Root: root, ProjectRoot: root, ProjectID: th.ProjectID, ThreadID: th.ID, ApprovalMode: "auto_all"})
	p := intentProposal(snap.Task, user, kind, predicate)
	turn := protocol.Turn{ID: user.TurnID, ThreadID: th.ID}
	if _, err := e.resolveTaskExpectations(ctx, ctx, th, turn, user, nil, nil, &p); err != nil {
		t.Fatal(err)
	}
	return e, th, turn, ctx, root
}
func expectationTool(t *testing.T, e *Engine, th protocol.Thread, turn protocol.Turn, ctx context.Context, name, args string) toolRunResult {
	t.Helper()
	out := e.runTool(ctx, ctx, th, turn, llm.ToolCall{ID: "call", Name: name, Args: json.RawMessage(args)}, nil)
	return out
}
func expectationResult(t *testing.T, e *Engine, th protocol.Thread, turn protocol.Turn, ctx context.Context) protocol.TaskExpectationEvaluation {
	t.Helper()
	snap, err := e.Store.ActiveExecutionTask(ctx, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.evaluateTaskExpectations(ctx, snap.Task.ID, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Catches completing a requested file change on narration, wrong-resource
// actions or old reads rather than a verified matching effect.
func TestTaskExpectationEvidence(t *testing.T) {
	for _, mode := range []string{"narration", "unrelated", "unverified", "verified", "stale_read"} {
		t.Run(mode, func(t *testing.T) {
			e, th, turn, ctx, _ := expectationEngine(t, "mutation", "file_content")
			if mode == "stale_read" {
				expectationTool(t, e, th, turn, ctx, "file.read", `{"path":"notes.txt"}`)
			}
			if mode != "narration" {
				path := "notes.txt"
				if mode == "unrelated" {
					path = "other.txt"
				}
				expectationTool(t, e, th, turn, ctx, "file.write", `{"path":"`+path+`","content":"after"}`)
			}
			if mode == "verified" || mode == "unrelated" {
				expectationTool(t, e, th, turn, ctx, "file.read", `{"path":"notes.txt"}`)
			}
			got := expectationResult(t, e, th, turn, ctx)
			want := "not_executed"
			if mode == "verified" {
				want = "satisfied"
			}
			if mode == "unverified" || mode == "stale_read" {
				want = "unverified"
			}
			if got.Status != want {
				t.Fatalf("outcome=%+v want=%s", got, want)
			}
		})
	}
}
func TestTaskExpectationInspection(t *testing.T) {
	e, th, turn, ctx, root := expectationEngine(t, "inspection", "exists")
	if err := os.Remove(filepath.Join(root, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	expectationTool(t, e, th, turn, ctx, "file.read", `{"path":"notes.txt"}`)
	got := expectationResult(t, e, th, turn, ctx)
	if got.Status != "satisfied" {
		t.Fatalf("actual absence failed existence inquiry: %+v", got)
	}
}
func TestTaskExpectationEnsure(t *testing.T) {
	e, th, turn, ctx, _ := expectationEngine(t, "mutation", "file_content")
	expectationTool(t, e, th, turn, ctx, "file.read", `{"path":"notes.txt"}`)
	if got := expectationResult(t, e, th, turn, ctx); got.Status == "satisfied" {
		t.Fatal("existence/read satisfied creation without effect")
	}
}
func TestTaskExpectationResourceRetarget(t *testing.T) {
	e, th, turn, ctx, root := expectationEngine(t, "mutation", "file_content")
	expectationTool(t, e, th, turn, ctx, "file.write", `{"path":"notes.txt","content":"after"}`)
	expectationTool(t, e, th, turn, ctx, "file.read", `{"path":"notes.txt"}`)
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("externally changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := expectationResult(t, e, th, turn, ctx); got.Status == "satisfied" {
		t.Fatal("stale proof certified changed resource")
	}
}

func TestTaskExpectationEnsureVerifiedDigest(t *testing.T) {
	e, th, old, ctx, _ := expectationEngine(t, "mutation", "file_content")
	next := old
	next.ID = "ensure-turn"
	next.Status = "running"
	if err := e.Store.CreateTurn(ctx, next); err != nil {
		t.Fatal(err)
	}
	// sha256 of the existing literal bytes "before", supplied in the user request.
	hash := sha256.Sum256([]byte("before"))
	digest := hex.EncodeToString(hash[:])
	user := persistTaskTestUser(t, e, th, next, "Ensure notes.txt already matches SHA256 "+digest)
	snap, _ := e.Store.ActiveExecutionTask(ctx, th.ID)
	p := intentProposal(snap.Task, user, "mutation", "file_content")
	p.Expectations[0].AllowExisting = true
	p.Expectations[0].Resource = json.RawMessage(`{"type":"file","path":"notes.txt","expectedSHA256":"` + digest + `"}`)
	if _, err := e.resolveTaskExpectations(ctx, ctx, th, next, user, nil, nil, &p); err != nil {
		t.Fatal(err)
	}
	expectationTool(t, e, th, next, ctx, "file.read", `{"path":"notes.txt"}`)
	if got := expectationResult(t, e, th, next, ctx); got.Status != "satisfied" {
		t.Fatalf("matching ensure failed: %+v", got)
	}
}
