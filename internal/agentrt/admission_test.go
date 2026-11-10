package agentrt

import (
	"errors"
	"fmt"
	"math"
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
		}, "spawn", "already has 1"},
		{"depth", func(in *AdmissionInput) {
			in.Caller.Depth = 3
			in.Plan.Limits.MaxDepth = 3
		}, "depth", "exceeds"},
		{"total", func(in *AdmissionInput) { in.All = make([]protocol.RuntimeAgent, 40) }, "total", "40"},
		{"model", func(in *AdmissionInput) { in.Request.Model = "opus" }, "model", "qwen-coder"},
		{"share", func(in *AdmissionInput) {
			in.Request.BudgetShare = 0.5
			in.ParentShareFree = 0.25
		}, "budget.share", "exceeds the free share"},
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
			if tc.field == "model" && !strings.Contains(ae.Message, "revised plan") {
				t.Fatalf("Message %q lacks revised plan", ae.Message)
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

func TestAdmitDefaultedNetworkForcedOffWithoutRejection(t *testing.T) {
	in := admitBase() // nil Capabilities; entry and parent allow network
	adm, err := Admit(in)
	if err != nil {
		t.Fatal(err)
	}
	if adm.Granted.Network || len(adm.Rejected) != 0 {
		t.Fatalf("got %+v", adm)
	}
}

func TestAdmitExplicitNetworkRequestRejectedOnce(t *testing.T) {
	in := admitBase()
	req := entryCaps(in)
	req.Network = true
	in.Request.Capabilities = &req
	adm, err := Admit(in)
	if err != nil || adm.Granted.Network || countField(adm.Rejected, "capabilities.network") != 1 {
		t.Fatalf("err=%v adm=%+v", err, adm)
	}
}

func TestAdmitInvalidShares(t *testing.T) {
	for _, s := range []float64{math.NaN(), -0.1, 1.5} {
		in := admitBase()
		in.Request.BudgetShare = s
		_, err := Admit(in)
		var ae *AdmissionError
		if !errors.As(err, &ae) || ae.Field != "budget.share" {
			t.Fatalf("share %v: got %v", s, err)
		}
		if !strings.Contains(ae.Message, fmt.Sprint(s)) {
			t.Fatalf("message %q does not name %v", ae.Message, s)
		}
	}
}

func TestAdmitShareEpsilonBoundary(t *testing.T) {
	cases := []struct {
		delta float64
		ok    bool
	}{{0, true}, {0.5e-9, true}, {2e-9, false}}
	for _, c := range cases {
		in := admitBase()
		in.ParentShareFree = 0.25
		in.Request.BudgetShare = 0.25 + c.delta
		_, err := Admit(in)
		if (err == nil) != c.ok {
			t.Fatalf("delta %v: err=%v want ok=%v", c.delta, err, c.ok)
		}
	}
}

func TestAdmitCallerCapsLimitGrant(t *testing.T) {
	in := admitBase()
	in.Plan.Limits.Network = true
	in.Caller.Capabilities.Shell = protocol.ShellNone
	adm, err := Admit(in)
	if err != nil {
		t.Fatal(err)
	}
	if adm.Granted.Shell != protocol.ShellNone {
		t.Fatalf("shell = %q", adm.Granted.Shell)
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

func TestAdmitNormalisesZeroPlanLimits(t *testing.T) {
	in := admitBase()
	in.Plan.Limits = protocol.PlanLimits{MaxConcurrent: 2}
	in.ParentShareFree = 0.1
	// SliceShare 0 must not make every default request "fit": the default
	// share (0.25) exceeds the free 0.1.
	if _, err := Admit(in); admissionFieldOf(err) != "budget.share" {
		t.Fatalf("zero sliceShare: want budget.share refusal, got %v", err)
	}
	in.ParentShareFree = 1
	adm, err := Admit(in)
	if err != nil {
		t.Fatalf("zero depth/total disabled admission: %v", err)
	}
	if adm.Share != protocol.DefaultPlanLimits().SliceShare {
		t.Fatalf("share = %v", adm.Share)
	}
	in.All = make([]protocol.RuntimeAgent, protocol.DefaultPlanLimits().MaxTotal)
	if _, err := Admit(in); admissionFieldOf(err) != "total" {
		t.Fatalf("zero maxTotal: want total refusal at default, got %v", err)
	}
	in.All = nil
	in.Caller.Depth = protocol.DefaultPlanLimits().MaxDepth
	if _, err := Admit(in); admissionFieldOf(err) != "depth" {
		t.Fatalf("zero maxDepth: want depth refusal at default, got %v", err)
	}
}

func admissionFieldOf(err error) string {
	var ae *AdmissionError
	if errors.As(err, &ae) {
		return ae.Field
	}
	return fmt.Sprintf("<%v>", err)
}
