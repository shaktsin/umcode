package store

import (
	"github.com/shaktsin/umcode/internal/protocol"
	"strings"
	"testing"
)

func TestEndpointCapacityProfilesAreIsolated(t *testing.T) {
	st, _ := workFixture(t)
	for i, window := range []int{8192, 32768} {
		id := strings.Repeat(string(rune('a'+i)), 64)
		p := protocol.ModelCapacityProfile{Provider: "openai_compatible", EndpointID: id, Model: "qwen3-coder", ContextWindow: &window, Backend: "generic"}
		if err := st.SetModelCapacityProfile(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []int{8192, 32768} {
		p, err := st.GetModelCapacityProfile(t.Context(), "openai_compatible", strings.Repeat(string(rune('a'+i)), 64), "qwen3-coder")
		if err != nil || p.ContextWindow == nil || *p.ContextWindow != want {
			t.Fatalf("endpoint capacity conflated: %+v %v", p, err)
		}
	}
}
func TestCapacityProfileRoundTrip(t *testing.T) {
	st, _ := workFixture(t)
	window, output, reserve := 32768, 8192, 4096
	p := protocol.ModelCapacityProfile{Provider: "openai_compatible", EndpointID: strings.Repeat("a", 64), Model: "coder", Backend: "ollama", ContextWindow: &window, MaxOutputTokens: &output, ResponseReserveTokens: &reserve}
	if err := st.SetModelCapacityProfile(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetModelCapacityProfile(t.Context(), p.Provider, p.EndpointID, p.Model)
	if err != nil || got.MaxOutputTokens == nil || *got.MaxOutputTokens != 8192 || got.ResponseReserveTokens == nil || *got.ResponseReserveTokens != 4096 || got.Backend != "ollama" {
		t.Fatalf("profile lost: %+v %v", got, err)
	}
	p.ContextWindow = nil
	p.MaxOutputTokens = nil
	p.ResponseReserveTokens = nil
	if err := st.SetModelCapacityProfile(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetModelCapacityProfile(t.Context(), p.Provider, p.EndpointID, p.Model)
	if err != nil || got.ContextWindow != nil {
		t.Fatal("cleared override retained", err)
	}
}
