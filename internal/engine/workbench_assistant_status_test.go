package engine

import (
	"testing"

	"github.com/shaktsin/umcode/internal/workbench"
)

func TestSameWorkbenchGraphIgnoresFormattingAndKeyOrder(t *testing.T) {
	saved := []byte("{\n  \"version\": 1,\n  \"nodes\": [{\"id\": \"input\", \"config\": {\"vocab\": 259}}],\n  \"edges\": []\n}\n")
	draft := []byte(`{"edges":[],"nodes":[{"config":{"vocab":259},"id":"input"}],"version":1}`)
	if !sameWorkbenchGraph(saved, draft) {
		t.Fatal("semantic match reported unsaved")
	}
	if sameWorkbenchGraph(saved, []byte(`{"version":1,"nodes":[{"id":"input","config":{"vocab":32000}}],"edges":[]}`)) {
		t.Fatal("real edit reported saved")
	}
}

func TestWorkbenchDatasetStatusShowsAssistantProvenance(t *testing.T) {
	plain := workbenchDatasetStatus(workbench.Dataset{ID: "a", Name: "reviews.csv", RowCount: 3})
	if plain["createdBy"] != nil {
		t.Fatalf("plain dataset createdBy = %v, want absent", plain["createdBy"])
	}
	made := workbenchDatasetStatus(workbench.Dataset{ID: "b", Name: "balanced", RowCount: 2, Provenance: &workbench.DataProvenance{CreatedBy: "assistant"}})
	if made["createdBy"] != "assistant" {
		t.Fatalf("assistant dataset createdBy = %v", made["createdBy"])
	}
}
