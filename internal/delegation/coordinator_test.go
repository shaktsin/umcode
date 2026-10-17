package delegation

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

func coordinatorFixture(t *testing.T) (*store.Store, protocol.AgentTask) {
	t.Helper()
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	th, err := s.CreateThread(t.Context(), protocol.Thread{Title: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	tr := protocol.Turn{ID: "root", ThreadID: th.ID, Status: protocol.TurnRunning, StartedAt: time.Now()}
	if err = s.CreateTurn(t.Context(), tr); err != nil {
		t.Fatal(err)
	}
	task := protocol.AgentTask{ID: "task", ThreadID: th.ID, RootTurnID: tr.ID, Activation: protocol.AgentActivationExplicitUser, State: protocol.AgentStateRunning, BaseRevision: "base"}
	if err = s.CreateAgentTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	return s, task
}

func TestCoordinatorSharedPersistenceAndCancellation(t *testing.T) {
	s, task := coordinatorFixture(t)
	base, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	c := NewCoordinator(base, s)
	limits := protocol.ExecutionLimits{MaxTokens: 100, MaxCostUSD: 1}
	ctx, b, err := c.Task(t.Context(), task, limits)
	if err != nil {
		t.Fatal(err)
	}
	settle, err := b.Reserve(ctx, "lead", protocol.UsageTotals{OutputTokens: 60})
	if err != nil {
		t.Fatal(err)
	}
	settle(protocol.UsageTotals{OutputTokens: 60, Requests: 1})
	_, same, err := c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 1000})
	if err != nil || same != b {
		t.Fatalf("follow-up reset: %p %p %v", same, b, err)
	}
	// A fresh coordinator restores actuals and the original task limits.
	restored := NewCoordinator(base, s)
	_, rb, err := restored.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if rb.Usage().OutputTokens != 60 {
		t.Fatal("lost durable usage")
	}
	if _, err = rb.Reserve(t.Context(), "too-much", protocol.UsageTotals{OutputTokens: 41}); err == nil {
		t.Fatal("restored task limits reset")
	}
	child, err := s.CreateAgentChild(t.Context(), protocol.AgentChild{TaskID: task.ID, Role: protocol.AgentRoleImplementer, Assignment: protocol.AgentAssignment{Prompt: "work"}}, "spawn")
	if err != nil {
		t.Fatal(err)
	}
	// Both pending approval-style waits and running subprocesses inherit task cancellation.
	wait := make(chan struct{})
	go func() { <-ctx.Done(); close(wait) }()
	cmd := exec.CommandContext(ctx, "sleep", "30")
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err = c.CancelTask(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wait:
	case <-time.After(time.Second):
		t.Fatal("task waiter leaked")
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("subprocess survived cancellation")
	}
	got, err := s.AgentChild(t.Context(), task.ID, child.ID)
	if err != nil || got.State != protocol.AgentStateCancelled {
		t.Fatalf("child not durably cancelled: %+v %v", got, err)
	}
	if _, _, err = c.Task(t.Context(), task, limits); err == nil {
		t.Fatal("stale admission snapshot revived cancelled task")
	}
	if err = c.CancelTask(t.Context(), task.ID); err != nil {
		t.Fatalf("idempotent cancel: %v", err)
	}
}

func TestCoordinatorCancellationFailureStillStopsContexts(t *testing.T) {
	s, task := coordinatorFixture(t)
	c := NewCoordinator(context.Background(), s)
	ctx, _, err := c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`CREATE TRIGGER fail_cancel BEFORE UPDATE ON agent_tasks BEGIN SELECT RAISE(FAIL,'disk failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err = c.CancelTask(t.Context(), task.ID); err == nil {
		t.Fatal("lost persistence error")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("paid execution remains possible after cancellation write failure")
	}
	if _, _, err = c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100}); err == nil {
		t.Fatal("failed cancellation reopened live owner")
	}
}

func TestCoordinatorShutdownOwnsTaskContext(t *testing.T) {
	s, task := coordinatorFixture(t)
	base, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	request, cancelRequest := context.WithCancel(t.Context())
	c := NewCoordinator(base, s)
	ctx, _, err := c.Task(request, task, protocol.ExecutionLimits{MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	cancelRequest()
	if ctx.Err() != nil {
		t.Fatal("HTTP request owned task lifetime")
	}
	shutdown()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("shutdown did not cancel task")
	}
}

func TestCoordinatorRestoredDurationAndDetachedAdmission(t *testing.T) {
	s, task := coordinatorFixture(t)
	if _, err := s.DB.Exec(`UPDATE agent_tasks SET created_at=? WHERE id=?`, store.FormatTime(time.Now().Add(-2*time.Minute)), task.ID); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(context.Background(), s)
	ctx, b, err := c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100, MaxDurationMinutes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("task deadline reset on follow-up")
	}
	if _, err = b.Reserve(t.Context(), "detached", protocol.UsageTotals{OutputTokens: 1}); err == nil {
		t.Fatal("detached request escaped task deadline")
	}
}

func TestCoordinatorReadFailureStillStopsContexts(t *testing.T) {
	s, task := coordinatorFixture(t)
	c := NewCoordinator(context.Background(), s)
	ctx, b, err := c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.CancelTask(t.Context(), task.ID); err == nil {
		t.Fatal("lost task read error")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("failed task read left live paid execution")
	}
	if _, err = b.Reserve(t.Context(), "detached-after-cancel", protocol.UsageTotals{OutputTokens: 1}); err == nil {
		t.Fatal("detached request escaped cancellation")
	}
}

// conflictTaskStore models concurrent durable updates between a failed CAS and
// its reload. It keeps records active on exhaustion, or can expose completion
// on the conflicting write; those are distinct outcomes for slot ownership.
type conflictTaskStore struct {
	task                          protocol.AgentTask
	child                         protocol.AgentChild
	taskConflicts, childConflicts int
	taskWrites, childWrites       int
	taskCompletes, childCompletes bool
}

func (s *conflictTaskStore) AgentTask(context.Context, string) (protocol.AgentTask, error) {
	return s.task, nil
}
func (s *conflictTaskStore) AgentTaskBudget(_ context.Context, _ string, limits protocol.ExecutionLimits) (protocol.ExecutionLimits, protocol.UsageTotals, error) {
	return limits, protocol.UsageTotals{}, nil
}
func (*conflictTaskStore) UpdateAgentTaskBudget(context.Context, string, protocol.UsageTotals) error {
	return nil
}
func (s *conflictTaskStore) UpdateAgentTaskState(_ context.Context, _, _, _ string, _, next protocol.AgentState) error {
	s.taskWrites++
	if s.taskWrites <= s.taskConflicts {
		if s.taskCompletes {
			s.task.State = protocol.AgentStateCompleted
		}
		return store.ErrAgentConflict
	}
	s.task.State = next
	return nil
}
func (s *conflictTaskStore) AgentChildren(context.Context, string) ([]protocol.AgentChild, error) {
	return []protocol.AgentChild{s.child}, nil
}
func (s *conflictTaskStore) AgentChild(context.Context, string, string) (protocol.AgentChild, error) {
	return s.child, nil
}
func (s *conflictTaskStore) UpdateAgentChild(_ context.Context, child protocol.AgentChild, _ int64) error {
	s.childWrites++
	if s.childWrites <= s.childConflicts {
		s.child.Version++
		if s.childCompletes {
			s.child.State = protocol.AgentStateCompleted
		}
		return store.ErrAgentConflict
	}
	s.child = child
	s.child.Version++
	return nil
}

func TestCoordinatorCancellationCASConflicts(t *testing.T) {
	for _, tt := range []struct {
		name                            string
		taskConflicts, childConflicts   int
		taskCompletes, childCompletes   bool
		wantTask, wantChild             protocol.AgentState
		wantTaskError, wantChildError   bool
		wantTaskWrites, wantChildWrites int
	}{
		{name: "task exhaustion remains an error", taskConflicts: 3, wantTask: protocol.AgentStateRunning, wantChild: protocol.AgentStateCancelled, wantTaskError: true, wantTaskWrites: 3, wantChildWrites: 1},
		{name: "child exhaustion remains an error", childConflicts: 3, wantTask: protocol.AgentStateCancelled, wantChild: protocol.AgentStateRunning, wantChildError: true, wantTaskWrites: 1, wantChildWrites: 3},
		{name: "task and child exhaustion are both surfaced", taskConflicts: 3, childConflicts: 3, wantTask: protocol.AgentStateRunning, wantChild: protocol.AgentStateRunning, wantTaskError: true, wantChildError: true, wantTaskWrites: 3, wantChildWrites: 3},
		{name: "successful retry cancels both records", taskConflicts: 2, childConflicts: 2, wantTask: protocol.AgentStateCancelled, wantChild: protocol.AgentStateCancelled, wantTaskWrites: 3, wantChildWrites: 3},
		{name: "terminal task observed on reload succeeds", taskConflicts: 3, taskCompletes: true, wantTask: protocol.AgentStateCompleted, wantChild: protocol.AgentStateCancelled, wantTaskWrites: 1, wantChildWrites: 1},
		{name: "terminal child observed on reload succeeds", childConflicts: 3, childCompletes: true, wantTask: protocol.AgentStateCancelled, wantChild: protocol.AgentStateCompleted, wantTaskWrites: 1, wantChildWrites: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &conflictTaskStore{task: protocol.AgentTask{ID: "task", ThreadID: "thread", State: protocol.AgentStateRunning, CreatedAt: time.Now()}, child: protocol.AgentChild{ID: "child", TaskID: "task", Role: protocol.AgentRoleImplementer, State: protocol.AgentStateRunning, Version: 1}, taskConflicts: tt.taskConflicts, childConflicts: tt.childConflicts, taskCompletes: tt.taskCompletes, childCompletes: tt.childCompletes}
			c := NewCoordinator(context.Background(), s)
			ctx, b, err := c.Task(t.Context(), s.task, protocol.ExecutionLimits{MaxTokens: 100})
			if err != nil {
				t.Fatal(err)
			}
			err = c.CancelTask(t.Context(), "task")
			if tt.wantTaskError || tt.wantChildError {
				if !errors.Is(err, store.ErrAgentConflict) {
					t.Fatalf("active durable records must return conflict: %v", err)
				}
				if tt.wantTaskError && !strings.Contains(err.Error(), "cancel task:") {
					t.Fatalf("task exhaustion error lost: %v", err)
				}
				if tt.wantChildError && !strings.Contains(err.Error(), "cancel child child:") {
					t.Fatalf("child exhaustion error lost: %v", err)
				}
			} else if err != nil {
				t.Fatalf("durable terminal record should succeed: %v", err)
			}
			if s.task.State != tt.wantTask || s.child.State != tt.wantChild {
				t.Fatalf("durable states task=%s child=%s", s.task.State, s.child.State)
			}
			if s.taskWrites != tt.wantTaskWrites || s.childWrites != tt.wantChildWrites {
				t.Fatalf("unbounded/unnecessary CAS writes task=%d child=%d", s.taskWrites, s.childWrites)
			}
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("durable conflicts left live execution running")
			}
			if _, err = b.Reserve(t.Context(), "after-cancel", protocol.UsageTotals{OutputTokens: 1}); err == nil {
				t.Fatal("detached request escaped cancellation")
			}
		})
	}
}
