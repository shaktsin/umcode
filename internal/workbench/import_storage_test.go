package workbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func importedStorageFixture(t *testing.T) json.RawMessage {
	t.Helper()
	config := `{"model_type":"gpt2","n_embd":16,"n_layer":2,"n_head":4,"vocab_size":32,"n_positions":16}`
	result, err := InspectImport([]ImportFile{{Name: "config.json", Size: int64(len(config)), Kind: "config", Content: config}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result.Graph)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestImportedModelPublicationAndImmutableSource(t *testing.T) {
	root := t.TempDir()
	graph := importedStorageFixture(t)
	created, err := CreateImported(root, "Imported transformer", graph)
	if err != nil {
		t.Fatal(err)
	}
	if !IsImportedGraph(created.Graph) || created.Model.ID != "imported-transformer" {
		t.Fatalf("created=%+v", created.Model)
	}
	if _, err = CreateImported(root, "Imported transformer", graph); err == nil {
		t.Fatal("overwrote existing import")
	}
	if _, err = os.Stat(filepath.Join(root, "models", created.Model.ID, "import-source.json")); err != nil {
		t.Fatal(err)
	}
	var edited map[string]any
	if err = json.Unmarshal(graph, &edited); err != nil {
		t.Fatal(err)
	}
	nodes := edited["nodes"].([]any)
	nodes[0].(map[string]any)["position"] = map[string]any{"x": 123, "y": 44}
	moved, _ := json.Marshal(edited)
	if err = SaveGraph(root, created.Model.ID, moved); err != nil {
		t.Fatalf("position edit failed: %v", err)
	}
	source := edited["source"].(map[string]any)
	source["family"] = "tampered"
	changed, _ := json.Marshal(edited)
	if err = SaveGraph(root, created.Model.ID, changed); err == nil {
		t.Fatal("source provenance changed")
	}
	if err = SaveGraph(root, created.Model.ID, json.RawMessage(`{"version":1,"nodes":[],"edges":[]}`)); err == nil {
		t.Fatal("import converted to native without weight handling")
	}
	reopened, err := Open(root, created.Model.ID)
	if err != nil || string(reopened.Graph) == string(changed) {
		t.Fatalf("invalid save changed model %v", err)
	}
}
func TestImportedModelRejectsUnsafeDestinationAndMalformedGraph(t *testing.T) {
	root := t.TempDir()
	graph := importedStorageFixture(t)
	if _, err := CreateImported(root, "Bad", json.RawMessage(`{"version":1,"format":"umcode.imported.v1","nodes":[],"edges":[]}`)); err == nil {
		t.Fatal("malformed source accepted")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "models")); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateImported(root, "Unsafe", graph); err == nil {
		t.Fatal("symlink models destination accepted")
	}
}

func TestImportedGraphRejectsForgedSourceIdentity(t *testing.T) {
	raw := importedStorageFixture(t)
	for name, mutate := range map[string]func(*ImportedGraph){
		"source id":   func(g *ImportedGraph) { g.Nodes[0].ID = "changed"; g.Edges[0].Source = "changed" },
		"source kind": func(g *ImportedGraph) { g.Nodes[0].Kind = "linear" },
		"reserved id": func(g *ImportedGraph) {
			g.Nodes[0].SourceKey = ""
			g.Nodes[0].TensorNames = []string{}
			g.Nodes[0].BaselineConfig = map[string]any{}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var g ImportedGraph
			if err := json.Unmarshal(raw, &g); err != nil {
				t.Fatal(err)
			}
			mutate(&g)
			changed, _ := json.Marshal(g)
			if ValidateImportedGraph(changed) == nil {
				t.Fatal("forged source identity accepted")
			}
		})
	}
}

func TestImportedGraphProtectsReadOnlySourceSettings(t *testing.T) {
	raw := importedStorageFixture(t)
	root := t.TempDir()
	created, err := CreateImported(root, "Read-only boundary", raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveGraph(root, created.Model.ID, raw); err != nil {
		t.Fatalf("unchanged save: %v", err)
	}
	if _, err := Open(root, created.Model.ID); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	var graph ImportedGraph
	if err := json.Unmarshal(raw, &graph); err != nil {
		t.Fatal(err)
	}
	if err := ValidateImportedGraph(raw); err != nil {
		t.Fatalf("original must remain valid: %v", err)
	}
	index := -1
	for i, node := range graph.Nodes {
		if node.Kind == "unknown" {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("fixture has no read-only boundary")
	}
	graph.Nodes[index].Config["reason"] = "forged"
	changed, _ := json.Marshal(graph)
	if ValidateImportedGraph(changed) == nil {
		t.Fatal("read-only settings changed")
	}
	if SaveGraph(root, created.Model.ID, changed) == nil {
		t.Fatal("saved changed read-only settings")
	}
}
