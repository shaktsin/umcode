package workbench

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

//go:embed pytorch/train.py
var trustedTrainingRunner string

type TrainingPublisher func(context.Context, TrainingSnapshot, []byte, string) (Checkpoint, error)
type trainingJob struct {
	finalizing                bool
	key, root, model, version string
	run                       TrainingRun
	cancel                    context.CancelFunc
	ctx                       context.Context
}
type TrainingManager struct {
	mu     sync.Mutex
	python string
	active *trainingJob
	closed bool
	wg     sync.WaitGroup
}

func NewTrainingManager(home string) *TrainingManager {
	return &TrainingManager{python: NewInferenceManager(home).python}
}
func (m *TrainingManager) Available() bool { return m.python != "" }
func (m *TrainingManager) ActiveElsewhere(root, id, version string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active != nil && m.active.key != trainingKey(root, id, version)
}

func trainingKey(root, id, version string) string {
	return filepath.Clean(root) + "/" + id + "/" + version
}
func cloneTrainingRun(run TrainingRun) TrainingRun {
	b, _ := json.Marshal(run)
	var copy TrainingRun
	_ = json.Unmarshal(b, &copy)
	return copy
}
func (m *TrainingManager) List(root, id, version string) ([]TrainingRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runs, err := ReadTrainingRuns(root, id, version)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		if m.active != nil && m.active.key == trainingKey(root, id, version) && m.active.run.ID == runs[i].ID {
			runs[i] = cloneTrainingRun(m.active.run)
			created, _ := time.Parse(time.RFC3339Nano, runs[i].CreatedAt)
			runs[i].DurationMs = time.Since(created).Milliseconds()
		} else if runs[i].Status == "running" {
			runs[i].Status = "failed"
			runs[i].Error = "Training was interrupted by an engine restart. Start a new run; completed checkpoints are preserved."
			runs[i].FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
			if err = WriteTrainingRun(root, id, version, runs[i]); err != nil {
				return nil, err
			}
		}
	}
	return runs, nil
}
func (m *TrainingManager) Start(root, id, version string, config TrainingConfig, publish TrainingPublisher) (TrainingRun, error) {
	if err := config.Validate(); err != nil {
		return TrainingRun{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return TrainingRun{}, errors.New("training is shutting down")
	}
	if m.python == "" {
		return TrainingRun{}, errors.New("install the managed PyTorch runtime before training")
	}
	if m.active != nil {
		return TrainingRun{}, errors.New("a local training run is already active; stop it or wait for it to finish")
	}
	snap, err := SnapshotTraining(root, id, version)
	if err != nil {
		return TrainingRun{}, err
	}
	runID, err := dataRandomName("train-")
	if err != nil {
		return TrainingRun{}, err
	}
	now := time.Now().UTC()
	run := TrainingRun{ID: runID, Name: "Local run " + now.Format("Jan 2 15:04:05"), ModelCreatedAt: version, Status: "running", Config: config, History: []TrainingEpoch{}, CreatedAt: now.Format(time.RFC3339Nano), PreparationVersion: snap.Selection.Version}
	if err = WriteTrainingRun(root, id, version, run); err != nil {
		return TrainingRun{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	job := &trainingJob{key: trainingKey(root, id, version), root: root, model: id, version: version, run: run, cancel: cancel, ctx: ctx}
	m.active = job
	m.wg.Add(1)
	go func() { defer m.wg.Done(); defer cancel(); m.execute(job, snap, publish) }()
	return cloneTrainingRun(run), nil
}
func (m *TrainingManager) Stop(root, id, version, runID string) (TrainingRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil || m.active.key != trainingKey(root, id, version) || m.active.run.ID != runID {
		runs, err := ReadTrainingRuns(root, id, version)
		if err != nil {
			return TrainingRun{}, err
		}
		for _, run := range runs {
			if run.ID == runID {
				return run, nil
			}
		}
		return TrainingRun{}, errors.New("training run is unavailable")
	}
	job := m.active
	if job.finalizing {
		return cloneTrainingRun(job.run), errors.New("training has finished and is saving its checkpoint; wait a moment")
	}
	job.cancel()
	job.run.Status = "cancelled"
	job.run.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	created, _ := time.Parse(time.RFC3339Nano, job.run.CreatedAt)
	job.run.DurationMs = time.Since(created).Milliseconds()
	if err := WriteTrainingRun(root, id, version, job.run); err != nil {
		return TrainingRun{}, err
	}
	return cloneTrainingRun(job.run), nil
}
func (m *TrainingManager) CancelModel(root, id, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil && m.active.key == trainingKey(root, id, version) {
		m.active.cancel()
	}
}
func (m *TrainingManager) Close() {
	m.mu.Lock()
	m.closed = true
	if m.active != nil {
		m.active.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

type trainingEvent struct {
	Type           string           `json:"type"`
	Error          string           `json:"error"`
	Epoch          int              `json:"epoch"`
	Train          *TrainingMetrics `json:"train"`
	Validation     *TrainingMetrics `json:"validation"`
	Test           *TrainingMetrics `json:"test"`
	ParameterCount int64            `json:"parameterCount"`
}

func (m *TrainingManager) execute(job *trainingJob, snap TrainingSnapshot, publish TrainingPublisher) {
	err := m.process(job, snap, publish)
	m.mu.Lock()
	defer m.mu.Unlock()
	if job.run.Status == "running" {
		if err != nil {
			job.run.Status = "failed"
			job.run.Error = err.Error()
			if job.ctx.Err() != nil {
				job.run.Status = "cancelled"
				job.run.Error = "Training stopped. No incomplete checkpoint was published."
			}
		}
		if job.run.Status == "running" {
			job.run.Status = "failed"
			job.run.Error = "Training ended without a completed checkpoint."
		}
	}
	job.run.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	created, _ := time.Parse(time.RFC3339Nano, job.run.CreatedAt)
	job.run.DurationMs = time.Since(created).Milliseconds()
	_ = WriteTrainingRun(job.root, job.model, job.version, job.run)
	if m.active == job {
		m.active = nil
	}
}
func (m *TrainingManager) process(job *trainingJob, snap TrainingSnapshot, publish TrainingPublisher) error {
	dir, err := os.MkdirTemp("", "umcode-training-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	paths := map[string]string{}
	for name, rows := range snap.Splits {
		path := filepath.Join(dir, name+".jsonl")
		if err = os.WriteFile(path, rows, 0600); err != nil {
			return err
		}
		paths[name] = path
	}
	// Drop copied split buffers after staging; the publisher only needs graph and preparation.
	snap.Splits = nil
	weightsPath := filepath.Join(dir, "trained.pt")
	input, err := json.Marshal(map[string]any{"graph": snap.Graph, "selection": snap.Selection, "config": job.run.Config, "splitPaths": paths, "weightsPath": weightsPath})
	if err != nil {
		return err
	}
	source := "import types,sys\nm=types.ModuleType('runner')\nexec(" + strconv.Quote(trustedInferenceRunner) + ",m.__dict__)\nsys.modules['runner']=m\n" + trustedTrainingRunner
	cmd := exec.CommandContext(job.ctx, m.python, "-I", "-u", "-c", source)
	cmd.Env = append(os.Environ(), "OMP_NUM_THREADS=1", "MKL_NUM_THREADS=1", "TOKENIZERS_PARALLELISM=false")
	cmd.Stdin = bytes.NewReader(append(input, '\n'))
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start local PyTorch training: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var completed *trainingEvent
	var processErr error
	for scanner.Scan() {
		var event trainingEvent
		if err = json.Unmarshal(scanner.Bytes(), &event); err != nil {
			processErr = errors.New("local training returned invalid progress")
			break
		}
		if event.Type == "error" {
			processErr = fmt.Errorf("training: %s", event.Error)
			break
		}
		if event.Type == "complete" {
			copy := event
			completed = &copy
			continue
		}
		if event.Type != "epoch" || event.Train == nil || event.Epoch < 1 || event.Epoch > job.run.Config.Epochs {
			processErr = errors.New("local training returned invalid epoch metrics")
			break
		}
		m.mu.Lock()
		if job.run.Status == "running" {
			job.run.Epoch = event.Epoch
			job.run.Train = event.Train
			job.run.Validation = event.Validation
			job.run.History = append(job.run.History, TrainingEpoch{Epoch: event.Epoch, Train: event.Train, Validation: event.Validation})
			processErr = WriteTrainingRun(job.root, job.model, job.version, job.run)
		}
		m.mu.Unlock()
		if processErr != nil {
			break
		}
	}
	if processErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if processErr != nil {
		return processErr
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	if job.ctx.Err() != nil {
		return job.ctx.Err()
	}
	if waitErr != nil {
		return errors.New("PyTorch training stopped. Verify torch, safetensors, Pillow and numpy are installed; try a smaller model or batch size.")
	}
	if completed == nil || completed.Train == nil {
		return errors.New("training returned no final metrics")
	}
	info, err := os.Lstat(weightsPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > MaxCheckpointBytes {
		return errors.New("training produced invalid or oversized weights")
	}
	weights, err := os.ReadFile(weightsPath)
	if err != nil {
		return err
	}
	if publish == nil {
		return errors.New("training checkpoint publisher is unavailable")
	}
	m.mu.Lock()
	if job.ctx.Err() != nil {
		m.mu.Unlock()
		return job.ctx.Err()
	}
	job.finalizing = true
	m.mu.Unlock()
	cp, err := publish(job.ctx, snap, weights, job.run.Name)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job.run.Status = "completed"
	job.run.Error = ""
	job.run.CheckpointID = cp.ID
	job.run.Train = completed.Train
	job.run.Validation = completed.Validation
	job.run.Test = completed.Test
	job.run.ParameterCount = completed.ParameterCount
	return nil
}
