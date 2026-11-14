package workbench

import (
	"encoding/json"
	"fmt"
)

// TrainingCompatibilityIssues checks saved preparation against the saved graph
// before launching Python. It complements graph-only validation in Build.
func TrainingCompatibilityIssues(graph json.RawMessage, selection DataSelection) []string {
	if IsImportedGraph(graph) {
		return nil
	}
	var parsed struct {
		Nodes []struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Config struct {
				Mode     string `json:"mode"`
				Sequence int    `json:"sequence"`
				Features int    `json:"features"`
				Vocab    int    `json:"vocab"`
			} `json:"config"`
		} `json:"nodes"`
	}
	if json.Unmarshal(graph, &parsed) != nil {
		return nil
	}
	issues := []string{}
	for _, node := range parsed.Nodes {
		switch node.Kind {
		case "input":
			if selection.Config.Mode == "text" && (node.Config.Mode != "tokens" || node.Config.Sequence != selection.Config.SequenceLength) {
				issues = append(issues, fmt.Sprintf("Saved text preparation needs Input sequence length %d in Build; save the graph before training.", selection.Config.SequenceLength))
			}
			if selection.Config.Mode == "features" && (node.Config.Mode != "features" || node.Config.Features != len(selection.Config.InputColumns)) {
				issues = append(issues, fmt.Sprintf("Saved numeric preparation needs Input features %d in Build; save the graph before training.", len(selection.Config.InputColumns)))
			}
		case "embedding":
			if selection.Config.Mode == "text" && node.Config.Vocab != 259 {
				issues = append(issues, fmt.Sprintf("Saved UTF-8 byte preparation needs Embedding vocabulary 259 in Build (layer %s is %d); save the graph before training.", node.ID, node.Config.Vocab))
			}
		}
	}
	return issues
}
