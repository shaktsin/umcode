package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/work"
)

type workflowBlocker struct {
	Code     string   `json:"code"`
	WorkID   string   `json:"work_id"`
	Revision int      `json:"expected_revision"`
	Missing  []string `json:"missing"`
	Recovery string   `json:"recovery"`
}

func (b *workflowBlocker) Error() string {
	raw, _ := json.Marshal(b)
	return "workflow not ready: " + string(raw)
}
func (*workflowBlocker) Unwrap() error { return errWorkflowNotReady }

func blockedWorkflow(d *protocol.WorkDetail) *workflowBlocker {
	missing := work.MutationBlockers(*d)
	if len(work.PendingWorkflowGates(*d)) > 0 {
		missing = append(missing, "unresolved workflow decisions")
	}
	return &workflowBlocker{Code: "workflow_not_ready", WorkID: d.Work.ID, Revision: d.Work.Revision, Missing: missing, Recovery: "Use work.update with this work_id and expected_revision to resolve the missing workflow requirements. File reads and requested specification preparation remain available. Retry the denied action after recovery; do not claim edits succeeded while it remains denied."}
}

// Counts the underlying blocker, independent of tool names, arguments or reads.
type workflowRecoveryState struct {
	pending    *workflowBlocker
	key        string
	denials    int
	unresolved map[string]*workflowBlocker
}

func (r *workflowRecoveryState) observe(b *workflowBlocker, action string) bool {
	key := fmt.Sprintf("%s:%d:%v", b.WorkID, b.Revision, b.Missing)
	if key != r.key {
		r.key = key
		r.denials = 0
	}
	r.pending = b
	if r.unresolved == nil {
		r.unresolved = map[string]*workflowBlocker{}
	}
	r.unresolved[action] = b
	r.denials++
	return r.denials >= 3
}

func (r *workflowRecoveryState) resolved(action string) {
	delete(r.unresolved, action)
	if len(r.unresolved) == 0 {
		*r = workflowRecoveryState{}
	}
}

func workflowActionKey(ctx context.Context, name string, args json.RawMessage) string {
	var a map[string]any
	if json.Unmarshal(args, &a) != nil {
		return name + ":" + string(args)
	}
	if name == "file.write" || name == "file.edit" {
		if p, ok := a["path"].(string); ok {
			if scope := tools.ScopeFrom(ctx); scope != nil {
				if abs, err := scope.Resolve(p); err == nil {
					a["path"] = pathutil.Resolved(abs)
				}
			}
		}
	}
	if a["workspace"] == "project" {
		delete(a, "workspace")
	}
	if name == "file.edit" && a["replace_all"] == nil {
		a["replace_all"] = false
	}
	raw, _ := json.Marshal(a)
	return fmt.Sprintf("%s:%x", name, sha256.Sum256(raw))
}

// A model's streamed fallback is not authoritative completion evidence.
func (e *Engine) blockWorkflowSummary(ctx context.Context, id string, blocker *workflowBlocker) error {
	if id == "" {
		return nil
	}
	it, err := e.Store.GetItem(ctx, id)
	if err != nil {
		return err
	}
	it.Status = protocol.ItemFailed
	it.Text = "Execution remains blocked; the denied action did not run. " + blocker.Error()
	return e.saveAndPublish(ctx, it, protocol.NotifyItemCompleted)
}
