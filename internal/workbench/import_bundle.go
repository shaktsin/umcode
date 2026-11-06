package workbench

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const BundleMaxBytes int64 = 8 << 30
const BundleChunkBytes = 1 << 20

var bundleMu sync.Mutex

type BundleTarget struct{ ProjectRoot, ModelID, CreatedAt string }
type bundleManifest struct {
	Files   []ImportSourceFile `json:"files"`
	Digests map[string]string  `json:"digests,omitempty"`
}

func BundleFileKind(name string) string {
	if name == "" || filepath.Base(name) != name || !safeDataPath(name) {
		return ""
	}
	switch name {
	case "config.json":
		return "config"
	case "tokenizer.json", "tokenizer_config.json", "special_tokens_map.json", "added_tokens.json", "vocab.json", "vocab.txt", "merges.txt", "tokenizer.model", "sentencepiece.bpe.model":
		return "tokenizer"
	case "preprocessor_config.json", "processor_config.json", "feature_extractor_config.json":
		return "processor"
	case "generation_config.json":
		return "generation"
	}
	if strings.HasSuffix(name, ".safetensors.index.json") {
		return "index"
	}
	if strings.HasSuffix(name, ".safetensors") {
		return "weights"
	}
	return ""
}
func RequireImportPair(files []ImportFile) error {
	config, weight := false, false
	for _, f := range files {
		config = config || f.Kind == "config"
		weight = weight || f.Kind == "weights"
	}
	if !config {
		return errors.New("Missing config.json. Select it together with the safetensors weights or choose the complete model folder.")
	}
	if !weight {
		return errors.New("Missing safetensors weights. Select them together with config.json or choose the complete model folder.")
	}
	return nil
}
func validateBundleManifest(files []ImportSourceFile) error {
	if len(files) < 2 || len(files) > 160 {
		return errors.New("Select config.json and all safetensors weights; at most 160 model files are supported.")
	}
	var total int64
	seen := map[string]bool{}
	pair := []ImportFile{}
	for _, f := range files {
		kind := BundleFileKind(f.Name)
		if kind == "" || seen[f.Name] || f.Size <= 0 || f.Size > BundleMaxBytes {
			return errors.New("invalid, duplicate or oversized model file")
		}
		if kind != "weights" && f.Size > 64<<20 {
			return errors.New("model metadata file exceeds 64 MiB")
		}
		if (kind == "config" || kind == "index") && f.Size > 2<<20 {
			return errors.New("configuration/index exceeds 2 MiB")
		}
		seen[f.Name] = true
		total += f.Size
		pair = append(pair, ImportFile{Kind: kind})
	}
	if total > BundleMaxBytes {
		return errors.New("local model folders support at most 8 GiB")
	}
	return RequireImportPair(pair)
}
func (b BundleTarget) model() (*os.Root, error) {
	r, _, e := openCheckpointModel(b.ProjectRoot, b.ModelID, b.CreatedAt)
	return r, e
}
func (b BundleTarget) Begin(files []ImportSourceFile) (string, error) {
	bundleMu.Lock()
	defer bundleMu.Unlock()
	if err := validateBundleManifest(files); err != nil {
		return "", err
	}
	root, e := b.model()
	if e != nil {
		return "", e
	}
	defer root.Close()
	if _, e = root.Lstat("pretrained"); e == nil {
		return "", errors.New("This model already has a complete folder. Import a new copy to replace it.")
	}
	uploads, e := root.OpenRoot(".")
	if e != nil {
		return "", e
	}
	defer uploads.Close()
	// Bound abandoned uploads; cancellation removes its own stage.
	dirs, e := fs.ReadDir(uploads.FS(), ".")
	if e != nil {
		return "", e
	}
	count := 0
	for _, d := range dirs {
		if strings.HasPrefix(d.Name(), "upload-") {
			count++
		}
	}
	if count >= 2 {
		return "", errors.New("Two uploads are already staged. Cancel an earlier upload or import another model.")
	}
	id, stage, e := createDataStage(uploads, "upload-")
	if e != nil {
		return "", e
	}
	defer stage.Close()
	if e = writeNewDataJSON(stage, "manifest.json", bundleManifest{Files: files}); e != nil {
		return "", e
	}
	return id, nil
}
func (b BundleTarget) stage(id string) (*os.Root, *os.Root, *os.Root, error) {
	if !strings.HasPrefix(id, "upload-") || !safeDataPath(id) || strings.Contains(id, "/") {
		return nil, nil, nil, errors.New("invalid model upload")
	}
	root, e := b.model()
	if e != nil {
		return nil, nil, nil, e
	}
	uploads, e := root.OpenRoot(".")
	if e != nil {
		root.Close()
		return nil, nil, nil, e
	}
	stage, e := openDataDirectory(uploads, id, false)
	if e != nil {
		root.Close()
		uploads.Close()
		return nil, nil, nil, e
	}
	return root, uploads, stage, nil
}
func (b BundleTarget) Chunk(id, name string, offset int64, encoded string) error {
	bundleMu.Lock()
	defer bundleMu.Unlock()
	if BundleFileKind(name) == "" || offset < 0 || len(encoded) > base64.StdEncoding.EncodedLen(BundleChunkBytes) {
		return errors.New("invalid file, offset or chunk size")
	}
	content, e := base64.StdEncoding.Strict().DecodeString(encoded)
	if e != nil || len(content) == 0 || len(content) > BundleChunkBytes {
		return errors.New("invalid model chunk")
	}
	root, uploads, stage, e := b.stage(id)
	if e != nil {
		return e
	}
	defer root.Close()
	defer uploads.Close()
	defer stage.Close()
	var manifest bundleManifest
	if e = readDataJSON(stage, "manifest.json", 64<<10, &manifest); e != nil {
		return e
	}
	declared := int64(-1)
	for _, f := range manifest.Files {
		if f.Name == name {
			declared = f.Size
		}
	}
	if declared < 0 || offset+int64(len(content)) > declared {
		return errors.New("chunk exceeds declared model file")
	}
	var file *os.File
	if offset == 0 {
		file, e = stage.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	} else {
		info, err := stage.Lstat(name)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != offset {
			return errors.New("unexpected model file offset")
		}
		file, e = stage.OpenFile(name, os.O_WRONLY|os.O_APPEND, 0600)
		if e == nil {
			opened, err := file.Stat()
			if err != nil || !os.SameFile(info, opened) {
				file.Close()
				return errors.New("model file changed during upload")
			}
		}
	}
	if e != nil {
		return e
	}
	defer file.Close()
	_, e = file.Write(content)
	return e
}
func (b BundleTarget) Abort(id string) error {
	bundleMu.Lock()
	defer bundleMu.Unlock()
	root, uploads, stage, e := b.stage(id)
	if e != nil {
		return e
	}
	defer root.Close()
	defer uploads.Close()
	defer stage.Close()
	discardDataStage(uploads, id, stage)
	return nil
}
func bundleMetadata(root *os.Root, manifest bundleManifest) ([]ImportFile, map[string]string, error) {
	files := []ImportFile{}
	digests := map[string]string{}
	for _, f := range manifest.Files {
		file, e := openDataRegularFile(root, f.Name)
		if e != nil {
			return nil, nil, e
		}
		info, e := file.Stat()
		if e != nil || info.Size() != f.Size {
			file.Close()
			return nil, nil, fmt.Errorf("incomplete model file: %s", f.Name)
		}
		digest := sha256.New()
		_, e = io.Copy(digest, file)
		file.Close()
		if e != nil {
			return nil, nil, e
		}
		digests[f.Name] = hex.EncodeToString(digest.Sum(nil))
		kind := BundleFileKind(f.Name)
		item := ImportFile{Name: f.Name, Size: f.Size, Kind: kind}
		if kind == "generation" {
			continue
		}
		if kind == "config" || kind == "index" {
			raw, e := readDataFile(root, f.Name, 2<<20)
			if e != nil {
				return nil, nil, e
			}
			item.Content = string(raw)
		}
		if kind == "weights" {
			file, e := openDataRegularFile(root, f.Name)
			if e != nil {
				return nil, nil, e
			}
			prefix := make([]byte, 8)
			_, e = io.ReadFull(file, prefix)
			if e != nil {
				file.Close()
				return nil, nil, e
			}
			size := binary.LittleEndian.Uint64(prefix)
			if size < 2 || size > 8<<20 || size+8 > uint64(f.Size) {
				file.Close()
				return nil, nil, errors.New("invalid safetensors header")
			}
			header := make([]byte, int(size))
			_, e = io.ReadFull(file, header)
			file.Close()
			if e != nil {
				return nil, nil, e
			}
			item.Header = string(header)
		}
		files = append(files, item)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, digests, nil
}
func (b BundleTarget) Finish(id string) error {
	bundleMu.Lock()
	defer bundleMu.Unlock()
	root, uploads, stage, e := b.stage(id)
	if e != nil {
		return e
	}
	defer root.Close()
	defer uploads.Close()
	defer stage.Close()
	var manifest bundleManifest
	if e = readDataJSON(stage, "manifest.json", 64<<10, &manifest); e != nil {
		return e
	}
	if e = validateBundleManifest(manifest.Files); e != nil {
		return e
	}
	files, hashes, e := bundleMetadata(stage, manifest)
	if e != nil {
		return e
	}
	inspected, e := InspectImport(files)
	if e != nil {
		return e
	}
	raw, e := readDataFile(root, "graph.json", maxImportedGraphBytes)
	if e != nil {
		return e
	}
	var g ImportedGraph
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if e = decoder.Decode(&g); e != nil {
		return e
	}
	// The uploaded assets must describe exactly the architecture and tensor
	// manifest inspected earlier. Tokenizer files may be added to a partial copy.
	if !importEqual(inspected.Graph.Source.Config, g.Source.Config) || !importEqual(inspected.Graph.Source.Tensors, g.Source.Tensors) {
		return errors.New("This folder does not match the imported configuration and weight tensor metadata. Import it as a new model.")
	}
	manifest.Digests = hashes
	if e = atomicDataJSON(stage, "manifest.json", manifest); e != nil {
		return e
	}
	return publishImportedDirectory(root, id, "pretrained")
}
func (b BundleTarget) VerifiedPath() (string, error) {
	root, e := b.model()
	if e != nil {
		return "", e
	}
	defer root.Close()
	folder, e := openDataDirectory(root, "pretrained", false)
	if e != nil {
		return "", errors.New("Add the complete model folder to load pretrained weights, tokenizer or image processor.")
	}
	defer folder.Close()
	var manifest bundleManifest
	if e = readDataJSON(folder, "manifest.json", 64<<10, &manifest); e != nil {
		return "", e
	}
	if e = validateBundleManifest(manifest.Files); e != nil {
		return "", e
	}
	_, hashes, e := bundleMetadata(folder, manifest)
	if e != nil {
		return "", e
	}
	if !importEqual(hashes, manifest.Digests) {
		return "", errors.New("The copied model files changed. Reimport the model folder.")
	}
	return filepath.Join(b.ProjectRoot, "models", b.ModelID, "pretrained"), nil
}

// RuntimeArchitecture checks semantic equality, allowing label and layout edits only.
func RuntimeArchitecture(raw []byte) (ImportedGraph, error) {
	var g ImportedGraph
	if e := ValidateImportedGraph(raw); e != nil {
		return g, e
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if e := decoder.Decode(&g); e != nil {
		return g, e
	}
	original, e := makeImportedGraph(g.Source)
	if e != nil {
		return g, e
	}
	if len(g.Nodes) != len(original.Nodes) || len(g.Edges) != len(original.Edges) {
		return g, errors.New("Build layers or connections changed. Restore the original architecture or import a fresh copy before using source weights.")
	}
	for _, n := range original.Nodes {
		match := false
		for _, x := range g.Nodes {
			if x.ID == n.ID && x.Kind == n.Kind && x.SourceKey == n.SourceKey && importEqual(x.Config, n.Config) {
				match = true
				break
			}
		}
		if !match {
			return g, errors.New("Build settings changed and no longer match the source weights. Restore the original architecture to run it.")
		}
	}
	for _, edge := range original.Edges {
		match := false
		for _, x := range g.Edges {
			if x.Source == edge.Source && x.Target == edge.Target {
				match = true
				break
			}
		}
		if !match {
			return g, errors.New("Build connections changed. Restore the original architecture to run it.")
		}
	}
	return g, nil
}
