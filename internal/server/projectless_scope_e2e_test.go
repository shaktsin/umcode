package server_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

func TestProjectlessToolsPreserveWorkspaceAndNoProjectBoundaries(t *testing.T) {
	// A thread-only workflow scope must never become a filesystem capability.
	// Separate roots make accidental reads of the daemon cwd observable.
	daemonDir := t.TempDir()
	t.Chdir(daemonDir)
	if err := os.WriteFile(filepath.Join(daemonDir, "daemon-only.txt"), []byte("scope-marker daemon-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, tool, args, want string
		noProject              bool
	}{
		{"named_read", "file__read", `{"workspace":"w","path":"workspace-only.txt"}`, "scope-marker configured-workspace", false},
		{"named_list", "file__list", `{"workspace":"w","path":"."}`, "workspace-only.txt", false},
		{"named_search", "file__search", `{"workspace":"w","path":".","pattern":"scope-marker"}`, "workspace-only.txt:1: scope-marker configured-workspace", false},
		{"default_read", "file__read", `{"path":"workspace-only.txt"}`, "scope-marker configured-workspace", false},
		{"default_list", "file__list", `{}`, "workspace-only.txt", false},
		{"default_search", "file__search", `{"path":".","pattern":"scope-marker"}`, "workspace-only.txt:1: scope-marker configured-workspace", false},
		{"shell", "shell__run", `{"command":"echo scope-marker"}`, "", true},
		{"exec", "exec__start", `{"command":"echo scope-marker","yield_seconds":1}`, "", true},
		{"edit", "file__edit", `{"path":"workspace-only.txt","old_string":"scope-marker","new_string":"changed"}`, "", true},
		{"write", "file__write", `{"path":"created.txt","content":"changed"}`, "", true},
		{"visual", "visual__inspect", `{}`, "", true},
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled_%t", enabled), func(t *testing.T) {
			h := newHarness(t, func(c *config.Config) { c.Models.DesignedWorkflow = enabled })
			h.addKey("claude", "workflow", "sk-1")
			if err := os.WriteFile(filepath.Join(h.ws, "workspace-only.txt"), []byte("scope-marker configured-workspace\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, tc := range cases {
				var th protocol.Thread
				h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{Title: tc.name}, &th)
				if enabled {
					// Exercise both real workflow-aware calls first, then an ordinary
					// tool in the same turn: neither call may leak its local scope.
					h.fake.push(toolReply("verification__plan", `{}`), workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
						goal := workflowNode(t, d, protocol.NodeGoal, "")
						return protocol.WorkUpdateRequest{WorkflowDepth: protocol.DepthGuided,
							Nodes: []protocol.WorkNodeChange{{Ref: "requirement", Kind: protocol.NodeRequirement, Title: "Keep tool boundaries"}},
							Edges: []protocol.WorkEdgeChange{{From: goal.ID, Relation: "requires", To: "requirement"}}}
					}))
				}
				h.fake.push(toolReply(tc.tool, tc.args), textReply("Inspected."))
				var res protocol.TurnStartResult
				h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "inspect the available tools",
					Override: protocol.ModelSelection{Provider: "claude", Model: "claude-sonnet-5", Complexity: "standard"}}, &res)
				turn, _, items := h.waitTurn(res.Turn.ID, func(a protocol.Approval) bool {
					if a.Kind == "workflow" {
						t.Errorf("%s: ungated planning requested workflow approval", tc.name)
						return false
					}
					return true
				})
				wantItems := 1
				if enabled {
					wantItems = 3
				}
				if turn.Status != protocol.TurnCompleted || len(items) != wantItems {
					t.Fatalf("%s: turn=%+v items=%+v", tc.name, turn, items)
				}
				if enabled {
					for _, item := range items[:2] {
						if item.Status != protocol.ItemCompleted {
							t.Fatalf("%s: workflow tool lost thread identity: %+v", tc.name, item)
						}
					}
					d := workflowDetail(h, th.ID)
					workflowNode(t, d, protocol.NodeRequirement, "Keep tool boundaries")
					criterion := workflowNode(t, d, protocol.NodeCriterion, "")
					if d.Work.ProjectID != "" || criterion.Status != "pending" || len(d.Attempts) != 0 || len(d.Evidence) == 0 || d.Evidence[0].Kind != "discovery" {
						t.Fatalf("%s: projectless planning lost its provenance or implied verification: %+v", tc.name, d)
					}
				}
				item := items[len(items)-1]
				if tc.noProject {
					if item.Status != protocol.ItemFailed || item.Tool.Error != tools.ErrNoProject.Error() {
						t.Errorf("%s: want ErrNoProject, got status=%s error=%q output=%q", tc.name, item.Status, item.Tool.Error, item.Tool.Output)
					}
				} else if item.Status != protocol.ItemCompleted || !strings.Contains(item.Tool.Output, tc.want) || strings.Contains(item.Tool.Output, "daemon-only") || strings.Contains(item.Tool.Output, "daemon-secret") {
					t.Errorf("%s: must read configured workspace only: status=%s error=%q output=%q", tc.name, item.Status, item.Tool.Error, item.Tool.Output)
				}
			}
		})
	}
}
