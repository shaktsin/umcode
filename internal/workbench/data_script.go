package workbench

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/sandbox"
)

const (
	dataScriptMaxBytes       = 32768
	dataScriptMaxInputs      = 8
	dataScriptDefaultTimeout = 120 * time.Second
	dataScriptOutputKeep     = 65536
	dataScriptWorkspaceCap   = 256 << 20
)

// detectSandbox is a seam so tests can run the script runner without a real
// Seatbelt or bubblewrap installation. Production always uses sandbox.Detect.
var detectSandbox = sandbox.Detect

// DataScriptRequest describes one approved assistant data script.
type DataScriptRequest struct {
	ProjectRoot     string
	ModelID         string
	Python          string
	Title           string
	OutputName      string
	Script          string
	InputDatasetIDs []string
	ExpectOutput    string
	Timeout         time.Duration
}

// DataScriptResult reports what an executed script produced.
type DataScriptResult struct {
	Stdout     string
	Stderr     string
	Dataset    *Dataset
	DurationMS int64
}

var dataScriptOutputExt = map[string]bool{".csv": true, ".tsv": true, ".jsonl": true, ".txt": true}
var dataScriptDirName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func dataScriptInputDir(name, id string) string {
	cleaned := dataScriptDirName.ReplaceAllString(name, "_")
	cleaned = strings.Trim(cleaned, "._")
	if cleaned == "" {
		cleaned = id[:min(12, len(id))]
	}
	return cleaned
}

func tailString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

func copyDatasetRaw(datasets *os.Root, ds Dataset, destination string) error {
	source, err := openDataDirectory(datasets, ds.ID, false)
	if err != nil {
		return err
	}
	defer source.Close()
	raw, err := openDataDirectory(source, "raw", false)
	if err != nil {
		return err
	}
	defer raw.Close()
	return fs.WalkDir(raw.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(destination, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("dataset file %s is not a regular file", path)
		}
		file, err := raw.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		content, err := io.ReadAll(io.LimitReader(file, dataScriptWorkspaceCap))
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o600)
	})
}

// pythonReadRoots lists the folders a sandboxed interpreter needs to start: its
// install root (the venv or prefix above bin/) and, for a venv, the base
// interpreter named by pyvenv.cfg, which holds the standard library.
func pythonReadRoots(python string) []string {
	resolved, err := filepath.EvalSymlinks(python)
	if err != nil {
		resolved = python
	}
	roots := []string{}
	addRoot := func(binary string) string {
		dir := filepath.Dir(binary)
		if filepath.Base(dir) == "bin" {
			dir = filepath.Dir(dir)
		}
		roots = append(roots, dir)
		return dir
	}
	addRoot(python)
	if resolved != python {
		addRoot(resolved)
	}
	installRoot := filepath.Dir(filepath.Dir(python))
	if config, err := os.ReadFile(filepath.Join(installRoot, "pyvenv.cfg")); err == nil {
		for _, line := range strings.Split(string(config), "\n") {
			key, value, found := strings.Cut(line, "=")
			if found && strings.TrimSpace(key) == "home" {
				home := strings.TrimSpace(value)
				if filepath.IsAbs(home) {
					addRoot(filepath.Join(home, "python"))
				}
			}
		}
	}
	return roots
}

func validateDataScriptRequest(req DataScriptRequest) error {
	if req.ExpectOutput != "dataset" && req.ExpectOutput != "report" {
		return errors.New("expectOutput must be dataset or report")
	}
	if strings.TrimSpace(req.Script) == "" || len(req.Script) > dataScriptMaxBytes {
		return errors.New("script must be 1 byte to 32 KiB")
	}
	if len(req.Title) == 0 || len(req.Title) > 80 {
		return errors.New("title must be 1-80 characters")
	}
	if len(req.InputDatasetIDs) > dataScriptMaxInputs {
		return errors.New("scripts take at most 8 input datasets")
	}
	if req.ExpectOutput == "dataset" && (strings.TrimSpace(req.OutputName) == "" || len(req.OutputName) > 256) {
		return errors.New("outputName must be 1-256 characters")
	}
	return nil
}

func collectDataScriptOutputs(outDir string) ([]DataFile, error) {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return nil, err
	}
	files := []DataFile{}
	for _, entry := range entries {
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil, fmt.Errorf("out/%s must be a regular file", entry.Name())
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !dataScriptOutputExt[ext] {
			return nil, fmt.Errorf("out/%s has unsupported format %s; write CSV, TSV, JSONL or TXT", entry.Name(), ext)
		}
		content, err := os.ReadFile(filepath.Join(outDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		files = append(files, DataFile{Name: entry.Name(), ContentBase64: base64.StdEncoding.EncodeToString(content)})
	}
	if len(files) == 0 {
		return nil, errors.New("the script produced no output files in out/")
	}
	return files, nil
}

// RunDataScript executes an approved assistant data script with the selected
// Workbench Python inside the OS sandbox and, for dataset runs, registers the
// files it wrote to out/ as one new imported dataset with provenance.
func RunDataScript(ctx context.Context, req DataScriptRequest) (DataScriptResult, error) {
	result := DataScriptResult{}
	if err := validateDataScriptRequest(req); err != nil {
		return result, err
	}
	confinement := detectSandbox()
	if confinement == nil {
		return result, errors.New("data scripts require the system sandbox (Seatbelt or bubblewrap), which is unavailable")
	}
	state, err := ReadData(req.ProjectRoot, req.ModelID, "")
	if err != nil {
		return result, err
	}
	inputs := make([]Dataset, 0, len(req.InputDatasetIDs))
	for _, id := range req.InputDatasetIDs {
		found := false
		for _, ds := range state.Datasets {
			if ds.ID == id {
				inputs = append(inputs, ds)
				found = true
			}
		}
		if !found {
			return result, fmt.Errorf("dataset %s is not imported for this model", id)
		}
	}
	workspace, err := os.MkdirTemp("", "umcode-data-script-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(workspace)
	for _, name := range []string{"inputs", "out", "job"} {
		if err = os.Mkdir(filepath.Join(workspace, name), 0o700); err != nil {
			return result, err
		}
	}
	if len(inputs) > 0 {
		if err = func() error {
			dataMu.Lock()
			defer dataMu.Unlock()
			root, _, err := openDataStorage(req.ProjectRoot, req.ModelID, false)
			if err != nil {
				return err
			}
			defer root.Close()
			datasets, err := openDataDirectory(root, "datasets", false)
			if err != nil {
				return err
			}
			defer datasets.Close()
			for _, ds := range inputs {
				destination := filepath.Join(workspace, "inputs", dataScriptInputDir(ds.Name, ds.ID))
				if err := copyDatasetRaw(datasets, ds, destination); err != nil {
					return err
				}
			}
			return nil
		}(); err != nil {
			return result, err
		}
	}
	if err = os.WriteFile(filepath.Join(workspace, "job", "script.py"), []byte(req.Script), 0o600); err != nil {
		return result, err
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = dataScriptDefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, req.Python, "-I", "-u", filepath.Join("job", "script.py"))
	cmd.Dir = workspace
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = append(os.Environ(), "OMP_NUM_THREADS=1", "MKL_NUM_THREADS=1", "TOKENIZERS_PARALLELISM=false")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	policy := sandbox.EvaluationPolicy(workspace, nil)
	policy.Readable = append(policy.Readable, pythonReadRoots(req.Python)...)
	if err = confinement.Wrap(cmd, policy); err != nil {
		return result, err
	}
	started := time.Now()
	err = cmd.Run()
	result.DurationMS = time.Since(started).Milliseconds()
	result.Stdout = tailString(stdout.String(), dataScriptOutputKeep)
	result.Stderr = tailString(stderr.String(), dataScriptOutputKeep)
	if runCtx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("the script hit its %s time limit", timeout)
	}
	if err != nil {
		return result, fmt.Errorf("the script failed: %v", err)
	}
	if req.ExpectOutput == "report" {
		return result, nil
	}
	files, err := collectDataScriptOutputs(filepath.Join(workspace, "out"))
	if err != nil {
		return result, err
	}
	prov := &DataProvenance{CreatedBy: "assistant", Title: req.Title, Script: req.Script, InputDatasetIDs: append([]string{}, req.InputDatasetIDs...), StdoutSummary: tailString(result.Stdout, 1024)}
	dataset, err := ImportDataWithProvenance(req.ProjectRoot, req.ModelID, DataSource{Kind: "assistant", Name: req.OutputName}, files, prov)
	if err != nil {
		return result, err
	}
	result.Dataset = &dataset
	return result, nil
}
