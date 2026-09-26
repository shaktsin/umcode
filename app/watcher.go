package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/protocol"
)

const (
	approvalCategory = "umcode.approval"
	actionApprove    = "approve"
	actionDeny       = "deny"
)

// Watcher keeps an admin connection to the engine over the Unix socket. It
// drives the menu-bar counts and turns approval requests into notifications
// with Approve / Deny buttons.
type Watcher struct {
	eng      *EngineManager
	log      *slog.Logger
	shell    *Shell
	notifier *notifications.NotificationService

	mu        sync.Mutex
	c         *client.Client
	pending   map[string]protocol.Approval
	canNotify bool
}

func NewWatcher(eng *EngineManager, log *slog.Logger, shell *Shell, n *notifications.NotificationService) *Watcher {
	w := &Watcher{eng: eng, log: log, shell: shell, notifier: n, pending: map[string]protocol.Approval{}}
	n.OnNotificationResponse(w.onNotificationResponse)
	return w
}

// Run connects and reconnects until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	w.setupNotifications()
	backoff := time.Second
	for ctx.Err() == nil {
		err := w.session(ctx)
		if ctx.Err() != nil {
			return
		}
		detail := ""
		if err != nil {
			detail = err.Error()
			w.log.Info("engine connection ended", "err", err)
		}
		w.shell.SetStatus(StatusOffline, detail)
		// Recover regardless of how the previous engine was started. An engine
		// discovered during app startup is marked external, but it may still go
		// away later; leaving that mode offline forever makes the app's Retry
		// affordance ineffective.
		ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := w.eng.Ensure(ectx); err != nil {
			w.log.Warn("engine restart", "err", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func (w *Watcher) setupNotifications() {
	ok, err := w.notifier.RequestNotificationAuthorization()
	if err != nil {
		w.log.Info("notifications unavailable", "err", err)
	}
	if !ok {
		return
	}
	if err := w.notifier.RegisterNotificationCategory(notifications.NotificationCategory{
		ID: approvalCategory,
		Actions: []notifications.NotificationAction{
			{ID: actionApprove, Title: "Approve"},
			{ID: actionDeny, Title: "Deny", Destructive: true},
		},
	}); err != nil {
		w.log.Warn("register notification category", "err", err)
		return
	}
	w.mu.Lock()
	w.canNotify = true
	w.mu.Unlock()
}

func (w *Watcher) session(ctx context.Context) error {
	ep, err := w.eng.Endpoints()
	if err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	c, err := client.Dial(dctx, ep.Socket, "umcode-mac-shell", true)
	cancel()
	if err != nil {
		return err
	}
	defer c.Close()
	w.mu.Lock()
	w.c = c
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.c = nil
		w.mu.Unlock()
	}()

	var list protocol.ApprovalListResult
	if err := c.Call(ctx, protocol.MethodApprovalList, nil, &list); err != nil {
		return err
	}
	w.mu.Lock()
	known := w.pending
	w.pending = map[string]protocol.Approval{}
	for _, a := range list.Approvals {
		w.pending[a.ID] = a
	}
	w.mu.Unlock()
	// Notify about approvals we have not shown yet (e.g. created while offline).
	for _, a := range list.Approvals {
		if _, seen := known[a.ID]; !seen {
			w.notifyApproval(a)
		}
	}
	w.shell.SetStatus(StatusOnline, "")
	w.refreshCounts(ctx, c)
	w.refreshProjects(ctx, c)

	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	notes := c.Notifications()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			w.refreshCounts(ctx, c)
		case n, ok := <-notes:
			if !ok {
				return fmt.Errorf("engine disconnected")
			}
			w.handle(n)
		}
	}
}

func (w *Watcher) refreshCounts(ctx context.Context, c *client.Client) {
	var st protocol.EngineStatus
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Call(cctx, protocol.MethodEngineStatus, nil, &st); err == nil {
		w.shell.SetCounts(st.PendingApprovals, st.ActiveTurns)
	}
}

// refreshProjects keeps the menu bar's project list in step with the engine.
func (w *Watcher) refreshProjects(ctx context.Context, c *client.Client) {
	var res protocol.ProjectListResult
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Call(cctx, protocol.MethodProjectList, protocol.ProjectListParams{}, &res); err == nil {
		if len(res.Projects) > 8 {
			res.Projects = res.Projects[:8]
		}
		w.shell.SetProjects(res.Projects)
	}
}

func (w *Watcher) handle(n client.Notification) {
	switch n.Method {
	case protocol.NotifyApprovalRequest:
		var ev protocol.ApprovalEvent
		if json.Unmarshal(n.Params, &ev) != nil {
			return
		}
		w.mu.Lock()
		w.pending[ev.Approval.ID] = ev.Approval
		count := len(w.pending)
		w.mu.Unlock()
		w.shell.SetCounts(count, -1)
		w.notifyApproval(ev.Approval)
	case protocol.NotifyApprovalResolved:
		var ev protocol.ApprovalEvent
		if json.Unmarshal(n.Params, &ev) != nil {
			return
		}
		w.mu.Lock()
		delete(w.pending, ev.Approval.ID)
		count := len(w.pending)
		canNotify := w.canNotify
		w.mu.Unlock()
		w.shell.SetCounts(count, -1)
		if canNotify {
			_ = w.notifier.RemoveNotification(notificationID(ev.Approval.ID))
		}
	case protocol.NotifyProjectUpdated:
		w.mu.Lock()
		c := w.c
		w.mu.Unlock()
		if c != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			w.refreshProjects(ctx, c)
			cancel()
		}
	case protocol.NotifyBudgetWarning:
		var bw protocol.BudgetWarning
		if json.Unmarshal(n.Params, &bw) != nil {
			return
		}
		w.send(notifications.NotificationOptions{
			ID:    "budget-" + bw.CredentialID,
			Title: "API key budget",
			Body:  fmt.Sprintf("%s has used %.0f%% of its $%.2f monthly budget.", bw.Label, bw.Percent, bw.BudgetUSD),
		}, false)
	}
}

func notificationID(approvalID string) string { return "approval-" + approvalID }

func (w *Watcher) notifyApproval(a protocol.Approval) {
	summary := a.ActionSummary
	if summary == "" {
		summary = "Run " + a.Tool
	}
	if len(summary) > 240 {
		summary = summary[:240] + "…"
	}
	w.send(notifications.NotificationOptions{
		ID:                notificationID(a.ID),
		Title:             "Approval needed",
		Subtitle:          fmt.Sprintf("%s · %s risk", a.Tool, a.Risk),
		Body:              summary,
		CategoryID:        approvalCategory,
		ThreadID:          "approvals",
		InterruptionLevel: notifications.InterruptionLevelTimeSensitive,
		Data:              map[string]any{"approvalId": a.ID, "threadId": a.ThreadID},
	}, true)
}

func (w *Watcher) send(opts notifications.NotificationOptions, withActions bool) {
	w.mu.Lock()
	ok := w.canNotify
	w.mu.Unlock()
	if !ok {
		return
	}
	var err error
	if withActions {
		err = w.notifier.SendNotificationWithActions(opts)
	} else {
		err = w.notifier.SendNotification(opts)
	}
	if err != nil {
		w.log.Warn("send notification", "err", err)
	}
}

func (w *Watcher) onNotificationResponse(res notifications.NotificationResult) {
	if res.Error != nil {
		w.log.Warn("notification response", "err", res.Error)
		return
	}
	r := res.Response
	approvalID, _ := r.UserInfo["approvalId"].(string)
	threadID, _ := r.UserInfo["threadId"].(string)
	switch r.ActionIdentifier {
	case actionApprove, actionDeny:
		if approvalID == "" {
			return
		}
		go w.respond(approvalID, r.ActionIdentifier == actionApprove)
	default:
		// Clicking the notification itself opens the chat that asked.
		if threadID != "" {
			w.shell.OpenThread(threadID)
		} else if approvalID != "" {
			w.shell.ShowView("approvals")
		} else {
			w.shell.ShowWindow()
		}
	}
}

func (w *Watcher) respond(approvalID string, approve bool) {
	w.mu.Lock()
	c := w.c
	w.mu.Unlock()
	if c == nil {
		w.log.Warn("cannot answer approval: engine not connected", "approval", approvalID)
		w.shell.ShowView("approvals")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Call(ctx, protocol.MethodApprovalRespond, protocol.ApprovalRespondParams{ApprovalID: approvalID, Approve: approve}, nil); err != nil {
		w.log.Warn("answer approval", "approval", approvalID, "err", err)
		w.shell.ShowView("approvals")
	}
}
