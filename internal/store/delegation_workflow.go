package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/protocol"
)

// AgentReviewReport is submitted only by the bound reviewer execution.
type AgentReviewReport = protocol.AgentReviewReport

func (s *Store) SaveAgentReviewReport(ctx context.Context, taskID string, r AgentReviewReport) error {
	if r.Verdict != "approved" && r.Verdict != "changes_requested" {
		return errors.New("explicit approved or changes_requested verdict required")
	}
	if r.Verdict == "approved" && len(r.Findings) != 0 {
		return errors.New("approved verdict cannot contain unresolved findings")
	}
	if r.Verdict == "changes_requested" && len(r.Findings) == 0 {
		return errors.New("changes_requested requires findings")
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO agent_review_reports(reviewer_id,task_id,data_json)
 SELECT ?,?,? WHERE EXISTS(SELECT 1 FROM agent_review_sources rs JOIN agent_children r ON r.id=rs.reviewer_id JOIN agent_children i ON i.id=rs.implementer_id JOIN agent_tasks t ON t.id=rs.task_id
 WHERE rs.task_id=? AND rs.reviewer_id=? AND rs.implementer_id=? AND rs.revision=? AND r.role='reviewer' AND r.state='running' AND i.state='completed' AND i.revision=rs.revision AND t.state='running')
 ON CONFLICT(reviewer_id) DO UPDATE SET data_json=excluded.data_json WHERE agent_review_reports.data_json=excluded.data_json`, r.ReviewerID, taskID, string(b), taskID, r.ReviewerID, r.ImplementerID, r.Revision)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrAgentConflict
	}
	return nil
}
func (s *Store) AgentReviewReport(ctx context.Context, taskID, reviewerID string) (AgentReviewReport, error) {
	var raw string
	var r AgentReviewReport
	err := s.DB.QueryRowContext(ctx, `SELECT data_json FROM agent_review_reports WHERE task_id=? AND reviewer_id=?`, taskID, reviewerID).Scan(&raw)
	if err != nil {
		return r, normalizeNotFound(err)
	}
	err = json.Unmarshal([]byte(raw), &r)
	return r, err
}

// AgentReviewEligible is deliberately storage-only: invoked under workspace locks.
func (s *Store) AgentReviewEligible(ctx context.Context, t protocol.AgentTask, c protocol.AgentChild, revision string) error {
	children, err := s.AgentChildren(ctx, t.ID)
	if err != nil {
		return err
	}
	current, err := s.AgentChild(ctx, t.ID, c.ID)
	if err != nil {
		return err
	}
	task, err := s.AgentTask(ctx, t.ID)
	if err != nil {
		return err
	}
	if task.State != protocol.AgentStateRunning || current.State != protocol.AgentStateCompleted || current.Revision != revision || revision == "" {
		return ErrAgentConflict
	}
	var latest *protocol.AgentChild
	ambiguous := false
	for _, r := range children {
		if r.Role != protocol.AgentRoleReviewer {
			continue
		}
		source, rev, err := s.AgentReviewerSource(ctx, t.ID, r.ID)
		if errors.Is(err, ErrNotFound) {
			// A terminal pre-binding setup failure never inspected a submitted
			// revision. Pending or malformed completed attempts fail closed.
			if r.State == protocol.AgentStateFailed || r.State == protocol.AgentStateCancelled || r.State == protocol.AgentStateInterrupted {
				continue
			}
			return errors.New("independent review source binding is pending or unavailable")
		}
		if err != nil {
			return err
		}
		if source != c.ID || rev != revision {
			continue
		}
		if latest == nil || r.UpdatedAt.After(latest.UpdatedAt) {
			candidate := r
			latest = &candidate
			ambiguous = false
		} else if r.UpdatedAt.Equal(latest.UpdatedAt) {
			ambiguous = true
		}
	}
	if latest == nil || latest.State != protocol.AgentStateCompleted {
		return errors.New("latest bound independent review must complete with a valid structured approval")
	}
	if ambiguous {
		return errors.New("bound review ordering is ambiguous; independent approval cannot be selected")
	}
	report, err := s.AgentReviewReport(ctx, t.ID, latest.ID)
	if err != nil {
		return err
	}
	if report.ReviewerID != latest.ID || report.ImplementerID != c.ID || report.Revision != revision || report.Verdict != "approved" || len(report.Findings) != 0 {
		return errors.New("latest independent review must approve the exact current revision without unresolved findings")
	}
	return nil
}

type AgentWorkflowCheck = protocol.AgentWorkflowCheck
type AgentWorkflow = protocol.AgentWorkflow

func (s *Store) AgentWorkflow(ctx context.Context, taskID string) (AgentWorkflow, error) {
	var raw string
	var w AgentWorkflow
	err := s.DB.QueryRowContext(ctx, `SELECT data_json FROM agent_workflow WHERE task_id=?`, taskID).Scan(&raw)
	if err != nil {
		return w, normalizeNotFound(err)
	}
	err = json.Unmarshal([]byte(raw), &w)
	return w, err
}
func (s *Store) SaveAgentWorkflow(ctx context.Context, w AgentWorkflow) error {
	b, err := json.Marshal(w)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO agent_workflow(task_id,data_json) VALUES(?,?) ON CONFLICT(task_id) DO UPDATE SET data_json=excluded.data_json`, w.TaskID, string(b))
	return err
}
