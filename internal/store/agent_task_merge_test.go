package store

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestTaskMergeJournalRecordsIntentAndReceipt(t *testing.T) {
	s, task := runtimeFixture(t)
	plan := json.RawMessage(`{"paths":["a.txt"],"before":{"a.txt":"old"},"after":{"a.txt":"new"}}`)
	if err := s.BeginTaskMerge(t.Context(), TaskMergeRecord{TaskID: task.ID, SourceRoot: "/source", HeadTree: "tree", Plan: plan}); err != nil {
		t.Fatal(err)
	}
	got, err := s.TaskMerge(t.Context(), task.ID)
	if err != nil || got.State != "intent" || string(got.Plan) != string(plan) {
		t.Fatalf("intent %+v %v", got, err)
	}
	if err := s.BeginTaskMerge(t.Context(), TaskMergeRecord{TaskID: task.ID, SourceRoot: "/other", HeadTree: "tree", Plan: plan}); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("changed source: %v", err)
	}
	receipt := json.RawMessage(`{"headTree":"tree","paths":["a.txt"]}`)
	if err := s.RecordTaskMergeReceipt(t.Context(), task.ID, receipt); err != nil {
		t.Fatal(err)
	}
	got, err = s.TaskMerge(t.Context(), task.ID)
	if err != nil || got.State != "merged" || string(got.Receipt) != string(receipt) {
		t.Fatalf("receipt %+v %v", got, err)
	}
	if err := s.RecordTaskMergeReceipt(t.Context(), task.ID, receipt); err != nil {
		t.Fatalf("idempotent receipt: %v", err)
	}
	if err := s.BeginTaskMerge(t.Context(), TaskMergeRecord{TaskID: task.ID, SourceRoot: "/source", HeadTree: "tree", Plan: plan}); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("merged intent replay: %v", err)
	}
}

func TestTaskMergeJournalConflictCanRetryButAmbiguousCannot(t *testing.T) {
	s, task := runtimeFixture(t)
	start := TaskMergeRecord{TaskID: task.ID, SourceRoot: "/source", HeadTree: "tree", Plan: json.RawMessage(`{"paths":[]}`)}
	if err := s.BeginTaskMerge(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTaskMerge(t.Context(), task.ID, "conflict", "target changed"); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginTaskMerge(t.Context(), start); err != nil {
		t.Fatalf("retry conflict: %v", err)
	}
	if err := s.MarkTaskMerge(t.Context(), task.ID, "ambiguous", "crash during apply"); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginTaskMerge(t.Context(), start); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("ambiguous retry: %v", err)
	}
	list, err := s.PendingTaskMerges(t.Context())
	if err != nil || len(list) != 1 || list[0].State != "ambiguous" {
		t.Fatalf("pending %+v %v", list, err)
	}
}
