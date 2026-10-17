package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/memory"
	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/skills"
	"github.com/shaktsin/umcode/internal/store"
)

// WorkspaceResolver is implemented by the managed delegation workspace owner.
// Resolve must verify durable task BaseRoot/BaseRevision and child.WorkspaceID,
// return an exclusive canonical checkout, and preserve it on failure/cancellation.
// A reviewer receives a distinct snapshot of the implementation revision.
type WorkspaceResolver interface {
	Resolve(context.Context, protocol.AgentTask, protocol.AgentChild) (ChildWorkspace, error)
}
type ChildWorkspace struct {
	ID, Root, Revision string
	ChangedPaths       []string
}

// ChildWorkspaceFinalizer snapshots the stopped execution. Task6's managed
// resolver implements this; a successful Resolve revision is only its input.
// Finalize must retain the workspace on error and honor its bounded context.
type ChildWorkspaceFinalizer interface {
	Finalize(context.Context, protocol.AgentTask, protocol.AgentChild, ChildWorkspace) (ChildWorkspace, error)
}

type childExecution struct {
	task          protocol.AgentTask
	child         protocol.AgentChild
	workspace     ChildWorkspace
	selection     candidateSet
	credentialID  string
	credentialIDs []string
}
type childExecutionKey struct{}

func withChildExecution(ctx context.Context, x *childExecution) context.Context {
	return context.WithValue(ctx, childExecutionKey{}, x)
}
func childExecutionFrom(ctx context.Context) *childExecution {
	x, _ := ctx.Value(childExecutionKey{}).(*childExecution)
	return x
}

type childRunner struct{ engine *Engine }

func (r childRunner) Run(ctx context.Context, task protocol.AgentTask, child protocol.AgentChild) (result protocol.AgentResult, err error) {
	e := r.engine
	if e.DelegationWorkspaces == nil {
		return result, errors.New("isolated child workspace resolver unavailable")
	}
	if delegation.BudgetFromContext(ctx) == nil {
		return result, errors.New("child shared task budget unavailable")
	}
	ws, err := e.DelegationWorkspaces.Resolve(ctx, task, child)
	if err != nil {
		return result, err
	}
	if ws.ID == "" || !filepath.IsAbs(ws.Root) || pathutil.Within(task.BaseRoot, ws.Root) || pathutil.Within(ws.Root, task.BaseRoot) || (task.ParentRoot != "" && (pathutil.Within(task.ParentRoot, ws.Root) || pathutil.Within(ws.Root, task.ParentRoot))) {
		return result, errors.New("child workspace must be an isolated owned checkout outside the parent")
	}
	ws.Root = pathutil.Resolved(ws.Root)
	info, err := os.Stat(ws.Root)
	if err != nil || !info.IsDir() {
		return result, errors.New("child workspace is unavailable")
	}
	parent, err := e.Store.GetThread(ctx, task.ThreadID)
	if err != nil {
		return result, err
	}
	if parent.ProjectID != task.ProjectID {
		return result, errors.New("child parent project changed")
	}
	proj, err := e.Projects.Get(ctx, task.ProjectID)
	if err != nil {
		return result, err
	}
	if proj.Missing || !pathutil.SameFolder(proj.Root, task.BaseRoot) {
		return result, errors.New("child project base changed")
	}
	if err = e.validateDelegationParent(ctx, task); err != nil {
		return result, err
	}
	modelScope, err := e.Store.AgentModelScope(ctx, task.ID)
	if err != nil {
		return result, err
	}
	pick := candidateSet{provider: modelScope.Provider, model: modelScope.Model, poolID: modelScope.PoolID, strategy: modelScope.Strategy, explicit: modelScope.Explicit, candidates: modelScope.Candidates}
	// Worker preferences may reorder the inherited approved candidates, never add one.
	preference := e.Cfg.Models.Roles["worker"]
	if preference.Model != "" || preference.Provider != "" {
		found := -1
		for i, m := range pick.candidates {
			if (preference.Model == "" || m.Model == preference.Model) && (preference.Provider == "" || preference.Provider == m.Provider) {
				found = i
				break
			}
		}
		if found < 0 {
			return result, errors.New("configured worker model is outside the parent's approved model set")
		}
		chosen := pick.candidates[found]
		pick.candidates = append([]protocol.ConfiguredModel{chosen}, append(append([]protocol.ConfiguredModel{}, pick.candidates[:found]...), pick.candidates[found+1:]...)...)
		pick.provider, pick.model, pick.explicit = chosen.Provider, chosen.Model, true
	}
	proj.Root = ws.Root
	guidance, sources := e.Projects.InstructionsForProjection(ctx, proj, "", "child-"+child.ID, memory.WithoutGeneratedMemory)
	for _, src := range sources {
		if src.Error != "" {
			return result, fmt.Errorf("project guidance: %s", src.Error)
		}
	}
	if name, explicit := skills.Invocation(child.Assignment.Prompt); explicit {
		instructions, readErr := e.Skills.InstructionsContext(ctx, name)
		if readErr != nil {
			return result, readErr
		}
		guidance += "\n" + instructions
	}
	builder := delegation.ScopedContextBuilder{Root: ws.Root, Guidance: guidance}
	supplied, err := builder.Build(ctx, task, child)
	if err != nil {
		return result, err
	}
	execution, err := e.Store.EnsureAgentExecution(ctx, task, child, parent, supplied)
	if err != nil {
		return result, err
	}
	if err = e.updateChildExecution(ctx, task.ID, child.ID, func(c *protocol.AgentChild) { c.WorkspaceID = ws.ID; c.Revision = "" }); err != nil {
		return result, err
	}
	defer func() {
		if finalizer, ok := e.DelegationWorkspaces.(ChildWorkspaceFinalizer); ok {
			fail := func(cause error) {
				result.Blocker = fmt.Sprintf("workspace finalization failed: %v", cause)
				err = errors.Join(err, cause)
			}
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			latest, readErr := e.Store.AgentChild(finalCtx, task.ID, child.ID)
			if readErr != nil {
				fail(readErr)
				return
			}
			final, finalErr := finalizer.Finalize(finalCtx, task, latest, ws)
			if finalErr == nil && (final.ID != ws.ID || !pathutil.SameFolder(final.Root, ws.Root) || final.Revision == "") {
				finalErr = errors.New("child finalizer returned invalid workspace or missing final revision")
			}
			if finalErr != nil {
				fail(finalErr)
				return
			}
			if final.ChangedPaths != nil {
				result.ChangedPaths = append([]string(nil), final.ChangedPaths...)
			}
			if updateErr := e.updateChildExecution(finalCtx, task.ID, child.ID, func(c *protocol.AgentChild) { c.WorkspaceID = final.ID; c.Revision = final.Revision }); updateErr != nil {
				fail(updateErr)
			}
		}
	}()
	x := &childExecution{task: task, child: child, workspace: ws, selection: pick, credentialID: modelScope.CredentialID, credentialIDs: modelScope.CredentialIDs}
	ctx = withChildExecution(store.WithInternalThreads(WithDelegationChild(ctx)), x)
	text := "Continue the original assignment using the recorded child history and queued corrections."
	prior, err := e.Store.ListTurns(ctx, execution.ThreadID)
	if err != nil {
		return result, err
	}
	if len(prior) == 0 {
		text = messageText(execution.Context)
	}
	wait := make(chan protocol.Turn, 1)
	_, err = e.startTurn(ctx, turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: execution.ThreadID, Text: text}, wait: wait})
	if err != nil {
		return result, err
	}
	// Always wait for the actual loop to exit; the coordinator owns the slot until
	// this returns. Cancellation has already reached the registered engine handle.
	done := <-wait
	sctx := context.WithoutCancel(ctx)
	items, readErr := e.Store.ListItems(sctx, execution.ThreadID, 0)
	if readErr != nil {
		err = readErr
	}
	for _, it := range items {
		if it.TurnID == done.ID && it.Kind == protocol.ItemAgentMessage {
			result.Summary = it.Text
		}
		if it.Kind == protocol.ItemFileChange {
			var change protocol.FileChangeData
			if json.Unmarshal(it.Data, &change) == nil && change.Path != "" && !slices.Contains(result.ChangedPaths, change.Path) {
				result.ChangedPaths = append(result.ChangedPaths, change.Path)
			}
		}
	}
	if child.Role == protocol.AgentRoleReviewer {
		result.Checks = append(result.Checks, protocol.AgentCheckEvidence{Command: "reviewer command verification", Status: "blocked", Summary: "Read-only command sandbox is unavailable; reviewer inspected files/evidence only. Implementer and lead must execute checks."})
	}
	if modelErr := e.Store.UpdateAgentExecutionModel(sctx, task.ID, child.ID, done.Resolved); modelErr != nil {
		err = errors.Join(err, modelErr)
	}
	usage, usageErr := e.Store.UsageTotalsWhere(sctx, "thread_id = ?", execution.ThreadID)
	if usageErr != nil {
		err = errors.Join(err, usageErr)
	} else {
		err = errors.Join(err, e.updateChildExecution(sctx, task.ID, child.ID, func(c *protocol.AgentChild) { c.Usage = usage }))
	}
	if done.Status != protocol.TurnCompleted {
		err = errors.Join(err, fmt.Errorf("child turn %s: %s", done.Status, done.Error))
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return result, err
}

func (e *Engine) updateChildExecution(ctx context.Context, taskID, childID string, mutate func(*protocol.AgentChild)) error {
	for i := 0; i < 5; i++ {
		c, err := e.Store.AgentChild(ctx, taskID, childID)
		if err != nil {
			return err
		}
		mutate(&c)
		err = e.Store.UpdateAgentChild(ctx, c, c.Version)
		if err == nil {
			updated, readErr := e.Store.AgentChild(ctx, taskID, childID)
			if readErr != nil {
				return readErr
			}
			task, readErr := e.Store.AgentTask(ctx, taskID)
			if readErr != nil {
				return readErr
			}
			e.Bus.Publish(task.ThreadID, protocol.NotifyDelegationUpdated, protocol.DelegationUpdated{ThreadID: task.ThreadID, TaskID: task.ID, Child: updated})
		}
		if !errors.Is(err, store.ErrAgentConflict) {
			return err
		}
	}
	return store.ErrAgentConflict
}

func (e *Engine) setChildApprovalState(ctx context.Context, x *childExecution, waiting bool) error {
	return e.updateChildExecution(ctx, x.task.ID, x.child.ID, func(c *protocol.AgentChild) {
		// A late approval response must never resurrect a cancelled child or
		// replace the concurrent correction ledger/results read on this CAS attempt.
		if waiting && c.State == protocol.AgentStateRunning {
			c.State = protocol.AgentStateWaitingApproval
		}
		if !waiting && c.State == protocol.AgentStateWaitingApproval {
			c.State = protocol.AgentStateRunning
		}
	})
}

func (e *Engine) recordChildModel(ctx context.Context, x *childExecution, model protocol.ModelSelection) error {
	if err := e.Store.UpdateAgentExecutionModel(ctx, x.task.ID, x.child.ID, model); err != nil {
		return err
	}
	return e.updateChildExecution(ctx, x.task.ID, x.child.ID, func(c *protocol.AgentChild) { c.Model = model })
}
func messageText(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		for _, p := range m.Parts {
			b.WriteString(p.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// consumeChildFollowups is called only outside a provider stream/tool action.
// The deterministic item identity makes persist-before-ack retries idempotent.
func (e *Engine) consumeChildFollowups(ctx, sctx context.Context, turn protocol.Turn, msgs []llm.Message) ([]llm.Message, bool, error) {
	queue := delegation.FollowupsFromContext(ctx)
	if queue == nil {
		return msgs, false, nil
	}
	pending, err := queue.Pending(ctx)
	if err != nil {
		return msgs, false, err
	}
	added := false
	for _, followup := range pending {
		if len(followup.Message) > delegation.AssignmentLimit {
			return msgs, added, errors.New("correction exceeds assignment context limit")
		}
		id := fmt.Sprintf("agent-followup-%x", sha256.Sum256([]byte(turn.ThreadID+"\x00"+followup.CallID)))
		existing, readErr := e.Store.GetItem(sctx, id)
		if errors.Is(readErr, store.ErrNotFound) {
			item, itemErr := e.newItem(sctx, turn, protocol.ItemUserMessage)
			if itemErr != nil {
				return msgs, added, itemErr
			}
			item.ID = id
			item.Text = followup.Message
			item.Status = protocol.ItemCompleted
			if err = e.Store.SaveItem(sctx, item); err != nil {
				return msgs, added, err
			}
			msgs = appendText(msgs, llm.RoleUser, followup.Message)
			added = true
		} else if readErr != nil {
			return msgs, added, readErr
		} else if existing.TurnID == turn.ID {
			// An acknowledgement failure ends the turn; a later run gets this from history.
			return msgs, added, errors.New("correction already recorded in current turn without acknowledgement")
		}
		if err = queue.Acknowledge(ctx, followup.CallID); err != nil {
			return msgs, added, err
		}
	}
	return msgs, added, nil
}
