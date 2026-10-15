package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

type runnerFunc func(context.Context, protocol.AgentTask, protocol.AgentChild) (protocol.AgentResult, error)

func (f runnerFunc) Run(ctx context.Context, t protocol.AgentTask, c protocol.AgentChild) (protocol.AgentResult, error) {
	return f(ctx, t, c)
}

func TestCoordinatorLifecycle(t *testing.T) {
	s, task := coordinatorFixture(t)
	started := make(chan context.Context, 4)
	release := make(chan struct{}, 4)
	var calls atomic.Int32
	c := NewCoordinator(context.Background(), s, WithRunner(runnerFunc(func(ctx context.Context, _ protocol.AgentTask, _ protocol.AgentChild) (protocol.AgentResult, error) {
		calls.Add(1)
		started <- ctx
		<-release
		return protocol.AgentResult{Summary: "done"}, ctx.Err()
	})))
	_, _, err := c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	child, err := c.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	runctx := <-started
	replay, err := c.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleReviewer, protocol.AgentAssignment{Prompt: "different"})
	if err != nil || replay.ID != child.ID || calls.Load() != 1 {
		t.Fatalf("spawn replay: %+v %v", replay, err)
	}
	_, err = c.Spawn(t.Context(), task.ID, "other", protocol.AgentRoleReviewer, protocol.AgentAssignment{Prompt: "review"})
	var busy *BusyError
	if !errors.As(err, &busy) || busy.ChildID != child.ID {
		t.Fatalf("busy: %v", err)
	}
	if _, err = c.Status(t.Context(), "foreign", child.ID); err == nil {
		t.Fatal("foreign child visible")
	}
	first, err := c.Followup(t.Context(), task.ID, child.ID, "fix1", "correct this")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Followup(t.Context(), task.ID, child.ID, "fix1", "different")
	if err != nil || first.CorrectionRounds != second.CorrectionRounds {
		t.Fatalf("followup replay: %v", err)
	}
	queue := FollowupsFromContext(runctx)
	if queue == nil {
		t.Fatal("missing safe-boundary queue")
	}
	pending, err := queue.Pending(t.Context())
	if err != nil || len(pending) != 1 || pending[0].Message != "correct this" {
		t.Fatalf("queue: %+v %v", pending, err)
	}
	if err = queue.Acknowledge(t.Context(), pending[0].CallID); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	done, err := c.Wait(t.Context(), task.ID, []string{child.ID})
	if err != nil || done[0].State != protocol.AgentStateCompleted {
		t.Fatalf("wait: %+v %v", done, err)
	}
	corrected, err := c.Followup(t.Context(), task.ID, child.ID, "fix2", "again")
	if err != nil || corrected.ID != child.ID || corrected.CorrectionRounds != 2 {
		t.Fatalf("correction: %+v %v", corrected, err)
	}
	<-started
	if _, err = c.Followup(t.Context(), task.ID, child.ID, "fix3", "third"); err == nil {
		t.Fatal("third correction admitted")
	}
	// Wait observes cancellation without releasing a still-running runner's slot.
	waiting := make(chan error, 1)
	go func() { _, e := c.Wait(t.Context(), task.ID, []string{child.ID}); waiting <- e }()
	if err = c.Cancel(t.Context(), task.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-waiting:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not wake")
	}
	_, err = c.Spawn(t.Context(), task.ID, "while-cancelling", protocol.AgentRoleReviewer, protocol.AgentAssignment{Prompt: "review"})
	if !errors.As(err, &busy) {
		t.Fatalf("released live slot: %v", err)
	}
	release <- struct{}{}
}

func TestCoordinatorPendingFollowupRetained(t *testing.T) {
	s, task := coordinatorFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	c := NewCoordinator(context.Background(), s, WithRunner(runnerFunc(func(context.Context, protocol.AgentTask, protocol.AgentChild) (protocol.AgentResult, error) {
		close(started)
		<-release
		return protocol.AgentResult{Summary: "partial"}, nil
	})))
	c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100})
	child, err := c.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err = c.Followup(t.Context(), task.ID, child.ID, "fix", "pending"); err != nil {
		t.Fatal(err)
	}
	close(release)
	got, err := c.Wait(t.Context(), task.ID, []string{child.ID})
	if err != nil || got[0].State != protocol.AgentStateFailed || got[0].Result.Blocker == "" {
		t.Fatalf("dropped pending correction: %+v %v", got, err)
	}
	// Durable idempotency survives a fresh coordinator and never resumes execution.
	restored := NewCoordinator(context.Background(), s)
	replay, err := restored.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "changed"})
	if err != nil || replay.ID != child.ID {
		t.Fatalf("durable replay: %+v %v", replay, err)
	}
}

type failingChildStore struct {
	*store.Store
	fail atomic.Bool
}

func (s *failingChildStore) UpdateAgentChild(ctx context.Context, child protocol.AgentChild, version int64) error {
	if s.fail.Load() {
		return errors.New("durable write unavailable")
	}
	return s.Store.UpdateAgentChild(ctx, child, version)
}
func TestCoordinatorCancellationRetainsSlotOnFailure(t *testing.T) {
	db, task := coordinatorFixture(t)
	s := &failingChildStore{Store: db}
	started := make(chan struct{})
	exit := make(chan struct{})
	returned := make(chan struct{})
	c := NewCoordinator(context.Background(), s, WithRunner(runnerFunc(func(ctx context.Context, _ protocol.AgentTask, _ protocol.AgentChild) (protocol.AgentResult, error) {
		close(started)
		<-ctx.Done()
		<-exit
		close(returned)
		return protocol.AgentResult{Summary: "partial", Findings: []protocol.AgentFinding{{Path: "file.go", Message: "recover this finding"}}}, ctx.Err()
	})))
	c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100})
	child, err := c.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	s.fail.Store(true)
	if err = c.Cancel(t.Context(), task.ID, child.ID); err == nil {
		t.Fatal("persistence failure hidden")
	}
	close(exit)
	<-returned
	// Finalization failure must wake Wait with an actionable recovery error.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err = c.Wait(ctx, task.ID, []string{child.ID}); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait did not surface finalization failure: %v", err)
	}
	_, err = c.Spawn(t.Context(), task.ID, "other", protocol.AgentRoleReviewer, protocol.AgentAssignment{Prompt: "review"})
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("failed terminal write freed slot: %v", err)
	}
	s.fail.Store(false)
	if err = c.Cancel(t.Context(), task.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	got, err := c.Status(t.Context(), task.ID, child.ID)
	if err != nil || got.State != protocol.AgentStateCancelled || got.Result.Summary != "partial" || len(got.Result.Findings) != 1 || got.Result.Findings[0].Message != "recover this finding" {
		t.Fatalf("cancel retry lost retained outcome: %+v %v", got, err)
	}
	if err = c.CancelTask(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := c.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleReviewer, protocol.AgentAssignment{Prompt: "different"})
	if err != nil || replay.ID != child.ID {
		t.Fatalf("terminal spawn replay: %+v %v", replay, err)
	}
	if _, err = c.Status(t.Context(), task.ID, child.ID); err != nil {
		t.Fatalf("terminal recovery inaccessible: %v", err)
	}
}

func TestCoordinatorActualChildCancellation(t *testing.T) {
	s, task := coordinatorFixture(t)
	started := make(chan context.Context)
	release := make(chan struct{})
	c := NewCoordinator(context.Background(), s, WithRunner(runnerFunc(func(ctx context.Context, _ protocol.AgentTask, _ protocol.AgentChild) (protocol.AgentResult, error) {
		reconstructed, cancel := context.WithCancel(InheritRunnerContext(context.Background(), ctx))
		defer cancel()
		if err := RegisterChildCancellation(reconstructed, cancel); err != nil {
			return protocol.AgentResult{}, err
		}
		started <- reconstructed
		<-release
		return protocol.AgentResult{}, nil
	})))
	c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100})
	child, err := c.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	reconstructed := <-started
	if FollowupsFromContext(reconstructed) == nil {
		t.Fatal("lost durable queue at engine boundary")
	}
	if err = c.Cancel(t.Context(), task.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reconstructed.Done():
	case <-time.After(time.Second):
		t.Fatal("actual child turn still running")
	}
	close(release)
}

func TestCoordinatorTaskCancellationStopsActualChild(t *testing.T) {
	s, task := coordinatorFixture(t)
	started := make(chan context.Context)
	release := make(chan struct{})
	c := NewCoordinator(context.Background(), s, WithRunner(runnerFunc(func(ctx context.Context, _ protocol.AgentTask, _ protocol.AgentChild) (protocol.AgentResult, error) {
		actual, cancel := context.WithCancel(InheritRunnerContext(context.Background(), ctx))
		defer cancel()
		RegisterChildCancellation(actual, cancel)
		started <- actual
		select {
		case <-actual.Done():
		case <-release:
		}
		return protocol.AgentResult{}, actual.Err()
	})))
	c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100})
	_, err := c.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	actual := <-started
	if err = c.CancelTask(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-actual.Done():
	case <-time.After(100 * time.Millisecond):
		close(release)
		t.Fatal("parent cancellation lost actual child turn handle")
	}
}

func TestCoordinatorDurableOwnershipChangesDenied(t *testing.T) {
	s, task := coordinatorFixture(t)
	c := NewCoordinator(context.Background(), s, WithRunner(runnerFunc(func(context.Context, protocol.AgentTask, protocol.AgentChild) (protocol.AgentResult, error) {
		t.Error("runner crossed changed ownership")
		return protocol.AgentResult{}, nil
	})))
	if _, _, err := c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE agent_tasks SET base_revision='changed' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "work"}); err == nil {
		t.Fatal("durable base snapshot changed without denial")
	}
}

type lifecycleEvents chan protocol.DelegationUpdated

func (e lifecycleEvents) Publish(_ string, method string, payload any) {
	if method == protocol.NotifyDelegationUpdated {
		e <- payload.(protocol.DelegationUpdated)
	}
}
func TestCoordinatorCancelledPartialResultAndEvents(t *testing.T) {
	s, task := coordinatorFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	events := make(lifecycleEvents, 12)
	c := NewCoordinator(context.Background(), s, WithEventSink(events), WithRunner(runnerFunc(func(ctx context.Context, _ protocol.AgentTask, _ protocol.AgentChild) (protocol.AgentResult, error) {
		close(started)
		<-ctx.Done()
		<-release
		return protocol.AgentResult{Summary: "recoverable partial", ChangedPaths: []string{"file.go"}}, ctx.Err()
	})))
	c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100})
	child, err := c.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err = c.Cancel(t.Context(), task.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	var version int64
	for {
		select {
		case event := <-events:
			if event.ThreadID != task.ThreadID || event.TaskID != task.ID || event.Child.ID != child.ID || event.Child.Version < version {
				t.Fatalf("unstable event %+v", event)
			}
			version = event.Child.Version
			if event.Child.Result.Summary == "recoverable partial" {
				if event.Child.State != protocol.AgentStateCancelled {
					t.Fatal(event.Child.State)
				}
				return
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatal("cancelled runner partial result lost")
		}
	}
}

func TestCoordinatorConcurrentSpawnsAndImmutableSnapshots(t *testing.T) {
	s, task := coordinatorFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	c := NewCoordinator(context.Background(), s, WithRunner(runnerFunc(func(_ context.Context, _ protocol.AgentTask, child protocol.AgentChild) (protocol.AgentResult, error) {
		calls.Add(1)
		child.Assignment.AcceptanceChecks[0] = "runner mutation"
		close(started)
		<-release
		return protocol.AgentResult{Summary: "done"}, nil
	})))
	c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100})
	type outcome struct {
		child protocol.AgentChild
		err   error
	}
	out := make(chan outcome, 12)
	for i := 0; i < 12; i++ {
		go func(i int) {
			child, err := c.Spawn(t.Context(), task.ID, fmt.Sprintf("call-%d", i), protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "work", AcceptanceChecks: []string{"original"}})
			out <- outcome{child, err}
		}(i)
	}
	var child protocol.AgentChild
	success := 0
	for i := 0; i < 12; i++ {
		got := <-out
		if got.err == nil {
			success++
			child = got.child
		} else {
			var busy *BusyError
			if !errors.As(got.err, &busy) {
				t.Fatal(got.err)
			}
		}
	}
	if success != 1 {
		t.Fatalf("active admissions=%d", success)
	}
	<-started
	child.Assignment.AcceptanceChecks[0] = "caller mutation"
	durable, err := c.Status(t.Context(), task.ID, child.ID)
	if err != nil || durable.Assignment.AcceptanceChecks[0] != "original" || calls.Load() != 1 {
		t.Fatalf("snapshot leaked %+v %v", durable, err)
	}
	close(release)
	if _, err = c.Wait(t.Context(), task.ID, []string{child.ID}); err != nil {
		t.Fatal(err)
	}
}

type failingTaskLifecycleStore struct {
	*store.Store
	failTask atomic.Bool
}

func (s *failingTaskLifecycleStore) UpdateAgentTaskState(ctx context.Context, taskID, threadID, projectID string, expected, next protocol.AgentState) error {
	if s.failTask.Load() {
		return store.ErrAgentConflict
	}
	return s.Store.UpdateAgentTaskState(ctx, taskID, threadID, projectID, expected, next)
}
func TestCoordinatorParentPersistenceFailureRetainsSlot(t *testing.T) {
	db, task := coordinatorFixture(t)
	s := &failingTaskLifecycleStore{Store: db}
	events := make(lifecycleEvents, 12)
	started := make(chan struct{})
	c := NewCoordinator(context.Background(), s, WithEventSink(events), WithRunner(runnerFunc(func(ctx context.Context, _ protocol.AgentTask, _ protocol.AgentChild) (protocol.AgentResult, error) {
		close(started)
		<-ctx.Done()
		return protocol.AgentResult{Summary: "stopped"}, ctx.Err()
	})))
	c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100})
	child, err := c.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	s.failTask.Store(true)
	if err = c.CancelTask(t.Context(), task.ID); !errors.Is(err, store.ErrAgentConflict) {
		t.Fatal(err)
	}
	for event := range events {
		if event.Child.Result.Summary == "stopped" {
			break
		}
	}
	if err = c.Cancel(t.Context(), task.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	c.lifecycleMu.Lock()
	slot := c.slots[task.ID]
	c.lifecycleMu.Unlock()
	if slot == nil {
		t.Fatal("child cancel bypassed failed task cancellation persistence")
	}
	s.failTask.Store(false)
	if err = c.CancelTask(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	c.lifecycleMu.Lock()
	slot = c.slots[task.ID]
	c.lifecycleMu.Unlock()
	if slot != nil {
		t.Fatal("successful task cancellation retained exited slot")
	}
}

type finalizationFailStore struct {
	*store.Store
	failRead, failWrite atomic.Bool
}

func (s *finalizationFailStore) AgentChild(ctx context.Context, taskID, childID string) (protocol.AgentChild, error) {
	if s.failRead.Load() {
		return protocol.AgentChild{}, errors.New("final read unavailable")
	}
	return s.Store.AgentChild(ctx, taskID, childID)
}
func (s *finalizationFailStore) UpdateAgentChild(ctx context.Context, child protocol.AgentChild, version int64) error {
	if s.failWrite.Load() {
		return errors.New("final write unavailable")
	}
	return s.Store.UpdateAgentChild(ctx, child, version)
}
func TestCoordinatorFinalizationFailureRecovery(t *testing.T) {
	for _, tt := range []struct {
		mode        string
		taskCancel  bool
		runnerError bool
	}{{"read", false, false}, {"write", false, false}, {"write", true, false}, {"write", false, true}} {
		mode := tt.mode
		t.Run(fmt.Sprintf("%s/taskCancel=%v/runnerError=%v", mode, tt.taskCancel, tt.runnerError), func(t *testing.T) {
			db, task := coordinatorFixture(t)
			s := &finalizationFailStore{Store: db}
			events := make(lifecycleEvents, 20)
			started := make(chan struct{})
			release := make(chan struct{})
			var calls atomic.Int32
			c := NewCoordinator(context.Background(), s, WithEventSink(events), WithRunner(runnerFunc(func(context.Context, protocol.AgentTask, protocol.AgentChild) (protocol.AgentResult, error) {
				calls.Add(1)
				close(started)
				<-release
				if mode == "read" {
					s.failRead.Store(true)
				} else {
					s.failWrite.Store(true)
				}
				var runErr error
				if tt.runnerError {
					runErr = errors.New("runner failed")
				}
				return protocol.AgentResult{Summary: "finished outcome", Findings: []protocol.AgentFinding{{Message: "retained finding"}}}, runErr
			})))
			c.Task(t.Context(), task, protocol.ExecutionLimits{MaxTokens: 100})
			child, err := c.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleImplementer, protocol.AgentAssignment{Prompt: "work"})
			if err != nil {
				t.Fatal(err)
			}
			<-started
			waiting := make(chan error, 1)
			go func() { _, err := c.Wait(t.Context(), task.ID, []string{child.ID}); waiting <- err }()
			close(release)
			select {
			case err = <-waiting:
				if err == nil {
					t.Fatal("wait hid finalization error")
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatal("wait remained blocked after finalization failure")
			}

			seen := false
			deadline := time.After(200 * time.Millisecond)
			for !seen {
				select {
				case event := <-events:
					raw, _ := json.Marshal(event)
					var envelope struct {
						Error string `json:"error"`
					}
					json.Unmarshal(raw, &envelope)
					if envelope.Error != "" {
						seen = true
						if event.Child.Result.Summary != "finished outcome" {
							t.Fatal("failure event lost pending result")
						}
						event.Child.Result.Findings[0].Message = "event mutation"
					}
				case <-deadline:
					t.Fatal("no finalization failure event")
				}
			}
			got, err := c.Status(t.Context(), task.ID, child.ID)
			if err == nil || got.Result.Summary != "finished outcome" {
				t.Fatalf("status lost pending result/error: %+v %v", got, err)
			}
			got.Result.Findings[0].Message = "caller mutation"
			if tt.taskCancel && mode == "write" {
				if retryErr := c.CancelTask(t.Context(), task.ID); retryErr == nil {
					t.Fatal("failed parent recovery hid write outage")
				}
				var last protocol.DelegationUpdated
				for len(events) > 0 {
					last = <-events
				}
				if last.Error == "" || last.Child.Result.Summary != "finished outcome" || len(last.Child.Result.Findings) != 1 || last.Child.Result.Findings[0].Message != "retained finding" {
					t.Fatalf("failed parent recovery cleared last retained result/error event: %+v", last)
				}
			}
			s.failRead.Store(false)
			s.failWrite.Store(false)
			if tt.taskCancel {
				err = c.CancelTask(t.Context(), task.ID)
			} else {
				err = c.Cancel(t.Context(), task.ID, child.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err = c.Status(t.Context(), task.ID, child.ID)
			if err != nil || got.Result.Summary != "finished outcome" || got.Result.Findings[0].Message != "retained finding" || calls.Load() != 1 {
				t.Fatalf("recovery lost/repeated outcome: %+v %v calls=%d", got, err, calls.Load())
			}
			if tt.taskCancel {
				var last protocol.DelegationUpdated
				for len(events) > 0 {
					last = <-events
				}
				if last.Error != "" || last.Child.Result.Summary != "finished outcome" || len(last.Child.Result.Findings) != 1 || last.Child.Result.Findings[0].Message != "retained finding" {
					t.Fatalf("successful recovery did not clear error with retained result: %+v", last)
				}
			}
			if tt.runnerError && got.Result.Blocker != "runner failed" {
				t.Fatalf("runner error lost after recovery: %+v", got.Result)
			}
			c.lifecycleMu.Lock()
			slot := c.slots[task.ID]
			c.lifecycleMu.Unlock()
			if slot != nil {
				t.Fatal("recovered exited slot retained")
			}
		})
	}
}
