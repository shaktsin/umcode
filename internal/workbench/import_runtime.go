package workbench

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

//go:embed pytorch/imported.py
var importedRunner string

type ImportRuntimeState struct {
	Ready      bool     `json:"ready"`
	Task       string   `json:"task,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Checkpoint string   `json:"checkpoint,omitempty"`
}
type ImportJob struct {
	ID             string         `json:"id"`
	Status         string         `json:"status"`
	Epoch          int            `json:"epoch"`
	Epochs         int            `json:"epochs"`
	TrainLoss      *float64       `json:"trainLoss,omitempty"`
	ValidationLoss *float64       `json:"validationLoss,omitempty"`
	Result         map[string]any `json:"result,omitempty"`
	Error          string         `json:"error,omitempty"`
}
type importedReceipt struct {
	Fingerprint string             `json:"fingerprint"`
	State       ImportRuntimeState `json:"state"`
}
type ImportedRuntime struct {
	python string
	mu     sync.Mutex
	job    *ImportJob
	target BundleTarget
	cancel context.CancelFunc
	closed bool
	busy   bool
}

func (r *ImportedRuntime) acquire() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.busy {
		return errors.New("The imported-model runtime is busy. Wait for the current operation or stop training.")
	}
	r.busy = true
	return nil
}
func (r *ImportedRuntime) release() { r.mu.Lock(); r.busy = false; r.mu.Unlock() }
func (r *ImportedRuntime) CancelModel(projectRoot, modelID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.target.ProjectRoot == projectRoot && r.target.ModelID == modelID && r.cancel != nil {
		r.cancel()
	}
}

func NewImportedRuntime(home string) *ImportedRuntime {
	return &ImportedRuntime{python: NewInferenceManager(home).python}
}
func (r *ImportedRuntime) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.cancel != nil {
		r.cancel()
	}
}

func importedTask(g ImportedGraph) (string, error) {
	arches, _ := g.Source.Config["architectures"].([]any)
	arch := ""
	if len(arches) > 0 {
		arch, _ = arches[0].(string)
	}
	switch g.Source.Family {
	case "gpt2", "llama", "mistral", "qwen2", "mixtral", "granitemoe", "qwen2_moe", "qwen3_moe":
		if arch != "" && !strings.HasSuffix(arch, "ForCausalLM") && arch != "GPT2LMHeadModel" {
			break
		}
		return "text-generation", nil
	case "bert":
		if arch == "BertForSequenceClassification" {
			return "text-classification", nil
		}
	case "vit", "resnet", "convnext":
		if strings.HasSuffix(arch, "ForImageClassification") {
			return "image-classification", nil
		}
	}
	return "", errors.New("This model task is not supported by the local imported runtime.")
}
func importedFingerprint(b BundleTarget) (string, string, error) {
	root, err := b.model()
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	raw, err := readDataFile(root, "graph.json", maxImportedGraphBytes)
	if err != nil {
		return "", "", err
	}
	dir, err := bundleDir(b.ProjectRoot, b.ModelID)
	if err != nil {
		return "", "", err
	}
	if err = verifyImportedSource(b.ProjectRoot, dir, raw); err != nil {
		return "", "", err
	}
	g, err := RuntimeArchitecture(raw)
	if err != nil {
		return "", "", err
	}
	task, err := importedTask(g)
	if err != nil {
		return "", "", err
	}
	folder, err := openDataDirectory(root, "pretrained", false)
	if err != nil {
		return task, "", errors.New("Add the complete model folder to load pretrained weights and its processor.")
	}
	defer folder.Close()
	manifest, err := readDataFile(folder, "manifest.json", 64<<10)
	if err != nil {
		return task, "", err
	}
	var m bundleManifest
	if json.Unmarshal(manifest, &m) != nil || validateBundleManifest(m.Files) != nil || len(m.Digests) != len(m.Files) {
		return task, "", errors.New("The model folder is incomplete. Add it again.")
	}
	source, err := importedSourceJSON(raw)
	if err != nil {
		return task, "", err
	}
	h := sha256.Sum256(append(source, manifest...))
	return task, hex.EncodeToString(h[:]), nil
}
func (r *ImportedRuntime) Status(b BundleTarget) (ImportRuntimeState, error) {
	task, fingerprint, err := importedFingerprint(b)
	if err != nil {
		return ImportRuntimeState{Task: task, Reason: err.Error()}, nil
	}
	if r.python == "" {
		return ImportRuntimeState{Task: task, Reason: "The local Python runtime is not installed."}, nil
	}
	root, err := b.model()
	if err != nil {
		return ImportRuntimeState{}, err
	}
	defer root.Close()
	var receipt importedReceipt
	if readDataJSON(root, "import-runtime.json", 1<<20, &receipt) != nil || receipt.Fingerprint != fingerprint || !receipt.State.Ready {
		return ImportRuntimeState{Task: task, Reason: "Validate the copied model before using it."}, nil
	}
	return receipt.State, nil
}
func (r *ImportedRuntime) run(ctx context.Context, request map[string]any, progress func(map[string]any)) (map[string]any, error) {
	if r.python == "" {
		return nil, errors.New("The local Python runtime is not installed.")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	input, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, r.python, "-I", "-u", "-c", importedRunner)
	cmd.Env = append(os.Environ(), "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "HF_DATASETS_OFFLINE=1", "TOKENIZERS_PARALLELISM=false")
	cmd.Stdin = bytes.NewReader(input)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return nil, errors.New("Unable to start the local Python runtime.")
	}
	scan := bufio.NewScanner(io.LimitReader(stdout, 8<<20))
	scan.Buffer(make([]byte, 4096), 2<<20)
	var result map[string]any
	for scan.Scan() {
		var item map[string]any
		if json.Unmarshal(scan.Bytes(), &item) != nil {
			continue
		}
		if item["event"] == "progress" {
			if progress != nil {
				progress(item)
			}
			continue
		}
		result = item
	}
	scanErr := scan.Err()
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if scanErr != nil || waitErr != nil || result == nil {
		return nil, errors.New("The local model runtime failed or returned an invalid response.")
	}
	if ok, _ := result["ok"].(bool); !ok {
		msg, _ := result["error"].(string)
		if len(msg) > 500 {
			msg = msg[:500]
		}
		if msg == "" {
			msg = "Model operation failed."
		}
		return nil, errors.New(msg)
	}
	return result, nil
}
func (r *ImportedRuntime) Validate(ctx context.Context, b BundleTarget) (ImportRuntimeState, error) {
	if err := r.acquire(); err != nil {
		return ImportRuntimeState{}, err
	}
	defer r.release()
	task, fingerprint, err := importedFingerprint(b)
	if err != nil {
		return ImportRuntimeState{Task: task, Reason: err.Error()}, nil
	}
	path, err := b.VerifiedPath()
	if err != nil {
		return ImportRuntimeState{Task: task, Reason: err.Error()}, nil
	}
	result, err := r.run(ctx, map[string]any{"action": "validate", "bundleDir": path, "task": task}, nil)
	if err != nil {
		return ImportRuntimeState{Task: task, Reason: err.Error()}, nil
	}
	state := ImportRuntimeState{Ready: true, Task: task}
	if previous, e := r.Status(b); e == nil && previous.Ready {
		state.Checkpoint = previous.Checkpoint
	}
	if labels, ok := result["labels"].([]any); ok {
		for _, label := range labels {
			if s, ok := label.(string); ok {
				state.Labels = append(state.Labels, s)
			}
		}
	}
	_, current, err := importedFingerprint(b)
	if err != nil || current != fingerprint {
		return ImportRuntimeState{}, errors.New("Model changed during validation.")
	}
	root, err := b.model()
	if err != nil {
		return ImportRuntimeState{}, err
	}
	defer root.Close()
	if err = atomicDataJSON(root, "import-runtime.json", importedReceipt{fingerprint, state}); err != nil {
		return ImportRuntimeState{}, err
	}
	return state, nil
}
func (r *ImportedRuntime) Infer(ctx context.Context, b BundleTarget, prompt, imageBase64, checkpoint string, maxTokens int) (map[string]any, error) {
	return r.infer(ctx, b, prompt, imageBase64, checkpoint, maxTokens, nil)
}
func (r *ImportedRuntime) InferChat(ctx context.Context, b BundleTarget, messages []ChatMessage, checkpoint string, maxTokens int) (map[string]any, error) {
	if len(messages) < 1 || len(messages) > 64 {
		return nil, errors.New("chat requires 1–64 messages")
	}
	return r.infer(ctx, b, "", "", checkpoint, maxTokens, messages)
}
func (r *ImportedRuntime) infer(ctx context.Context, b BundleTarget, prompt, imageBase64, checkpoint string, maxTokens int, messages []ChatMessage) (map[string]any, error) {
	if err := r.acquire(); err != nil {
		return nil, err
	}
	defer r.release()
	state, err := r.Status(b)
	if err != nil {
		return nil, err
	}
	if !state.Ready {
		return nil, errors.New(state.Reason)
	}
	_, before, err := importedFingerprint(b)
	if err != nil {
		return nil, err
	}
	path, err := b.VerifiedPath()
	if err != nil {
		return nil, err
	}
	if maxTokens == 0 {
		maxTokens = 32
	}
	if len(prompt) > 1<<20 || len(imageBase64) > 12<<20 || maxTokens < 1 || maxTokens > 256 {
		return nil, errors.New("Inference input exceeds limits.")
	}
	req := map[string]any{"action": "infer", "bundleDir": path, "task": state.Task, "prompt": prompt, "imageBase64": imageBase64, "maxNewTokens": maxTokens}
	if messages != nil {
		req["messages"] = messages
		req["sequenceLength"] = 2048
	}
	if checkpoint != "" {
		checkpointPath, e := verifiedImportedCheckpoint(b, checkpoint)
		if e != nil {
			return nil, e
		}
		req["checkpointDir"] = checkpointPath
	}
	result, err := r.run(ctx, req, nil)
	if err != nil {
		return nil, err
	}
	_, after, err := importedFingerprint(b)
	if err != nil {
		return nil, err
	}
	if before != after {
		return nil, errors.New("model changed during inference; test again")
	}
	return result, nil
}
func (r *ImportedRuntime) Job(b BundleTarget) (*ImportJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job == nil || r.target != b {
		root, e := b.model()
		if e != nil {
			return nil, e
		}
		defer root.Close()
		var saved ImportJob
		e = readDataJSON(root, "import-job.json", 1<<20, &saved)
		if errors.Is(e, os.ErrNotExist) {
			return nil, nil
		}
		if e != nil {
			return nil, e
		}
		if saved.Status == "running" {
			saved.Status = "cancelled"
			saved.Error = "The app restarted before this job finished. Start a new run."
		}
		return &saved, nil
	}
	copy := *r.job
	return &copy, nil
}
func (r *ImportedRuntime) Cancel(b BundleTarget) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job == nil || r.target != b || r.cancel == nil {
		return errors.New("No active training job.")
	}
	r.cancel()
	return nil
}
func (r *ImportedRuntime) Train(ctx context.Context, b BundleTarget, epochs int, learningRate float64) (ImportJob, error) {
	if err := r.acquire(); err != nil {
		return ImportJob{}, err
	}
	started := false
	defer func() {
		if !started {
			r.release()
		}
	}()
	if epochs < 1 || epochs > 10 || learningRate < 0.0000001 || learningRate > 0.1 {
		return ImportJob{}, errors.New("Training requires 1–10 epochs and a valid learning rate.")
	}
	state, err := r.Status(b)
	if err != nil {
		return ImportJob{}, err
	}
	if !state.Ready {
		return ImportJob{}, errors.New(state.Reason)
	}
	path, err := b.VerifiedPath()
	if err != nil {
		return ImportJob{}, err
	}
	splits, selection, err := readImportedSplits(b)
	if err != nil {
		return ImportJob{}, err
	}
	if selection.Counts.Train == 0 || selection.Counts.Validation == 0 || selection.Counts.Test == 0 {
		return ImportJob{}, errors.New("Training requires nonempty training, validation and test splits.")
	}
	r.mu.Lock()
	if r.closed || r.cancel != nil {
		r.mu.Unlock()
		return ImportJob{}, errors.New("A training job is already running or the runtime is closed.")
	}
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	job := ImportJob{ID: id, Status: "running", Epochs: epochs}
	r.job = &job
	r.target = b
	jobCtx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.mu.Unlock()
	started = true
	go func() {
		defer cancel()
		defer r.release()
		temporary, err := os.MkdirTemp("", "umcode-import-job-")
		if err == nil {
			defer os.RemoveAll(temporary)
		}
		output := temporary + "/checkpoint"
		var result map[string]any
		if err == nil {
			request := map[string]any{"action": "train", "bundleDir": path, "task": state.Task, "outputDir": output, "epochs": epochs, "learningRate": learningRate, "seed": selection.Config.Seed, "splits": splits}
			if state.Task != "image-classification" {
				request["sequenceLength"] = selection.Config.SequenceLength
			}
			result, err = r.run(jobCtx, request, func(p map[string]any) {
				r.mu.Lock()
				defer r.mu.Unlock()
				if v, ok := p["epoch"].(float64); ok {
					r.job.Epoch = int(v)
				}
				if v, ok := p["trainLoss"].(float64); ok {
					r.job.TrainLoss = &v
				}
				if v, ok := p["validationLoss"].(float64); ok {
					r.job.ValidationLoss = &v
				}
			})
		}
		if err == nil {
			err = jobCtx.Err()
		}
		if err == nil {
			_, _, err = importedFingerprint(b)
		}
		if err == nil {
			err = publishImportedCheckpoint(b, output, id)
		}
		if err == nil {
			_, fingerprint, e := importedFingerprint(b)
			if e != nil {
				err = e
			} else {
				root, e := b.model()
				if e != nil {
					err = e
				} else {
					var rec importedReceipt
					e = readDataJSON(root, "import-runtime.json", 1<<20, &rec)
					if e == nil && rec.Fingerprint != fingerprint {
						e = errors.New("Model changed while training.")
					}
					if e == nil {
						rec.State.Checkpoint = id
						e = atomicDataJSON(root, "import-runtime.json", rec)
					}
					root.Close()
					err = e
				}
			}
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.cancel = nil
		if err != nil {
			r.job.Status = "failed"
			r.job.Error = err.Error()
			if jobCtx.Err() != nil {
				r.job.Status = "cancelled"
			}
			return
		}
		r.job.Status = "completed"
		result["checkpoint"] = id
		r.job.Result = result
		if root, e := b.model(); e == nil {
			if e = atomicDataJSON(root, "import-job.json", r.job); e != nil {
				r.job.Error = "Checkpoint saved, but the run summary could not be saved: " + e.Error()
			}
			root.Close()
		}
	}()
	return job, nil
}
