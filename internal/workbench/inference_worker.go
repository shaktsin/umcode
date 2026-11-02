package workbench

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

//go:embed pytorch/runner.py
var trustedInferenceRunner string

// ArchitectureTest runs real random weights, without creating a checkpoint.
type ArchitectureTest struct {
	InputShape     []int  `json:"inputShape"`
	OutputShape    []int  `json:"outputShape"`
	ParameterCount int64  `json:"parameterCount"`
	Device         string `json:"device"`
	Untrained      bool   `json:"untrained"`
}
type workerResponse struct {
	OK                   bool   `json:"ok"`
	Error                string `json:"error"`
	CheckpointValidation `json:"-"`
	Prediction           `json:"-"`
	ArchitectureTest     `json:"-"`
}
type pythonWorker struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines *bufio.Scanner
	gate  chan struct{}
	once  sync.Once
}

func startPythonWorker(python string) (*pythonWorker, error) {
	if python == "" {
		return nil, errors.New("PyTorch runtime is not installed; configure UMCODE_WORKBENCH_PYTHON to a Python environment with torch, safetensors, Pillow and numpy")
	}
	cmd := exec.Command(python, "-I", "-u", "-c", trustedInferenceRunner)
	cmd.Env = append(os.Environ(), "OMP_NUM_THREADS=1", "MKL_NUM_THREADS=1", "TOKENIZERS_PARALLELISM=false")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	// Never send weights, inputs or dependency output to application logs.
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("start local PyTorch: %w", err)
	}
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 4096), 12<<20)
	return &pythonWorker{cmd: cmd, stdin: stdin, lines: lines, gate: make(chan struct{}, 1)}, nil
}
func (w *pythonWorker) close() {
	w.once.Do(func() { _ = w.stdin.Close(); _ = w.cmd.Process.Kill(); _ = w.cmd.Wait() })
}
func (w *pythonWorker) call(ctx context.Context, request any) (workerResponse, error) {
	var out workerResponse
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	select {
	case w.gate <- struct{}{}:
	case <-ctx.Done():
		return out, ctx.Err()
	}
	defer func() { <-w.gate }()
	done := make(chan error, 1)
	go func() {
		b, err := json.Marshal(request)
		if err == nil && len(b) > 12<<20 {
			err = errors.New("local inference request exceeds 12 MiB")
		}
		if err == nil {
			_, err = w.stdin.Write(append(b, '\n'))
		}
		if err == nil {
			if !w.lines.Scan() {
				err = errors.New("local PyTorch worker stopped; verify its dependencies and reload the checkpoint")
			} else {
				raw := w.lines.Bytes()
				err = json.Unmarshal(raw, &out)
				if err == nil {
					_ = json.Unmarshal(raw, &out.CheckpointValidation)
					_ = json.Unmarshal(raw, &out.Prediction)
					_ = json.Unmarshal(raw, &out.ArchitectureTest)
				}
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			w.close()
			return out, err
		}
		if !out.OK {
			return out, errors.New(out.Error)
		}
		return out, nil
	case <-ctx.Done():
		w.close()
		<-done
		return workerResponse{}, fmt.Errorf("local inference cancelled or timed out: %w", ctx.Err())
	}
}

type InferenceManager struct {
	python    string
	mu        sync.Mutex
	loadMu    sync.Mutex
	workers   map[string]*pythonWorker
	transient map[*pythonWorker]bool
	closed    bool
}

func NewInferenceManager(home string) *InferenceManager {
	python := os.Getenv("UMCODE_WORKBENCH_PYTHON")
	if python == "" {
		candidate := filepath.Join(home, "runtimes", "pytorch", "bin", "python")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			python = candidate
		}
	}
	return &InferenceManager{python: python, workers: map[string]*pythonWorker{}, transient: map[*pythonWorker]bool{}}
}
func (m *InferenceManager) start() (*pythonWorker, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("local inference is shutting down")
	}
	w, err := startPythonWorker(m.python)
	if err == nil {
		m.transient[w] = true
	}
	return w, err
}
func (m *InferenceManager) release(w *pythonWorker) {
	m.mu.Lock()
	delete(m.transient, w)
	m.mu.Unlock()
	w.close()
}
func (m *InferenceManager) Available() bool { return m.python != "" }
func (m *InferenceManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for w := range m.transient {
		w.close()
		delete(m.transient, w)
	}
	for k, w := range m.workers {
		w.close()
		delete(m.workers, k)
	}
}
func (m *InferenceManager) Unload(prefix string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, w := range m.workers {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			w.close()
			delete(m.workers, k)
		}
	}
}
func (m *InferenceManager) Loaded(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workers[key] != nil
}
func (m *InferenceManager) load(ctx context.Context, w *pythonWorker, graph json.RawMessage, selection DataSelection, weights []byte, format string, promptFormats ...string) (CheckpointValidation, error) {
	dir, err := os.MkdirTemp("", "umcode-local-weights-")
	if err != nil {
		return CheckpointValidation{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "weights.bin")
	if err = os.WriteFile(path, weights, 0600); err != nil {
		return CheckpointValidation{}, err
	}
	promptFormat := "chat-v1"
	if len(promptFormats) > 0 && promptFormats[0] != "" {
		promptFormat = promptFormats[0]
	}
	out, err := w.call(ctx, map[string]any{"promptFormat": promptFormat, "op": "load", "graph": graph, "selection": selection, "weightsPath": path, "format": format})
	return out.CheckpointValidation, err
}
func (m *InferenceManager) Validate(ctx context.Context, graph json.RawMessage, selection DataSelection, weights []byte, format string) (CheckpointValidation, error) {
	w, err := m.start()
	if err != nil {
		return CheckpointValidation{}, err
	}
	defer m.release(w)
	return m.load(ctx, w, graph, selection, weights, format)
}
func (m *InferenceManager) Shape(ctx context.Context, graph json.RawMessage) (ArchitectureTest, error) {
	w, err := m.start()
	if err != nil {
		return ArchitectureTest{}, err
	}
	defer m.release(w)
	out, err := w.call(ctx, map[string]any{"op": "shape", "graph": graph})
	return out.ArchitectureTest, err
}
func (m *InferenceManager) Load(ctx context.Context, key string, snap CheckpointSnapshot) error {
	m.loadMu.Lock()
	defer m.loadMu.Unlock()
	m.mu.Lock()
	if m.workers[key] != nil {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	w, err := m.start()
	if err != nil {
		return err
	}
	if _, err = m.load(ctx, w, snap.Graph, snap.Selection, snap.Weights, snap.Checkpoint.Format, snap.Checkpoint.PromptFormat); err != nil {
		m.release(w)
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.transient, w)
	if m.closed {
		w.close()
		return errors.New("local inference is shutting down")
	}
	// Keep at most two CPU workers resident. Chat reloads pinned snapshots on demand.
	if len(m.workers) >= 2 {
		for k, old := range m.workers {
			old.close()
			delete(m.workers, k)
			break
		}
	}
	m.workers[key] = w
	return nil
}
func (m *InferenceManager) Predict(ctx context.Context, key string, snap CheckpointSnapshot, input map[string]any) (Prediction, error) {
	if err := m.Load(ctx, key, snap); err != nil {
		return Prediction{}, err
	}
	m.mu.Lock()
	w := m.workers[key]
	m.mu.Unlock()
	if w == nil {
		return Prediction{}, errors.New("checkpoint was unloaded; load it again")
	}
	request := map[string]any{"op": "predict"}
	for k, v := range input {
		request[k] = v
	}
	start := time.Now()
	out, err := w.call(ctx, request)
	if err != nil {
		m.mu.Lock()
		if m.workers[key] == w {
			delete(m.workers, key)
		}
		m.mu.Unlock()
		w.close()
		return Prediction{}, err
	}
	p := out.Prediction
	p.CheckpointID = snap.Checkpoint.ID
	p.DurationMs = time.Since(start).Milliseconds()
	return p, nil
}
