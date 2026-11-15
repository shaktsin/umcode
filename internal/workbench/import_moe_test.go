package workbench

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Losing a family adapter, expert configuration or tensor binding breaks these
// imports; expectations are small, independently specified native architectures.
func TestImportMoEFamilies(t *testing.T) {
	for _, tc := range []struct{ family, arch, expertKey, tensor string }{
		{"mixtral", "MixtralForCausalLM", "num_local_experts", "block_sparse_moe.experts.0.w1.weight"},
		{"granitemoe", "GraniteMoeForCausalLM", "num_local_experts", "block_sparse_moe.input_linear.weight"},
		{"qwen2_moe", "Qwen2MoeForCausalLM", "num_experts", "mlp.experts.0.gate_proj.weight"},
		{"qwen3_moe", "Qwen3MoeForCausalLM", "num_experts", "mlp.experts.0.gate_proj.weight"},
	} {
		t.Run(tc.family, func(t *testing.T) {
			config := fmt.Sprintf(`{"model_type":%q,"architectures":[%q],"num_hidden_layers":2,"hidden_size":16,"num_attention_heads":4,"num_key_value_heads":2,"intermediate_size":32,"moe_intermediate_size":24,"shared_expert_intermediate_size":48,%q:8,"num_experts_per_tok":2}`, tc.family, tc.arch, tc.expertKey)
			tensor := "model.layers.0." + tc.tensor
			result, err := InspectImport([]ImportFile{importConfig(config), importWeight("model.safetensors", fmt.Sprintf(`{%q:{"dtype":"F32","shape":[1],"data_offsets":[0,4]}}`, tensor), 4)})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Supported {
				t.Fatal("MoE remains inspection-only")
			}
			if task, err := importedTask(result.Graph); err != nil || task != "text-generation" {
				t.Fatalf("MoE causal model is not routed to text execution: %s %v", task, err)
			}
			blocks, head := 0, false
			for _, n := range result.Graph.Nodes {
				if n.Kind == "transformer" {
					blocks++
					if n.Config["mlp"] != "sparse MoE" || n.Config["expertCount"] != 8 || n.Config["expertsPerToken"] != 2 {
						t.Fatalf("lost MoE settings: %+v", n.Config)
					}
					if n.SourceKey == "layers.0.0" && (len(n.TensorNames) != 1 || n.TensorNames[0] != tensor) {
						t.Fatal("lost expert binding")
					}
				}
				if n.SourceKey == "head" {
					head = n.Config["task"] == "causalLanguageModeling"
				}
			}
			if blocks != 2 || !head {
				t.Fatalf("blocks=%d head=%v", blocks, head)
			}
			raw, _ := json.Marshal(result.Graph)
			if err = ValidateImportedGraph(raw); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			saved, err := CreateImported(root, "MoE test", raw)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Model.Task != "text-generation" {
				t.Fatalf("wrong task: %s", saved.Model.Task)
			}
			for i := range result.Graph.Nodes {
				if result.Graph.Nodes[i].Kind == "transformer" {
					result.Graph.Nodes[i].Config["expertsPerToken"] = 1
					break
				}
			}
			edited, _ := json.Marshal(result.Graph)
			if err = SaveGraph(root, saved.Model.ID, edited); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(root, saved.Model.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !importEqual(json.RawMessage(reopened.Graph), json.RawMessage(edited)) {
				t.Fatal("MoE edits did not survive reopen")
			}
		})
	}
}

func TestImportMoERealFolder(t *testing.T) {
	root := os.Getenv("UMCODE_TEST_MOE_FOLDER")
	if root == "" {
		t.Skip("set UMCODE_TEST_MOE_FOLDER for a real metadata-only import")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	files := []ImportFile{}
	for _, entry := range entries {
		name := entry.Name()
		kind := ""
		switch {
		case name == "config.json":
			kind = "config"
		case strings.HasSuffix(name, ".safetensors.index.json"):
			kind = "index"
		case strings.HasSuffix(name, ".safetensors"):
			kind = "weights"
		default:
			continue
		}
		f, err := os.Open(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		stat, err := f.Stat()
		if err != nil {
			f.Close()
			t.Fatal(err)
		}
		item := ImportFile{Name: name, Kind: kind, Size: stat.Size()}
		if kind == "weights" {
			var prefix [8]byte
			_, err = io.ReadFull(f, prefix[:])
			if err != nil {
				f.Close()
				t.Fatal(err)
			}
			n := binary.LittleEndian.Uint64(prefix[:])
			if n > 8*1024*1024 {
				f.Close()
				t.Fatal("oversized header")
			}
			b := make([]byte, n)
			_, err = io.ReadFull(f, b)
			item.Header = string(b)
		} else {
			var b []byte
			b, err = io.ReadAll(io.LimitReader(f, 2*1024*1024+1))
			item.Content = string(b)
		}
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, item)
	}
	result, err := InspectImport(files)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Supported || !importMoE(result.Family) {
		t.Fatal("real MoE not supported")
	}
	raw, _ := json.Marshal(result.Graph)
	if err = ValidateImportedGraph(raw); err != nil {
		t.Fatal(err)
	}
	for _, n := range result.Graph.Nodes {
		if n.Kind == "unknown" {
			t.Fatalf("unmapped native tensors: %s", n.Label)
		}
	}
	t.Logf("family=%s nodes=%d tensors=%d parameters=%d", result.Family, len(result.Graph.Nodes), result.TensorCount, result.ParameterCount)
}

func TestImportMoESparseScheduleAndHeadDimensions(t *testing.T) {
	result, err := InspectImport([]ImportFile{importConfig(`{"model_type":"qwen3_moe","num_hidden_layers":4,"hidden_size":16,"num_attention_heads":4,"head_dim":8,"num_key_value_heads":2,"num_experts":8,"num_experts_per_tok":2,"decoder_sparse_step":2,"mlp_only_layers":[3],"moe_intermediate_size":24,"intermediate_size":32}`)})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gated MLP (SwiGLU)", "sparse MoE", "gated MLP (SwiGLU)", "gated MLP (SwiGLU)"}
	i := 0
	for _, n := range result.Graph.Nodes {
		if n.Kind == "transformer" {
			if i >= 4 || n.Config["mlp"] != want[i] || n.Config["headDimension"] != 8 || n.Config["queryKeyNorm"] != true {
				t.Fatalf("wrong layer %d: %+v", i, n.Config)
			}
			i++
		}
	}
	if i != 4 {
		t.Fatalf("want 4 decoder layers, got %d", i)
	}
}

func TestImportMoENullHeadDimensionAndRoutingDefaults(t *testing.T) {
	for _, family := range []string{"mixtral", "qwen3_moe"} {
		result, err := InspectImport([]ImportFile{importConfig(fmt.Sprintf(`{"model_type":%q,"num_hidden_layers":1,"head_dim":null}`, family))})
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range result.Graph.Nodes {
			if node.Kind == "transformer" {
				if node.Config["normalizeTopK"] != (family == "mixtral") {
					t.Fatalf("wrong native routing normalization: %+v", node.Config)
				}
				if family == "qwen3_moe" && node.Config["hiddenSize"] != 2048 {
					t.Fatal("wrong Qwen3 default width")
				}
			}
		}
	}
}

func TestImportMoERejectsInvalidRouting(t *testing.T) {
	for _, extra := range []string{`"num_experts_per_tok":9`, `"num_experts_per_tok":0`, `"decoder_sparse_step":0`, `"mlp_only_layers":[-1]`} {
		_, err := InspectImport([]ImportFile{importConfig(`{"model_type":"qwen2_moe","num_hidden_layers":2,"hidden_size":16,"num_attention_heads":4,"num_experts":8,` + extra + `}`)})
		if err == nil {
			t.Fatalf("accepted invalid routing: %s", extra)
		}
	}
}

func TestImportMoERejectsInvalidEditedRouting(t *testing.T) {
	result, err := InspectImport([]ImportFile{importConfig(`{"model_type":"mixtral","num_hidden_layers":1,"hidden_size":16,"num_attention_heads":4,"num_key_value_heads":2,"num_local_experts":8,"num_experts_per_tok":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{0, -1, 1.5, 9} {
		for i := range result.Graph.Nodes {
			if result.Graph.Nodes[i].Kind == "transformer" {
				result.Graph.Nodes[i].Config["expertsPerToken"] = value
			}
		}
		raw, _ := json.Marshal(result.Graph)
		if ValidateImportedGraph(raw) == nil {
			t.Fatalf("accepted invalid edited top-k: %v", value)
		}
	}
}
