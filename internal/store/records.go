package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workflowgraph"
)

// ---- credentials ----

const credCols = `id, provider, label, base_url, last4, enabled, is_default, fallback,
	monthly_budget_usd, hard_stop, last_tested_at, last_test_ok, created_at, kind`

func scanCred(sc interface{ Scan(...any) error }) (protocol.Credential, error) {
	var c protocol.Credential
	var enabled, isDefault, fallback, hardStop int
	var tested sql.NullString
	var ok sql.NullInt64
	var created string
	err := sc.Scan(&c.ID, &c.Provider, &c.Label, &c.BaseURL, &c.Last4, &enabled, &isDefault, &fallback,
		&c.MonthlyBudgetUSD, &hardStop, &tested, &ok, &created, &c.Kind)
	if err != nil {
		return c, err
	}
	c.Enabled, c.IsDefault, c.Fallback, c.HardStop = enabled != 0, isDefault != 0, fallback != 0, hardStop != 0
	c.LastTestedAt = nullTime(tested)
	if ok.Valid {
		v := ok.Int64 != 0
		c.LastTestOK = &v
	}
	c.CreatedAt = ParseTime(created)
	return c, nil
}

// CreateCredential inserts a credential record (the secret lives in the secrets store).
// If it is the provider's first credential, or IsDefault is set, it becomes the default.
func (s *Store) CreateCredential(ctx context.Context, c protocol.Credential) (protocol.Credential, error) {
	if c.ID == "" {
		c.ID = NewID("key")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM credentials WHERE provider = ?`, c.Provider).Scan(&n); err != nil {
		return c, err
	}
	if n == 0 {
		c.IsDefault = true
	}
	if c.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE credentials SET is_default = 0 WHERE provider = ?`, c.Provider); err != nil {
			return c, err
		}
	}
	now := time.Now().UTC()
	c.CreatedAt = now
	c.Enabled = true
	if c.Kind == "" {
		c.Kind = "api_key"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO credentials (id, provider, label, base_url, last4, enabled, is_default,
		fallback, monthly_budget_usd, hard_stop, created_at, updated_at, kind) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Provider, c.Label, c.BaseURL, c.Last4, 1, b2i(c.IsDefault), b2i(c.Fallback),
		c.MonthlyBudgetUSD, b2i(c.HardStop), FormatTime(now), FormatTime(now), c.Kind)
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}

// GetCredential loads one credential.
func (s *Store) GetCredential(ctx context.Context, id string) (protocol.Credential, error) {
	c, err := scanCred(s.DB.QueryRowContext(ctx, `SELECT `+credCols+` FROM credentials WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ListCredentials returns credentials, optionally for one provider.
func (s *Store) ListCredentials(ctx context.Context, provider string) ([]protocol.Credential, error) {
	q := `SELECT ` + credCols + ` FROM credentials`
	var args []any
	if provider != "" {
		q += ` WHERE provider = ?`
		args = append(args, provider)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY provider, is_default DESC, created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Credential
	for rows.Next() {
		c, err := scanCred(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateCredential applies a partial update.
func (s *Store) UpdateCredential(ctx context.Context, p protocol.CredentialUpdateParams) error {
	c, err := s.GetCredential(ctx, p.CredentialID)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sets []string
	var args []any
	if p.Label != nil {
		sets, args = append(sets, "label = ?"), append(args, *p.Label)
	}
	if p.Enabled != nil {
		sets, args = append(sets, "enabled = ?"), append(args, b2i(*p.Enabled))
	}
	if p.Fallback != nil {
		sets, args = append(sets, "fallback = ?"), append(args, b2i(*p.Fallback))
	}
	if p.BaseURL != nil {
		sets, args = append(sets, "base_url = ?"), append(args, *p.BaseURL)
	}
	if p.IsDefault != nil {
		if *p.IsDefault {
			if _, err := tx.ExecContext(ctx, `UPDATE credentials SET is_default = 0 WHERE provider = ?`, c.Provider); err != nil {
				return err
			}
		}
		sets, args = append(sets, "is_default = ?"), append(args, b2i(*p.IsDefault))
	}
	sets, args = append(sets, "updated_at = ?"), append(args, Now(), p.CredentialID)
	if _, err := tx.ExecContext(ctx, `UPDATE credentials SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return err
	}
	return tx.Commit()
}

// SetCredentialLast4 updates the displayed suffix after a rotation.
func (s *Store) SetCredentialLast4(ctx context.Context, id, last4 string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE credentials SET last4 = ?, updated_at = ? WHERE id = ?`, last4, Now(), id)
	return err
}

// SetCredentialBudget sets the monthly budget.
func (s *Store) SetCredentialBudget(ctx context.Context, id string, usd float64, hardStop bool) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE credentials SET monthly_budget_usd = ?, hard_stop = ?, updated_at = ? WHERE id = ?`,
		usd, b2i(hardStop), Now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordCredentialTest stores the outcome of a key test.
func (s *Store) RecordCredentialTest(ctx context.Context, id string, ok bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE credentials SET last_tested_at = ?, last_test_ok = ? WHERE id = ?`,
		Now(), b2i(ok), id)
	return err
}

// DeleteCredential removes a credential record; usage rows keep its id for history.
// If it was the default, the oldest remaining key for the provider becomes default.
func (s *Store) DeleteCredential(ctx context.Context, id string) error {
	c, err := s.GetCredential(ctx, id)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE id = ?`, id); err != nil {
		return err
	}
	if c.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE credentials SET is_default = 1 WHERE id = (
			SELECT id FROM credentials WHERE provider = ? ORDER BY created_at LIMIT 1)`, c.Provider); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- usage ----

// UsageRecord is one LLM request's metering row.
type UsageRecord struct {
	CreatedAt    time.Time
	CredentialID string
	Provider     string
	Model        string
	ThreadID     string
	TurnID       string
	Role         string
	Usage        protocol.UsageTotals
	LatencyMs    int64
	Status       string
}

// InsertUsage writes a usage row.
func (s *Store) InsertUsage(ctx context.Context, r UsageRecord) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	if r.Status == "" {
		r.Status = "ok"
	}
	if r.Role == "" {
		r.Role = "chat"
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO llm_usage (created_at, credential_id, provider, model, thread_id, turn_id,
		role, input_tokens, cached_input_tokens, output_tokens, reasoning_tokens, cost_usd, latency_ms, status, estimated)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		FormatTime(r.CreatedAt), r.CredentialID, r.Provider, r.Model, r.ThreadID, r.TurnID, r.Role,
		r.Usage.InputTokens, r.Usage.CachedInputTokens, r.Usage.OutputTokens, r.Usage.ReasoningTokens,
		r.Usage.CostUSD, r.LatencyMs, r.Status, b2i(r.Usage.Estimated))
	return err
}

const usageAgg = `COALESCE(SUM(input_tokens),0), COALESCE(SUM(cached_input_tokens),0),
	COALESCE(SUM(output_tokens),0), COALESCE(SUM(reasoning_tokens),0), COALESCE(SUM(cost_usd),0),
	COUNT(*), COALESCE(MAX(estimated),0)`

func scanTotals(sc interface{ Scan(...any) error }, extra ...any) (protocol.UsageTotals, error) {
	var u protocol.UsageTotals
	var est int
	dest := append(extra, &u.InputTokens, &u.CachedInputTokens, &u.OutputTokens, &u.ReasoningTokens,
		&u.CostUSD, &u.Requests, &est)
	err := sc.Scan(dest...)
	u.Estimated = est != 0
	return u, err
}

// UsageTotalsWhere sums usage rows matching a WHERE clause.
func (s *Store) UsageTotalsWhere(ctx context.Context, where string, args ...any) (protocol.UsageTotals, error) {
	return scanTotals(s.DB.QueryRowContext(ctx, `SELECT `+usageAgg+` FROM llm_usage WHERE `+where, args...))
}

func (s *Store) usageByThread(ctx context.Context, ph []string, ids []any) (map[string]protocol.UsageTotals, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT thread_id, `+usageAgg+` FROM llm_usage
		WHERE thread_id IN (`+strings.Join(ph, ",")+`) GROUP BY thread_id`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]protocol.UsageTotals{}
	for rows.Next() {
		var id string
		u, err := scanTotals(rows, &id)
		if err != nil {
			return nil, err
		}
		out[id] = u
	}
	return out, rows.Err()
}

func (s *Store) usageByTurn(ctx context.Context, threadID string) (map[string]protocol.UsageTotals, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT turn_id, `+usageAgg+` FROM llm_usage
		WHERE thread_id = ? GROUP BY turn_id`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]protocol.UsageTotals{}
	for rows.Next() {
		var id string
		u, err := scanTotals(rows, &id)
		if err != nil {
			return nil, err
		}
		out[id] = u
	}
	return out, rows.Err()
}

// UsageGrouped aggregates usage between from and to, grouped by a dimension.
func (s *Store) UsageGrouped(ctx context.Context, p protocol.UsageSummaryParams) ([]protocol.UsageRow, protocol.UsageTotals, error) {
	var key string
	switch p.GroupBy {
	case "credential", "":
		key = "credential_id"
	case "model":
		key = "provider || '/' || model"
	case "thread":
		key = "thread_id"
	case "role":
		key = "role"
	case "day":
		key = "substr(created_at, 1, 10)"
	default:
		return nil, protocol.UsageTotals{}, fmt.Errorf("unknown groupBy %q", p.GroupBy)
	}
	where := []string{"created_at >= ?", "created_at < ?"}
	args := []any{FormatTime(p.From), FormatTime(p.To)}
	if p.CredentialID != "" {
		where, args = append(where, "credential_id = ?"), append(args, p.CredentialID)
	}
	if p.ThreadID != "" {
		where, args = append(where, "thread_id = ?"), append(args, p.ThreadID)
	}
	w := strings.Join(where, " AND ")
	rows, err := s.DB.QueryContext(ctx, `SELECT `+key+` AS k, `+usageAgg+` FROM llm_usage WHERE `+w+
		` GROUP BY k ORDER BY k`, args...)
	if err != nil {
		return nil, protocol.UsageTotals{}, err
	}
	defer rows.Close()
	var out []protocol.UsageRow
	var total protocol.UsageTotals
	for rows.Next() {
		var k string
		u, err := scanTotals(rows, &k)
		if err != nil {
			return nil, total, err
		}
		out = append(out, protocol.UsageRow{Key: k, Usage: u})
		total.Add(u)
	}
	return out, total, rows.Err()
}

// ---- approvals ----

// DecideWorkflowApproval commits the approval, its exact node outcome and task
// readiness under one writer lock and one work revision. Expiry changes only
// the approval row, including when the graph identity has since become stale.
func (s *Store) DecideWorkflowApproval(ctx context.Context, id, status, by string) (int, error) {
	if status != "approved" && status != "denied" && status != "expired" {
		return 0, ErrWorkUpdateConflict
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var workID, nodeID, threadID string
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT work_id,node_id,node_revision,thread_id FROM approvals WHERE id=? AND kind='workflow' AND status='pending'`, id).Scan(&workID, &nodeID, &revision, &threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	d, err := getWorkDetail(ctx, tx, workID)
	if err != nil {
		return 0, err
	}
	if status != "expired" && (d.Work.ThreadID != threadID || d.Work.Status != protocol.WorkOpen) {
		return 0, ErrWorkUpdateConflict
	}
	index := -1
	for i, n := range d.Nodes {
		if n.ID == nodeID {
			index = i
			break
		}
	}
	if status != "expired" && index < 0 {
		return 0, ErrWorkUpdateConflict
	}
	var n protocol.WorkNode
	if index >= 0 {
		n = d.Nodes[index]
	}
	var content struct {
		Required bool   `json:"required"`
		GateKind string `json:"gate_kind"`
		Blocking bool   `json:"blocking"`
	}
	if status != "expired" && len(n.Content) > 0 && json.Unmarshal(n.Content, &content) != nil {
		return 0, ErrWorkUpdateConflict
	}
	decision := n.Kind == protocol.NodeDecision && n.Status == protocol.StatusProposed && content.Required && workflowgraph.ValidGate(content.GateKind)
	unknown := n.Kind == protocol.NodeUnknown && n.Status == protocol.StatusOpen && content.Blocking
	if status != "expired" && (n.WorkID != workID || n.Revision != revision || n.ValidUntil != nil || n.SupersededBy != "" || (!decision && !unknown)) {
		return 0, ErrWorkUpdateConflict
	}
	finalRevision := d.Work.Revision
	if status != "expired" {
		originalNodes := append([]protocol.WorkNode(nil), d.Nodes...)
		outcome := protocol.StatusRejected
		if unknown {
			outcome = protocol.StatusOpen
		}
		if status == "approved" {
			outcome = protocol.StatusApproved
			if unknown {
				outcome = protocol.StatusAcceptedRisk
			}
		}
		d.Nodes[index].Status = outcome
		res, err := tx.ExecContext(ctx, `UPDATE work_nodes SET status=?,revision=revision+1,updated_at=? WHERE id=? AND work_id=? AND revision=? AND status=?`, outcome, Now(), nodeID, workID, revision, n.Status)
		if err != nil {
			return 0, err
		}
		if err = requireWorkUpdateRow(res); err != nil {
			return 0, err
		}
		if status == "approved" && d.Work.ProjectID == "" {
			if err := recordWorkflowAcceptance(ctx, tx, d, n, id, revision); err != nil {
				return 0, err
			}
		}
		statuses := workflowgraph.DeriveApprovalTaskStatuses(d, unknown && status == "denied")
		for _, node := range originalNodes {
			if node.Kind != protocol.NodeTask {
				continue
			}
			to := statuses[node.ID]
			old := node.Status
			if to == old {
				continue
			}
			res, err := tx.ExecContext(ctx, `UPDATE work_nodes SET status=?,revision=revision+1,updated_at=? WHERE id=? AND work_id=? AND revision=? AND status=?`, to, Now(), node.ID, workID, node.Revision, old)
			if err != nil {
				return 0, err
			}
			if err = requireWorkUpdateRow(res); err != nil {
				return 0, err
			}
		}
		res, err = tx.ExecContext(ctx, `UPDATE works SET revision=revision+1 WHERE id=? AND revision=? AND status='open'`, workID, d.Work.Revision)
		if err != nil {
			return 0, err
		}
		if err = requireWorkUpdateRow(res); err != nil {
			return 0, err
		}
		finalRevision++
	}
	res, err := tx.ExecContext(ctx, `UPDATE approvals SET status=?,decided_by=?,decided_at=? WHERE id=? AND kind='workflow' AND status='pending'`, status, by, Now(), id)
	if err != nil {
		return 0, err
	}
	if err = requireWorkUpdateRow(res); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return finalRevision, nil
}

// Workflow acceptance is narrowly scoped to the exact approved decision (or
// accepted risk). It never substitutes for executable or unrelated criteria.
func recordWorkflowAcceptance(ctx context.Context, tx *sql.Tx, d protocol.WorkDetail, target protocol.WorkNode, approvalID string, revision int) error {
	linked := map[string]bool{}
	for _, edge := range d.Edges {
		if edge.Relation == protocol.RelVerifies && edge.ToNodeID == target.ID {
			linked[edge.FromNodeID] = true
		}
	}
	for _, n := range d.Nodes {
		if n.Kind != protocol.NodeCriterion || !linked[n.ID] || n.Status == protocol.StatusSuperseded || n.ValidUntil != nil || n.SupersededBy != "" {
			continue
		}
		var content struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(n.Content, &content) != nil || content.Command != "workflow:approval" {
			continue
		}
		now := time.Now().UTC()
		ev, err := insertEvidence(ctx, tx, protocol.Evidence{WorkID: d.Work.ID, NodeID: n.ID, Kind: protocol.EvidenceWorkflowApproval, SourceURI: "approval://" + approvalID, SourceRevision: fmt.Sprintf("%s:%d", target.ID, revision), Summary: "approved", ObservedAt: now})
		if err != nil {
			return err
		}
		if _, err = insertAttempt(ctx, tx, protocol.VerificationAttempt{WorkID: d.Work.ID, CriterionNodeID: n.ID, CheckType: "workflow_approval", Command: content.Command, Status: protocol.AttemptPassed, EvidenceID: ev.ID, StartedAt: now, FinishedAt: now}); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE work_nodes SET status='passed', revision=revision+1, updated_at=? WHERE id=? AND work_id=? AND revision=?`, FormatTime(now), n.ID, d.Work.ID, n.Revision)
		if err != nil {
			return err
		}
		if err = requireWorkUpdateRow(res); err != nil {
			return err
		}
	}
	return nil
}

// CreateApproval inserts a pending approval.
func (s *Store) CreateApproval(ctx context.Context, a protocol.Approval) error {
	if a.Kind == "" {
		a.Kind = "tool"
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO approvals (id, thread_id, turn_id, item_id, tool, args_json, risk,
		reason, action_summary, status, created_at, expires_at, kind, work_id, node_id, node_revision) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.ThreadID, a.TurnID, a.ItemID, a.Tool, string(a.Args), a.Risk, a.Reason, a.ActionSummary,
		a.Status, FormatTime(a.CreatedAt), FormatTime(a.ExpiresAt), a.Kind, a.WorkID, a.NodeID, a.NodeRevision)
	return err
}

// DecideApproval moves a pending approval to a final status. It returns
// ErrNotFound if the approval does not exist or was already decided.
func (s *Store) DecideApproval(ctx context.Context, id, status, by string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE approvals SET status = ?, decided_by = ?, decided_at = ?
		WHERE id = ? AND status = 'pending'`, status, by, Now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetApproval loads one exact approval identity, without the history page cap.
func (s *Store) GetApproval(ctx context.Context, id string) (protocol.Approval, error) {
	var a protocol.Approval
	var args, created, expires string
	var decided sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT id,thread_id,turn_id,item_id,tool,args_json,risk,reason,action_summary,status,decided_by,created_at,expires_at,decided_at,kind,work_id,node_id,node_revision FROM approvals WHERE id=?`, id).Scan(&a.ID, &a.ThreadID, &a.TurnID, &a.ItemID, &a.Tool, &args, &a.Risk, &a.Reason, &a.ActionSummary, &a.Status, &a.DecidedBy, &created, &expires, &decided, &a.Kind, &a.WorkID, &a.NodeID, &a.NodeRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.Args = json.RawMessage(args)
	a.CreatedAt, a.ExpiresAt, a.DecidedAt = ParseTime(created), ParseTime(expires), nullTime(decided)
	return a, err
}

// PendingWorkflowApproval finds a pending transport for one exact gate; stale
// revisions and unrelated approvals cannot suppress a recovery request.
func (s *Store) PendingWorkflowApproval(ctx context.Context, workID, nodeID string, revision int) (protocol.Approval, bool, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM approvals WHERE kind='workflow' AND status='pending' AND work_id=? AND node_id=? AND node_revision=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, workID, nodeID, revision).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Approval{}, false, nil
	}
	if err != nil {
		return protocol.Approval{}, false, err
	}
	a, err := s.GetApproval(ctx, id)
	return a, err == nil, err
}

// ListApprovals returns approvals with the given status ("" = all), newest first.
func (s *Store) ListApprovals(ctx context.Context, status string) ([]protocol.Approval, error) {
	q := `SELECT id, thread_id, turn_id, item_id, tool, args_json, risk, reason, action_summary, status,
		decided_by, created_at, expires_at, decided_at, kind, work_id, node_id, node_revision FROM approvals`
	var args []any
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY created_at DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Approval
	for rows.Next() {
		var a protocol.Approval
		var args, created, expires string
		var decided sql.NullString
		if err := rows.Scan(&a.ID, &a.ThreadID, &a.TurnID, &a.ItemID, &a.Tool, &args, &a.Risk, &a.Reason,
			&a.ActionSummary, &a.Status, &a.DecidedBy, &created, &expires, &decided, &a.Kind, &a.WorkID, &a.NodeID, &a.NodeRevision); err != nil {
			return nil, err
		}
		a.Args = json.RawMessage(args)
		a.CreatedAt, a.ExpiresAt, a.DecidedAt = ParseTime(created), ParseTime(expires), nullTime(decided)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ExpirePendingApprovals marks all pending approvals expired (used at startup:
// the turns that were waiting on them no longer exist).
func (s *Store) ExpirePendingApprovals(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE approvals SET status = 'expired', decided_at = ? WHERE status = 'pending'`, Now())
	return err
}

// ---- audit ----

// Audit appends an event to the shared audit_log table.
func (s *Store) Audit(ctx context.Context, event string, details any) error {
	b, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO audit_log (event_type, details_json, created_at) VALUES (?,?,?)`,
		event, string(b), Now())
	return err
}

// ---- settings and model prefs ----

// GetSetting decodes a JSON setting into v. It returns ErrNotFound if unset.
func (s *Store) GetSetting(ctx context.Context, key string, v any) error {
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), v)
}

// SetSetting stores v as JSON under key.
func (s *Store) SetSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO settings (key, value_json, updated_at) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		key, string(b), Now())
	return err
}

// ModelPref is a user preference row for a model.
type ModelPref struct {
	Provider, Model    string
	Hidden             bool
	PriceOverride      bool
	InputPerMTok       float64
	CachedInputPerMTok float64
	OutputPerMTok      float64
}

// ListModelPrefs returns all model preference rows keyed by "provider/model".
func (s *Store) ListModelPrefs(ctx context.Context) (map[string]ModelPref, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT provider, model, hidden, price_override, input_per_mtok,
		cached_input_per_mtok, output_per_mtok FROM model_prefs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ModelPref{}
	for rows.Next() {
		var p ModelPref
		var hidden, over int
		if err := rows.Scan(&p.Provider, &p.Model, &hidden, &over, &p.InputPerMTok, &p.CachedInputPerMTok, &p.OutputPerMTok); err != nil {
			return nil, err
		}
		p.Hidden, p.PriceOverride = hidden != 0, over != 0
		out[p.Provider+"/"+p.Model] = p
	}
	return out, rows.Err()
}

// SetModelHidden hides or shows a model in the picker.
func (s *Store) SetModelHidden(ctx context.Context, provider, model string, hidden bool) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO model_prefs (provider, model, hidden) VALUES (?,?,?)
		ON CONFLICT(provider, model) DO UPDATE SET hidden = excluded.hidden`, provider, model, b2i(hidden))
	return err
}

// SetModelPrice stores a user price override (USD per million tokens).
func (s *Store) SetModelPrice(ctx context.Context, p protocol.ModelSetPriceParams) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO model_prefs (provider, model, price_override, input_per_mtok,
		cached_input_per_mtok, output_per_mtok) VALUES (?,?,1,?,?,?)
		ON CONFLICT(provider, model) DO UPDATE SET price_override = 1, input_per_mtok = excluded.input_per_mtok,
		cached_input_per_mtok = excluded.cached_input_per_mtok, output_per_mtok = excluded.output_per_mtok`,
		p.Provider, p.Model, p.InputPerMTok, p.CachedInputPerMTok, p.OutputPerMTok)
	return err
}

// ReplaceModelCache stores the model ids fetched from a provider.
func (s *Store) ReplaceModelCache(ctx context.Context, provider string, models []string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM model_cache WHERE provider = ?`, provider); err != nil {
		return err
	}
	now := Now()
	for _, m := range models {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO model_cache (provider, model, fetched_at) VALUES (?,?,?)`,
			provider, m, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListModelCache returns cached model ids per provider.
func (s *Store) ListModelCache(ctx context.Context) (map[string][]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT provider, model FROM model_cache ORDER BY provider, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var p, m string
		if err := rows.Scan(&p, &m); err != nil {
			return nil, err
		}
		out[p] = append(out[p], m)
	}
	return out, rows.Err()
}
