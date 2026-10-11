package engine

import (
	"github.com/shaktsin/umcode/internal/evaluation"
	"github.com/shaktsin/umcode/internal/optimization"
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluationBundleMatrix(t *testing.T) {
	f, err := os.Open("../evaluation/testdata/deterministic.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := evaluation.DecodeManifest(f)
	if err != nil {
		t.Fatal(err)
	}
	m.BaseDir, err = filepath.Abs("../evaluation/testdata")
	if err != nil {
		t.Fatal(err)
	}
	policies := map[string]optimization.Policy{
		"compiler": {ContextCompiler: true}, "retrieval": {ContextCompiler: true, ContextRetrieval: true}, "reducers": {ToolResultReducers: true}, "progressive": {ProgressiveTools: true}, "workflow": {DesignedWorkflow: true, AutomaticWorkflow: true}, "memory": {DesignedWorkflow: true, AutomaticWorkflow: true, AutoPromote: true},
	}
	full := optimization.All(true)
	for _, name := range []string{"compiler", "retrieval", "reducers", "progressive", "workflow", "memory"} {
		p := full
		switch name {
		case "compiler":
			p.ContextCompiler = false
			p.ContextRetrieval = false
		case "retrieval":
			p.ContextRetrieval = false
		case "reducers":
			p.ToolResultReducers = false
		case "progressive":
			p.ProgressiveTools = false
		case "workflow":
			p.DesignedWorkflow = false
			p.AutomaticWorkflow = false
			p.AutoPromote = false
		case "memory":
			p.AutoPromote = false
		}
		policies["minus-"+name] = p
	}
	seen := map[optimization.Policy]bool{optimization.All(false): true, full: true}
	for name, p := range policies {
		if seen[p] {
			continue
		}
		seen[p] = true
		copy := p
		m.Arms = append(m.Arms, evaluation.Arm{ID: name, Mode: "override", Policy: &copy, Attribution: name})
		m.Comparisons = append(m.Comparisons, evaluation.Comparison{Baseline: "off", Treatment: name})
	}
	c, err := evaluation.RunCampaign(t.Context(), m, NewEvaluationRunner(), evaluation.CampaignOptions{Out: filepath.Join(t.TempDir(), "matrix")})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Results) != len(m.Arms) {
		t.Fatalf("missing arms %d/%d", len(c.Results), len(m.Arms))
	}
	for _, r := range c.Results {
		if r.Status != "completed" || len(r.Checks) != 1 || r.Checks[0].Status != "passed" {
			t.Fatalf("%s: %+v", r.Unit.ArmID, r)
		}
		arm, _ := m.Arm(r.Unit.ArmID)
		want, _ := evaluation.EffectivePolicy(arm)
		if len(r.Policies) != 1 || r.Policies[0] != want || len(r.Attempts) != 2 {
			t.Fatalf("policy/accounting %s %+v", arm.ID, r)
		}
	}
}

// These diagnostics exercise real existing engine lifecycle and action boundaries;
// they are excluded from steady-policy release campaign denominators.
func TestEvaluationAutomaticWorkflow(t *testing.T) {
	t.Run("automatic-no-wait", TestAutomaticWorkflowNoApprovalWait)
	t.Run("legacy-recovery", TestAutomaticWorkflowPendingGateRecovery)
	t.Run("action-permission", TestAutomaticWorkflowActionPermission)
	t.Run("agent-provenance", TestAutomaticDecisionCannotBecomeUserAuthority)
	t.Run("memory-off", TestOptimizationOffStopsMemoryUse)
}
func TestEvaluationToggleRevocation(t *testing.T) {
	t.Run("admission-snapshot", TestOptimizationTurnSnapshot)
	t.Run("revocation-during-approval", TestProgressiveRevocationDuringApproval)
	t.Run("mcp-revocation", TestProgressiveMCPRevocationDuringApproval)
}
func TestEvaluationComponentDiagnostics(t *testing.T) {
	t.Run("long-context", TestRetrievalRecoversEarlierContextWithoutQualityLoss)
	t.Run("restart", TestRetrievalRestartAndEdit)
	t.Run("repeated-memory", TestCuratedMemoryReducesRepeatedDiscoveryWithoutQualityLoss)
	t.Run("specialist-discovery", TestProgressiveSpecialistDirectAndDiscovery)
	t.Run("fallback", TestProgressiveFallbackAndPairing)
}
