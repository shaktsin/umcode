package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/work"
)

type workUpdate struct{ updater WorkUpdater }

// NewWorkUpdate adapts a thread-scoped graph updater to the tool boundary.
func NewWorkUpdate(updater WorkUpdater) WorkflowUpdateTool { return &workUpdate{updater: updater} }

func (*workUpdate) Name() string { return "work.update" }
func (*workUpdate) Description() string {
	return "Record a bounded semantic graph batch for the current work: create nodes with local refs, transition existing nodes with revision/status predicates, and link nodes or existing evidence IDs. Returns only revision and counts. Readiness and approvals are derived by the engine."
}
func (*workUpdate) Schema() json.RawMessage {
	// JSON Schema lengths count characters; the updater enforces the stricter
	// UTF-8 byte bounds, including serialized content and the entire request.
	return schema(fmt.Sprintf(`{
		"type":"object","additionalProperties":false,
		"description":"Semantic batch; maximum %d serialized UTF-8 bytes. String limits are UTF-8 byte limits enforced by the service.",
		"required":["work_id","expected_revision"],
		"properties":{
			"work_id":{"type":"string","minLength":1,"maxLength":128},
			"expected_revision":{"type":"integer","minimum":1},
			"workflow_depth":{"type":"string","enum":["guided","designed"]},
			"rationale":{"type":"string","maxLength":%d},
			"nodes":{"type":"array","maxItems":%d,"items":{
				"type":"object","additionalProperties":false,
				"description":"Create with ref/kind/title/content, or transition with id/expected_revision/from_status/to_status. Evidence IDs link existing observations.",
				"properties":{
					"ref":{"type":"string","minLength":1,"maxLength":%d},
					"id":{"type":"string","minLength":1,"maxLength":128},
					"superseded_by":{"type":"string","minLength":1,"maxLength":128,"description":"Explicit replacement task ID or batch-local ref when retiring a blocked/failed task. The replacement must preserve criteria, requirements and dependencies and have an approved solution."},
					"kind":{"type":"string","enum":["requirement","non_goal","option","decision","task","unknown","memory_candidate"]},
					"title":{"type":"string","minLength":1,"maxLength":%d},
					"content":{"type":"object","description":"Extensible semantic content; maximum %d serialized UTF-8 bytes. Known fields: solution_rung (option), required (criterion/requirement/decision/task), blocking (unknown), gate_kind (decision). Memory candidates use category, semantic_key, text, optional scope_paths, source_revision, and optional replaces_memory. Text is one durable project-specific bullet; bounds are UTF-8 bytes.","properties":{
						"category":{"type":"string","enum":["capability","command","boundary","invariant","convention","path","approved_decision"]},
						"semantic_key":{"type":"string","minLength":1,"maxLength":128},
						"text":{"type":"string","minLength":1,"maxLength":512},
						"scope_paths":{"type":"array","maxItems":16,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":512}},
						"source_revision":{"type":"string","minLength":1},
						"replaces_memory":{"type":"string","minLength":1,"maxLength":128}
					}},
					"expected_revision":{"type":"integer","minimum":1},
					"from_status":{"type":"string","enum":["active","proposed","approved","pending","ready","in_progress","completed","failed","blocked","open","resolved","accepted_risk","conflicted","stale","rejected","superseded"]},
					"to_status":{"type":"string","enum":["active","proposed","approved","pending","ready","in_progress","completed","failed","blocked","open","resolved","accepted_risk","conflicted","stale","rejected","superseded"]},
					"evidence_ids":{"type":"array","maxItems":%d,"items":{"type":"string","minLength":1,"maxLength":128}}
				}
			}},
			"edges":{"type":"array","maxItems":%d,"items":{
				"type":"object","additionalProperties":false,"required":["from","relation","to"],
				"properties":{
					"from":{"type":"string","minLength":1,"maxLength":128},
					"relation":{"type":"string","enum":["requires","serves","depends_on","supports","contradicts","selects","implements","verifies","candidate_for"]},
					"to":{"type":"string","minLength":1,"maxLength":128}
				}
			}}
		}
	}`, work.MaxWorkUpdateBytes, work.MaxRationaleBytes, work.MaxNodeChanges, work.MaxClientRefBytes, work.MaxNodeTitleBytes, work.MaxNodeContentBytes, work.MaxNodeEvidenceIDs, work.MaxEdgeChanges))
}

func (*workUpdate) Assess(json.RawMessage) (Risk, string) {
	return RiskGreen, "Update the internal work graph"
}

func (t *workUpdate) Apply(ctx context.Context, args json.RawMessage) (protocol.WorkUpdateResult, []protocol.WorkflowGate, error) {
	var zero protocol.WorkUpdateResult
	scope := ScopeFrom(ctx)
	if scope == nil || strings.TrimSpace(scope.ThreadID) == "" {
		return zero, nil, fmt.Errorf("work.update requires a scoped thread")
	}
	if len(args) > work.MaxWorkUpdateBytes {
		return zero, nil, fmt.Errorf("work.update rejected: request: limit; reduce the batch")
	}
	var req protocol.WorkUpdateRequest
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return zero, nil, fmt.Errorf("work.update rejected: invalid arguments; correct the request")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return zero, nil, fmt.Errorf("work.update rejected: invalid arguments; supply one JSON object")
	}
	result, gates, err := t.updater.Update(ctx, scope.ThreadID, req)
	if err != nil {
		return zero, nil, fmt.Errorf("work.update rejected: %w; correct the batch or refresh its revision", err)
	}
	return result, gates, nil
}

func (t *workUpdate) Call(ctx context.Context, args json.RawMessage) (string, error) {
	result, _, err := t.Apply(ctx, args)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(result)
	return string(out), err
}
