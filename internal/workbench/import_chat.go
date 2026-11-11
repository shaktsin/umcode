package workbench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Identity pins an immutable bundle/checkpoint manifest. Payload hashes are
// checked by Infer before execution; catalog reads do not load model weights.
func ImportedChatIdentity(b BundleTarget, checkpoint string) (string, int, error) {
	task, fingerprint, err := importedFingerprint(b)
	if err != nil {
		return "", 0, err
	}
	if task != "text-generation" {
		return "", 0, errors.New("only imported text-generation models can be promoted")
	}
	root, err := b.model()
	if err != nil {
		return "", 0, err
	}
	defer root.Close()
	if checkpoint != "" {
		if !safeDataPath(checkpoint) || strings.Contains(checkpoint, "/") {
			return "", 0, errors.New("invalid checkpoint")
		}
		parent, err := openDataDirectory(root, "import-checkpoints", false)
		if err != nil {
			return "", 0, err
		}
		defer parent.Close()
		folder, err := openDataDirectory(parent, checkpoint, false)
		if err != nil {
			return "", 0, err
		}
		defer folder.Close()
		raw, err := readDataFile(folder, "manifest.json", 64<<10)
		if err != nil {
			return "", 0, err
		}
		var manifest bundleManifest
		if json.Unmarshal(raw, &manifest) != nil || validateBundleManifest(manifest.Files) != nil || len(manifest.Digests) != len(manifest.Files) {
			return "", 0, errors.New("invalid checkpoint manifest")
		}
		fingerprint += "/" + checkpoint + "/" + string(raw)
	}
	raw, err := readDataFile(root, "graph.json", maxImportedGraphBytes)
	if err != nil {
		return "", 0, err
	}
	var graph ImportedGraph
	if err = json.Unmarshal(raw, &graph); err != nil {
		return "", 0, err
	}
	key := "max_position_embeddings"
	if graph.Source.Family == "gpt2" {
		key = "n_positions"
	}
	// Read via the normal number-preserving parser used by graph validation.
	configRaw, _ := json.Marshal(graph.Source.Config)
	config, err := importObject(string(configRaw), importJSONLimit)
	if err != nil {
		return "", 0, err
	}
	window, err := importConfigInt(config, key, 2048)
	if err != nil {
		return "", 0, err
	}
	window = min(window, 2048)
	digest := sha256.Sum256([]byte(fingerprint))
	return hex.EncodeToString(digest[:]), window, nil
}
