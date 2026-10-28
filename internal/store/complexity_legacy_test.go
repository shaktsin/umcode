package store

import (
	"context"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestLegacyComplexityNormalizedOnRead(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	th, err := s.CreateThread(ctx, protocol.Thread{Title: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTurn(ctx, protocol.Turn{ID: "trn_legacy", ThreadID: th.ID, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	pr, err := s.CreateProject(ctx, protocol.Project{Name: "legacy", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTask(ctx, protocol.Task{Name: "legacy", Prompt: "p", Status: "active", NextRunAt: nil})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ legacy, want string }{{"auto", "medium"}, {"quick", "low"}, {"standard", "medium"}, {"deep", "high"}}
	for _, c := range cases {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"UPDATE threads SET complexity = ? WHERE id = ?", []any{c.legacy, th.ID}},
			{"UPDATE turns SET sel_complexity = ?, res_complexity = ? WHERE id = 'trn_legacy'", []any{c.legacy, c.legacy}},
			{"UPDATE projects SET complexity = ? WHERE id = ?", []any{c.legacy, pr.ID}},
			{"UPDATE tasks SET complexity = ? WHERE id = ?", []any{c.legacy, tk.ID}},
		} {
			if _, err := s.DB.ExecContext(ctx, q.sql, q.args...); err != nil {
				t.Fatal(err)
			}
		}
		want := protocol.Complexity(c.want)
		gt, _ := s.GetThread(ctx, th.ID)
		turns, _ := s.ListTurns(ctx, th.ID)
		gp, _ := s.GetProject(ctx, pr.ID)
		gk, _ := s.GetTask(ctx, tk.ID)
		if gt.Settings.Complexity != want || len(turns) != 1 || turns[0].Selection.Complexity != want || turns[0].Resolved.Complexity != want ||
			gp.Settings.Complexity != want || gk.Settings.Complexity != want {
			t.Fatalf("%s: thread=%q turns=%+v project=%q task=%q want %q", c.legacy, gt.Settings.Complexity, turns, gp.Settings.Complexity, gk.Settings.Complexity, want)
		}
	}
}
