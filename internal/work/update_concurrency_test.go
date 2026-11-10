package work

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// Forward every statement to the actual SQLite driver, inserting the real
// winning transaction precisely between the detail's work and node SELECTs.
// This catches stale requests being mislabeled as invalid duplicate nodes.
type updateInterleavingConnector struct {
	base        driver.Driver
	dsn         string
	beforeNodes func()
	once        sync.Once
}

func (c *updateInterleavingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &updateInterleavingConn{Conn: conn, owner: c}, nil
}
func (c *updateInterleavingConnector) Driver() driver.Driver { return c.base }

type updateInterleavingConn struct {
	driver.Conn
	owner *updateInterleavingConnector
}

func (c *updateInterleavingConn) Prepare(query string) (driver.Stmt, error) {
	if strings.Contains(query, "FROM work_nodes WHERE work_id") {
		c.owner.once.Do(c.owner.beforeNodes)
	}
	return c.Conn.Prepare(query)
}

func TestServiceUpdateConcurrentSnapshotWinner(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "work.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	th, err := st.CreateThread(ctx, protocol.Thread{Title: "concurrent update"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	svc := &Service{Store: st, DesignedWorkflow: true, Now: func() time.Time { return now }}
	if err = svc.Begin(ctx, th, "ship bounded work"); err != nil {
		t.Fatal(err)
	}
	w, ok, err := st.OpenWorkForThread(ctx, th.ID)
	if err != nil || !ok {
		t.Fatalf("open work: %+v %v", w, err)
	}
	detail, err := st.GetWorkDetail(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.WorkUpdateRequest{WorkID: w.ID, ExpectedRevision: w.Revision, WorkflowDepth: "guided", Nodes: []protocol.WorkNodeChange{{Ref: "r", Kind: "requirement", Title: "correct"}}}
	winner, err := PrepareUpdate(detail, req, now)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	connector := &updateInterleavingConnector{base: st.DB.Driver(), dsn: "file:" + st.Path + "?_pragma=foreign_keys(1)", beforeNodes: func() {
		if _, err := st.ApplyWorkUpdate(ctx, winner); err != nil {
			t.Fatal(err)
		}
		committed = true
	}}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)
	svc.Store = &store.Store{DB: db, Path: st.Path}
	_, _, err = svc.Update(ctx, th.ID, req)
	var validation *ValidationError
	if !committed {
		t.Fatal("winning transaction did not interleave")
	}
	if !errors.Is(err, store.ErrWorkUpdateConflict) && !(errors.As(err, &validation) && validation.Code == "stale") {
		t.Fatalf("concurrent winner must report stale conflict, got: %v", err)
	}
	after, err := st.GetWorkDetail(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Work.Revision != w.Revision+1 || len(kinds(after, "requirement")) != 1 {
		t.Fatalf("loser changed winner graph: %+v", after)
	}
}
