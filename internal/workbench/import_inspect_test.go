package workbench

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestImportNestedConfigSurvivesBrowserRoundTrip(t *testing.T) {
	config := `{"model_type":"llama","num_hidden_layers":1,"hidden_size":8,"num_attention_heads":2,"intermediate_size":16,"rope_scaling":{"factor":8.0,"type":"linear","original_max_position_embeddings":8192.0}}`
	inspection, err := InspectImport([]ImportFile{importConfig(config)})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(inspection.Graph)
	if err := ValidateImportedGraph(raw); err != nil {
		t.Fatal(err)
	}
	var browser any
	if err := json.Unmarshal(raw, &browser); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(browser)
	if err := ValidateImportedGraph(raw); err != nil {
		t.Fatalf("browser numeric normalization changed source baseline: %v", err)
	}
}

func TestImportGroupedTensorMatchingHasBoundedCost(t *testing.T) {
	config, err := importObject(`{"model_type":"gpt2","n_layer":100000,"n_embd":8,"n_head":2}`, importJSONLimit)
	if err != nil {
		t.Fatal(err)
	}
	source := ImportedSource{Config: config}
	for i := 0; i < 10000; i++ {
		source.Tensors = append(source.Tensors, ImportTensor{Name: fmt.Sprintf("transformer.h.99999.weight_%d", i)})
	}
	start := time.Now()
	graph, err := makeImportedGraph(source)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("100k layers/10k tensors took %v; matching must not scan every expanded layer prefix for every tensor", elapsed)
	}
	bound := 0
	for _, node := range graph.Nodes {
		if node.Kind == "transformer" {
			bound += len(node.TensorNames)
		}
	}
	if bound != 10000 {
		t.Fatalf("last grouped block lost tensor bindings: %d", bound)
	}
}

func BenchmarkImportGroupedTensorMatching(b *testing.B) {
	config, _ := importObject(`{"model_type":"gpt2","n_layer":100000,"n_embd":8,"n_head":2}`, importJSONLimit)
	source := ImportedSource{Config: config}
	for i := 0; i < 100000; i++ {
		source.Tensors = append(source.Tensors, ImportTensor{Name: fmt.Sprintf("transformer.h.99999.weight_%d", i)})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := makeImportedGraph(source); err != nil {
			b.Fatal(err)
		}
	}
}

func TestImportOptionalHeadsFollowArchitectureEvidence(t *testing.T) {
	for _, c := range []struct {
		family, architecture, tensor, task string
		pooler, boundary                   bool
	}{
		{"gpt2", "GPT2Model", "", "", false, false},
		{"gpt2", "GPT2LMHeadModel", "", "causalLanguageModeling", false, false},
		{"llama", "LlamaModel", "", "", false, false},
		{"llama", "LlamaForCausalLM", "", "causalLanguageModeling", false, false},
		{"bert", "BertForMaskedLM", "cls.predictions.decoder.weight", "maskedLanguageModeling", false, false},
		{"bert", "BertForTokenClassification", "classifier.weight", "tokenClassification", false, false},
		{"bert", "BertForSequenceClassification", "", "sequenceClassification", true, false},
		{"bert", "BertModel", "bert.pooler.dense.weight", "", true, false},
		{"vit", "ViTModel", "", "", false, false},
		{"vit", "ViTForImageClassification", "classifier.weight", "imageClassification", false, false},
		{"resnet", "ResNetModel", "", "", true, false},
		{"resnet", "ResNetForImageClassification", "classifier.1.weight", "imageClassification", true, false},
		{"convnext", "ConvNextModel", "", "", true, false},
		{"convnext", "ConvNextForImageClassification", "classifier.weight", "imageClassification", true, false},
		{"vit", "ViTForMaskedImageModeling", "decoder.0.weight", "", false, true},
		{"bert", "CustomBertVariant", "classifier.weight", "", false, true},
		{"gpt2", "", "", "", false, true},
	} {
		t.Run(c.architecture+c.family, func(t *testing.T) {
			config := fmt.Sprintf(`{"model_type":%q,"architectures":[%q],"n_layer":1,"n_embd":8,"n_head":2,"num_hidden_layers":1,"hidden_size":8,"num_attention_heads":2,"intermediate_size":16,"depths":[1],"hidden_sizes":[8]}`, c.family, c.architecture)
			files := []ImportFile{importConfig(config)}
			if c.tensor != "" {
				files = append(files, importWeight("model.safetensors", fmt.Sprintf(`{%q:{"dtype":"F32","shape":[1],"data_offsets":[0,4]}}`, c.tensor), 4))
			}
			inspection, err := InspectImport(files)
			if err != nil {
				t.Fatal(err)
			}
			pooler, head, boundary := false, false, false
			for _, n := range inspection.Graph.Nodes {
				if n.SourceKey == "pooler" {
					pooler = true
				}
				if n.SourceKey == "head" {
					head = true
					if n.Config["task"] != c.task {
						t.Fatalf("wrong native task head: %+v", n)
					}
				}
				if n.SourceKey == "variant_boundary" {
					boundary = true
				}
			}
			if pooler != c.pooler || head != (c.task != "") || boundary != c.boundary {
				t.Fatalf("invented or omitted optional topology: pooler=%v head=%v boundary=%v", pooler, head, boundary)
			}
			raw, _ := json.Marshal(inspection.Graph)
			if err := ValidateImportedGraph(raw); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func importWeight(name, header string, payload int64) ImportFile {
	return ImportFile{Name: name, Kind: "weights", Header: header, Size: int64(8+len(header)) + payload}
}
func importConfig(content string) ImportFile {
	return ImportFile{Name: "config.json", Kind: "config", Content: content, Size: int64(len(content))}
}
func TestImportTransformerAndCNN(t *testing.T) {
	cases := []struct {
		config, header, family, label string
		count, bytes                  int64
	}{
		{`{"model_type":"gpt2","n_layer":2,"n_head":2,"n_embd":4,"vocab_size":8,"n_positions":16}`, `{"transformer.wte.weight":{"dtype":"F32","shape":[8,4],"data_offsets":[0,128]},"transformer.h.0.attn.c_attn.weight":{"dtype":"F32","shape":[4,12],"data_offsets":[128,320]},"transformer.h.1.ln_1.weight":{"dtype":"F32","shape":[4],"data_offsets":[320,336]}}`, "gpt2", "GPT-2", 84, 336},
		{`{"model_type":"resnet","depths":[2,2,2,2],"hidden_sizes":[64,128,256,512],"layer_type":"basic","num_channels":3,"num_labels":10}`, `{"resnet.embedder.embedder.convolution.weight":{"dtype":"F32","shape":[4,3,3,3],"data_offsets":[0,432]},"resnet.encoder.stages.0.layers.0.layer.0.convolution.weight":{"dtype":"F32","shape":[4,4,3,3],"data_offsets":[432,1008]}}`, "resnet", "ResNet", 252, 1008}}
	for _, c := range cases {
		t.Run(c.family, func(t *testing.T) {
			got, err := InspectImport([]ImportFile{importConfig(c.config), importWeight("model.safetensors", c.header, c.bytes)})
			if err != nil {
				t.Fatal(err)
			}
			if !got.Supported || !got.WeightsVerified || got.Family != c.family || got.ParameterCount != c.count {
				t.Fatalf("unexpected inspection: %+v", got)
			}
			found := false
			bound := 0
			for _, n := range got.Graph.Nodes {
				found = found || strings.Contains(n.Label, c.label)
				bound += len(n.TensorNames)
			}
			if !found || bound != got.TensorCount {
				t.Fatal("native labels or bindings missing")
			}
			raw, _ := json.Marshal(got.Graph)
			if err := ValidateImportedGraph(raw); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestImportRejectsMalformedMetadata(t *testing.T) {
	valid := `{"x":{"dtype":"F32","shape":[1],"data_offsets":[0,4]}}`
	cases := map[string][]ImportFile{
		"duplicate config": {importConfig(`{"model_type":"gpt2","model_type":"bert"}`)}, "duplicate tensor": {importWeight("model.safetensors", `{"x":{},"x":{}}`, 0)},
		"dtype": {importWeight("model.safetensors", `{"x":{"dtype":"NOPE","shape":[1],"data_offsets":[0,4]}}`, 4)}, "shape mismatch": {importWeight("model.safetensors", `{"x":{"dtype":"F32","shape":[2],"data_offsets":[0,4]}}`, 4)},
		"overflow": {importWeight("model.safetensors", `{"x":{"dtype":"F64","shape":[9223372036854775807,9],"data_offsets":[0,4]}}`, 4)}, "negative": {importWeight("model.safetensors", `{"x":{"dtype":"F32","shape":[-1],"data_offsets":[0,4]}}`, 4)},
		"gap": {importWeight("model.safetensors", `{"x":{"dtype":"F32","shape":[1],"data_offsets":[1,5]}}`, 5)}, "out of bounds": {importWeight("model.safetensors", valid, 3)},
		"unindexed shards": {importWeight("a.safetensors", valid, 4), importWeight("b.safetensors", `{"y":{"dtype":"F32","shape":[1],"data_offsets":[0,4]}}`, 4)}, "unsafe path": {importWeight("../model.safetensors", valid, 4)}, "metadata payload": {{Name: "tokenizer.json", Kind: "tokenizer", Content: `{}`, Size: 2}}}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := InspectImport(files); err == nil {
				t.Fatal("accepted invalid metadata")
			}
		})
	}
}
func TestImportShardsAndUnknownPreview(t *testing.T) {
	a := importWeight("a.safetensors", `{"a":{"dtype":"BF16","shape":[2],"data_offsets":[0,4]}}`, 4)
	b := importWeight("b.safetensors", `{"b":{"dtype":"F32","shape":[1],"data_offsets":[0,4]}}`, 4)
	idx := `{"metadata":{"total_size":8},"weight_map":{"a":"a.safetensors","b":"b.safetensors"}}`
	index := ImportFile{Name: "model.safetensors.index.json", Kind: "index", Content: idx, Size: int64(len(idx))}
	got, err := InspectImport([]ImportFile{a, b, index})
	if err != nil {
		t.Fatal(err)
	}
	if got.Supported || !got.WeightsVerified || got.TensorCount != 2 || len(got.Graph.Nodes) == 0 {
		t.Fatalf("bad fallback %+v", got)
	}
	if _, err := InspectImport([]ImportFile{a, index}); err == nil {
		t.Fatal("accepted missing shard")
	}
	got, err = InspectImport([]ImportFile{importConfig(`{"model_type":"llama","num_hidden_layers":2,"hidden_size":8,"num_attention_heads":2,"intermediate_size":16}`)})
	if err != nil || !got.Supported || got.WeightsVerified || len(got.Warnings) == 0 {
		t.Fatalf("bad config preview %+v %v", got, err)
	}
}
func TestImportedGraphPreservesBaseline(t *testing.T) {
	got, err := InspectImport([]ImportFile{importConfig(`{"model_type":"bert","num_hidden_layers":2,"hidden_size":8,"num_attention_heads":2,"intermediate_size":32}`)})
	if err != nil {
		t.Fatal(err)
	}
	got.Graph.Nodes[1].BaselineConfig["hiddenSize"] = 999
	raw, _ := json.Marshal(got.Graph)
	if err := ValidateImportedGraph(raw); err == nil {
		t.Fatal("accepted forged baseline")
	}
}

// A dimensional edit must not be accepted merely because it is a JSON number.
func TestImportedGraphRejectsInvalidEditsAndOversizedSource(t *testing.T) {
	got, err := InspectImport([]ImportFile{importConfig(`{"model_type":"gpt2","n_layer":2,"n_head":2,"n_embd":4}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ImportedGraph){
		func(g *ImportedGraph) { g.Nodes[1].Config["hiddenSize"] = -4 },
		func(g *ImportedGraph) { g.Source.Config["oversized"] = strings.Repeat("x", 2<<20) },
		func(g *ImportedGraph) {
			g.Source.Tensors = []ImportTensor{{Name: "fake", Dtype: "F32", Shape: []int64{1}, Bytes: 4, File: "missing.safetensors"}}
		},
		func(g *ImportedGraph) { g.Nodes[1].TensorNames = []string{"nonexistent"} },
		func(g *ImportedGraph) {
			g.Edges = append(g.Edges, ImportedEdge{ID: "invalid", Source: g.Nodes[0].ID, Target: "missing"})
		},
	} {
		raw, _ := json.Marshal(got.Graph)
		var copy ImportedGraph
		json.Unmarshal(raw, &copy)
		change(&copy)
		raw, _ = json.Marshal(copy)
		if err := ValidateImportedGraph(raw); err == nil {
			t.Fatal("accepted invalid edit/source")
		}
	}
}
func TestImportAllFamiliesAndGroupedLayers(t *testing.T) {
	for _, family := range []string{"llama", "mistral", "qwen2", "bert", "vit", "convnext"} {
		t.Run(family, func(t *testing.T) {
			cfg := `{"model_type":"` + family + `","num_hidden_layers":512,"hidden_size":8,"num_attention_heads":2,"num_key_value_heads":2,"intermediate_size":16}`
			got, e := InspectImport([]ImportFile{importConfig(cfg)})
			if e != nil {
				t.Fatal(e)
			}
			if !got.Supported || len(got.Graph.Nodes) > 200 {
				t.Fatal("unsupported or unbounded adapter")
			}
			if family != "convnext" {
				represented := 0
				for _, n := range got.Graph.Nodes {
					if n.Kind == "transformer" {
						represented += n.Config["repeatCount"].(int)
					}
				}
				if represented != 512 {
					t.Fatalf("silently omitted repeats: %d", represented)
				}
			}
			raw, _ := json.Marshal(got.Graph)
			if e := ValidateImportedGraph(raw); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestImportRejectsIndexAndHeaderStructure(t *testing.T) {
	for name, header := range map[string]string{"overlap": `{"a":{"dtype":"F32","shape":[1],"data_offsets":[0,4]},"b":{"dtype":"F32","shape":[1],"data_offsets":[0,4]}}`, "nested duplicate": `{"a":{"dtype":"F32","dtype":"I32","shape":[1],"data_offsets":[0,4]}}`, "wrong shape type": `{"a":{"dtype":"F32","shape":["1"],"data_offsets":[0,4]}}`, "metadata type": `{"__metadata__":{"format":4}}`, "oversized header": strings.Repeat(" ", 8<<20) + `{}`} {
		t.Run(name, func(t *testing.T) {
			if _, e := InspectImport([]ImportFile{importWeight("model.safetensors", header, 4)}); e == nil {
				t.Fatal("accepted invalid header")
			}
		})
	}
	header := `{"a":{"dtype":"F32","shape":[1],"data_offsets":[0,4]}}`
	for _, idx := range []string{`{"weight_map":{"a":"missing.safetensors"}}`, `{"weight_map":{"a":4}}`, `{"weight_map":{"a":"model.safetensors"},"metadata":{"total_size":8}}`, `{"weight_map":{"a":"model.safetensors"},"metadata":4}`} {
		t.Run(idx, func(t *testing.T) {
			if _, e := InspectImport([]ImportFile{importWeight("model.safetensors", header, 4), {Name: "model.safetensors.index.json", Kind: "index", Content: idx, Size: int64(len(idx))}}); e == nil {
				t.Fatal("accepted invalid index")
			}
		})
	}
}
func TestImportWarnsWhenConfigAndTensorShapesDisagree(t *testing.T) {
	got, e := InspectImport([]ImportFile{importConfig(`{"model_type":"gpt2","n_layer":1,"n_embd":8,"n_head":2,"vocab_size":10}`), importWeight("model.safetensors", `{"transformer.wte.weight":{"dtype":"F32","shape":[4,4],"data_offsets":[0,64]}}`, 64)})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(strings.Join(got.Warnings, " "), "shape mismatch") {
		t.Fatal("configuration mismatch not surfaced")
	}
}
func TestImportLargeWeightSizeUsesHeadersOnly(t *testing.T) {
	got, e := InspectImport([]ImportFile{importWeight("model.safetensors", `{"huge":{"dtype":"F32","shape":[1000000000],"data_offsets":[0,4000000000]}}`, 4000000000)})
	if e != nil {
		t.Fatal(e)
	}
	if got.ParameterCount != 1000000000 || got.TotalWeightBytes != 4000000000 {
		t.Fatalf("truncated large size: %+v", got)
	}
}
func TestImportWarnsOnCNNStageShape(t *testing.T) {
	got, e := InspectImport([]ImportFile{importConfig(`{"model_type":"resnet","layer_type":"basic","depths":[1],"hidden_sizes":[64]}`), importWeight("model.safetensors", `{"resnet.encoder.stages.0.layers.0.layer.0.convolution.weight":{"dtype":"F32","shape":[4,4,3,3],"data_offsets":[0,576]}}`, 576)})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(strings.Join(got.Warnings, " "), "shape mismatch") {
		t.Fatal("CNN stage mismatch not surfaced")
	}
}
func TestImportDecoderDefaultsRemainFamilyNative(t *testing.T) {
	for _, c := range []struct {
		family    string
		inner, kv int
	}{{"llama", 11008, 32}, {"mistral", 14336, 8}, {"qwen2", 22016, 32}} {
		t.Run(c.family, func(t *testing.T) {
			got, e := InspectImport([]ImportFile{importConfig(`{"model_type":"` + c.family + `"}`)})
			if e != nil {
				t.Fatal(e)
			}
			for _, node := range got.Graph.Nodes {
				if node.Kind == "transformer" {
					if node.Config["intermediateSize"] != c.inner || node.Config["keyValueHeads"] != c.kv {
						t.Fatalf("incorrect native decoder defaults %+v", node.Config)
					}
					return
				}
			}
			t.Fatal("no decoder blocks")
		})
	}
}
