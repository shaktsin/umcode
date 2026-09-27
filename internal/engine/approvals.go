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
	// A decision the user asked to remember for this project answers immediately.
	if th.ProjectID != "" {
		switch e.Store.RememberedDecision(sctx, th.ProjectID, tool, ApprovalSignature(tool, args)) {
		case "allow":
			_ = e.Store.Audit(sctx, "approval.remembered", map[string]any{"tool": tool, "project": th.ProjectID, "decision": "allow"})
			return true, nil
		case "deny":
			_ = e.Store.Audit(sctx, "approval.remembered", map[string]any{"tool": tool, "project": th.ProjectID, "decision": "deny"})
			return false, nil
		}
	}
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
	_ = e.Store.Audit(sctx, "approval.request", map[string]any{"id": a.ID, "tool": tool, "risk": risk, "summary": summary})

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case ok := <-ch:
		return ok, nil
	case <-timer.C:
		if err := e.Store.DecideApproval(sctx, a.ID, "expired", "timeout"); err == nil {
			a.Status = "expired"
			e.Bus.PublishAdmin(protocol.NotifyApprovalResolved, protocol.ApprovalEvent{Approval: a})
			return false, ErrApprovalExpired
		}
		// Someone answered at the last moment.
		select {
		case ok := <-ch:
			return ok, nil
		default:
			return false, ErrApprovalExpired
		}
	case <-ctx.Done():
		if err := e.Store.DecideApproval(sctx, a.ID, "expired", "turn interrupted"); err == nil {
			a.Status = "expired"
			e.Bus.PublishAdmin(protocol.NotifyApprovalResolved, protocol.ApprovalEvent{Approval: a})
		}
		return false, ctx.Err()
	}
}

// RespondApproval records a decision. The first answer wins; later answers
// get a conflict error.
func (e *Engine) RespondApproval(ctx context.Context, id string, approve, remember bool, clientID string) (protocol.Approval, error) {
	status := "denied"
	if approve {
		status = "approved"
	}
	if err := e.Store.DecideApproval(ctx, id, status, clientID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
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
	var decided protocol.Approval
	if list, err := e.Store.ListApprovals(ctx, ""); err == nil {
		for _, a := range list {
			if a.ID == id {
				decided = a
			}
		}
	}
	if remember && decided.ID != "" {
		if th, err := e.Store.GetThread(ctx, decided.ThreadID); err == nil && th.ProjectID != "" {
			decision := "deny"
			if approve {
				decision = "allow"
			}
			if err := e.Store.RememberDecision(ctx, th.ProjectID, decided.Tool, ApprovalSignature(decided.Tool, decided.Args), decision); err != nil {
				e.Log.Warn("remember approval", "err", err)
			}
		}
	}
	_ = e.Store.Audit(ctx, "approval.decide", map[string]any{"id": id, "status": status, "by": clientID, "remember": remember})
	e.Bus.PublishAdmin(protocol.NotifyApprovalResolved, protocol.ApprovalEvent{Approval: decided})
	if decided.ID == "" {
		return decided, fmt.Errorf("approval %s not found after decision", id)
	}
	return decided, nil
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
