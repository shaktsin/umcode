package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/evaluation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/vault"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type evaluationRunner struct{}

func NewEvaluationRunner() evaluation.Runner { return &evaluationRunner{} }
func (*evaluationRunner) Run(ctx context.Context, in evaluation.RunInput) (result evaluation.TaskResult, err error) {
	result.Unit = in.Unit
	result.ConfigurationHash = evaluation.Hash(in.Manifest)
	result.ArtifactDir = filepath.Join(in.Workspace, "workspace")
	result.Status = "incomplete"
	if err = evaluation.Validate(in.Manifest); err != nil {
		return
	}
	task, ok := in.Manifest.Task(in.Unit.TaskID)
	if !ok {
		return result, errors.New("unknown task")
	}
	arm, ok := in.Manifest.Arm(in.Unit.ArmID)
	if !ok {
		return result, errors.New("unknown arm")
	}
	model, ok := in.Manifest.Model(in.Unit.ModelID)
	if !ok {
		return result, errors.New("unknown model")
	}
	if in.Live && (in.Credential == "" || model.CredentialRef == "" || model.CredentialKind == llm.KindChatGPT) {
		return result, errors.New("explicit token-billed credential required")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(task.Limits.Seconds)*time.Second)
	defer cancel()
	script := &evaluationScript{id: model.Provider, steps: task.Script}
	var e *Engine
	var st *store.Store
	var observer *evaluationProvider
	var attemptMu sync.Mutex
	defer func() {
		if observer != nil && observer.diagnostic() != nil {
			result.Status = "incomplete"
			result.IncompleteReasons = append(result.IncompleteReasons, observer.diagnostic().Error())
		}
	}()
	open := func() error {
		home := filepath.Join(in.Workspace, "home")
		if err := os.MkdirAll(home, 0700); err != nil {
			return err
		}
		var err error
		st, err = store.Open(ctx, filepath.Join(home, "umcode.db"))
		if err != nil {
			return err
		}
		cfg := config.Default(home)
		cfg.Models.Complexity = map[string]config.ComplexityPreset{model.Reasoning: {MaxOutputTokens: int(model.MaxOutput)}}
		cfg.Models.Configured = []config.ConfiguredModel{{ID: "evaluation", Provider: model.Provider, Model: model.ID}}
		cfg.Models.ExecutionLimits = config.ExecutionLimits{MaxDurationMinutes: max(1, (task.Limits.Seconds+59)/60), MaxTokens: task.Limits.Tokens, MaxCostUSD: cfg.Models.ExecutionLimits.MaxCostUSD, MaxToolRounds: task.Limits.ToolRounds}
		cfg.Tools.Workspaces = []config.WorkspaceConfig{{Name: "evaluation", Path: result.ArtifactDir, Default: true}}
		registry := llm.NewRegistry()
		if !in.Live {
			registry.Register(script)
		}
		inner, found := registry.Get(model.Provider)
		if !found {
			return errors.New("unsupported provider")
		}
		observer = &evaluationProvider{inner: inner, model: model, synthetic: !in.Live, before: in.BeforeAttempt, sink: func(a evaluation.Attempt) error {
			attemptMu.Lock()
			result.Attempts = append(result.Attempts, a)
			attemptMu.Unlock()
			if in.AttemptSink != nil {
				return in.AttemptSink(a)
			}
			return nil
		}}
		registry.Register(observer)
		secretStore := secrets.NewMemoryStore()
		e, err = New(ctx, Options{Config: cfg, Store: st, Secrets: secretStore, LLMs: registry, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DisableScheduler: true, DisableMCP: true})
		if err != nil {
			st.Close()
			return err
		}
		tools.ConstrainEvaluation(e.Tools, result.ArtifactDir, in.Manifest.BaseDir)
		if arm.Mode == "override" {
			p, _ := evaluation.EffectivePolicy(arm)
			e.evaluationPolicy = &p
		} else {
			if _, err = e.SetTokenOptimization(ctx, protocol.TokenOptimizationParams{Enabled: arm.Mode == "product-on"}); err != nil {
				return err
			}
		}
		key := in.Credential
		if !in.Live {
			key = "evaluation-synthetic"
		}
		creds, _ := st.ListCredentials(ctx, model.Provider)
		if len(creds) == 0 {
			_, err = e.Creds.Add(ctx, protocol.CredentialAddParams{Provider: model.Provider, Label: "evaluation", Secret: key})
		} else {
			err = secretStore.Set("credential/"+creds[0].ID, key)
		}
		return err
	}
	closeEngine := func() {
		if e != nil {
			sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
			e.Shutdown(sctx)
			c()
		}
		if st != nil {
			st.Close()
		}
	}
	defer closeEngine()
	if err = open(); err != nil {
		return
	}
	var projectID string
	if !task.Projectless {
		p, e2 := e.Projects.Create(ctx, protocol.ProjectCreateParams{Root: result.ArtifactDir, Name: "evaluation"})
		if e2 != nil {
			return result, e2
		}
		projectID = p.ID
	}
	mode := task.ApprovalMode
	if mode == "" {
		mode = "normal"
	}
	th, e2 := e.StartThread(ctx, protocol.ThreadStartParams{Title: "Evaluation " + task.ID, ProjectID: projectID, Settings: protocol.ModelSelection{Provider: model.Provider, Model: model.ID, Complexity: protocol.Complexity(model.Reasoning)}, ApprovalMode: mode})
	if e2 != nil {
		return result, e2
	}
	for i, prompt := range task.Prompts {
		if i > 0 && script.restartPending() {
			closeEngine()
			e = nil
			st = nil
			if err = open(); err != nil {
				return
			}
		}
		policy, e2 := e.resolveOptimizationPolicy(ctx)
		if e2 != nil {
			return result, e2
		}
		result.Policies = append(result.Policies, policy)
		sub := &evaluationSubscriber{id: store.NewID("eval"), engine: e, automatic: policy.AutomaticWorkflow, allow: task.AllowActions, ctx: ctx}
		e.Bus.Add(sub)
		wait := make(chan protocol.Turn, 1)
		turn, e2 := e.startTurn(ctx, turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: prompt}, wait: wait})
		if e2 != nil {
			return result, e2
		}
		select {
		case finished := <-wait:
			if finished.Status != protocol.TurnCompleted {
				return result, fmt.Errorf("evaluation turn: %s %s", finished.Status, finished.Error)
			}
		case <-ctx.Done():
			e.InterruptTurn(turn.ID)
			return result, ctx.Err()
		}
		e.Bus.Remove(sub.id)
		sub.mu.Lock()
		subErr := sub.err
		sub.mu.Unlock()
		if subErr != nil {
			return result, subErr
		}
	}
	items, e2 := st.ListItems(ctx, th.ID, 0)
	if e2 != nil {
		return result, e2
	}
	var response strings.Builder
	for _, item := range items {
		if item.Kind == protocol.ItemAgentMessage {
			response.WriteString(item.Text)
			response.WriteByte('\n')
		}
	}
	redacted, _ := vault.Redact([]byte(response.String()))
	if len(redacted) > 64<<10 {
		redacted = redacted[:64<<10]
	}
	result.Response = string(redacted)
	result.ArtifactHash, err = evaluation.TreeHash(result.ArtifactDir)
	if err != nil {
		return
	}
	result.Status = "completed"
	return
}

type evaluationSubscriber struct {
	id               string
	engine           *Engine
	automatic, allow bool
	ctx              context.Context
	mu               sync.Mutex
	err              error
}

func (s *evaluationSubscriber) ID() string      { return s.id }
func (*evaluationSubscriber) IsAdmin() bool     { return true }
func (*evaluationSubscriber) Wants(string) bool { return true }
func (s *evaluationSubscriber) Notify(method string, p any) {
	if method != protocol.NotifyApprovalRequest {
		return
	}
	b, _ := json.Marshal(p)
	var event protocol.ApprovalEvent
	_ = json.Unmarshal(b, &event)
	a := event.Approval
	// Resolve asynchronously: Bus holds a read lock while delivering notifications.
	go func() {
		allow := s.allow
		if a.Kind == "workflow" && s.automatic {
			s.mu.Lock()
			s.err = errors.New("unexpected internal workflow approval")
			s.mu.Unlock()
			allow = false
		}
		_, err := s.engine.RespondApproval(s.ctx, a.ID, allow, false, "evaluation fixture")
		if err != nil {
			s.mu.Lock()
			s.err = err
			s.mu.Unlock()
		}
	}()
}

type evaluationScript struct {
	id    string
	steps []evaluation.ScriptStep
	mu    sync.Mutex
	index int
}

func (s *evaluationScript) ID() string { return s.id }
func (s *evaluationScript) ListModels(context.Context, llm.Credential) ([]string, error) {
	return []string{"evaluation"}, nil
}
func (s *evaluationScript) restartPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.index < len(s.steps) && s.steps[s.index].Restart
}
func (s *evaluationScript) Stream(ctx context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index >= len(s.steps) {
		return nil, errors.New("evaluation script exhausted")
	}
	step := s.steps[s.index]
	if step.When != "" {
		b, _ := json.Marshal(req)
		if !strings.Contains(string(b), step.When) {
			return nil, errors.New("evaluation script request condition missing")
		}
	}
	s.index++
	ch := make(chan llm.Event, 2)
	if step.Tool != "" {
		name := tools.ToWire(step.Tool)
		found := false
		for _, tool := range req.Tools {
			if tool.Name == name {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("scripted tool is not exposed: %s", step.Tool)
		}
		ch <- llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: fmt.Sprintf("eval-%d", s.index), Name: name, Args: step.Args}}
	} else {
		ch <- llm.Event{Type: llm.EventTextDelta, Text: step.Text}
	}
	ch <- llm.Event{Type: llm.EventDone, Usage: llm.Usage{Reported: true, InputTokens: estimateInput(req), OutputTokens: llm.EstimateTokens(step.Text + string(step.Args))}}
	close(ch)
	return ch, nil
}
