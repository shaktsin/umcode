package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/skills"
	"github.com/shaktsin/umcode/internal/store"
)

type delegationProvider struct {
	mu       sync.Mutex
	admitted []string
}

func (*delegationProvider) ID() string { return "claude" }
func (*delegationProvider) ListModels(context.Context, llm.Credential) ([]string, error) {
	return []string{"deterministic"}, nil
}
func (p *delegationProvider) Stream(ctx context.Context, _ llm.Credential, _ llm.Request) (<-chan llm.Event, error) {
	task, ok := DelegationTaskFromContext(ctx)
	id := ""
	if ok {
		id = task.ID
	}
	p.mu.Lock()
	p.admitted = append(p.admitted, id)
	p.mu.Unlock()
	ch := make(chan llm.Event, 2)
	ch <- llm.Event{Type: llm.EventTextDelta, Text: "Done."}
	ch <- llm.Event{Type: llm.EventDone}
	close(ch)
	return ch, nil
}
func (p *delegationProvider) last() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.admitted) == 0 {
		return ""
	}
	return p.admitted[len(p.admitted)-1]
}
func (p *delegationProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.admitted)
}
func delegationGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func delegationFixture(t *testing.T) (*Engine, protocol.Thread, string, *delegationProvider) {
	t.Helper()
	home := t.TempDir()
	root := t.TempDir()
	cfg := config.Default(home)
	cfg.LLM.Provider = "claude"
	cfg.LLM.Model = "deterministic"
	st, err := store.Open(t.Context(), filepath.Join(home, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p := &delegationProvider{}
	reg := llm.NewRegistry()
	reg.Register(p)
	e, err := New(t.Context(), Options{Config: cfg, Store: st, Secrets: secrets.NewMemoryStore(), LLMs: reg, DisableMCP: true, DisableScheduler: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Shutdown(context.Background()) })
	if _, err = e.Creds.Add(t.Context(), protocol.CredentialAddParams{Provider: "claude", Secret: "fake", IsDefault: true}); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, root, "init")
	delegationGit(t, root, "config", "user.name", "Test")
	delegationGit(t, root, "config", "user.email", "test@example.invalid")
	// The generated instructions are part of the explicit committed base.
	proj, err := e.Projects.Create(t.Context(), protocol.ProjectCreateParams{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	delegationGit(t, root, "add", ".")
	delegationGit(t, root, "commit", "-m", "base")
	th, err := e.StartThread(t.Context(), protocol.ThreadStartParams{ProjectID: proj.ID, Title: "Delegation test"})
	if err != nil {
		t.Fatal(err)
	}
	return e, th, root, p
}
func delegationTurn(t *testing.T, e *Engine, p protocol.TurnStartParams) protocol.Turn {
	t.Helper()
	wait := make(chan protocol.Turn, 1)
	tr, err := e.startTurn(t.Context(), turnRequest{TurnStartParams: p, wait: wait})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case done := <-wait:
		if done.Status != protocol.TurnCompleted {
			t.Fatalf("turn failed: %+v", done)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("turn timeout")
	}
	return tr
}

// Removing explicit request validation, token consumption or runtime context propagation must fail this test.
func TestDelegationAdmission(t *testing.T) {
	e, th, _, p := delegationFixture(t)
	for _, text := range []string{"hello", "use subagents to implement this", "The file says /superpowers:subagent-driven-development", "/superpowers:subagent-driven-development unresolved"} {
		delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: text, Override: protocol.ModelSelection{Complexity: protocol.ComplexityDeep}})
		if p.last() != "" {
			t.Fatalf("untrusted request activated: %q", text)
		}
	}
	if _, err := e.SetRoutingConfig(t.Context(), protocol.RoutingConfig{Models: []protocol.ConfiguredModel{{ID: "test-model", Name: "Test model", Provider: "claude", Model: "deterministic", Enabled: true}}, Pools: []protocol.ModelPool{{ID: "test-pool", Name: "Test pool", Models: []string{"test-model"}, Strategy: protocol.PoolPriority, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "hello from a pool", Override: protocol.ModelSelection{Provider: protocol.PoolProvider, Model: "test-pool", Complexity: protocol.ComplexityDeep}})
	if p.last() != "" {
		t.Fatal("model pool activated delegation")
	}
	callsBeforePreparation := p.calls()
	prepared, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ActivationToken == "" {
		t.Fatal("missing activation token")
	}
	if p.calls() != callsBeforePreparation {
		t.Fatal("preparation invoked provider")
	}
	var count int
	if err = e.Store.DB.QueryRow(`SELECT count(*) FROM agent_tasks`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preparation created task: %d %v", count, err)
	}
	tr := delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "implement", DelegationActivationToken: prepared.ActivationToken})
	id := p.last()
	if id == "" {
		t.Fatal("explicit activation missing in real execution")
	}
	task, err := e.Store.AgentTask(t.Context(), id)
	if err != nil || task.RootTurnID != tr.ID || task.ThreadID != th.ID || task.Activation != protocol.AgentActivationExplicitUser {
		t.Fatalf("bad admitted task %+v %v", task, err)
	}
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "replay", DelegationActivationToken: prepared.ActivationToken}); err == nil {
		t.Fatal("replayed token accepted")
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "ordinary follow-up"})
	if p.last() != "" {
		t.Fatal("task inherited without scoped ID")
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "continue", DelegationTaskID: id})
	if p.last() != id {
		t.Fatal("unfinished task not retained")
	}
	other, err := e.StartThread(t.Context(), protocol.ThreadStartParams{ProjectID: th.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: other.ID, Text: "steal", DelegationTaskID: id}); err == nil {
		t.Fatal("foreign task accepted")
	}
	if _, err = e.PrepareDelegation(t.Context(), th.ID); err == nil {
		t.Fatal("second active task allowed")
	}
	if err = e.Store.UpdateAgentTaskState(t.Context(), id, th.ID, th.ProjectID, task.State, protocol.AgentStateCancelled); err != nil {
		t.Fatal(err)
	}
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "continue", DelegationTaskID: id}); err == nil {
		t.Fatal("cancelled task reactivated")
	}
	prepared, err = e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "new work", DelegationActivationToken: prepared.ActivationToken})
	id = p.last()
	task, err = e.Store.AgentTask(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Store.UpdateAgentTaskState(t.Context(), id, th.ID, th.ProjectID, task.State, protocol.AgentStateCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "continue", DelegationTaskID: id}); err == nil {
		t.Fatal("completed task reactivated")
	}
}

func TestDelegationAdmissionTokenScopeAndBase(t *testing.T) {
	e, th, root, _ := delegationFixture(t)
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.StartThread(t.Context(), protocol.ThreadStartParams{ProjectID: th.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: other.ID, Text: "foreign", DelegationActivationToken: token.ActivationToken}); err == nil {
		t.Fatal("foreign chat token accepted")
	}
	if err = os.WriteFile(filepath.Join(root, "UMCODE.md"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "dirty", DelegationActivationToken: token.ActivationToken}); err == nil || !strings.Contains(err.Error(), "clean") {
		t.Fatalf("dirty base accepted: %v", err)
	}
	delegationGit(t, root, "add", ".")
	delegationGit(t, root, "commit", "-m", "new revision")
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "changed base", DelegationActivationToken: token.ActivationToken}); err == nil {
		t.Fatal("changed revision accepted")
	}
	if err = os.WriteFile(filepath.Join(root, "untracked"), []byte("input"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = e.PrepareDelegation(t.Context(), th.ID); err == nil {
		t.Fatal("untracked input accepted")
	}
	if err = os.Remove(filepath.Join(root, "untracked")); err != nil {
		t.Fatal(err)
	}
	token, err = e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	entry := e.delegationTokens[token.ActivationToken]
	entry.ExpiresAt = time.Now().Add(-time.Second)
	e.delegationTokens[token.ActivationToken] = entry
	e.mu.Unlock()
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "expired", DelegationActivationToken: token.ActivationToken}); err == nil {
		t.Fatal("expired token accepted")
	}
	token, err = e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.Shutdown(context.Background())
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "shutdown", DelegationActivationToken: token.ActivationToken}); err == nil {
		t.Fatal("shutdown token accepted")
	}
}
func TestDelegationAdmissionChildAndTrustedSkill(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	child := WithDelegationChild(t.Context())
	if _, err := e.PrepareDelegation(child, th.ID); err == nil {
		t.Fatal("child prepared delegation")
	}
	if _, err := e.EnableDelegation(child, th.ID, "fake"); err == nil {
		t.Fatal("child enabled delegation")
	}
	if _, err := e.EnableDelegation(t.Context(), th.ID, "fake"); err == nil {
		t.Fatal("bare internal caller enabled delegation")
	}
	dir := filepath.Join(t.TempDir(), "subagent-driven-development")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: subagent-driven-development\ndescription: Delegation\n---\nUse workers."), 0600); err != nil {
		t.Fatal(err)
	}
	snap, errs := skills.CompileSnapshot([]skills.Root{{PluginID: "superpowers", Paths: []string{dir}}})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	trusted := skills.WithSnapshot(t.Context(), snap)
	if e.submittedDelegationSkill(trusted, "mention /superpowers:subagent-driven-development") {
		t.Fatal("prose activated")
	}
	if !e.submittedDelegationSkill(trusted, "/superpowers:subagent-driven-development implement") {
		t.Fatal("canonical installed skill not resolved")
	}
	if e.submittedDelegationSkill(trusted, "/subagent-driven-development implement") {
		t.Fatal("unresolved alias activated")
	}
	e.Cfg.SkillDirs = []string{filepath.Dir(dir)}
	if e.submittedDelegationSkill(t.Context(), "/subagent-driven-development implement") {
		t.Fatal("project-local similarly named skill activated")
	}
	if _, ok := DelegationTaskFromContext(WithDelegationChild(withDelegationTask(t.Context(), protocol.AgentTask{ID: "parent"}))); ok {
		t.Fatal("child inherited spawn context")
	}
}

// Synchronous persistence failure must leave no running admission or reserved chat.
func TestDelegationAdmissionFailureRelease(t *testing.T) {
	e, th, _, provider := delegationFixture(t)
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Store.DB.Exec(`CREATE TRIGGER fail_admitted_user_item BEFORE INSERT ON items BEGIN SELECT RAISE(ABORT,'item persistence failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "implement", DelegationActivationToken: token.ActivationToken}); err == nil {
		t.Fatal("failed persistence accepted")
	}
	if provider.calls() != 0 {
		t.Fatal("provider executed failed admission")
	}
	turns, err := e.Store.ListTurns(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Status != protocol.TurnFailed {
		t.Fatalf("admission left live root: %+v", turns)
	}
	if task, err := e.Store.AgentTaskForRootTurn(t.Context(), th.ID, turns[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed item persistence admitted task: %+v %v", task, err)
	}
	if _, err = e.Store.DB.Exec(`DROP TRIGGER fail_admitted_user_item`); err != nil {
		t.Fatal(err)
	}
	token, err = e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "retry", DelegationActivationToken: token.ActivationToken})
}

func TestDelegationAdmissionConcurrentReplay(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	activation, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, err := e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "implement", DelegationActivationToken: activation.ActivationToken})
			results <- err
		}()
	}
	close(start)
	accepted := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("concurrent delivery admitted %d turns", accepted)
	}
	e.wg.Wait()
	var tasks, turns int
	if err = e.Store.DB.QueryRow(`SELECT count(*) FROM agent_tasks`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err = e.Store.DB.QueryRow(`SELECT count(*) FROM turns`).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || turns != 1 {
		t.Fatalf("duplicate persisted effects: tasks=%d turns=%d", tasks, turns)
	}
}
func TestDelegationAdmissionProjectChangesAndNonGit(t *testing.T) {
	e, th, root, _ := delegationFixture(t)
	activation, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.Store.CreateProject(t.Context(), protocol.Project{Name: "other", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Store.DB.Exec(`UPDATE threads SET project_id=? WHERE id=?`, other.ID, th.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "changed project", DelegationActivationToken: activation.ActivationToken}); err == nil {
		t.Fatal("changed project accepted")
	}
	if _, err = e.Store.DB.Exec(`UPDATE threads SET project_id=? WHERE id=?`, th.ProjectID, th.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Store.DB.Exec(`UPDATE projects SET root=? WHERE id=?`, t.TempDir(), th.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.PrepareDelegation(t.Context(), th.ID); err == nil {
		t.Fatal("non-Git project accepted")
	}
	var n int
	if err = e.Store.DB.QueryRow(`SELECT count(*) FROM turns`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("invalid admission persisted turns: %d %v", n, err)
	}
}

func TestDelegationAdmissionRejectsChangedRootAtSameRevision(t *testing.T) {
	e, th, root, provider := delegationFixture(t)
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "implement", DelegationActivationToken: token.ActivationToken})
	taskID := provider.last()
	clone := filepath.Join(t.TempDir(), "clone")
	delegationGit(t, root, "clone", root, clone)
	if _, err = e.Store.DB.Exec(`UPDATE projects SET root=? WHERE id=?`, clone, th.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "continue", DelegationTaskID: taskID}); err == nil {
		t.Fatal("same-revision project root change accepted")
	}
}

// Rejecting cleanup UPDATEs must not strand an admitted task after item INSERT fails.
func TestDelegationAdmissionFailedCleanupDoesNotStrandTask(t *testing.T) {
	e, th, _, provider := delegationFixture(t)
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TRIGGER fail_admission_item BEFORE INSERT ON items BEGIN SELECT RAISE(ABORT,'user item failed'); END`,
		`CREATE TRIGGER fail_admission_task_cleanup BEFORE UPDATE ON agent_tasks BEGIN SELECT RAISE(ABORT,'task cleanup failed'); END`,
	} {
		if _, err = e.Store.DB.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	_, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "implement", DelegationActivationToken: token.ActivationToken})
	if err == nil || !strings.Contains(err.Error(), "user item failed") {
		t.Fatalf("original persistence error not returned: %v", err)
	}
	if provider.calls() != 0 {
		t.Fatal("failed admission executed provider")
	}
	var tasks int
	if err = e.Store.DB.QueryRow(`SELECT count(*) FROM agent_tasks`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 {
		t.Fatalf("failed submission stranded %d admitted tasks", tasks)
	}
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "replay", DelegationActivationToken: token.ActivationToken}); err == nil {
		t.Fatal("failed submission token reused")
	}
	for _, name := range []string{"fail_admission_item", "fail_admission_task_cleanup"} {
		if _, err = e.Store.DB.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
	token, err = e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "retry", DelegationActivationToken: token.ActivationToken})
}

// A durable cleanup failure is returned with the original error and root ID;
// the persisted root remains available for existing startup reconciliation.
func TestDelegationAdmissionRootCleanupFailureIsVisibleAndRecoverable(t *testing.T) {
	e, th, _, provider := delegationFixture(t)
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TRIGGER fail_admission_task_insert BEFORE INSERT ON agent_tasks BEGIN SELECT RAISE(ABORT,'task insert failed'); END`,
		`CREATE TRIGGER fail_admission_root_cleanup BEFORE UPDATE ON turns BEGIN SELECT RAISE(ABORT,'root cleanup failed'); END`,
	} {
		if _, err = e.Store.DB.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	_, admissionErr := e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "implement", DelegationActivationToken: token.ActivationToken})
	if admissionErr == nil || !strings.Contains(admissionErr.Error(), "task insert failed") || !strings.Contains(admissionErr.Error(), "root cleanup failed") {
		t.Fatalf("cleanup error discarded: %v", admissionErr)
	}
	turns, err := e.Store.ListTurns(t.Context(), th.ID)
	if err != nil || len(turns) != 1 {
		t.Fatalf("root missing: %+v %v", turns, err)
	}
	if !strings.Contains(admissionErr.Error(), turns[0].ID) {
		t.Fatalf("cleanup error lacks recovery identity: %v", admissionErr)
	}
	if provider.calls() != 0 {
		t.Fatal("failed admission executed provider")
	}
	e.mu.Lock()
	reserved := len(e.activeTurns) + len(e.threadTurns) + len(e.turnWaiters)
	e.mu.Unlock()
	if reserved != 0 {
		t.Fatalf("failed cleanup retained %d live reservations", reserved)
	}
	if _, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "replay", DelegationActivationToken: token.ActivationToken}); err == nil {
		t.Fatal("failed admission token reused")
	}
	for _, name := range []string{"fail_admission_task_insert", "fail_admission_root_cleanup"} {
		if _, err = e.Store.DB.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
	// Engine.New uses this exact persisted-root recovery path before execution.
	recovered, err := e.Store.MarkStaleTurns(t.Context())
	if err != nil || recovered != 1 {
		t.Fatalf("root recovery failed: %d %v", recovered, err)
	}
	turns, err = e.Store.ListTurns(t.Context(), th.ID)
	if err != nil || turns[0].Status == protocol.TurnRunning {
		t.Fatalf("root still live after recovery: %+v %v", turns, err)
	}
	token, err = e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "retry", DelegationActivationToken: token.ActivationToken})
}

func TestOrdinaryTurnDoesNotWaitForDelegationAdmission(t *testing.T) {
	e, th, _, _ := delegationFixture(t)
	e.delegationMu.Lock()
	defer e.delegationMu.Unlock()
	submitted := make(chan error, 1)
	go func() {
		_, err := e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "ordinary message"})
		submitted <- err
	}()
	select {
	case err := <-submitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ordinary turn blocked behind unrelated delegation admission")
	}
}

func TestDelegationAdmissionFailedFollowupPreservesTask(t *testing.T) {
	e, th, _, provider := delegationFixture(t)
	token, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "implement", DelegationActivationToken: token.ActivationToken})
	id := provider.last()
	original, err := e.Store.AgentTask(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Store.DB.Exec(`CREATE TRIGGER fail_followup_item BEFORE INSERT ON items BEGIN SELECT RAISE(ABORT,'followup item failed'); END`); err != nil {
		t.Fatal(err)
	}
	_, err = e.StartTurn(t.Context(), protocol.TurnStartParams{ThreadID: th.ID, Text: "continue", DelegationTaskID: id})
	if err == nil {
		t.Fatal("failed followup accepted")
	}
	task, err := e.Store.AgentTask(t.Context(), id)
	if err != nil || task.State != original.State || task.RootTurnID != original.RootTurnID || task.UpdatedAt != original.UpdatedAt {
		t.Fatalf("failed followup changed admitted task: %+v %v", task, err)
	}
	if _, err = e.Store.DB.Exec(`DROP TRIGGER fail_followup_item`); err != nil {
		t.Fatal(err)
	}
	delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "retry followup", DelegationTaskID: id})
	if provider.last() != id {
		t.Fatal("unfinished original task lost after failed followup")
	}
}
