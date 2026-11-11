package workbench

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Opt in to real CPU execution; ordinary tests do not install dependencies.
func TestImportedRuntimeRealWorkflows(t *testing.T) {
	python := os.Getenv("UMCODE_IMPORTED_TEST_PYTHON")
	if python == "" {
		t.Skip("set UMCODE_IMPORTED_TEST_PYTHON to a Transformers/PyTorch interpreter")
	}
	for _, family := range []string{"gpt2", "resnet", "mixtral", "granitemoe", "qwen2_moe", "qwen3_moe"} {
		t.Run(family, func(t *testing.T) {
			fixture := t.TempDir()
			script := `import sys,io
from pathlib import Path
from transformers import GPT2Config,GPT2LMHeadModel,PreTrainedTokenizerFast,ResNetConfig,ResNetForImageClassification,ConvNextImageProcessor
from tokenizers import Tokenizer
from tokenizers.models import WordLevel
from tokenizers.pre_tokenizers import Whitespace
from PIL import Image
p=Path(sys.argv[1]); family=sys.argv[2]
if family=='gpt2':
 t=Tokenizer(WordLevel({'[PAD]':0,'[UNK]':1,'hello':2,'world':3},unk_token='[UNK]'));t.pre_tokenizer=Whitespace()
 PreTrainedTokenizerFast(tokenizer_object=t,pad_token='[PAD]',unk_token='[UNK]').save_pretrained(p)
 GPT2LMHeadModel(GPT2Config(vocab_size=4,n_positions=32,n_embd=8,n_layer=1,n_head=2)).save_pretrained(p,safe_serialization=True)
elif family=='resnet':
 ResNetForImageClassification(ResNetConfig(embedding_size=8,hidden_sizes=[8,16],depths=[1,1],num_labels=2)).save_pretrained(p,safe_serialization=True)
 ConvNextImageProcessor(size={'shortest_edge':32},crop_pct=1.0).save_pretrained(p)
 Image.new('RGB',(32,32),'red').save(p/'sample.png')
else:
 from moe_fixture import create
 create(p,family)
`
			command := exec.Command(python, "-c", script, fixture, family)
			command.Dir = "pytorch"
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v %s", err, out)
			}
			dir, err := os.OpenRoot(fixture)
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Close()
			entries, err := os.ReadDir(fixture)
			if err != nil {
				t.Fatal(err)
			}
			manifest := bundleManifest{}
			for _, e := range entries {
				if BundleFileKind(e.Name()) != "" {
					info, _ := e.Info()
					manifest.Files = append(manifest.Files, ImportSourceFile{Name: e.Name(), Size: info.Size()})
				}
			}
			files, _, err := bundleMetadata(dir, manifest)
			if err != nil {
				t.Fatal(err)
			}
			inspected, err := InspectImport(files)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(inspected.Graph)
			project := t.TempDir()
			model, err := CreateImported(project, family, raw)
			if err != nil {
				t.Fatal(err)
			}
			target := BundleTarget{project, model.Model.ID, model.Model.CreatedAt.Format(time.RFC3339Nano)}
			upload, err := target.Begin(manifest.Files)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range manifest.Files {
				content, err := os.ReadFile(filepath.Join(fixture, f.Name))
				if err != nil {
					t.Fatal(err)
				}
				for off := 0; off < len(content); off += BundleChunkBytes {
					end := min(len(content), off+BundleChunkBytes)
					if err = target.Chunk(upload, f.Name, int64(off), base64.StdEncoding.EncodeToString(content[off:end])); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err = target.Finish(upload); err != nil {
				t.Fatal(err)
			}
			runtime := NewImportedRuntime(t.TempDir())
			runtime.python = python
			defer runtime.Close()
			state, err := runtime.Validate(context.Background(), target)
			if err != nil || !state.Ready {
				t.Fatalf("validate: %+v %v", state, err)
			}
			image := ""
			if family == "resnet" {
				bytes, _ := os.ReadFile(filepath.Join(fixture, "sample.png"))
				image = base64.StdEncoding.EncodeToString(bytes)
			}
			inferred, err := runtime.Infer(context.Background(), target, "hello world", image, "", 2)
			if err != nil || inferred["ok"] != true {
				t.Fatalf("infer: %+v %v", inferred, err)
			}
			config := DataConfig{Mode: "text", Purpose: "text-generation", InputColumns: []string{"text"}, SequenceLength: 8, TrainPercent: 60, ValidationPercent: 20, TestPercent: 20}
			inputs := []DataFile{uploadDataFixture("text.csv", "text\nhello world\nworld hello\nhello world\nworld hello\nhello world\n")}
			if family == "resnet" {
				config.Mode = "image"
				config.Purpose = "classification"
				config.InputColumns = []string{"image"}
				config.TargetColumn = "label"
				inputs = nil
				for i := 0; i < 5; i++ {
					inputs = append(inputs, DataFile{Name: filepath.Join("LABEL_0", string(rune('a'+i))+".png"), ContentBase64: image})
				}
			}
			dataset, err := ImportData(project, model.Model.ID, DataSource{Kind: "local"}, inputs)
			if err != nil {
				t.Fatal(err)
			}
			config.DatasetID = dataset.ID
			if _, err = PrepareImportedData(project, model.Model.ID, config); err != nil {
				t.Fatal(err)
			}
			started, err := runtime.Train(context.Background(), target, 1, .001)
			if err != nil || started.Status != "running" {
				t.Fatalf("train: %+v %v", started, err)
			}
			encodedJob, _ := json.Marshal(started)
			var snapshot struct {
				Data struct {
					DatasetName string        `json:"datasetName"`
					Selection   DataSelection `json:"selection"`
				} `json:"data"`
			}
			if err = json.Unmarshal(encodedJob, &snapshot); err != nil || snapshot.Data.DatasetName != dataset.Name || snapshot.Data.Selection.DatasetID != dataset.ID || snapshot.Data.Selection.Counts.Train != 3 {
				t.Fatalf("training job lost its dataset snapshot: %s (%v)", encodedJob, err)
			}
			deadline := time.Now().Add(time.Minute)
			for time.Now().Before(deadline) {
				job, err := runtime.Job(target)
				if err != nil {
					t.Fatal(err)
				}
				if job != nil && job.Status != "running" {
					if job.Status != "completed" {
						t.Fatalf("job: %+v", job)
					}
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			job, _ := runtime.Job(target)
			if job == nil || job.Status != "completed" {
				t.Fatalf("timeout: %+v", job)
			}
			state, err = runtime.Status(target)
			if err != nil || state.Checkpoint == "" {
				t.Fatalf("checkpoint state: %+v %v", state, err)
			}
			inferred, err = runtime.Infer(context.Background(), target, "hello world", image, state.Checkpoint, 2)
			if err != nil || inferred["ok"] != true {
				t.Fatalf("checkpoint inference: %+v %v", inferred, err)
			}
		})
	}
}
func uploadDataFixture(name, content string) DataFile {
	return DataFile{Name: name, ContentBase64: base64.StdEncoding.EncodeToString([]byte(content))}
}
