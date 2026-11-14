package workbench

import (
	"encoding/json"
	"math"
)

const trainingActivationElements = 8_000_000
const trainingBatchWorkLimit = 500_000_000

// TrainingMaxBatchSize estimates peak training memory for a single-path native
// token or feature graph. A separate per-batch CPU work cap still applies.
// Unknown memory falls back to the old conservative activation cap.
func TrainingMaxBatchSize(raw json.RawMessage, selection DataSelection, availableBytes uint64) (int, bool) {
	var graph struct {
		Nodes []struct {
			ID     string         `json:"id"`
			Kind   string         `json:"kind"`
			Config map[string]any `json:"config"`
		} `json:"nodes"`
		Edges []struct {
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"edges"`
	}
	if json.Unmarshal(raw, &graph) != nil || len(graph.Nodes) < 2 || len(graph.Edges) != len(graph.Nodes)-1 {
		return 0, false
	}
	nodes := map[string]int{}
	input := -1
	for i, n := range graph.Nodes {
		if n.ID == "" || nodes[n.ID] != 0 {
			return 0, false
		}
		nodes[n.ID] = i + 1
		if n.Kind == "input" {
			if input >= 0 {
				return 0, false
			}
			input = i
		}
	}
	if input < 0 {
		return 0, false
	}
	next := map[string]string{}
	incoming := map[string]bool{}
	for _, edge := range graph.Edges {
		if nodes[edge.Source] == 0 || nodes[edge.Target] == 0 || next[edge.Source] != "" || incoming[edge.Target] {
			return 0, false
		}
		next[edge.Source] = edge.Target
		incoming[edge.Target] = true
	}
	setting := func(config map[string]any, key string, fallback int) (int, bool) {
		value, exists := config[key]
		if !exists {
			return fallback, true
		}
		number, ok := value.(float64)
		if !ok || number < 1 || number > 1_000_000 || math.Trunc(number) != number {
			return 0, false
		}
		return int(number), true
	}
	current := graph.Nodes[input].ID
	seen := map[string]bool{}
	lastKind := ""
	dims := []float64{}
	activations := float64(0)
	parameters := float64(0)
	work := float64(0)
	inputElements := float64(0)
	for current != "" && !seen[current] {
		seen[current] = true
		node := graph.Nodes[nodes[current]-1]
		lastKind = node.Kind
		c := node.Config
		var value int
		var ok bool
		switch node.Kind {
		case "input":
			if c["mode"] == "tokens" {
				value, ok = setting(c, "sequence", 8)
			} else if c["mode"] == "features" {
				value, ok = setting(c, "features", 16)
			} else {
				return 0, false
			}
			if !ok {
				return 0, false
			}
			dims = []float64{float64(value)}
			inputElements = float64(value)
		case "embedding":
			if len(dims) != 1 {
				return 0, false
			}
			value, ok = setting(c, "width", 16)
			if !ok {
				return 0, false
			}
			dims = append(dims, float64(value))
			vocab, good := setting(c, "vocab", 32)
			if !good {
				return 0, false
			}
			parameters += float64(vocab * value)
		case "linear":
			prior := float64(1)
			for _, dimension := range dims {
				prior *= dimension
			}
			width := dims[len(dims)-1]
			value, ok = setting(c, "outFeatures", 64)
			if !ok {
				return 0, false
			}
			dims[len(dims)-1] = float64(value)
			parameters += width*float64(value) + float64(value)
			work += prior * float64(value)
		case "attention", "transformer":
			if len(dims) != 2 {
				return 0, false
			}
			heads, good := setting(c, "heads", 4)
			if !good {
				return 0, false
			}
			repeat := 1
			expansion := 0
			if node.Kind == "transformer" {
				repeat, good = setting(c, "repeat", 2)
				if !good {
					return 0, false
				}
				expansion, good = setting(c, "expansion", 4)
				if !good {
					return 0, false
				}
			}
			if node.Kind == "transformer" {
				parameters += float64(repeat) * ((4+2*float64(expansion))*dims[1]*dims[1] + (9+float64(expansion))*dims[1])
				work += float64(repeat) * ((4+2*float64(expansion))*dims[0]*dims[1]*dims[1] + 2*dims[0]*dims[0]*dims[1])
			} else {
				parameters += 4*dims[1]*dims[1] + 4*dims[1]
				work += 4*dims[0]*dims[1]*dims[1] + 2*dims[0]*dims[0]*dims[1]
			}
			activations += float64(repeat) * (dims[0]*dims[0]*float64(heads)*2 + dims[0]*dims[1]*float64(9+2*expansion))
		case "meanpool":
			if len(dims) != 2 {
				return 0, false
			}
			dims = dims[1:]
		case "flatten":
			product := float64(1)
			for _, dimension := range dims {
				product *= dimension
			}
			dims = []float64{product}
		case "position":
			value, ok = setting(c, "length", 128)
			if !ok {
				return 0, false
			}
			parameters += float64(value) * dims[len(dims)-1]
		case "layernorm":
			parameters += 2 * dims[len(dims)-1]
		case "gelu", "relu", "dropout", "output":
		default:
			return 0, false
		}
		product := float64(1)
		for _, dimension := range dims {
			product *= dimension
		}
		activations += product
		current = next[current]
	}
	if len(seen) != len(graph.Nodes) || lastKind != "output" {
		return 0, false
	}
	if activations <= 0 || math.IsInf(activations, 0) || math.IsNaN(activations) {
		return 0, false
	}
	max := 256
	if availableBytes == 0 {
		max = min(max, int(float64(trainingActivationElements)/activations))
	} else {
		// Reserve memory for the operating system and other apps. The estimate
		// includes PyTorch startup, dataset tensors, parameters/Adam state, and
		// four copies of forward/backward activations.
		budget := float64(availableBytes) * .25
		rows := float64(selection.Counts.Train + selection.Counts.Validation + selection.Counts.Test)
		dataset := rows * (inputElements*8 + 8) * 2
		base := float64(256<<20) + dataset + parameters*16
		memoryMax := math.Floor((budget - base) / (activations * 4 * 4))
		if memoryMax < 0 {
			memoryMax = 0
		}
		max = min(max, int(memoryMax))
	}
	if work > 0 {
		max = min(max, int(float64(trainingBatchWorkLimit)/work))
	}
	return max, true
}
