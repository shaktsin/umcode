package engine

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/workbench"
)

// Exercises the actual offline runtime, persisted promotion, catalog and chat
// route. No downloaded model or cloud credentials are needed.
func TestImportedPromotionRealLocalChat(t *testing.T) {
	python := os.Getenv("UMCODE_IMPORTED_TEST_PYTHON")
	if python == "" {
		t.Skip("set UMCODE_IMPORTED_TEST_PYTHON to run the offline model integration")
	}
	t.Setenv("UMCODE_WORKBENCH_PYTHON", python)
	e, th, cloudCalls, _ := workbenchLocalChatFixture(t)
	project, err := e.CreateProject(t.Context(), protocol.ProjectCreateParams{Root: t.TempDir(), Name: "imported chat test"})
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	script := `import sys, torch
from transformers import GPT2Config,GPT2LMHeadModel,PreTrainedTokenizerFast
from tokenizers import Tokenizer
from tokenizers.models import WordLevel
from tokenizers.pre_tokenizers import Whitespace
torch.manual_seed(1)
raw=Tokenizer(WordLevel({'[UNK]':0,'hello':1,'reply':2,'user':3,'assistant':4},unk_token='[UNK]'));raw.pre_tokenizer=Whitespace()
tok=PreTrainedTokenizerFast(tokenizer_object=raw,unk_token='[UNK]',pad_token='[UNK]')
tok.chat_template="{% for m in messages %}{{ m['role'] + ' ' + m['content'] + ' ' }}{% endfor %}{% if add_generation_prompt %}{{ 'assistant' }}{% endif %}"
tok.save_pretrained(sys.argv[1])
model=GPT2LMHeadModel(GPT2Config(vocab_size=32768,n_positions=2048,n_embd=8,n_layer=1,n_head=2,eos_token_id=None,pad_token_id=0,tie_word_embeddings=False))
with torch.no_grad():
 for p in model.parameters(): p.zero_()
 model.transformer.ln_f.bias.fill_(1)
 model.lm_head.weight[2].fill_(1)
model.save_pretrained(sys.argv[1])
`
	if out, err := exec.Command(python, "-c", script, folder).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	files := []workbench.ImportFile{}
	manifest := []workbench.ImportSourceFile{}
	assets := map[string][]byte{}
	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		kind := workbench.BundleFileKind(name)
		if kind == "" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(folder, name))
		if err != nil {
			t.Fatal(err)
		}
		f := workbench.ImportFile{Name: name, Kind: kind, Size: int64(len(content))}
		if name == "config.json" {
			f.Content = string(content)
		}
		if strings.HasSuffix(name, ".safetensors") {
			n := binary.LittleEndian.Uint64(content[:8])
			f.Header = string(content[8 : 8+n])
		}
		if kind == "config" || kind == "weights" || kind == "tokenizer" {
			files = append(files, f)
		}
		manifest = append(manifest, workbench.ImportSourceFile{Name: name, Size: int64(len(content))})
		assets[name] = content
	}
	inspection, err := workbench.InspectImport(files)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(inspection.Graph)
	model, err := workbench.CreateImported(project.Root, "Tiny imported chat", raw)
	if err != nil {
		t.Fatal(err)
	}
	p := protocol.ImportRuntimeParams{ProjectID: project.ID, ModelID: model.Model.ID, ModelCreatedAt: model.Model.CreatedAt.Format(time.RFC3339Nano)}
	target := workbench.BundleTarget{ProjectRoot: project.Root, ModelID: p.ModelID, CreatedAt: p.ModelCreatedAt}
	upload, err := target.Begin(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range assets {
		for off := 0; off < len(content); off += workbench.BundleChunkBytes {
			end := min(len(content), off+workbench.BundleChunkBytes)
			if err = target.Chunk(upload, name, int64(off), base64.StdEncoding.EncodeToString(content[off:end])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = target.Finish(upload); err != nil {
		t.Fatal(err)
	}
	p.Action = "validate"
	validated, err := e.WorkbenchImportRuntime(t.Context(), p)
	if err != nil || !validated.(workbench.ImportRuntimeState).Ready {
		t.Fatalf("validate: %+v %v", validated, err)
	}
	p.Action = "promote"
	if _, err = e.WorkbenchImportRuntime(t.Context(), p); err == nil || !strings.Contains(err.Error(), "test") {
		t.Fatalf("must require a successful prompt test, got %v", err)
	}
	p.Action = "infer"
	p.Prompt = "hello"
	p.MaxTokens = 2
	result, err := e.WorkbenchImportRuntime(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	inferred := result.(map[string]any)
	if inferred["text"] != "reply reply" || inferred["chatCompatible"] != true {
		t.Fatalf("infer: %+v", inferred)
	}
	p.TestID, _ = inferred["testId"].(string)
	if p.TestID == "" {
		t.Fatal("missing successful prompt receipt")
	}
	p.Action = "promote"
	result, err = e.WorkbenchImportRuntime(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	promotion := result.(workbench.Promotion)
	if promotion.Runtime != "transformers" || promotion.PromptFormat != "imported-chat-v1" || promotion.ContextWindow != 2048 || promotion.SourceFingerprint == "" {
		t.Fatalf("unbound promotion: %+v", promotion)
	}
	catalog, err := e.LocalWorkbenchModels(t.Context(), "")
	if err != nil || len(catalog) != 1 || catalog[0].ID != promotion.ID {
		t.Fatalf("catalog: %+v %v", catalog, err)
	}
	// A copied/replayed receipt must not authorize another checkpoint or model version.
	changed := p
	changed.Checkpoint = "missing-checkpoint"
	if _, err = e.WorkbenchImportRuntime(t.Context(), changed); err == nil {
		t.Fatal("receipt authorized another checkpoint")
	}
	changed = p
	changed.ModelCreatedAt = "2000-01-01T00:00:00Z"
	if _, err = e.WorkbenchImportRuntime(t.Context(), changed); err == nil {
		t.Fatal("receipt authorized another model version")
	}
	wait := make(chan protocol.Turn, 1)
	request := turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: "hello", Override: protocol.ModelSelection{Provider: "workbench_local", Model: promotion.ID}}, wait: wait}
	if _, err = e.startWorkbenchLocalTurn(t.Context(), th, request, e); err != nil {
		t.Fatal(err)
	}
	var completed protocol.Turn
	select {
	case completed = <-wait:
	case <-time.After(30 * time.Second):
		t.Fatal("imported local chat timed out")
	}
	if completed.Status != protocol.TurnCompleted {
		t.Fatalf("chat: %+v", completed)
	}
	items, err := e.Store.ListItems(t.Context(), th.ID, 0)
	if err != nil || len(items) != 2 || !strings.HasPrefix(items[1].Text, "reply reply") {
		t.Fatalf("transcript: %+v %v", items, err)
	}
	if cloudCalls.Load() != 0 {
		t.Fatal("imported chat contacted cloud")
	}
	p.Action = "unpromote"
	if _, err = e.WorkbenchImportRuntime(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	catalog, err = e.LocalWorkbenchModels(t.Context(), "")
	if err != nil || len(catalog) != 0 {
		t.Fatalf("unpromote: %+v %v", catalog, err)
	}
}
