package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

const delegationWorkflowPrompt = `Delegation tools are available for scoped work when useful. You decide whether to use an implementer or an independent reviewer, what to check, and when the user's request is satisfied. Give each child focused requirements and relevant file/diff references; its context is fresh. Findings and check results are evidence for your judgment, not engine certification. Existing workspace isolation, permissions, cancellation and shared resource limits apply. Never claim a blocked check passed.`

// Policy runs under the coordinator lifecycle lock: storage-only, never a
// workspace call. Injected runners/resolvers retain the generic lifecycle seam.
func (e *Engine) delegationWorkflowPolicy(ctx context.Context, t protocol.AgentTask, role protocol.AgentRole, childID string) error {
	if _, managed := e.DelegationWorkspaces.(managedChildWorkspaces); !managed {
		return nil
	}
	children, err := e.Store.AgentChildren(ctx, t.ID)
	if err != nil {
		return err
	}
	var impl *protocol.AgentChild
	for i := range children {
		if children[i].Role == protocol.AgentRoleImplementer {
			if impl != nil {
				return errors.New("task has multiple implementer identities")
			}
			impl = &children[i]
		}
	}
	if childID != "" {
		if role != protocol.AgentRoleImplementer || impl == nil || impl.ID != childID {
			return errors.New("corrections must reuse the original implementer; use a fresh independent reviewer")
		}
		integration, err := e.Store.AgentIntegration(ctx, t.ID, childID)
		if err == nil && integration.Applied {
			return errors.New("integrated implementation cannot be corrected; retain evidence and start a new task")
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return nil
	}
	if role == protocol.AgentRoleImplementer {
		if impl != nil {
			return errors.New("one implementer identity per task; use agent.followup")
		}
		return nil
	}
	if role == protocol.AgentRoleReviewer && impl == nil {
		return nil
	}
	if role != protocol.AgentRoleReviewer || impl == nil || impl.State != protocol.AgentStateCompleted || impl.Revision == "" {
		return errors.New("review requires a completed submitted implementer revision")
	}
	return nil
}

var errNoImplementer = errors.New("implementer required")

func (e *Engine) workflowImplementer(ctx context.Context, taskID string) (protocol.AgentChild, error) {
	children, err := e.Store.AgentChildren(ctx, taskID)
	if err != nil {
		return protocol.AgentChild{}, err
	}
	var impl protocol.AgentChild
	for _, c := range children {
		if c.Role == protocol.AgentRoleImplementer {
			if impl.ID != "" {
				return impl, errors.New("multiple implementers are unsupported")
			}
			impl = c
		}
	}
	if impl.ID == "" {
		return impl, errNoImplementer
	}
	return impl, nil
}

type workflowRunner struct{ engine *Engine }

func (r workflowRunner) Run(ctx context.Context, t protocol.AgentTask, c protocol.AgentChild) (protocol.AgentResult, error) {
	e := r.engine
	_, managed := e.DelegationWorkspaces.(managedChildWorkspaces)
	if managed && c.Role == protocol.AgentRoleReviewer {
		impl, err := e.workflowImplementer(ctx, t.ID)
		if err != nil && !errors.Is(err, errNoImplementer) {
			return protocol.AgentResult{}, err
		}
		if errors.Is(err, errNoImplementer) {
			if _, err = e.DelegationWorkspaceService.PrepareStandaloneReview(ctx, t, c); err != nil {
				return protocol.AgentResult{}, err
			}
		} else if err == nil {
			if err = e.DelegationWorkspaceService.BindReviewer(ctx, t.ID, c.ID, impl.ID); err != nil {
				return protocol.AgentResult{}, err
			}
		}
	}
	result, err := (childRunner{engine: e}).Run(ctx, t, c)
	if managed && c.Role == protocol.AgentRoleReviewer {
		report, reportErr := e.Store.AgentReviewReport(context.WithoutCancel(ctx), t.ID, c.ID)
		if reportErr != nil && !errors.Is(reportErr, store.ErrNotFound) {
			return result, errors.Join(err, reportErr)
		}
		if reportErr == nil {
			result.Review = &report
			result.Findings = report.Findings
		}
	}
	return result, err
}
func decodeWorkflow(args json.RawMessage, v any) error {
	d := json.NewDecoder(bytes.NewReader(args))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("one argument object required")
	}
	return nil
}
func (e *Engine) delegationWorkflowCall(ctx context.Context, action string, args json.RawMessage) (string, error) {
	if action == "review_report" {
		return e.submitReview(ctx, args)
	}
	var empty struct{}
	if err := decodeWorkflow(args, &empty); err != nil {
		return "", err
	}
	task, err := e.authorizeDelegationTool(ctx)
	if err != nil {
		return "", err
	}
	e.delegationWorkflowMu.Lock()
	defer e.delegationWorkflowMu.Unlock()
	// Reload after acquiring the workflow lock.
	task, err = e.Store.AgentTask(ctx, task.ID)
	if err != nil {
		return "", err
	}
	if task.State == protocol.AgentStateCompleted && action == "complete" {
		w, err := e.Store.AgentWorkflow(ctx, task.ID)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(w)
		return string(b), nil
	}
	if !unfinishedDelegation(task.State) {
		return "", errors.New("delegation task is no longer active")
	}
	impl, err := e.workflowImplementer(ctx, task.ID)
	if err != nil {
		return "", err
	}
	children, err := e.Store.AgentChildren(ctx, task.ID)
	if err != nil {
		return "", err
	}
	for _, c := range children {
		if unfinishedDelegation(c.State) {
			return "", errors.New("wait for the active worker before integration or completion")
		}
	}
	var w store.AgentWorkflow
	switch action {
	case "integrate":
		if err = tools.RequireWritable(ctx); err != nil {
			return "", err
		}
		_, err = e.DelegationWorkspaceService.Integrate(ctx, task.ID, impl.ID, task.BaseRevision)
		if err != nil {
			return "", err
		}
		w, err = e.Store.AgentWorkflow(ctx, task.ID)
		if errors.Is(err, store.ErrNotFound) {
			planner, ok := e.Tools.Get("verification.plan")
			if !ok {
				return "", errors.New("verification planner unavailable")
			}
			raw, planErr := planner.Call(ctx, json.RawMessage(`{}`))
			if planErr != nil {
				return "", planErr
			}
			var plan struct {
				Checks   []store.AgentWorkflowCheck `json:"checks"`
				VisualQA string                     `json:"visual_qa"`
			}
			if err = json.Unmarshal([]byte(raw), &plan); err != nil {
				return "", err
			}
			w = store.AgentWorkflow{TaskID: task.ID, ChildID: impl.ID, Revision: impl.Revision, Checks: plan.Checks}
			for i := range w.Checks {
				w.Checks[i].Status = "not_run"
			}
			if strings.HasPrefix(plan.VisualQA, "required:") {
				w.Checks = append(w.Checks, store.AgentWorkflowCheck{Label: "Visual QA", Command: "visual QA", Kind: "visual", Reason: plan.VisualQA, Status: "blocked", Summary: "Trusted interactive visual QA receipts are unavailable in delegated completion v1."})
			}
			if err = e.Store.SaveAgentWorkflow(ctx, w); err != nil {
				return "", err
			}
		} else if err != nil {
			return "", err
		}
		if w.Revision != impl.Revision {
			return "", errors.New("verification plan revision changed")
		}
	case "complete":
		w, err = e.Store.AgentWorkflow(ctx, task.ID)
		if err != nil {
			return "", err
		}
		if len(w.Checks) == 0 {
			return "", errors.New("no executable affected checks available; verification is blocked, task remains incomplete")
		}
		var checks []protocol.AgentCheckEvidence
		for _, c := range w.Checks {
			if c.Status != "passed" {
				return "", fmt.Errorf("required check %s is %s", c.Command, c.Status)
			}
			checks = append(checks, protocol.AgentCheckEvidence{Command: c.Command, Status: c.Status, Summary: c.Summary})
		}
		if err = e.DelegationWorkspaceService.ValidateIntegrated(ctx, task.ID, impl.ID, w.Revision); err != nil {
			return "", err
		}
		if err = e.DelegationWorkspaceService.RecordVerification(ctx, task.ID, impl.ID, w.Revision, checks); err != nil {
			return "", err
		}
		if err = e.Store.SaveAgentWorkflow(ctx, w); err != nil {
			return "", err
		}
		if err = e.Store.UpdateAgentTaskState(ctx, task.ID, task.ThreadID, task.ProjectID, task.State, protocol.AgentStateCompleted); err != nil {
			return "", err
		}
		// Successful completion is durable before cleanup. Cancellation winning
		// the CAS retains every artifact; cleanup refusal remains a warning.
		if cleanupErr := e.DelegationWorkspaceService.Cleanup(ctx, task.ID, impl.ID); cleanupErr != nil {
			w.CleanupWarning = "Verified work retained: " + cleanupErr.Error()
		}
		if err = e.Store.SaveAgentWorkflow(ctx, w); err != nil {
			return "", err
		}
	default:
		return "", errors.New("unknown delegation workflow action")
	}
	if latest, readErr := e.Store.AgentTask(ctx, task.ID); readErr == nil {
		e.Bus.Publish(task.ThreadID, protocol.NotifyDelegationUpdated, protocol.DelegationUpdated{ThreadID: task.ThreadID, TaskID: task.ID, Task: &latest, Workflow: &w})
	}
	b, err := json.Marshal(w)
	return string(b), err
}
func (e *Engine) submitReview(ctx context.Context, args json.RawMessage) (string, error) {
	x := childExecutionFrom(ctx)
	if x == nil || x.child.Role != protocol.AgentRoleReviewer {
		return "", errors.New("review report requires actual reviewer execution")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var r struct {
		Revision string                  `json:"revision"`
		Verdict  string                  `json:"verdict"`
		Findings []protocol.AgentFinding `json:"findings"`
	}
	if err := decodeWorkflow(args, &r); err != nil {
		return "", err
	}
	if r.Findings == nil {
		return "", errors.New("explicit findings array required")
	}
	for _, f := range r.Findings {
		if strings.TrimSpace(f.Message) == "" || strings.TrimSpace(f.Severity) == "" {
			return "", errors.New("finding message and severity required")
		}
	}
	id, revision, err := e.Store.AgentReviewerSource(ctx, x.task.ID, x.child.ID)
	if err != nil {
		return "", err
	}
	if r.Revision != revision || x.workspace.Revision != revision {
		return "", errors.New("review report does not match bound source revision")
	}
	report := store.AgentReviewReport{ReviewerID: x.child.ID, ImplementerID: id, Revision: revision, Verdict: r.Verdict, Findings: r.Findings}
	if err = e.Store.SaveAgentReviewReport(ctx, x.task.ID, report); err != nil {
		return "", err
	}
	b, err := json.Marshal(report)
	return string(b), err
}

// begin/end bracket the real built-in call, after normal tool approval. Neither
// model text nor tool arguments provide status evidence. Pre-integration and
// child checks intentionally never create lead receipts.
func (e *Engine) beginDelegationVerification(ctx context.Context, name string) (*store.AgentWorkflow, error) {
	if name != "verification.run" && name != "browser.verify" {
		return nil, nil
	}
	task, ok := DelegationTaskFromContext(ctx)
	if !ok {
		return nil, nil
	}
	w, err := e.Store.AgentWorkflow(ctx, task.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = e.DelegationWorkspaceService.ValidateIntegrated(ctx, task.ID, w.ChildID, w.Revision); err != nil {
		return nil, err
	}
	return &w, nil
}
func (e *Engine) finishDelegationVerification(ctx context.Context, name string, w *store.AgentWorkflow, raw string) error {
	if w == nil {
		return nil
	}
	e.delegationWorkflowMu.Lock()
	defer e.delegationWorkflowMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.DelegationWorkspaceService.ValidateIntegrated(ctx, w.TaskID, w.ChildID, w.Revision); err != nil {
		return err
	}
	current, err := e.Store.AgentWorkflow(ctx, w.TaskID)
	if err != nil {
		return err
	}
	if current.Revision != w.Revision {
		return errors.New("verification revision changed")
	}
	type checkResult struct {
		Command   string `json:"command"`
		Directory string `json:"directory"`
		Status    string `json:"status"`
		Output    string `json:"output"`
		Error     string `json:"error"`
		Reason    string `json:"reason"`
	}
	var results []checkResult
	if name == "browser.verify" {
		var r checkResult
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			return err
		}
		results = append(results, r)
	} else {
		var r struct {
			Results []checkResult `json:"results"`
		}
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			return err
		}
		results = r.Results
	}
	for _, r := range results {
		for i := range current.Checks {
			c := &current.Checks[i]
			if c.Command == r.Command && c.Directory == r.Directory && (c.Kind == "browser") == (name == "browser.verify") {
				switch r.Status {
				case "passed", "failed", "blocked", "not_run":
					c.Status = r.Status
				default:
					return errors.New("unknown verification status")
				}
				c.Summary = r.Error
				if c.Summary == "" {
					c.Summary = r.Reason
				}
				if c.Summary == "" {
					c.Summary = r.Output
				}
				if len(c.Summary) > 4096 {
					c.Summary = c.Summary[:4096]
				}
			}
		}
	}
	if err = e.Store.SaveAgentWorkflow(ctx, current); err != nil {
		return err
	}
	if task, readErr := e.Store.AgentTask(ctx, w.TaskID); readErr == nil {
		e.Bus.Publish(task.ThreadID, protocol.NotifyDelegationUpdated, protocol.DelegationUpdated{ThreadID: task.ThreadID, TaskID: task.ID, Task: &task, Workflow: &current})
	}
	return nil
}
