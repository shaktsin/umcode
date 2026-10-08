package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// ErrApprovalExpired is returned when nobody answered in time.
var ErrApprovalExpired = errors.New("approval request expired")

// requestApproval persists an approval, broadcasts it to admin clients and
// blocks until one answers, it expires, or the turn is cancelled.
func (e *Engine) requestApproval(ctx, sctx context.Context, turn protocol.Turn, it protocol.Item, tool string,
	args json.RawMessage, risk tools.Risk, reason, summary string) (bool, error) {
	timeout := time.Duration(e.Cfg.Policy.ApprovalTimeoutMinutes) * time.Minute
	now := time.Now().UTC()
	th, _ := e.Store.GetThread(sctx, turn.ThreadID)
	a := protocol.Approval{
		ID: store.NewID("apr"), ThreadID: turn.ThreadID, ProjectID: th.ProjectID, TurnID: turn.ID, ItemID: it.ID, Tool: tool, Args: args,
		Risk: string(risk), Reason: reason, ActionSummary: summary, Status: "pending",
		CreatedAt: now, ExpiresAt: now.Add(timeout),
	}
	// A running Computer Use session's latest screenshot lets the person see
	// exactly what they're being asked to approve, instead of judging a
	// click or keystroke from its coordinates alone.
	if e.ComputerUse != nil {
		if shot, ok := e.ComputerUse.LastScreenshot(turn.ThreadID); ok {
			a.Screenshot = shot
		}
	}
	// A decision the user asked to remember for this chat answers
	// immediately. Computer Use actions are excluded: their approval
	// signature can't distinguish one click or keystroke from another (see
	// ApprovalSignature), so "remember" would silently rubber-stamp every
	// future action in the project rather than the one the person actually
	// reviewed. Each one always gets a fresh, human decision.
	if th.ID != "" && !isComputerUseTool(tool) {
		switch e.Store.RememberedThreadDecision(sctx, th.ID, tool, ApprovalSignature(tool, args)) {
		case "allow":
			_ = e.Store.Audit(sctx, "approval.remembered", map[string]any{"tool": tool, "thread": th.ID, "decision": "allow"})
			return true, nil
		case "deny":
			_ = e.Store.Audit(sctx, "approval.remembered", map[string]any{"tool": tool, "thread": th.ID, "decision": "deny"})
			return false, nil
		}
	}
	return e.persistAndWaitApproval(ctx, sctx, a, timeout, "approval.request")
}

// persistAndWaitApproval is shared transport; workflow decisions never consult
// remembered tool decisions and commit their graph before this waiter wakes.
func (e *Engine) persistAndWaitApproval(ctx, sctx context.Context, a protocol.Approval, timeout time.Duration, auditEvent string) (bool, error) {
	if err := e.Store.CreateApproval(sctx, a); err != nil {
		return false, err
	}
	ch := make(chan bool, 1)
	e.mu.Lock()
	e.approvals[a.ID] = ch
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.approvals, a.ID)
		e.mu.Unlock()
	}()

	// Admin clients get the request; clients following the thread see it too.
	ev := protocol.ApprovalEvent{Approval: a}
	e.Bus.PublishAdmin(protocol.NotifyApprovalRequest, ev)
	_ = e.Store.Audit(sctx, auditEvent, map[string]any{"id": a.ID, "tool": a.Tool, "risk": a.Risk, "summary": a.ActionSummary, "args": a.Args})

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case ok := <-ch:
		return ok, nil
	case <-timer.C:
		if err := e.expireApproval(sctx, a, "timeout"); err == nil {
			a.Status = "expired"
			e.Bus.PublishAdmin(protocol.NotifyApprovalResolved, protocol.ApprovalEvent{Approval: a})
			return false, ErrApprovalExpired
		}
		// Someone answered at the last moment.
		select {
		case ok := <-ch:
			return ok, nil
		default:
			if ok, decided := e.persistedApprovalOutcome(sctx, a.ID); decided {
				return ok, nil
			}
			return false, ErrApprovalExpired
		}
	case <-ctx.Done():
		if err := e.expireApproval(sctx, a, "turn interrupted"); err == nil {
			a.Status = "expired"
			e.Bus.PublishAdmin(protocol.NotifyApprovalResolved, protocol.ApprovalEvent{Approval: a})
		} else if ok, decided := e.persistedApprovalOutcome(sctx, a.ID); decided {
			return ok, nil
		}
		return false, ctx.Err()
	}
}

func (e *Engine) persistedApprovalOutcome(ctx context.Context, id string) (bool, bool) {
	a, err := e.Store.GetApproval(ctx, id)
	if err != nil {
		return false, false
	}
	switch a.Status {
	case "approved":
		return true, true
	case "denied":
		return false, true
	}
	return false, false
}

// RespondApproval records a decision. The first answer wins; later answers
// get a conflict error.
func (e *Engine) RespondApproval(ctx context.Context, id string, approve, remember bool, clientID string) (protocol.Approval, error) {
	status := "denied"
	if approve {
		status = "approved"
	}
	a, err := e.Store.GetApproval(ctx, id)
	if err != nil {
		return protocol.Approval{}, protocol.Errorf(protocol.CodeConflict, "approval %s does not exist", id)
	}
	if a.Kind == "workflow" {
		_, err = e.Store.DecideWorkflowApproval(ctx, id, status, clientID)
		remember = false
	} else {
		err = e.Store.DecideApproval(ctx, id, status, clientID)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrWorkUpdateConflict) {
			return protocol.Approval{}, protocol.Errorf(protocol.CodeConflict, "approval %s was already decided or does not exist", id)
		}
		return protocol.Approval{}, err
	}
	e.mu.Lock()
	ch := e.approvals[id]
	e.mu.Unlock()
	if ch != nil {
		ch <- approve
	}
	decided, _ := e.Store.GetApproval(ctx, id)
	if remember && decided.ID != "" && !isComputerUseTool(decided.Tool) {
		if _, err := e.Store.GetThread(ctx, decided.ThreadID); err == nil {
			decision := "deny"
			if approve {
				decision = "allow"
			}
			if err := e.Store.RememberThreadDecision(ctx, decided.ThreadID, decided.Tool, ApprovalSignature(decided.Tool, decided.Args), decision); err != nil {
				e.Log.Warn("remember approval", "err", err)
			}
		}
	}
	event := "approval.decide"
	if a.Kind == "workflow" {
		event = "workflow.approval.decide"
	}
	_ = e.Store.Audit(ctx, event, map[string]any{"id": id, "status": status, "by": clientID, "remember": remember})
	e.Bus.PublishAdmin(protocol.NotifyApprovalResolved, protocol.ApprovalEvent{Approval: decided})
	if decided.ID == "" {
		return decided, fmt.Errorf("approval %s not found after decision", id)
	}
	return decided, nil
}

func (e *Engine) expireApproval(ctx context.Context, a protocol.Approval, by string) error {
	if a.Kind == "workflow" {
		_, err := e.Store.DecideWorkflowApproval(ctx, a.ID, "expired", by)
		return err
	}
	return e.Store.DecideApproval(ctx, a.ID, "expired", by)
}

// isComputerUseTool reports whether tool is one of the computer.* tools,
// whose actions must never be covered by a remembered "always allow"
// decision (see requestApproval and RespondApproval).
func isComputerUseTool(tool string) bool {
	return strings.HasPrefix(tool, "computer.")
}

// ApprovalSignature identifies the exact command or path a person approved.
// In particular, shell approvals must not generalize from a broad prefix such
// as "npm run" to unrelated scripts in the same project.
func ApprovalSignature(tool string, args json.RawMessage) string {
	var a struct {
		Command string `json:"command"`
		Path    string `json:"path"`
		Skill   string `json:"skill"`
		Script  string `json:"script"`
	}
	_ = json.Unmarshal(args, &a)
	switch {
	case a.Command != "":
		return strings.TrimSpace(a.Command)
	case a.Skill != "" && a.Script != "":
		return a.Skill + "/" + a.Script
	case a.Path != "":
		return a.Path
	}
	return tool
}
