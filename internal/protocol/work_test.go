package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWorkUpdateToolJSON(t *testing.T) {
	input := `{"work_id":"wrk_1","expected_revision":4,"workflow_depth":"designed","nodes":[{"ref":"task","kind":"task","title":"implement","content":{"required":true},"evidence_ids":["evd_1"]},{"id":"wnd_1","expected_revision":2,"from_status":"pending","to_status":"ready"}],"edges":[{"from":"task","relation":"implements","to":"wnd_1"}],"rationale":"phase change"}`
	var req WorkUpdateRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatal(err)
	}
	if req.WorkID != "wrk_1" || req.ExpectedRevision != 4 || req.WorkflowDepth != "designed" || len(req.Nodes) != 2 || len(req.Edges) != 1 || req.Rationale != "phase change" {
		t.Fatalf("request = %+v", req)
	}
	if n := req.Nodes[0]; n.Ref != "task" || n.Kind != "task" || n.Title != "implement" || string(n.Content) != `{"required":true}` || !reflect.DeepEqual(n.EvidenceIDs, []string{"evd_1"}) {
		t.Fatalf("create = %+v", n)
	}
	if n := req.Nodes[1]; n.ID != "wnd_1" || n.ExpectedRevision != 2 || n.FromStatus != "pending" || n.ToStatus != "ready" {
		t.Fatalf("transition = %+v", n)
	}
	if e := req.Edges[0]; e.From != "task" || e.Relation != "implements" || e.To != "wnd_1" {
		t.Fatalf("edge = %+v", e)
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(input), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request JSON = %s", encoded)
	}
	result, err := json.Marshal(WorkUpdateResult{Revision: 5, Created: 1, Transitioned: 1, Linked: 2})
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `{"revision":5,"created":1,"transitioned":1,"linked":2}` {
		t.Fatalf("result JSON = %s", result)
	}
}

func TestWorkflowGateMetadataIsInternal(t *testing.T) {
	gate := WorkflowGate{WorkID: "wrk_1", NodeID: "wnd_1", NodeRevision: 2, Kind: "schema", Reason: "reason", Summary: "summary"}
	for _, value := range []any{gate, PreparedWorkUpdate{WorkID: "wrk_1", ExpectedRevision: 1, Gates: []WorkflowGate{gate}}} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != `{}` {
			t.Fatalf("internal metadata leaked to JSON: %s", encoded)
		}
	}
}
