package workbench

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runtimeBundleFixture(t *testing.T) (string, string, string, []ImportSourceFile, map[string][]byte) {
	t.Helper()
	root := t.TempDir()
	cfg := []byte(`{"model_type":"gpt2","architectures":["GPT2LMHeadModel"],"n_layer":1,"n_embd":4,"n_head":2,"vocab_size":8,"n_positions":16}`)
	header := `{"transformer.wte.weight":{"dtype":"F32","shape":[8,4],"data_offsets":[0,128]}}`
	weight := make([]byte, 8+len(header)+128)
	binary.LittleEndian.PutUint64(weight, uint64(len(header)))
	copy(weight[8:], header)
	assets := map[string][]byte{"config.json": cfg, "model.safetensors": weight, "tokenizer.json": []byte(`{"version":"1.0"}`), "tokenizer_config.json": []byte(`{"tokenizer_class":"PreTrainedTokenizerFast"}`)}
	files := []ImportFile{}
	manifest := []ImportSourceFile{}
	for name, content := range assets {
		kind := "tokenizer"
		if name == "config.json" {
			kind = "config"
		}
		if name == "model.safetensors" {
			kind = "weights"
		}
		f := ImportFile{Name: name, Size: int64(len(content)), Kind: kind}
		if kind == "config" {
			f.Content = string(content)
		}
		if kind == "weights" {
			f.Header = header
		}
		files = append(files, f)
		manifest = append(manifest, ImportSourceFile{Name: name, Size: int64(len(content))})
	}
	inspected, e := InspectImport(files)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(inspected.Graph)
	m, e := CreateImported(root, "runtime test", raw)
	if e != nil {
		t.Fatal(e)
	}
	return root, m.Model.ID, m.Model.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), manifest, assets
}
func TestImportedBundleRequiresPairAndPublishesCompleteFiles(t *testing.T) {
	root, id, created, manifest, assets := runtimeBundleFixture(t)
	b := BundleTarget{ProjectRoot: root, ModelID: id, CreatedAt: created}
	if _, err := b.Begin([]ImportSourceFile{{Name: "config.json", Size: 2}}); err == nil {
		t.Fatal("accepted config only")
	}
	upload, err := b.Begin(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Finish(upload); err == nil {
		t.Fatal("published incomplete files")
	}
	for name, content := range assets {
		if err = b.Chunk(upload, name, 0, base64.StdEncoding.EncodeToString(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err = b.Chunk(upload, "config.json", 0, "e30="); err == nil {
		t.Fatal("accepted duplicate/out of order chunk")
	}
	if err = b.Finish(upload); err != nil {
		t.Fatal(err)
	}
	folder, err := b.VerifiedPath()
	if err != nil {
		t.Fatal(err)
	}
	if got, e := os.ReadFile(filepath.Join(folder, "model.safetensors")); e != nil || string(got) != string(assets["model.safetensors"]) {
		t.Fatal("weights not copied intact", e)
	}
	if e := os.WriteFile(filepath.Join(folder, "config.json"), []byte(strings.Repeat(" ", len(assets["config.json"]))), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := b.VerifiedPath(); e == nil {
		t.Fatal("accepted changed bundle")
	}
}
func TestImportedBundleRejectsTraversalSymlinkAndReplacedModel(t *testing.T) {
	root, id, created, manifest, _ := runtimeBundleFixture(t)
	b := BundleTarget{root, id, created}
	if _, e := b.Begin(append(manifest, ImportSourceFile{Name: "../escape", Size: 1})); e == nil {
		t.Fatal("traversal")
	}
	upload, e := b.Begin(manifest)
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Chunk(upload, "../escape", 0, "eA=="); e == nil {
		t.Fatal("chunk traversal")
	}
	bad := b
	bad.CreatedAt = "2000-01-01T00:00:00Z"
	if e = bad.Chunk(upload, "config.json", 0, "e30="); e == nil {
		t.Fatal("replaced model")
	}
}
