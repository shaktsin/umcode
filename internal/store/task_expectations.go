package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/protocol"
)

func strictExpectationJSON(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("one JSON object required")
	}
	return nil
}
func validateExpectationProposal(p protocol.TaskExpectationProposal, user protocol.Item) error {
	if p.Source.ItemID != user.ID || p.Source.Start < 0 || p.Source.End <= p.Source.Start || p.Source.End > utf8.RuneCountInString(user.Text) {
		return errors.New("invalid expectation user source span")
	}
	var r protocol.TaskResource
	if err := strictExpectationJSON(p.Resource, &r); err != nil {
		return err
	}
	switch p.Kind {
	case "explanation":
		if p.Predicate != "answer" || r != (protocol.TaskResource{}) || p.AllowExisting {
			return errors.New("explanation cannot claim an effect")
		}
	case "inspection":
		if p.Predicate != "exists" && p.Predicate != "read" && p.Predicate != "list" {
			return errors.New("invalid inspection predicate")
		}
		if r.Type != "file" || r.Path == "" || r.Tool != "" || r.Target != "" || p.AllowExisting {
			return errors.New("file inspection resource required")
		}
	case "mutation":
		if p.Predicate == "file_content" {
			if r.Type != "file" || r.Path == "" || r.Tool != "" || r.Target != "" {
				return errors.New("file mutation resource required")
			}
		} else if p.Predicate == "operation_verified" {
			if r.Type != "operation" || r.Tool == "" || r.Target == "" || r.Path != "" || r.ExpectedSHA256 != "" || p.AllowExisting {
				return errors.New("operation resource required")
			}
		} else {
			return errors.New("invalid mutation predicate")
		}
	default:
		return errors.New("unknown expectation kind")
	}
	return nil
}
func readTaskExpectations(ctx context.Context, tx *sql.Tx, taskID string) ([]protocol.TaskExpectation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT data_json FROM task_expectations WHERE task_id=? ORDER BY rowid`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.TaskExpectation
	for rows.Next() {
		var raw []byte
		var ex protocol.TaskExpectation
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &ex); err != nil {
			return nil, err
		}
		out = append(out, ex)
	}
	return out, rows.Err()
}
func (s *Store) SetTaskExpectations(ctx context.Context, taskID string, expectedRevision int, originItemID, requestTurnID string, proposals []protocol.TaskExpectationProposal) (protocol.TaskSnapshot, error) {
	var empty protocol.TaskSnapshot
	raw, err := json.Marshal(proposals)
	if err != nil || len(raw) > 16384 || len(proposals) == 0 || len(proposals) > 16 {
		return empty, errors.New("bounded nonempty expectation batch required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	task, err := readExecutionTask(ctx, tx, taskID)
	if err != nil {
		return empty, err
	}
	var current int
	if err = tx.QueryRowContext(ctx, `SELECT current FROM engine_tasks WHERE id=?`, taskID).Scan(&current); err != nil {
		return empty, err
	}
	if task.Revision != expectedRevision || current != 1 || task.Status == "superseded" {
		return empty, ErrTaskConflict
	}
	var user protocol.Item
	var userRaw string
	if err = tx.QueryRowContext(ctx, `SELECT id,thread_id,kind,text FROM items WHERE id=?`, originItemID).Scan(&user.ID, &user.ThreadID, &user.Kind, &userRaw); err != nil {
		return empty, err
	}
	user.Text = userRaw
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT thread_id FROM turns WHERE id=?`, requestTurnID).Scan(&owner); err != nil {
		return empty, err
	}
	if user.ThreadID != task.ThreadID || user.Kind != protocol.ItemUserMessage || owner != task.ThreadID {
		return empty, errors.New("expectation origin/turn belongs to another thread")
	}
	for _, p := range proposals {
		if err = validateExpectationProposal(p, user); err != nil {
			return empty, err
		}
	}
	existing, err := readTaskExpectations(ctx, tx, taskID)
	if err != nil {
		return empty, err
	}
	var prior []protocol.TaskExpectationProposal
	for _, ex := range existing {
		if ex.OriginItemID == originItemID && ex.RequestTurnID == requestTurnID {
			prior = append(prior, protocol.TaskExpectationProposal{Source: ex.Source, Kind: ex.Kind, Predicate: ex.Predicate, Resource: ex.Resource, AllowExisting: ex.AllowExisting})
		}
	}
	if len(prior) > 0 {
		if !reflect.DeepEqual(prior, proposals) {
			return empty, errors.New("existing expectation batch cannot be changed")
		}
		if err = tx.Commit(); err != nil {
			return empty, err
		}
		return s.ActiveExecutionTask(ctx, task.ThreadID)
	}
	for _, p := range proposals {
		ex := protocol.TaskExpectation{ID: NewID("exp"), TaskID: taskID, OriginItemID: originItemID, RequestTurnID: requestTurnID, Revision: 1, Source: p.Source, Kind: p.Kind, Predicate: p.Predicate, Resource: p.Resource, AllowExisting: p.AllowExisting, Status: "pending"}
		b, er := taskJSON(ex)
		if er != nil {
			return empty, er
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_expectations(id,task_id,origin_item_id,request_turn_id,revision,status,data_json) VALUES(?,?,?,?,?,?,?)`, ex.ID, taskID, originItemID, requestTurnID, 1, "pending", b); err != nil {
			return empty, err
		}
	}
	task.ExpectationsVersion = 1
	task.Revision++
	task.UpdatedAt = time.Now().UTC()
	b, err := taskJSON(task)
	if err != nil {
		return empty, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE engine_tasks SET revision=?,data_json=? WHERE id=? AND revision=? AND current=1`, task.Revision, b, taskID, expectedRevision)
	if err != nil {
		return empty, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return empty, ErrTaskConflict
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return s.ActiveExecutionTask(ctx, task.ThreadID)
}
func (s *Store) ResolveTaskExpectation(ctx context.Context, id string, expectedRevision int, evidenceIDs []string) (protocol.TaskExpectation, error) {
	var ex protocol.TaskExpectation
	if len(evidenceIDs) == 0 || len(evidenceIDs) > 64 {
		return ex, errors.New("bounded actual evidence required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ex, err
	}
	defer tx.Rollback()
	var raw []byte
	if err = tx.QueryRowContext(ctx, `SELECT data_json FROM task_expectations WHERE id=?`, id).Scan(&raw); err != nil {
		return ex, err
	}
	if err = json.Unmarshal(raw, &ex); err != nil {
		return ex, err
	}
	if ex.Revision != expectedRevision || ex.Status != "pending" {
		return ex, ErrTaskConflict
	}
	task, err := readExecutionTask(ctx, tx, ex.TaskID)
	if err != nil {
		return ex, err
	}
	for _, eid := range evidenceIDs {
		var owner, kind string
		if err = tx.QueryRowContext(ctx, `SELECT thread_id,kind FROM items WHERE id=?`, eid).Scan(&owner, &kind); err != nil {
			return ex, err
		}
		if owner != task.ThreadID || kind != protocol.ItemToolCall {
			return ex, errors.New("actual same-thread tool evidence required")
		}
	}
	ex.Status = "satisfied"
	ex.Revision++
	ex.EvidenceIDs = evidenceIDs
	b, err := taskJSON(ex)
	if err != nil {
		return ex, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE task_expectations SET status=?,revision=?,data_json=? WHERE id=? AND revision=?`, ex.Status, ex.Revision, b, id, expectedRevision)
	if err != nil {
		return ex, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ex, ErrTaskConflict
	}
	return ex, tx.Commit()
}
func (s *Store) ReserveTaskCorrection(ctx context.Context, turnID string) (int, bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `INSERT INTO task_corrections(turn_id,count) VALUES(?,1) ON CONFLICT(turn_id) DO UPDATE SET count=count+1 WHERE count<2 RETURNING count`, turnID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 2, false, nil
	}
	return n, err == nil, err
}
