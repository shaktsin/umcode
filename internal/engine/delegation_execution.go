package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/memory"
	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/skills"
	"github.com/shaktsin/umcode/internal/store"
)

// ChildWorkspace is the isolated checkout an agent execution works in.
type ChildWorkspace struct {
	ID, Root, Revision string
	ChangedPaths       []string
}

type childExecution struct {
	// runtime is the runtime agent this execution runs; its authority comes
	// only from the agent's capabilities. An execution without it is refused
	// every tool (childToolAllowed).
	runtime *runtimeExecution
	task    protocol.AgentTask
	// child carries the agent's identity (child.ID is the agent ID, used to
	// attribute approvals); it grants nothing.
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

// childProjectBase revalidates the delegation parent thread, project and
// active parent checkout before a child execution is prepared.
func (e *Engine) childProjectBase(ctx context.Context, task protocol.AgentTask) (protocol.Thread, protocol.Project, error) {
	parent, err := e.Store.GetThread(ctx, task.ThreadID)
	if err != nil {
		return parent, protocol.Project{}, err
	}
	if parent.ProjectID != task.ProjectID {
		return parent, protocol.Project{}, errors.New("child parent project changed")
	}
	proj, err := e.Projects.Get(ctx, task.ProjectID)
	if err != nil {
		return parent, proj, err
	}
	if proj.Missing || !pathutil.SameFolder(proj.Root, task.BaseRoot) {
		return parent, proj, errors.New("child project base changed")
	}
	if err = e.validateDelegationParent(ctx, task); err != nil {
		return parent, proj, err
	}
	return parent, proj, nil
}

// childGuidance projects the project guidance onto the execution checkout
// root and appends an explicitly invoked skill's instructions.
func (e *Engine) childGuidance(ctx context.Context, proj protocol.Project, root, executionID, prompt string) (string, error) {
	proj.Root = root
	guidance, sources := e.Projects.InstructionsForProjection(ctx, proj, "", "child-"+executionID, memory.WithoutGeneratedMemory)
	for _, src := range sources {
		if src.Error != "" {
			return "", fmt.Errorf("project guidance: %s", src.Error)
		}
	}
	if name, explicit := skills.Invocation(prompt); explicit {
		instructions, readErr := e.Skills.InstructionsContext(ctx, name)
		if readErr != nil {
			return "", readErr
		}
		guidance += "\n" + instructions
	}
	return guidance, nil
}

// childTurnOutcome is what driveChildTurn observed of one execution turn.
type childTurnOutcome struct {
	done    protocol.Turn
	sctx    context.Context // store context carrying the execution markers
	summary string          // the turn's final assistant text
	changed []string        // paths of recorded file changes, in order
	readErr error           // a failure reading the turn's items
}

// driveChildTurn runs one engine turn of a runtime agent's execution on its
// internal thread and waits until the actual loop exits.
func (e *Engine) driveChildTurn(ctx context.Context, x *childExecution, execution store.AgentExecution) (childTurnOutcome, error) {
	var out childTurnOutcome
	ctx = withChildExecution(store.WithInternalThreads(WithDelegationChild(ctx)), x)
	text := "Continue the original assignment using the recorded child history and queued corrections."
	prior, err := e.Store.ListTurns(ctx, execution.ThreadID)
	if err != nil {
		return out, err
	}
	if len(prior) == 0 {
		text = messageText(execution.Context)
	}
	wait := make(chan protocol.Turn, 1)
	_, err = e.startTurn(ctx, turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: execution.ThreadID, Text: text}, wait: wait})
	if err != nil {
		return out, err
	}
	// Always wait for the actual loop to exit; the coordinator owns the slot until
	// this returns. Cancellation has already reached the registered engine handle.
	out.done = <-wait
	out.sctx = context.WithoutCancel(ctx)
	items, readErr := e.Store.ListItems(out.sctx, execution.ThreadID, 0)
	if readErr != nil {
		out.readErr = readErr
	}
	for _, it := range items {
		if it.TurnID == out.done.ID && it.Kind == protocol.ItemAgentMessage {
			out.summary = it.Text
		}
		if it.Kind == protocol.ItemFileChange {
			var change protocol.FileChangeData
			if json.Unmarshal(it.Data, &change) == nil && change.Path != "" && !slices.Contains(out.changed, change.Path) {
				out.changed = append(out.changed, change.Path)
			}
		}
	}
	return out, nil
}

func (e *Engine) setChildApprovalState(ctx context.Context, x *childExecution, waiting bool) error {
	if x.runtime == nil {
		return errNotRuntimeExecution
	}
	return e.setRuntimeApprovalState(ctx, x, waiting)
}

// errNotRuntimeExecution refuses an execution that is not a runtime agent's.
var errNotRuntimeExecution = errors.New("agent execution without a runtime agent")

func (e *Engine) recordChildModel(ctx context.Context, x *childExecution, model protocol.ModelSelection) error {
	if x.runtime == nil {
		return errNotRuntimeExecution
	}
	return e.recordRuntimeModel(ctx, x, model)
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

// consumeChildFollowups delivers a runtime agent's pending messages; it is
// called only outside a provider stream/tool action. Other turns have none.
func (e *Engine) consumeChildFollowups(ctx, sctx context.Context, turn protocol.Turn, msgs []llm.Message) ([]llm.Message, bool, error) {
	if x := childExecutionFrom(ctx); x != nil && x.runtime != nil {
		return e.consumeAgentMessages(ctx, sctx, turn, msgs, x)
	}
	return msgs, false, nil
}
