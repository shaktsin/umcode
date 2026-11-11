package workbench

import "fmt"

// Build support is deliberately independent of the execution-runtime allowlist.
func importMoE(family string) bool {
	switch family {
	case "mixtral", "granitemoe", "qwen2_moe", "qwen3_moe":
		return true
	}
	return false
}
func importRMSDecoder(family string) bool {
	return family == "llama" || family == "mistral" || family == "qwen2" || importMoE(family)
}

func configureImportMoE(c, cfg map[string]any, family string, layers, hidden, heads int) error {
	expertKey, expertsDefault, topDefault := "num_local_experts", 8, 2
	innerKey, innerDefault := "intermediate_size", hidden*4
	if family == "qwen2_moe" {
		expertKey, expertsDefault, topDefault = "num_experts", 60, 4
		innerKey, innerDefault = "moe_intermediate_size", 1408
	}
	if family == "qwen3_moe" {
		expertKey, expertsDefault, topDefault = "num_experts", 128, 8
		innerKey, innerDefault = "moe_intermediate_size", 768
	}
	experts, err := importConfigInt(c, expertKey, expertsDefault)
	if err != nil || experts > 4096 {
		return fmt.Errorf("invalid MoE expert count")
	}
	top, err := importConfigInt(c, "num_experts_per_tok", topDefault)
	if err != nil || top > experts {
		return fmt.Errorf("MoE experts per token must be between 1 and expert count")
	}
	inner, err := importConfigInt(c, innerKey, innerDefault)
	if err != nil {
		return err
	}
	head := hidden / heads
	if c["head_dim"] != nil {
		head, err = importConfigInt(c, "head_dim", head)
		if err != nil {
			return err
		}
	}
	if head < 1 {
		return fmt.Errorf("invalid attention head dimension")
	}
	cfg["mlp"], cfg["expertCount"], cfg["expertsPerToken"], cfg["expertIntermediateSize"] = "sparse MoE", experts, top, inner
	cfg["headDimension"], cfg["queryKeyNorm"] = head, family == "qwen3_moe"
	cfg["router"] = "softmax top-k"
	cfg["normalizeTopK"] = importConfigValue(c, "norm_topk_prob", family == "mixtral" || family == "granitemoe")
	if family == "mixtral" {
		cfg["slidingWindow"] = importConfigValue(c, "sliding_window", "none")
		cfg["rmsNormEpsilon"] = importConfigValue(c, "rms_norm_eps", 1e-5)
		cfg["ropeTheta"] = importConfigValue(c, "rope_theta", 1e6)
	}
	if family == "granitemoe" {
		cfg["expertStorage"] = "packed expert tensors"
		cfg["residualMultiplier"] = importConfigValue(c, "residual_multiplier", 1.0)
		cfg["attentionMultiplier"] = importConfigValue(c, "attention_multiplier", 1.0)
	}
	if family == "qwen2_moe" || family == "qwen3_moe" {
		step, err := importConfigInt(c, "decoder_sparse_step", 1)
		if err != nil {
			return err
		}
		cfg["decoderSparseStep"] = step
		if value, ok := c["mlp_only_layers"]; ok {
			list, ok := value.([]any)
			if !ok {
				return fmt.Errorf("invalid mlp_only_layers")
			}
			for _, v := range list {
				n, err := importInteger(v)
				if err != nil || n >= int64(layers) {
					return fmt.Errorf("invalid dense-only layer index")
				}
			}
		}
		if family == "qwen2_moe" {
			shared, err := importConfigInt(c, "shared_expert_intermediate_size", 5632)
			if err != nil {
				return err
			}
			cfg["sharedExpertIntermediateSize"] = shared
			cfg["attentionBias"] = true
		}
	}
	return nil
}

func importMoELayer(c map[string]any, family string, index int) bool {
	if family != "qwen2_moe" && family != "qwen3_moe" {
		return true
	}
	step, _ := importConfigInt(c, "decoder_sparse_step", 1)
	if (index+1)%step != 0 {
		return false
	}
	list, _ := c["mlp_only_layers"].([]any)
	for _, v := range list {
		n, _ := importInteger(v)
		if n == int64(index) {
			return false
		}
	}
	return true
}
