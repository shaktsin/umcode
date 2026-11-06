package workbench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	importJSONLimit     = 2 << 20
	importHeaderLimit   = 8 << 20
	importMetadataLimit = 24 << 20
	importTensorLimit   = 100000
	importGraphLimit    = 32 << 20
	importSafeInteger   = int64(9007199254740991)
)

var importDtypes = map[string]int64{"BOOL": 1, "U8": 1, "I8": 1, "F8_E4M3": 1, "F8_E5M2": 1, "F8_E4M3FN": 1, "F8_E4M3FNUZ": 1, "F8_E5M2FNUZ": 1, "I16": 2, "U16": 2, "F16": 2, "BF16": 2, "I32": 4, "U32": 4, "F32": 4, "I64": 8, "U64": 8, "F64": 8}

// strictImportJSON rejects duplicate keys at every depth and bounds recursive parsing.
func strictImportJSON(raw []byte, limit int) (any, error) {
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) {
		return nil, errors.New("invalid or oversized UTF-8 JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	tokens := 0
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 64 {
			return nil, errors.New("JSON nesting exceeds 64")
		}
		tokens++
		if tokens > 2000000 {
			return nil, errors.New("JSON has too many values")
		}
		t, e := d.Token()
		if e != nil {
			return nil, e
		}
		switch x := t.(type) {
		case json.Delim:
			switch x {
			case '{':
				m := map[string]any{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return nil, e
					}
					key, ok := k.(string)
					if !ok {
						return nil, errors.New("invalid JSON key")
					}
					if _, ok := m[key]; ok {
						return nil, fmt.Errorf("duplicate JSON key %q", key)
					}
					v, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					m[key] = v
				}
				_, e := d.Token()
				return m, e
			case '[':
				a := []any{}
				for d.More() {
					v, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					a = append(a, v)
				}
				_, e := d.Token()
				return a, e
			}
			return nil, errors.New("unexpected JSON delimiter")
		default:
			return t, nil
		}
	}
	v, e := read(0)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, errors.New("trailing JSON data")
	}
	return v, nil
}
func importObject(raw string, limit int) (map[string]any, error) {
	v, e := strictImportJSON([]byte(raw), limit)
	if e != nil {
		return nil, e
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("expected JSON object")
	}
	return m, nil
}
func importInteger(v any) (int64, error) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, errors.New("expected integer")
	}
	i, e := n.Int64()
	if e != nil || i < 0 || i > importSafeInteger {
		return 0, errors.New("integer out of range")
	}
	return i, nil
}
func safeImportName(s string) bool {
	if s == "" || len(s) > 1024 || !utf8.ValidString(s) || path.IsAbs(s) || path.Clean(s) != s || strings.ContainsAny(s, "\\:") {
		return false
	}
	for _, p := range strings.Split(s, "/") {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}
func importShape(v any, dtype string) ([]int64, int64, int64, error) {
	a, ok := v.([]any)
	width, known := importDtypes[dtype]
	if !ok || len(a) > 32 || !known {
		return nil, 0, 0, errors.New("invalid shape or dtype")
	}
	shape := make([]int64, len(a))
	count := int64(1)
	for i, n := range a {
		x, e := importInteger(n)
		if e != nil {
			return nil, 0, 0, e
		}
		shape[i] = x
		if x != 0 && count > math.MaxInt64/x {
			return nil, 0, 0, errors.New("shape arithmetic overflow")
		}
		count *= x
	}
	if count > math.MaxInt64/width {
		return nil, 0, 0, errors.New("tensor byte arithmetic overflow")
	}
	return shape, count, count * width, nil
}
func importAdd(a, b int64) (int64, error) {
	if b < 0 || a > math.MaxInt64-b {
		return 0, errors.New("metadata arithmetic overflow")
	}
	return a + b, nil
}

type importSpan struct{ start, end int64 }

func InspectImport(files []ImportFile) (ImportInspection, error) {
	result := ImportInspection{}
	if len(files) == 0 || len(files) > 1024 {
		return result, errors.New("select config.json or safetensors metadata (maximum 1024 files)")
	}
	config := map[string]any{}
	var index map[string]any
	names := map[string]bool{}
	weightNames := map[string]bool{}
	tensorNames := map[string]bool{}
	tensors := []ImportTensor{}
	manifest := []ImportSourceFile{}
	totalMeta, params, weightBytes := int64(0), int64(0), int64(0)
	hasConfig := false
	for _, f := range files {
		if !safeImportName(f.Name) || names[f.Name] || f.Size < 0 || f.Size > importSafeInteger {
			return result, fmt.Errorf("invalid or duplicate filename/size %q", f.Name)
		}
		names[f.Name] = true
		manifest = append(manifest, ImportSourceFile{f.Name, f.Size})
		totalMeta += int64(len(f.Content) + len(f.Header) + len(f.Name))
		if totalMeta > importMetadataLimit {
			return result, errors.New("submitted metadata exceeds 24 MiB")
		}
		switch f.Kind {
		case "config", "index":
			if f.Header != "" || int64(len(f.Content)) != f.Size {
				return result, errors.New("config/index size must match UTF-8 metadata")
			}
			m, e := importObject(f.Content, importJSONLimit)
			if e != nil {
				return result, fmt.Errorf("%s: %w", f.Name, e)
			}
			if f.Kind == "config" {
				if hasConfig || path.Base(f.Name) != "config.json" {
					return result, errors.New("select exactly one config.json")
				}
				config = m
				hasConfig = true
			} else {
				if index != nil || !strings.HasSuffix(f.Name, ".safetensors.index.json") {
					return result, errors.New("select at most one safetensors index")
				}
				index = m
			}
		case "tokenizer", "processor":
			if f.Content != "" || f.Header != "" {
				return result, errors.New("tokenizer/processor files must contain filename and size only")
			}
		case "weights":
			if f.Content != "" || !strings.HasSuffix(f.Name, ".safetensors") {
				return result, errors.New("weight files require safetensors headers only")
			}
			weightNames[f.Name] = true
			if len(weightNames) > 128 {
				return result, errors.New("weight files exceed 128")
			}
			header, e := importObject(f.Header, importHeaderLimit)
			if e != nil {
				return result, fmt.Errorf("%s: %w", f.Name, e)
			}
			payloadSize := f.Size - 8 - int64(len(f.Header))
			if payloadSize < 0 {
				return result, errors.New("safetensors header exceeds file size")
			}
			spans := []importSpan{}
			for name, value := range header {
				if name == "__metadata__" {
					m, ok := value.(map[string]any)
					if !ok {
						return result, errors.New("invalid safetensors metadata")
					}
					for _, v := range m {
						if _, ok := v.(string); !ok {
							return result, errors.New("safetensors metadata must contain strings")
						}
					}
					continue
				}
				if name == "" || len(name) > 2048 || !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) || tensorNames[name] {
					return result, errors.New("invalid or duplicate tensor name")
				}
				tensorNames[name] = true
				if len(tensorNames) > importTensorLimit {
					return result, errors.New("tensor count exceeds 100000")
				}
				m, ok := value.(map[string]any)
				if !ok || len(m) != 3 {
					return result, errors.New("tensor requires dtype, shape and data_offsets")
				}
				dtype, ok := m["dtype"].(string)
				if !ok {
					return result, errors.New("invalid tensor dtype")
				}
				shape, count, size, e := importShape(m["shape"], dtype)
				if e != nil {
					return result, e
				}
				a, ok := m["data_offsets"].([]any)
				if !ok || len(a) != 2 {
					return result, errors.New("invalid data_offsets")
				}
				start, e := importInteger(a[0])
				if e != nil {
					return result, e
				}
				end, e := importInteger(a[1])
				if e != nil || end < start || end > payloadSize || end-start != size {
					return result, errors.New("tensor data_offsets do not match shape and file size")
				}
				spans = append(spans, importSpan{start, end})
				params, e = importAdd(params, count)
				if e != nil {
					return result, e
				}
				weightBytes, e = importAdd(weightBytes, size)
				if e != nil {
					return result, e
				}
				tensors = append(tensors, ImportTensor{Name: name, Dtype: dtype, Shape: shape, File: f.Name, Bytes: size})
			}
			sort.Slice(spans, func(i, j int) bool {
				if spans[i].start == spans[j].start {
					return spans[i].end < spans[j].end
				}
				return spans[i].start < spans[j].start
			})
			offset := int64(0)
			for _, s := range spans {
				if s.start != offset {
					return result, errors.New("safetensors offsets must be contiguous without overlaps or gaps")
				}
				offset = s.end
			}
			if offset != payloadSize {
				return result, errors.New("safetensors payload size does not match header")
			}
		default:
			return result, fmt.Errorf("unsupported metadata kind %q", f.Kind)
		}
	}
	if !hasConfig && len(weightNames) == 0 {
		return result, errors.New("config or safetensors weights required")
	}
	if len(weightNames) > 1 && index == nil {
		return result, errors.New("multiple weight shards require a safetensors index")
	}
	if index != nil {
		if v, exists := index["metadata"]; exists {
			if _, ok := v.(map[string]any); !ok {
				return result, errors.New("index metadata must be an object")
			}
		}
		wm, ok := index["weight_map"].(map[string]any)
		if !ok || len(wm) != len(tensors) {
			return result, errors.New("index weight_map must cover every tensor exactly")
		}
		for _, t := range tensors {
			file, ok := wm[t.Name].(string)
			if !ok || file != t.File || !weightNames[file] {
				return result, fmt.Errorf("missing or mismatched shard for %s", t.Name)
			}
		}
		if meta, ok := index["metadata"].(map[string]any); ok {
			if v, exists := meta["total_size"]; exists {
				n, e := importInteger(v)
				if e != nil || n != weightBytes {
					return result, errors.New("index total_size does not match tensors")
				}
			}
		}
	}
	sort.Slice(tensors, func(i, j int) bool { return tensors[i].Name < tensors[j].Name })
	sort.Slice(manifest, func(i, j int) bool { return manifest[i].Name < manifest[j].Name })
	family, supported := importFamily(config)
	warnings := []string{}
	if len(weightNames) == 0 {
		warnings = append(warnings, "Architecture preview only: source weights are missing.")
	}
	if len(weightNames) > 0 {
		warnings = append(warnings, "Metadata verified: headers and index layout are consistent; weight payload integrity and trained quality have not been verified.")
	}
	if !supported {
		warnings = append(warnings, "Unknown architecture: tensor hierarchy is read-only; no executable topology was inferred.")
	}
	warnings = append(warnings, importCompatibilityWarnings(config, tensors)...)
	source := ImportedSource{Family: family, Config: config, Files: manifest, Tensors: tensors, WeightsVerified: len(weightNames) > 0, Warnings: warnings}
	if variant := importVariant(source); supported && variant.boundary != "" {
		warnings = append(warnings, variant.boundary)
		source.Warnings = warnings
	}
	graph, e := makeImportedGraph(source)
	if e != nil {
		return result, e
	}
	graphJSON, e := json.Marshal(graph)
	if e != nil || len(graphJSON) > importGraphLimit {
		return result, errors.New("imported graph metadata exceeds 32 MiB")
	}
	result = ImportInspection{Family: family, Name: importDisplayName(config, family), Supported: supported, WeightsVerified: source.WeightsVerified, Warnings: warnings, TensorCount: len(tensors), ParameterCount: params, TotalWeightBytes: weightBytes, Graph: graph}
	return result, nil
}

// ValidateImportedGraph validates both bounded edits and self-consistent immutable provenance.
// Persistence additionally compares Source against the original saved source on subsequent saves.
func ValidateImportedGraph(raw json.RawMessage) error {
	v, e := strictImportJSON(raw, importGraphLimit)
	if e != nil {
		return e
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return errors.New("imported graph must be an object")
	}
	if _, ok := obj["source"]; !ok {
		return errors.New("source provenance required")
	}
	var graph ImportedGraph
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	if e = d.Decode(&graph); e != nil {
		return e
	}
	if graph.Version != 1 || graph.Format != "umcode.imported.v1" || len(graph.Nodes) > 200 || len(graph.Edges) > 1000 {
		return errors.New("invalid imported graph format or size")
	}
	configJSON, err := json.Marshal(graph.Source.Config)
	if err != nil || graph.Source.Config == nil || len(configJSON) > importJSONLimit {
		return errors.New("source config exceeds 2 MiB or is not an object")
	}
	family, _ := importFamily(graph.Source.Config)
	if family != graph.Source.Family || len(graph.Source.Files) > 1024 || len(graph.Source.Tensors) > importTensorLimit || len(graph.Source.Warnings) > 100 {
		return errors.New("invalid imported source provenance")
	}
	files := map[string]int64{}
	weightFiles := 0
	for _, f := range graph.Source.Files {
		if !safeImportName(f.Name) || f.Size < 0 || f.Size > importSafeInteger {
			return errors.New("invalid source file")
		}
		if _, ok := files[f.Name]; ok {
			return errors.New("duplicate source file")
		}
		files[f.Name] = f.Size
		if strings.HasSuffix(f.Name, ".safetensors") {
			weightFiles++
		}
	}
	if weightFiles > 128 || graph.Source.WeightsVerified != (weightFiles > 0) {
		return errors.New("invalid metadata verification provenance")
	}
	seenTensor := map[string]bool{}
	bytesByFile := map[string]int64{}
	for _, t := range graph.Source.Tensors {
		size, ok := files[t.File]
		if !ok || !strings.HasSuffix(t.File, ".safetensors") || t.Name == "" || len(t.Name) > 2048 || strings.ContainsFunc(t.Name, unicode.IsControl) || seenTensor[t.Name] {
			return errors.New("invalid source tensor binding")
		}
		seenTensor[t.Name] = true
		a := make([]any, len(t.Shape))
		for i, x := range t.Shape {
			a[i] = json.Number(fmt.Sprint(x))
		}
		_, _, n, e := importShape(a, t.Dtype)
		if e != nil || n != t.Bytes || n > size {
			return errors.New("invalid source tensor shape/bytes")
		}
		bytesByFile[t.File], e = importAdd(bytesByFile[t.File], n)
		if e != nil || bytesByFile[t.File] > size-8 {
			return errors.New("source tensors exceed file bounds")
		}
	}
	canonical, e := makeImportedGraph(graph.Source)
	if e != nil {
		return e
	}
	if !importEqual(graph.Source.BaselineNodes, canonical.Source.BaselineNodes) {
		return errors.New("source baseline does not match family configuration and tensor manifest")
	}
	baseline := map[string]ImportedNode{}
	baselineIDs := map[string]bool{}
	for _, n := range canonical.Nodes {
		baseline[n.SourceKey] = n
		baselineIDs[n.ID] = true
	}
	ids := map[string]bool{}
	sourceKeys := map[string]bool{}
	validKinds := map[string]bool{"input": true, "embedding": true, "position": true, "transformer": true, "normalization": true, "linear": true, "conv2d": true, "pool": true, "cnnblock": true, "output": true, "unknown": true}
	for _, n := range graph.Nodes {
		if n.ID == "" || len(n.ID) > 128 || ids[n.ID] || !validKinds[n.Kind] || len(n.Label) > 256 || !utf8.ValidString(n.Label) || math.IsNaN(n.Position.X) || math.IsInf(n.Position.X, 0) || math.Abs(n.Position.X) > 1e7 || math.IsNaN(n.Position.Y) || math.IsInf(n.Position.Y, 0) || math.Abs(n.Position.Y) > 1e7 {
			return errors.New("invalid imported node")
		}
		ids[n.ID] = true
		if e = validateImportNodeConfig(n.Config); e != nil {
			return e
		}
		if n.SourceKey != "" {
			original, ok := baseline[n.SourceKey]
			if !ok || n.ID != original.ID || n.Kind != original.Kind || sourceKeys[n.SourceKey] || !importEqual(n.BaselineConfig, original.BaselineConfig) || !reflect.DeepEqual(n.TensorNames, original.TensorNames) {
				return errors.New("source binding/baseline identity changed")
			}
			if (original.Kind == "unknown" || original.Config["readOnly"] == true) && !importEqual(n.Config, original.Config) {
				return errors.New("read-only source layer settings cannot be changed")
			}
			sourceKeys[n.SourceKey] = true
		} else if baselineIDs[n.ID] || n.Kind == "unknown" || n.Config["readOnly"] == true || len(n.TensorNames) != 0 || len(n.BaselineConfig) != 0 {
			return errors.New("new nodes cannot claim source tensor bindings")
		}
	}
	edgeIDs := map[string]bool{}
	for _, edge := range graph.Edges {
		if edge.ID == "" || len(edge.ID) > 128 || edgeIDs[edge.ID] || !ids[edge.Source] || !ids[edge.Target] || edge.Source == edge.Target {
			return errors.New("invalid imported edge")
		}
		edgeIDs[edge.ID] = true
	}
	return nil
}
func importEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func validateImportNodeConfig(config map[string]any) error {
	if config == nil || len(config) > 100 {
		return errors.New("invalid layer settings")
	}
	for k, v := range config {
		if k == "" || len(k) > 128 {
			return errors.New("invalid setting key")
		}
		switch x := v.(type) {
		case string:
			if len(x) > 2048 || !utf8.ValidString(x) {
				return errors.New("invalid setting string")
			}
		case bool:
		case json.Number:
			n, e := x.Float64()
			if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 1e12 {
				return errors.New("setting number out of bounds")
			}
			lower := strings.ToLower(k)
			if strings.Contains(lower, "dropout") || strings.Contains(lower, "drop_path") {
				if n < 0 || n > 1 {
					return errors.New("dropout must be between zero and one")
				}
			} else if strings.HasSuffix(lower, "start") || strings.HasSuffix(lower, "end") || lower == "stage" {
				if n < 0 || n != math.Trunc(n) {
					return errors.New("layer indices must be nonnegative integers")
				}
			} else if strings.Contains(lower, "size") || strings.Contains(lower, "heads") || strings.Contains(lower, "channels") || strings.Contains(lower, "features") || strings.Contains(lower, "count") || strings.Contains(lower, "stride") || strings.Contains(lower, "kernel") || strings.Contains(lower, "positions") || strings.Contains(lower, "labels") || strings.Contains(lower, "expansion") {
				if n <= 0 || n != math.Trunc(n) || n > 1e9 {
					return errors.New("layer dimensions must be positive bounded integers")
				}
			} else if strings.Contains(lower, "epsilon") || strings.Contains(lower, "theta") {
				if n <= 0 {
					return errors.New("normalization/position constants must be positive")
				}
			}
		default:
			return errors.New("layer settings must be scalar values")
		}
	}
	return nil
}
