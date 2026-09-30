package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shaktsin/umcode/internal/preview"
	"github.com/shaktsin/umcode/internal/visualqa"
)

type visualStart struct {
	previews *preview.Manager
	manager  *visualqa.Manager
}

func (*visualStart) Name() string { return "visual.start" }
func (*visualStart) Description() string {
	return "Open this task's live preview in UMCode's isolated Chromium, inspect the rendered UI as a black-box user, and capture an initial screenshot plus console/network diagnostics. Use after frontend changes."
}
func (*visualStart) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"preview_id":{"type":"string"}},"required":["preview_id"]}`)
}
func (*visualStart) Assess(json.RawMessage) (Risk, string) {
	return RiskGreen, "Open the task preview in isolated Visual QA"
}
func (t *visualStart) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct {
		PreviewID string `json:"preview_id"`
	}](args)
	if err != nil {
		return "", err
	}
	s := ScopeFrom(ctx)
	if s == nil || s.Root == "" {
		return "", ErrNoProject
	}
	p, err := t.previews.GetFor(a.PreviewID, s.ThreadID)
	if err != nil {
		return "", err
	}
	r, err := t.manager.Start(s.ThreadID, s.Root, p.URL)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(r)
	return string(b), nil
}

type visualInspect struct{ manager *visualqa.Manager }

func (*visualInspect) Name() string { return "visual.inspect" }
func (*visualInspect) Description() string {
	return "Inspect the current isolated browser page and return visible text, controls, broken images, layout overflow, console errors, and failed requests; optionally capture a screenshot artifact."
}
func (*visualInspect) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"screenshot":{"type":"boolean"}}}`)
}
func (*visualInspect) Assess(json.RawMessage) (Risk, string) {
	return RiskGreen, "Inspect the Visual QA browser"
}
func (t *visualInspect) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct {
		Screenshot bool `json:"screenshot"`
	}](args)
	if err != nil {
		return "", err
	}
	s := ScopeFrom(ctx)
	if s == nil {
		return "", ErrNoProject
	}
	session := t.manager.ForThread(s.ThreadID)
	if session == nil {
		return "", errors.New("no Visual QA browser is running for this task")
	}
	snap, err := session.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	r := session.Report("passed", snap)
	if a.Screenshot {
		artifact, e := session.Screenshot(ctx, "inspection.png")
		if e != nil {
			return "", e
		}
		r.Artifacts = append(r.Artifacts, artifact)
	}
	b, _ := json.Marshal(r)
	return string(b), nil
}

type visualAct struct{ manager *visualqa.Manager }

func (*visualAct) Name() string { return "visual.act" }
func (*visualAct) Description() string {
	return "Perform one user-like action on a control from the latest visual.inspect snapshot, then return the resulting page state. Supported actions are click, fill, and press."
}
func (*visualAct) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"action":{"type":"string","enum":["click","fill","press"]},"control_index":{"type":"integer","minimum":0},"value":{"type":"string"}},"required":["action","control_index"]}`)
}
func (*visualAct) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct {
		Action string `json:"action"`
		Index  int    `json:"control_index"`
	}](args)
	return RiskGreen, fmt.Sprintf("Visual QA %s control %d in the isolated local preview", a.Action, a.Index)
}
func (t *visualAct) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct {
		Action string `json:"action"`
		Index  int    `json:"control_index"`
		Value  string `json:"value"`
	}](args)
	if err != nil {
		return "", err
	}
	s := ScopeFrom(ctx)
	if s == nil {
		return "", ErrNoProject
	}
	session := t.manager.ForThread(s.ThreadID)
	if session == nil {
		return "", errors.New("no Visual QA browser is running for this task")
	}
	snap, err := session.Act(ctx, a.Action, a.Index, a.Value)
	if err != nil {
		return "", err
	}
	r := session.Report("passed", snap)
	b, _ := json.Marshal(r)
	return string(b), nil
}

type visualStop struct{ manager *visualqa.Manager }

func (*visualStop) Name() string { return "visual.stop" }
func (*visualStop) Description() string {
	return "Stop this task's isolated Visual QA browser and delete its ephemeral browser profile."
}
func (*visualStop) Schema() json.RawMessage               { return schema(`{"type":"object","properties":{}}`) }
func (*visualStop) Assess(json.RawMessage) (Risk, string) { return RiskGreen, "Stop Visual QA" }
func (t *visualStop) Call(ctx context.Context, _ json.RawMessage) (string, error) {
	s := ScopeFrom(ctx)
	if s == nil {
		return "", ErrNoProject
	}
	t.manager.Stop(s.ThreadID)
	return "Visual QA browser stopped; screenshot artifacts remain reviewable.", nil
}

func visualNotRun(reason string) string {
	b, _ := json.Marshal(visualqa.Report{Status: "not_run", Framework: "umcode-visual-qa", Diagnostics: []string{}, Artifacts: []visualqa.Artifact{}, Reason: reason})
	return string(b)
}
