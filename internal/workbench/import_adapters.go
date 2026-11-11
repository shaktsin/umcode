package workbench

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func importFamily(config map[string]any) (string, bool) {
	family, _ := config["model_type"].(string)
	family = strings.ToLower(family)
	switch family {
	case "gpt2", "llama", "mistral", "qwen2", "bert", "vit", "resnet", "convnext", "mixtral", "granitemoe", "qwen2_moe", "qwen3_moe":
		return family, true
	}
	if family == "" {
		family = "unknown"
	}
	return family, false
}
func importDisplayName(config map[string]any, family string) string {
	if name, ok := config["_name_or_path"].(string); ok && name != "" {
		parts := strings.Split(strings.TrimRight(name, "/"), "/")
		if len(parts[len(parts)-1]) <= 128 {
			return parts[len(parts)-1]
		}
	}
	name := map[string]string{"gpt2": "GPT-2", "llama": "Llama", "mistral": "Mistral", "qwen2": "Qwen2", "bert": "BERT", "vit": "ViT", "resnet": "ResNet", "convnext": "ConvNeXT", "mixtral": "Mixtral", "granitemoe": "Granite MoE", "qwen2_moe": "Qwen2 MoE", "qwen3_moe": "Qwen3 MoE", "unknown": "Imported model"}[family]
	if name == "" {
		return "Imported model"
	}
	return name
}
func importConfigInt(c map[string]any, key string, fallback int) (int, error) {
	v, ok := c[key]
	if !ok {
		return fallback, nil
	}
	n, e := importInteger(v)
	if e != nil || n == 0 || n > 1000000000 {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return int(n), nil
}
func importConfigInts(c map[string]any, key string, fallback []int) ([]int, error) {
	v, ok := c[key]
	if !ok {
		return fallback, nil
	}
	a, ok := v.([]any)
	if !ok || len(a) == 0 || len(a) > 16 {
		return nil, fmt.Errorf("invalid %s", key)
	}
	r := make([]int, len(a))
	for i, n := range a {
		x, e := importInteger(n)
		if e != nil || x == 0 || x > 1000000 {
			return nil, fmt.Errorf("invalid %s", key)
		}
		r[i] = int(x)
	}
	return r, nil
}
func importConfigValue(c map[string]any, key string, fallback any) any {
	if v, ok := c[key]; ok {
		switch v.(type) {
		case string, json.Number, bool:
			return v
		case []any, map[string]any:
			raw, _ := json.Marshal(v)
			// The browser parses source JSON as IEEE-754 numbers. Normalize nested
			// values the same way before embedding JSON in a scalar editor field,
			// so 8.0 and 8 produce identical baseline strings on create/save.
			var normalized any
			if json.Unmarshal(raw, &normalized) == nil {
				raw, _ = json.Marshal(normalized)
			}
			return string(raw)
		}
	}
	return fallback
}
func cloneImportConfig(c map[string]any) map[string]any {
	r := map[string]any{}
	for k, v := range c {
		r[k] = v
	}
	return r
}

type importTaskVariant struct {
	task, label, tokenPool, boundary string
	prefixes                         []string
	pooler                           bool
}

// Optional task modules are identified separately from the family backbone.
// Unrecognized or ambiguous variants stop at an explicit inspection boundary.
func importVariant(source ImportedSource) importTaskVariant {
	family, _ := importFamily(source.Config)
	evidence := map[string]bool{}
	for _, tensor := range source.Tensors {
		for _, prefix := range []string{"lm_head.", "score.", "classifier.", "cls.predictions.", "cls.seq_relationship.", "qa_outputs.", "bert.pooler.", "pooler.", "vit.pooler."} {
			if strings.HasPrefix(tensor.Name, prefix) {
				evidence[prefix] = true
			}
		}
	}
	variant := ""
	architectures, _ := source.Config["architectures"].([]any)
	for _, value := range architectures {
		name, ok := value.(string)
		if !ok || name == "" {
			continue
		}
		if variant != "" && variant != name {
			return importTaskVariant{boundary: "Multiple architecture variants are declared; task-head topology has not been inferred."}
		}
		variant = name
	}
	base := map[string]string{"gpt2": "GPT2Model", "llama": "LlamaModel", "mistral": "MistralModel", "qwen2": "Qwen2Model", "bert": "BertModel", "vit": "ViTModel", "resnet": "ResNetModel", "convnext": "ConvNextModel", "mixtral": "MixtralModel", "granitemoe": "GraniteMoeModel", "qwen2_moe": "Qwen2MoeModel", "qwen3_moe": "Qwen3MoeModel"}[family]
	if variant == base && base != "" {
		v := importTaskVariant{}
		if family == "bert" {
			v.pooler = evidence["bert.pooler."] || evidence["pooler."]
		}
		if family == "vit" {
			v.pooler = evidence["vit.pooler."] || evidence["pooler."]
		}
		if evidence["lm_head."] || evidence["score."] || evidence["classifier."] || evidence["cls.predictions."] || evidence["qa_outputs."] {
			v.boundary = "Declared base architecture also contains task-head tensors; conflicting task topology has not been inferred."
		}
		return v
	}
	if family == "bert" {
		switch variant {
		case "BertForMaskedLM":
			return importTaskVariant{task: "maskedLanguageModeling", label: "BERT masked-LM head: dense / activation / LayerNorm / vocabulary decoder", prefixes: []string{"cls.predictions."}}
		case "BertForSequenceClassification":
			return importTaskVariant{task: "sequenceClassification", label: "BERT sequence classifier: dropout / dense", prefixes: []string{"classifier."}, pooler: true}
		case "BertForTokenClassification":
			return importTaskVariant{task: "tokenClassification", label: "BERT token classifier: dropout / dense", prefixes: []string{"classifier."}}
		case "BertForQuestionAnswering":
			return importTaskVariant{task: "questionAnswering", label: "BERT token start/end span projection", prefixes: []string{"qa_outputs."}}
		}
	} else if family == "gpt2" || importRMSDecoder(family) {
		stem := strings.TrimSuffix(base, "Model")
		causal := stem + "ForCausalLM"
		if family == "gpt2" {
			causal = "GPT2LMHeadModel"
		}
		switch variant {
		case causal:
			return importTaskVariant{task: "causalLanguageModeling", label: importDisplayName(nil, family) + " vocabulary LM projection", prefixes: []string{"lm_head."}}
		case stem + "ForSequenceClassification":
			return importTaskVariant{task: "sequenceClassification", label: importDisplayName(nil, family) + " sequence score projection", prefixes: []string{"score."}, tokenPool: "last non-padding token"}
		case stem + "ForTokenClassification":
			return importTaskVariant{task: "tokenClassification", label: importDisplayName(nil, family) + " token classifier", prefixes: []string{"classifier."}}
		}
	} else if family == "vit" || family == "resnet" || family == "convnext" {
		if variant == strings.TrimSuffix(base, "Model")+"ForImageClassification" {
			v := importTaskVariant{task: "imageClassification", label: importDisplayName(nil, family) + " image classifier", prefixes: []string{"classifier."}}
			if family == "vit" {
				v.tokenPool = "CLS token selection"
			}
			return v
		}
	}
	if variant != "" {
		return importTaskVariant{boundary: "Architecture " + variant + " has an unhandled task variant; only the family backbone is represented."}
	}
	// Tensor evidence is used only when it unambiguously determines a task.
	if (family == "gpt2" || importRMSDecoder(family)) && evidence["lm_head."] && !evidence["score."] && !evidence["classifier."] {
		return importTaskVariant{task: "causalLanguageModeling", label: importDisplayName(nil, family) + " vocabulary LM projection", prefixes: []string{"lm_head."}}
	}
	if family == "bert" && evidence["cls.predictions."] && !evidence["cls.seq_relationship."] && !evidence["classifier."] {
		return importTaskVariant{task: "maskedLanguageModeling", label: "BERT masked-LM head: dense / activation / LayerNorm / vocabulary decoder", prefixes: []string{"cls.predictions."}}
	}
	if (family == "vit" || family == "resnet" || family == "convnext") && evidence["classifier."] {
		v := importTaskVariant{task: "imageClassification", label: importDisplayName(nil, family) + " image classifier", prefixes: []string{"classifier."}}
		if family == "vit" {
			v.tokenPool = "CLS token selection"
		}
		return v
	}
	return importTaskVariant{boundary: "Optional task head and pooling are unspecified; backbone inspection stops at this boundary."}
}

func makeImportedGraph(source ImportedSource) (ImportedGraph, error) {
	graph := ImportedGraph{Version: 1, Format: "umcode.imported.v1", Source: source, Nodes: []ImportedNode{}, Edges: []ImportedEdge{}}
	family, supported := importFamily(source.Config)
	variant := importVariant(source)
	c := source.Config
	// Prefix registration is linear in configured layer count. Each tensor
	// then performs at most one indexed lookup per dot boundary in its name,
	// rather than comparing against every expanded prefix in every block.
	matcherIndex := map[string]int{}
	add := func(key, kind, label string, config map[string]any, prefixes ...string) {
		if config == nil {
			config = map[string]any{}
		}
		index := len(graph.Nodes)
		id := fmt.Sprintf("import-%d", index+1)
		node := ImportedNode{ID: id, Kind: kind, Label: label, Position: ImportedPosition{X: float64(index%4) * 300, Y: float64(index/4) * 180}, Config: cloneImportConfig(config), BaselineConfig: cloneImportConfig(config), SourceKey: key, TensorNames: []string{}}
		graph.Nodes = append(graph.Nodes, node)
		for _, prefix := range prefixes {
			matcherIndex[prefix] = index
		}
	}
	if supported {
		add("input", "input", "Model input", map[string]any{"family": family})
		switch family {
		case "gpt2", "llama", "mistral", "qwen2", "bert", "vit", "mixtral", "granitemoe", "qwen2_moe", "qwen3_moe":
			layerKey, hiddenKey, headKey, defaultLayers, defaultHidden, defaultHeads := "num_hidden_layers", "hidden_size", "num_attention_heads", 12, 768, 12
			if family == "gpt2" {
				layerKey, hiddenKey, headKey = "n_layer", "n_embd", "n_head"
			}
			if importRMSDecoder(family) {
				defaultLayers, defaultHidden, defaultHeads = 32, 4096, 32
			}
			if family == "qwen2_moe" {
				defaultLayers, defaultHidden, defaultHeads = 24, 2048, 16
			}
			if family == "qwen3_moe" {
				defaultLayers, defaultHidden, defaultHeads = 24, 2048, 32
			}
			layers, e := importConfigInt(c, layerKey, defaultLayers)
			if e != nil || layers > 100000 || (importMoE(family) && layers > 128) {
				return graph, errors.New("invalid transformer layer count")
			}
			hidden, e := importConfigInt(c, hiddenKey, defaultHidden)
			if e != nil {
				return graph, e
			}
			heads, e := importConfigInt(c, headKey, defaultHeads)
			if e != nil || (hidden%heads != 0 && !(importMoE(family) && c["head_dim"] != nil)) {
				return graph, errors.New("hidden size must be divisible by attention heads")
			}
			defaultIntermediate := hidden * 4
			switch family {
			case "llama":
				defaultIntermediate = 11008
			case "mistral":
				defaultIntermediate = 14336
			case "qwen2":
				defaultIntermediate = 22016
			case "mixtral":
				defaultIntermediate = 14336
			case "granitemoe":
				defaultIntermediate = 11008
			case "qwen2_moe":
				defaultIntermediate = 5632
			case "qwen3_moe":
				defaultIntermediate = 6144
			}
			intermediate, e := importConfigInt(c, "intermediate_size", defaultIntermediate)
			if e != nil {
				return graph, e
			}
			cfg := map[string]any{"family": family, "hiddenSize": hidden, "attentionHeads": heads, "intermediateSize": intermediate, "residual": true}
			switch family {
			case "gpt2":
				if v, ok := c["n_inner"]; ok && v != nil {
					inner, e := importConfigInt(c, "n_inner", hidden*4)
					if e != nil {
						return graph, e
					}
					cfg["intermediateSize"] = inner
				}
				add("embeddings.token", "embedding", "GPT-2 token embedding", map[string]any{"hiddenSize": hidden, "vocabSize": importConfigValue(c, "vocab_size", 50257)}, "transformer.wte.", "wte.")
				add("embeddings.position", "position", "GPT-2 learned absolute positions", map[string]any{"maxPositions": importConfigValue(c, "n_positions", 1024)}, "transformer.wpe.", "wpe.")
				cfg["normalization"] = "pre LayerNorm"
				cfg["attention"] = "causal multi-head self-attention"
				cfg["projection"] = "Conv1D (transposed linear weights)"
				cfg["activation"] = importConfigValue(c, "activation_function", "gelu_new")
				cfg["layerNormEpsilon"] = importConfigValue(c, "layer_norm_epsilon", 1e-5)
				cfg["scaleAttentionWeights"] = importConfigValue(c, "scale_attn_weights", true)
				cfg["scaleAttentionByInverseLayer"] = importConfigValue(c, "scale_attn_by_inverse_layer_idx", false)
				cfg["reorderAndUpcastAttention"] = importConfigValue(c, "reorder_and_upcast_attn", false)
			case "llama", "mistral", "qwen2", "mixtral", "granitemoe", "qwen2_moe", "qwen3_moe":
				defaultKV := heads
				if family == "mistral" || family == "mixtral" {
					defaultKV = 8
				}
				if family == "qwen3_moe" {
					defaultKV = 4
				}
				kv, e := importConfigInt(c, "num_key_value_heads", defaultKV)
				if e != nil || heads%kv != 0 {
					return graph, errors.New("invalid grouped-query key/value head count")
				}
				defaultVocab := 32000
				if family == "qwen2" || family == "qwen2_moe" || family == "qwen3_moe" {
					defaultVocab = 151936
				}
				add("embeddings.token", "embedding", importDisplayName(c, family)+" token embedding", map[string]any{"hiddenSize": hidden, "vocabSize": importConfigValue(c, "vocab_size", defaultVocab)}, "model.embed_tokens.", "embed_tokens.")
				cfg["normalization"] = "pre RMSNorm"
				cfg["rmsNormEpsilon"] = importConfigValue(c, "rms_norm_eps", 1e-6)
				cfg["attention"] = "causal grouped-query self-attention"
				cfg["keyValueHeads"] = kv
				cfg["positions"] = "rotary (RoPE)"
				cfg["ropeTheta"] = importConfigValue(c, "rope_theta", 10000)
				cfg["ropeScaling"] = importConfigValue(c, "rope_scaling", "none")
				cfg["mlp"] = "gated MLP (SwiGLU)"
				cfg["activation"] = importConfigValue(c, "hidden_act", "silu")
				cfg["attentionBias"] = importConfigValue(c, "attention_bias", family == "qwen2")
				if family == "mistral" {
					cfg["slidingWindow"] = importConfigValue(c, "sliding_window", 4096)
				}
				if family == "qwen2" {
					cfg["useSlidingWindow"] = importConfigValue(c, "use_sliding_window", false)
					cfg["maxWindowLayers"] = importConfigValue(c, "max_window_layers", 28)
				}
				if importMoE(family) {
					if err := configureImportMoE(c, cfg, family, layers, hidden, heads); err != nil {
						return graph, err
					}
				}
			case "bert":
				add("embeddings", "embedding", "BERT word + position + token-type embeddings / LayerNorm", map[string]any{"hiddenSize": hidden, "positions": "learned absolute", "maxPositions": importConfigValue(c, "max_position_embeddings", 512), "typeVocabSize": importConfigValue(c, "type_vocab_size", 2)}, "bert.embeddings.", "embeddings.")
				cfg["normalization"] = "post LayerNorm"
				cfg["layerNormEpsilon"] = importConfigValue(c, "layer_norm_eps", 1e-12)
				cfg["attention"] = "bidirectional multi-head self-attention"
				cfg["activation"] = importConfigValue(c, "hidden_act", "gelu")
				cfg["isDecoder"] = importConfigValue(c, "is_decoder", false)
				cfg["crossAttention"] = importConfigValue(c, "add_cross_attention", false)
				if cfg["isDecoder"] == true {
					cfg["attention"] = "causal multi-head self-attention (BERT decoder configuration)"
				}
			case "vit":
				add("embeddings", "embedding", "ViT patch projection + CLS token + learned positions", map[string]any{"hiddenSize": hidden, "patchSize": importConfigValue(c, "patch_size", 16), "imageSize": importConfigValue(c, "image_size", 224), "channels": importConfigValue(c, "num_channels", 3)}, "vit.embeddings.", "embeddings.")
				cfg["normalization"] = "pre LayerNorm"
				cfg["layerNormEpsilon"] = importConfigValue(c, "layer_norm_eps", 1e-12)
				cfg["attention"] = "bidirectional multi-head self-attention"
				cfg["qkvBias"] = importConfigValue(c, "qkv_bias", true)
				cfg["activation"] = importConfigValue(c, "hidden_act", "gelu")
			}
			group := (layers + 127) / 128
			for start := 0; start < layers; start += group {
				end := start + group
				if end > layers {
					end = layers
				}
				settings := cloneImportConfig(cfg)
				if importMoE(family) && !importMoELayer(c, family, start) {
					settings["mlp"] = "gated MLP (SwiGLU)"
				}
				settings["layerStart"] = start
				settings["layerEnd"] = end - 1
				settings["repeatCount"] = end - start
				label := importDisplayName(nil, family) + fmt.Sprintf(" block %d", start)
				if end > start+1 {
					label = importDisplayName(nil, family) + fmt.Sprintf(" blocks %d–%d", start, end-1)
				}
				prefixes := []string{}
				for i := start; i < end; i++ {
					switch family {
					case "gpt2":
						prefixes = append(prefixes, fmt.Sprintf("transformer.h.%d.", i), fmt.Sprintf("h.%d.", i))
					case "bert":
						prefixes = append(prefixes, fmt.Sprintf("bert.encoder.layer.%d.", i), fmt.Sprintf("encoder.layer.%d.", i))
					case "vit":
						prefixes = append(prefixes, fmt.Sprintf("vit.encoder.layer.%d.", i), fmt.Sprintf("encoder.layer.%d.", i))
					default:
						prefixes = append(prefixes, fmt.Sprintf("model.layers.%d.", i), fmt.Sprintf("layers.%d.", i))
					}
				}
				add(fmt.Sprintf("layers.%d.%d", start, end-1), "transformer", label, settings, prefixes...)
			}
			switch family {
			case "gpt2":
				add("final_norm", "normalization", "GPT-2 final LayerNorm", map[string]any{"normalization": "LayerNorm", "hiddenSize": hidden}, "transformer.ln_f.", "ln_f.")
			case "llama", "mistral", "qwen2", "mixtral", "granitemoe", "qwen2_moe", "qwen3_moe":
				add("final_norm", "normalization", importDisplayName(nil, family)+" final RMSNorm", map[string]any{"normalization": "RMSNorm", "hiddenSize": hidden}, "model.norm.", "norm.")
			case "bert":
				if variant.pooler {
					add("pooler", "pool", "BERT CLS pooler (dense + tanh)", map[string]any{"hiddenSize": hidden, "activation": "tanh"}, "bert.pooler.", "pooler.")
				}
			case "vit":
				add("final_norm", "normalization", "ViT final LayerNorm", map[string]any{"hiddenSize": hidden, "normalization": "LayerNorm"}, "vit.layernorm.", "layernorm.")
				if variant.pooler {
					add("pooler", "pool", "ViT CLS pooler (dense + tanh)", map[string]any{"hiddenSize": hidden, "activation": "tanh"}, "vit.pooler.", "pooler.")
				}
			}
			if variant.tokenPool != "" {
				add("head_pool", "pool", variant.tokenPool, map[string]any{"operation": variant.tokenPool})
			}
			if variant.task != "" {
				settings := map[string]any{"hiddenSize": hidden, "task": variant.task, "tiedEmbeddings": importConfigValue(c, "tie_word_embeddings", family == "gpt2" || family == "bert")}
				if variant.task == "maskedLanguageModeling" {
					settings["activation"] = importConfigValue(c, "hidden_act", "gelu")
					settings["normalization"] = "LayerNorm"
				}
				add("head", "linear", variant.label, settings, variant.prefixes...)
			}
		case "resnet", "convnext":
			defaultDepths, defaultSizes := []int{3, 4, 6, 3}, []int{256, 512, 1024, 2048}
			if family == "convnext" {
				defaultDepths, defaultSizes = []int{3, 3, 9, 3}, []int{96, 192, 384, 768}
			}
			depths, e := importConfigInts(c, "depths", defaultDepths)
			if e != nil {
				return graph, e
			}
			sizes, e := importConfigInts(c, "hidden_sizes", defaultSizes)
			if e != nil || len(depths) != len(sizes) {
				return graph, errors.New("CNN stage depths and hidden sizes must match")
			}
			total := 0
			for _, d := range depths {
				total += d
			}
			if total > 100000 {
				return graph, errors.New("CNN depth exceeds limit")
			}
			if family == "resnet" {
				add("stem", "conv2d", "ResNet stem: 7×7 conv / BatchNorm / ReLU / max pool", map[string]any{"kernelSize": 7, "stride": 2, "poolKernel": 3, "poolStride": 2, "channels": importConfigValue(c, "num_channels", 3), "outChannels": importConfigValue(c, "embedding_size", 64), "normalization": "BatchNorm2d"}, "resnet.embedder.", "embedder.")
			} else {
				add("stem", "conv2d", "ConvNeXT patch stem: conv / channels-first LayerNorm", map[string]any{"patchSize": importConfigValue(c, "patch_size", 4), "channels": importConfigValue(c, "num_channels", 3), "outChannels": sizes[0]}, "convnext.embeddings.", "embeddings.")
			}
			group := (total + 119) / 120
			for stage, depth := range depths {
				for start := 0; start < depth; start += group {
					end := start + group
					if end > depth {
						end = depth
					}
					settings := map[string]any{"family": family, "stage": stage, "blockStart": start, "blockEnd": end - 1, "repeatCount": end - start, "outChannels": sizes[stage], "residual": true}
					label := importDisplayName(nil, family) + fmt.Sprintf(" stage %d block %d", stage, start)
					if end > start+1 {
						label = importDisplayName(nil, family) + fmt.Sprintf(" stage %d blocks %d–%d", stage, start, end-1)
					}
					if family == "resnet" {
						settings["blockType"] = importConfigValue(c, "layer_type", "bottleneck")
						settings["normalization"] = "BatchNorm2d"
						settings["activation"] = importConfigValue(c, "hidden_act", "relu")
						settings["downsampleInFirstStage"] = importConfigValue(c, "downsample_in_first_stage", false)
						settings["downsampleInBottleneck"] = importConfigValue(c, "downsample_in_bottleneck", false)
					} else {
						settings["depthwiseKernel"] = 7
						settings["normalization"] = "channels-last LayerNorm"
						settings["mlpExpansion"] = 4
						settings["activation"] = importConfigValue(c, "hidden_act", "gelu")
						settings["layerScaleInit"] = importConfigValue(c, "layer_scale_init_value", 1e-6)
						settings["stageDownsampling"] = "LayerNorm + stride-2 conv before stage blocks"
					}
					prefixes := []string{}
					for i := start; i < end; i++ {
						prefixes = append(prefixes, fmt.Sprintf("%s.encoder.stages.%d.layers.%d.", family, stage, i), fmt.Sprintf("encoder.stages.%d.layers.%d.", stage, i))
					}
					if start == 0 {
						prefixes = append(prefixes, fmt.Sprintf("%s.encoder.stages.%d.downsampling_layer.", family, stage), fmt.Sprintf("encoder.stages.%d.downsampling_layer.", stage))
					}
					add(fmt.Sprintf("stages.%d.blocks.%d.%d", stage, start, end-1), "cnnblock", label, settings, prefixes...)
				}
			}
			add("pooler", "pool", importDisplayName(nil, family)+" global average pooling", map[string]any{"operation": "global average pooling"})
			if family == "convnext" {
				add("final_norm", "normalization", "ConvNeXT final LayerNorm", map[string]any{"hiddenSize": sizes[len(sizes)-1], "normalization": "LayerNorm"}, "convnext.layernorm.", "layernorm.")
			}
			if variant.task != "" {
				add("head", "linear", variant.label, map[string]any{"inFeatures": sizes[len(sizes)-1], "numLabels": importConfigValue(c, "num_labels", 1000), "task": variant.task}, variant.prefixes...)
			}
		}
		if variant.boundary != "" {
			add("variant_boundary", "unknown", "Incomplete task boundary", map[string]any{"readOnly": true, "reason": variant.boundary})
		}
		add("output", "output", "Model output", map[string]any{"family": family})
	} else {
		groups := map[string]bool{}
		for _, t := range source.Tensors {
			parts := strings.Split(t.Name, ".")
			key := parts[0]
			if len(parts) > 1 {
				key += "." + parts[1]
			}
			groups[key] = true
		}
		keys := []string{}
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 128 {
			keys = keys[:127]
			keys = append(keys, "(remaining hierarchy)")
		}
		for i, k := range keys {
			prefixes := []string{k + "."}
			if i == len(keys)-1 && k == "(remaining hierarchy)" {
				prefixes = nil
			}
			add("hierarchy."+strconv.Itoa(i), "unknown", k, map[string]any{"readOnly": true}, prefixes...)
		}
		if len(graph.Nodes) == 0 {
			add("hierarchy", "unknown", "Architecture config (unknown family)", map[string]any{"readOnly": true})
		}
	}
	extra := []string{}
	for _, tensor := range source.Tensors {
		best := -1
		for end := len(tensor.Name) - 1; end >= 0; end-- {
			if tensor.Name[end] != '.' {
				continue
			}
			if index, ok := matcherIndex[tensor.Name[:end+1]]; ok {
				best = index
				break
			}
		}
		if best < 0 {
			extra = append(extra, tensor.Name)
		} else {
			graph.Nodes[best].TensorNames = append(graph.Nodes[best].TensorNames, tensor.Name)
		}
	}
	if len(extra) > 0 {
		add("unassigned_tensors", "unknown", fmt.Sprintf("Additional source tensors (%d)", len(extra)), map[string]any{"readOnly": true, "tensorCount": len(extra)})
		graph.Nodes[len(graph.Nodes)-1].TensorNames = extra
	}
	if len(graph.Nodes) > 200 {
		return graph, errors.New("imported graph exceeds 200 nodes")
	}
	if supported {
		for i := 1; i < len(graph.Nodes); i++ {
			if graph.Nodes[i].Kind == "unknown" || graph.Nodes[i-1].Kind == "unknown" {
				continue
			}
			graph.Edges = append(graph.Edges, ImportedEdge{ID: fmt.Sprintf("edge-%d", i), Source: graph.Nodes[i-1].ID, Target: graph.Nodes[i].ID})
		}
	}
	graph.Source.BaselineNodes = []ImportedBaselineNode{}
	for _, n := range graph.Nodes {
		graph.Source.BaselineNodes = append(graph.Source.BaselineNodes, ImportedBaselineNode{ID: n.ID, Kind: n.Kind, Config: cloneImportConfig(n.Config), TensorNames: append([]string{}, n.TensorNames...), SourceKey: n.SourceKey})
	}
	return graph, nil
}

// These checks surface obvious incompatibilities without pretending that a header
// inspection establishes complete architecture or payload compatibility.
func importCompatibilityWarnings(config map[string]any, tensors []ImportTensor) []string {
	family, supported := importFamily(config)
	if !supported {
		return nil
	}
	warnings := []string{}
	integer := func(key string, fallback int) int64 {
		n, e := importConfigInt(config, key, fallback)
		if e != nil {
			return 0
		}
		return int64(n)
	}
	hidden := integer("hidden_size", 768)
	if family == "gpt2" {
		hidden = integer("n_embd", 768)
	}
	if family == "llama" || family == "mistral" || family == "qwen2" {
		hidden = integer("hidden_size", 4096)
	}
	vocab := int64(0)
	if _, ok := config["vocab_size"]; ok {
		vocab = integer("vocab_size", 0)
	}
	sizes, _ := importConfigInts(config, "hidden_sizes", []int{96, 192, 384, 768})
	channels := integer("num_channels", 3)
	for _, tensor := range tensors {
		n := tensor.Name
		shape := tensor.Shape
		expected := []int64{}
		reason := ""
		switch family {
		case "gpt2":
			if strings.HasSuffix(n, "wte.weight") {
				expected = []int64{vocab, hidden}
				reason = "token embedding"
			} else if strings.Contains(n, ".attn.c_attn.weight") {
				expected = []int64{hidden, hidden * 3}
				reason = "fused QKV Conv1D"
			}
		case "llama", "mistral", "qwen2":
			if strings.HasSuffix(n, "embed_tokens.weight") {
				expected = []int64{vocab, hidden}
				reason = "token embedding"
			} else if strings.HasSuffix(n, "mlp.down_proj.weight") {
				defaultInner := map[string]int{"llama": 11008, "mistral": 14336, "qwen2": 22016}[family]
				expected = []int64{hidden, integer("intermediate_size", defaultInner)}
				reason = "MLP down projection"
			}
		case "bert":
			if strings.HasSuffix(n, "word_embeddings.weight") {
				expected = []int64{vocab, hidden}
				reason = "word embedding"
			}
		case "vit":
			if strings.HasSuffix(n, "patch_embeddings.projection.weight") {
				expected = []int64{hidden, channels, 0, 0}
				reason = "patch projection"
			}
		case "resnet":
			if strings.HasSuffix(n, "embedder.embedder.convolution.weight") {
				expected = []int64{integer("embedding_size", 64), channels, 7, 7}
				reason = "convolution stem"
			} else if strings.Contains(n, "encoder.stages.") && strings.HasSuffix(n, ".convolution.weight") {
				stageSizes, _ := importConfigInts(config, "hidden_sizes", []int{256, 512, 1024, 2048})
				parts := strings.Split(n, ".")
				for i, part := range parts {
					if part != "stages" || i+1 >= len(parts) {
						continue
					}
					stage, err := strconv.Atoi(parts[i+1])
					if err != nil || stage < 0 || stage >= len(stageSizes) {
						break
					}
					out := int64(stageSizes[stage])
					layerType := importConfigValue(config, "layer_type", "bottleneck")
					if layerType == "bottleneck" && (strings.Contains(n, ".layer.0.") || strings.Contains(n, ".layer.1.")) {
						out /= 4
					}
					expected = []int64{out, 0, 0, 0}
					reason = "residual-stage convolution"
					break
				}
			}
		case "convnext":
			if strings.HasSuffix(n, "embeddings.patch_embeddings.weight") && len(sizes) > 0 {
				expected = []int64{int64(sizes[0]), channels, integer("patch_size", 4), integer("patch_size", 4)}
				reason = "patch stem"
			}
		}
		if expected == nil || len(expected) == 0 {
			continue
		}
		mismatch := len(shape) != len(expected)
		if !mismatch {
			for i, want := range expected {
				if want > 0 && shape[i] != want {
					mismatch = true
				}
			}
		}
		if mismatch {
			warnings = append(warnings, fmt.Sprintf("Config/tensor shape mismatch for %s (%s): actual %v, configured %v (zero means unspecified). Architecture edits and source weights require compatibility review.", n, reason, shape, expected))
			if len(warnings) >= 20 {
				warnings = append(warnings, "Further configuration/tensor mismatches omitted from this warning list; every source tensor remains available in the manifest.")
				break
			}
		}
	}
	return warnings
}
