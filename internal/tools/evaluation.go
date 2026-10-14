package tools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/sandbox"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ConstrainEvaluation adds stricter execution boundaries to an already isolated
// evaluation registry. It is never called for an ordinary engine.
func ConstrainEvaluation(r *Registry, root, source string) {
	if tool, ok := r.Get("shell.run"); ok {
		if shell, ok := tool.(*shellRun); ok {
			shell.sandbox = evaluationSandbox{inner: sandbox.Detect(), root: root, source: source}
			shell.skillEnv = nil
		}
	}
	for _, tool := range r.All() {
		name := tool.Name()
		if strings.HasPrefix(name, "computer.") || strings.HasPrefix(name, "visual.") || strings.HasPrefix(name, "web.") || strings.HasPrefix(name, "preview.") || name == "browser.verify" {
			r.Add(evaluationUnavailable{Tool: tool})
		}
	}
}

type evaluationUnavailable struct{ Tool }

func (evaluationUnavailable) Call(context.Context, json.RawMessage) (string, error) {
	return "", errors.New("specialist requires an isolated evaluation adapter; evidence unavailable")
}

type evaluationSandbox struct {
	inner        sandbox.Sandbox
	root, source string
}

func (evaluationSandbox) Name() string { return "evaluation" }
func (s evaluationSandbox) Wrap(cmd *exec.Cmd, _ sandbox.Policy) error {
	if s.inner == nil {
		return errors.New("evaluation requires OS execution confinement")
	}
	root, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		return err
	}
	for _, name := range []string{".evaluation-home", ".evaluation-tmp", ".evaluation-cache"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			return err
		}
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/go/bin:/opt/homebrew/bin", "HOME=" + filepath.Join(root, ".evaluation-home"), "TMPDIR=" + filepath.Join(root, ".evaluation-tmp"), "GOCACHE=" + filepath.Join(root, ".evaluation-cache"), "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off"}
	p := sandbox.EvaluationPolicy(root, nil)
	if s.source != "" {
		source, err := filepath.EvalSymlinks(s.source)
		if err != nil {
			return err
		}
		p.DenyRead = append(p.DenyRead, source)
	}
	return s.inner.Wrap(cmd, p)
}
