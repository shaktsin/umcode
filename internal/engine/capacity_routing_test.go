package engine

import (
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
	"testing"
)

func TestDirectModelHasNoCrossProviderFallback(t *testing.T) {
	e, _, _, _ := taskRuntimeFixture(t, "summary_only", false)
	e.routing = protocol.RoutingConfig{Models: []protocol.ConfiguredModel{{ID: "q", Provider: "openai_compatible", Model: "qwen", Enabled: true}, {ID: "c", Provider: "claude", Model: "sonnet", Enabled: true}}}
	got := e.configuredFrom("openai_compatible", "qwen")
	if len(got) != 1 || got[0].Provider != "openai_compatible" || got[0].Model != "qwen" {
		t.Fatalf("direct model broadened: %+v", got)
	}
}
func TestPoolFallbackStaysWithinMembership(t *testing.T) {
	e, _, _, _ := taskRuntimeFixture(t, "summary_only", false)
	e.routing = protocol.RoutingConfig{Models: []protocol.ConfiguredModel{{ID: "one", Provider: "claude", Model: "first", Enabled: true}, {ID: "two", Provider: "claude", Model: "second", Enabled: false}, {ID: "other", Provider: "claude", Model: "third", Enabled: true}}, Pools: []protocol.ModelPool{{ID: "pool", Name: "Pool", Enabled: true, Models: []string{"one", "two"}}}}
	_, got, err := e.poolModels("pool")
	if err != nil || len(got) != 1 || got[0].ID != "one" {
		t.Fatal(got, err)
	}
}
func TestFallbackDoesNotChangePersistedSelection(t *testing.T) {
	e, th, _, _ := taskRuntimeFixture(t, "summary_only", false)
	original := protocol.ModelSelection{Provider: protocol.PoolProvider, Model: "coding"}
	th, err := e.SetThreadSettings(t.Context(), protocol.ThreadSetSettingsParams{ThreadID: th.ID, Settings: original})
	if err != nil {
		t.Fatal(err)
	}
	res := resolved{sel: original}
	res.sel = protocol.ModelSelection{Provider: "claude", Model: "fallback"}
	got, err := e.Store.GetThread(t.Context(), th.ID)
	if err != nil || got.Settings != original {
		t.Fatal(got.Settings, err)
	}
}
func TestDiscoveryWorksWithoutNativeSearch(t *testing.T) { TestProgressiveDiscoveryNextRequest(t) }
func TestDiscoveryFallbackStillFits(t *testing.T) {
	e, th, turn, _ := pluginHookEngine(t)
	p := installMemoryProvider(t, e)
	e.Cfg.Models.ProgressiveTools = true
	for i := 0; i < 100; i++ {
		e.Tools.Add(&engineTestTool{name: "special.action" + string(rune('a'+i)), risk: tools.RiskGreen, output: "done"})
	}
	e.selectionHook = func() { panic("selection failure") }
	p.script = func(req llm.Request) []llm.Event {
		if len(req.Tools) > 40 || !requestHasTool(req, "task.context") {
			t.Fatal("unbounded fallback or no original-source retrieval")
		}
		return memoryAnswer(req)
	}
	runMemoryTurn(t, e, th, turn)
}
func TestFallbackAfterMutationDoesNotReplay(t *testing.T) { TestProgressiveFallbackAndPairing(t) }
