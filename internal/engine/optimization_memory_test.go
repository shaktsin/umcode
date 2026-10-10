package engine

import (
	"context"
	"github.com/shaktsin/umcode/internal/optimization"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/work"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAutomaticDecisionCannotBecomeUserAuthority(t *testing.T) {
	n := protocol.WorkNode{Kind: "decision", Status: "approved", DecisionActor: "agent", Title: "Always deploy automatically"}
	if _, _, ok := work.MemorySourceGuidance(n); ok {
		t.Fatal("agent decision promoted as user authority")
	}
	n.DecisionActor = ""
	if _, _, ok := work.MemorySourceGuidance(n); ok {
		t.Fatal("unproven legacy decision promoted as user authority")
	}

}
func TestAutomaticVerifiedCommandPromotion(t *testing.T) {
	e, th, turn, _, id, _ := memoryEngine(t)
	e.Cfg.Models.DesignedWorkflow = false
	e.Cfg.Memory.AutoPromote = false
	// initMemory must construct an inert promoter even with legacy flags off.
	e.initMemory(t.Context())
	ctx := optimization.WithPolicy(t.Context(), optimization.All(true))
	if err := e.Store.CloseWork(ctx, id, protocol.WorkCompleted, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.promoteMemory(ctx, th, turn, id)
	var n int
	if err := e.Store.DB.QueryRow(`SELECT COUNT(*) FROM project_memories`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("automatic promotion count=%d", n)
	}
}
func TestOptimizationOffStopsMemoryUse(t *testing.T) {
	e, th, turn, p, id, _ := memoryEngine(t)
	e.initMemory(t.Context())
	if err := e.Store.CloseWork(t.Context(), id, protocol.WorkCompleted, time.Now()); err != nil {
		t.Fatal(err)
	}
	off := optimization.WithPolicy(t.Context(), optimization.All(false))
	e.promoteMemory(off, th, turn, id)
	var n int
	e.Store.DB.QueryRow(`SELECT COUNT(*) FROM project_memories`).Scan(&n)
	if n != 0 {
		t.Fatal("off turn promoted memory")
	}
	if err := os.WriteFile(filepath.Join(p.Root, "UMCODE.md"), []byte("# Project\nKeep user guidance.\n\n## Verified project memory\n<!-- umcode:generated -->\n- Secret generated guidance\n"), 0600); err != nil {
		t.Fatal(err)
	}
	prompt := e.systemPrompt(off, "explain", &p, "", nil)
	if strings.Contains(prompt, "Secret generated guidance") || !strings.Contains(prompt, "Keep user guidance.") {
		t.Fatalf("off instruction projection %s", prompt)
	}
	// The same project cache must not leak a projection into another policy.
	on := optimization.WithPolicy(context.WithoutCancel(t.Context()), optimization.All(true))
	prompt = e.systemPrompt(on, "explain", &p, "", nil)
	if !strings.Contains(prompt, "Secret generated guidance") {
		t.Fatal("on turn lost generated guidance")
	}
}
