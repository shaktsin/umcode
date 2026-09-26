package credentials

import (
	"context"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
)

func TestImportConfigKeys(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sec := secrets.NewMemoryStore()
	s := New(st, sec, llm.NewRegistry())
	cfg := config.Default(t.TempDir())
	cfg.LLM.Provider = "openai"
	cfg.LLM.APIKey = "sk-main-openai"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"gemini": {APIKey: "gm-key-1111"}}
	added, err := s.ImportConfigKeys(ctx, cfg)
	if err != nil || len(added) != 2 {
		t.Fatalf("added=%+v err=%v", added, err)
	}
	for _, prov := range []string{"openai", "gemini"} {
		r, err := s.Resolve(ctx, prov, "")
		if err != nil {
			t.Fatalf("%s: %v", prov, err)
		}
		if r.Material.APIKey == "" || r.Record.Label != "Imported" {
			t.Fatalf("%s: %+v", prov, r)
		}
	}
	// Second run imports nothing.
	added, _ = s.ImportConfigKeys(ctx, cfg)
	if len(added) != 0 {
		t.Fatalf("re-imported: %+v", added)
	}
}
