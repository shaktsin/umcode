package agentrt

import (
	"errors"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func admitBase() AdmissionInput {
	return AdmissionInput{
		Plan: protocol.AgentPlan{
			State:  protocol.PlanApproved,
			Limits: protocol.DefaultPlanLimits(),
			Agents: []protocol.PlanAgent{{
				Name:  "coder",
				Model: "qwen-coder",
				Capabilities: protocol.AgentCapabilities{
					Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite, Network: true, SpawnMax: 2,
				},
			}},
		},
		Caller:          protocol.RuntimeAgent{ID: "", Capabilities: RootCapabilities()},
		ParentShareFree: 1.0,
		Options:         ResolveOptions{ShellReadOnlySupported: true},
		Request:         SpawnRequest{PlanEntry: "coder"},
	}
}

func TestAdmitRefusals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(in *AdmissionInput)
		field  string
		msg    string
	}{
		{"plan not approved", func(in *AdmissionInput) { in.Plan.State = protocol.PlanProposed }, "plan", "approved"},
		{"unknown entry", func(in *AdmissionInput) { in.Request.PlanEntry = "ghost" }, "planEntry", "ghost"},
		{"spawn limit", func(in *AdmissionInput) {
			in.Caller = protocol.RuntimeAgent{ID: "a1", Capabilities: protocol.AgentCapabilities{SpawnMax: 1}}
			in.All = []protocol.RuntimeAgent{{ID: "a2", ParentAgentID: "a1"}}
		}, "spawn", ""},
		{"depth", func(in *AdmissionInput) {
			in.Caller.Depth = 3
			in.Plan.Limits.MaxDepth = 3
		}, "depth", ""},
		{"total", func(in *AdmissionInput) { in.All = make([]protocol.RuntimeAgent, 40) }, "total", ""},
		{"model", func(in *AdmissionInput) { in.Request.Model = "opus" }, "model", "revised plan"},
		{"share", func(in *AdmissionInput) {
			in.Request.BudgetShare = 0.5
			in.ParentShareFree = 0.25
		}, "budget.share", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := admitBase()
			tc.mutate(&in)
			_, err := Admit(in)
			var ae *AdmissionError
			if !errors.As(err, &ae) {
				t.Fatalf("want *AdmissionError, got %v", err)
			}
			if ae.Field != tc.field {
				t.Fatalf("Field = %q, want %q (%s)", ae.Field, tc.field, ae.Message)
			}
			if !strings.Contains(ae.Message, tc.msg) {
				t.Fatalf("Message %q lacks %q", ae.Message, tc.msg)
			}
			if ae.Error() == "" {
				t.Fatal("empty Error()")
			}
		})
	}
}

func TestAdmitFirstFailureWins(t *testing.T) {
	in := admitBase()
	in.Plan.State = protocol.PlanProposed
	in.Request.PlanEntry = "ghost"
	_, err := Admit(in)
	var ae *AdmissionError
	if !errors.As(err, &ae) || ae.Field != "plan" {
		t.Fatalf("got %v", err)
	}
}

func TestAdmitDefaultsFromPlanEntry(t *testing.T) {
	in := admitBase()
	in.Plan.Limits.Network = true
	in.Caller.Depth = 1
	adm, err := Admit(in)
	if err != nil {
		t.Fatal(err)
	}
	entry := in.Plan.Agents[0]
	if want := Intersect(in.Caller.Capabilities, entry.Capabilities); adm.Granted != want {
		t.Fatalf("Granted = %+v, want %+v", adm.Granted, want)
	}
	if adm.Model != "qwen-coder" || adm.Share != 0.25 || adm.Depth != 2 || adm.Entry.Name != "coder" {
		t.Fatalf("got %+v", adm)
	}
	if len(adm.Rejected) != 0 {
		t.Fatalf("rejected: %+v", adm.Rejected)
	}
}

func TestAdmitPartialGrantStillAdmits(t *testing.T) {
	in := admitBase()
	req := entryCaps(in)
	req.Network = true
	in.Request.Capabilities = &req
	adm, err := Admit(in)
	if err != nil {
		t.Fatal(err)
	}
	if adm.Granted.Network {
		t.Fatal("network granted despite Limits.Network=false")
	}
	if n := countField(adm.Rejected, "capabilities.network"); n != 1 {
		t.Fatalf("network rejections = %d: %+v", n, adm.Rejected)
	}
}

func TestAdmitNetworkForcedOffWhenNotRequested(t *testing.T) {
	in := admitBase() // parent, entry true; request nil -> entry caps (network true)
	adm, err := Admit(in)
	if err != nil {
		t.Fatal(err)
	}
	if adm.Granted.Network || countField(adm.Rejected, "capabilities.network") != 1 {
		t.Fatalf("got %+v", adm)
	}
	// parent has no network, nothing requests it: no rejection.
	in = admitBase()
	in.Caller.Capabilities.Network = false
	req := entryCaps(in)
	req.Network = false
	in.Request.Capabilities = &req
	adm, err = Admit(in)
	if err != nil || len(adm.Rejected) != 0 {
		t.Fatalf("err=%v rej=%+v", err, adm.Rejected)
	}
}

func TestAdmitSpawnCountsOnlyCallersChildren(t *testing.T) {
	in := admitBase()
	in.Caller = protocol.RuntimeAgent{ID: "a1", Capabilities: protocol.AgentCapabilities{
		Files: protocol.FilesRead, Shell: protocol.ShellNone, SpawnMax: 1}}
	in.All = []protocol.RuntimeAgent{{ID: "x", ParentAgentID: "other"}}
	if _, err := Admit(in); err != nil {
		t.Fatal(err)
	}
}

func entryCaps(in AdmissionInput) protocol.AgentCapabilities { return in.Plan.Agents[0].Capabilities }

func countField(r []protocol.CapabilityRejection, f string) int {
	n := 0
	for _, x := range r {
		if x.Field == f {
			n++
		}
	}
	return n
}
