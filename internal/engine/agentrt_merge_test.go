package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/worktree"
)

func TestTaskMergeCompletionPreservesSourceIndex(t *testing.T) {
	env := planFixture(t)
	defer env.endTurn()
	ctx := t.Context()
	private := filepath.Join(t.TempDir(), "task")
	snapshot, err := worktree.CaptureTaskSnapshot(ctx, env.root, private)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(snapshot.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.e.Store.SaveAgentTaskWorkspace(ctx, store.AgentTaskWorkspace{TaskID: env.task.ID, SourceRoot: snapshot.SourceRoot, ExecutionRoot: private, SourceHead: snapshot.SourceHead, SnapshotCommit: snapshot.BaseCommit, Manifest: manifest, State: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "accepted.txt"), []byte("accepted\n"), 0644); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, private, "add", "-A")
	delegationGit(t, private, "-c", "user.name=Agent", "-c", "user.email=agent@example.invalid", "commit", "-m", "accepted")
	headTree := delegationGit(t, private, "rev-parse", "HEAD^{tree}")
	indexPath := delegationGit(t, env.root, "rev-parse", "--path-format=absolute", "--git-path", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.e.mergeRuntimeTaskResult(ctx, env.task.ID, headTree); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(env.root, "accepted.txt")); err != nil || string(got) != "accepted\n" {
		t.Fatalf("merged file %q %v", got, err)
	}
	if after, err := os.ReadFile(indexPath); err != nil || string(after) != string(before) {
		t.Fatal("source index changed")
	}
	journal, err := env.e.Store.TaskMerge(ctx, env.task.ID)
	if err != nil || journal.State != "merged" {
		t.Fatalf("journal %+v %v", journal, err)
	}
	if err := env.e.mergeRuntimeTaskResult(ctx, env.task.ID, headTree); err != nil {
		t.Fatalf("idempotent completion: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.root, "accepted.txt"), []byte("new user edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := env.e.mergeRuntimeTaskResult(ctx, env.task.ID, headTree); err == nil {
		t.Fatal("completion accepted a changed target after merge receipt")
	}
}

func TestMergeRecoveryLeavesIntentWithoutReapplying(t *testing.T) {
	env := planFixture(t)
	defer env.endTurn()
	ctx := t.Context()
	private := filepath.Join(t.TempDir(), "task")
	snapshot, err := worktree.CaptureTaskSnapshot(ctx, env.root, private)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(snapshot.Manifest)
	if err := env.e.Store.SaveAgentTaskWorkspace(ctx, store.AgentTaskWorkspace{TaskID: env.task.ID, SourceRoot: snapshot.SourceRoot, ExecutionRoot: private, SourceHead: snapshot.SourceHead, SnapshotCommit: snapshot.BaseCommit, Manifest: manifest, State: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "new.txt"), []byte("accepted\n"), 0644); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, private, "add", "-A")
	delegationGit(t, private, "-c", "user.name=Agent", "-c", "user.email=agent@example.invalid", "commit", "-m", "accepted")
	headTree := delegationGit(t, private, "rev-parse", "HEAD^{tree}")
	plan, err := worktree.PrepareTaskMerge(ctx, env.root, snapshot, headTree)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(plan)
	if err := env.e.Store.BeginTaskMerge(ctx, store.TaskMergeRecord{TaskID: env.task.ID, SourceRoot: snapshot.SourceRoot, HeadTree: headTree, Plan: raw}); err != nil {
		t.Fatal(err)
	}
	if err := env.e.mergeRuntimeTaskResult(ctx, env.task.ID, headTree); err == nil {
		t.Fatal("unresolved intent was reapplied")
	}
	if _, err := os.Stat(filepath.Join(env.root, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("source changed: %v", err)
	}
	env.e.reconcileTaskMerges(ctx)
	journal, err := env.e.Store.TaskMerge(ctx, env.task.ID)
	if err != nil || journal.State != "conflict" {
		t.Fatalf("recovered %+v %v", journal, err)
	}
	if err := env.e.mergeRuntimeTaskResult(ctx, env.task.ID, headTree); err != nil {
		t.Fatalf("retry after all-before recovery: %v", err)
	}
}
