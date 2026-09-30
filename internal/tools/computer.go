package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shaktsin/umcode/internal/computeruse"
)

type computerList struct{ manager *computeruse.Manager }

func (*computerList) Name() string { return "computer.list" }
func (*computerList) Description() string {
	return "List visible desktop applications available for Computer Use. Use this before selecting an application by name or bundle ID."
}
func (*computerList) Schema() json.RawMessage { return schema(`{"type":"object","properties":{}}`) }
func (*computerList) Assess(json.RawMessage) (Risk, string) {
	return RiskGreen, "List visible desktop applications"
}
func (t *computerList) Call(ctx context.Context, _ json.RawMessage) (string, error) {
	r, err := t.manager.List(ctx)
	return encodeComputerReport(r), err
}

type computerStart struct{ manager *computeruse.Manager }

func (*computerStart) Name() string { return "computer.start" }
func (*computerStart) Description() string {
	return "Open or select a macOS application for this task, optionally open a URL in that application, and return a full-resolution screenshot. Starting control requires approval in Ask every time mode; later routine interactions do not. The session persists across later computer.inspect and computer.act calls."
}
func (*computerStart) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"app_name":{"type":"string"},"bundle_id":{"type":"string"},"app_path":{"type":"string"},"url":{"type":"string"}},"additionalProperties":false}`)
}
func (*computerStart) Assess(args json.RawMessage) (Risk, string) {
	var a struct {
		AppName  string `json:"app_name"`
		BundleID string `json:"bundle_id"`
		AppPath  string `json:"app_path"`
		URL      string `json:"url"`
	}
	_ = json.Unmarshal(args, &a)
	target := firstNonBlank(a.AppName, a.BundleID, a.AppPath, a.URL, "desktop application")
	return RiskRed, "Start Computer Use session for " + target
}
func (t *computerStart) Call(ctx context.Context, args json.RawMessage) (string, error) {
	s, err := requireComputerScope(ctx)
	if err != nil {
		return "", err
	}
	var a struct {
		AppName  string `json:"app_name"`
		BundleID string `json:"bundle_id"`
		AppPath  string `json:"app_path"`
		URL      string `json:"url"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	r, err := t.manager.Start(ctx, s.ThreadID, s.Root, computeruse.Target{Name: a.AppName, BundleID: a.BundleID, Path: a.AppPath, URL: a.URL})
	return encodeComputerReport(r), err
}

type computerInspect struct{ manager *computeruse.Manager }

func (*computerInspect) Name() string { return "computer.inspect" }
func (*computerInspect) Description() string {
	return "Capture the selected application's current window at original resolution and return its window metadata plus redacted accessibility controls. Prefer an enabled control's exact id with computer.act when available; bounds are screenshot-pixel coordinates. Fall back to vision-coordinates for inaccessible/custom UI. Inspect before acting and verify after each action."
}
func (*computerInspect) Schema() json.RawMessage { return schema(`{"type":"object","properties":{}}`) }
func (*computerInspect) Assess(json.RawMessage) (Risk, string) {
	return RiskGreen, "Inspect selected desktop application"
}
func (t *computerInspect) Call(ctx context.Context, _ json.RawMessage) (string, error) {
	s, err := requireComputerScope(ctx)
	if err != nil {
		return "", err
	}
	r, err := t.manager.Inspect(ctx, s.ThreadID)
	return encodeComputerReport(r), err
}

type computerAct struct{ manager *computeruse.Manager }

func (*computerAct) Name() string { return "computer.act" }
func (*computerAct) Description() string {
	return "Dispatch one user-like action in the selected desktop app, then return a fresh screenshot. Routine navigation and editing do not require individual approval after the session starts. Consequential actions (submitting/sending, purchases, deletion, security changes, or entering credentials/secrets) require approval in Ask every time mode. Include a short target_description without field values or secrets. Prefer element_id copied exactly from the latest accessibility controls; otherwise use x/y screenshot pixels. For pointer actions, copy observation_id from the screenshot report. Use move first when helpful to position the visible pointer, then click separately. Inspect the returned screenshot and verify the intended state change. Supported actions: move, click, double_click, fill, type, key, scroll."
}
func (*computerAct) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"action":{"type":"string","enum":["move","click","double_click","fill","type","key","scroll"]},"element_id":{"type":"string","description":"Exact control id from the latest computer.inspect accessibility controls."},"target_description":{"type":"string","description":"Short intended target or outcome; never include form values or secrets."},"x":{"type":"number","minimum":0},"y":{"type":"number","minimum":0},"observation_id":{"type":"string","description":"The observation_id from the screenshot currently being used to choose coordinates."},"text":{"type":"string"},"key":{"type":"string"},"delta":{"type":"integer","minimum":-4000,"maximum":4000}},"required":["action"],"additionalProperties":false}`)
}
func (*computerAct) Assess(args json.RawMessage) (Risk, string) {
	var a struct {
		Action            string `json:"action"`
		Key               string `json:"key"`
		TargetDescription string `json:"target_description"`
	}
	_ = json.Unmarshal(args, &a)
	detail := strings.TrimSpace(a.Action)
	if a.Action == "key" {
		detail += " " + strings.TrimSpace(a.Key)
	}
	if computerActionIsConsequential(a.Action, a.Key, a.TargetDescription) {
		return RiskRed, "Computer Use " + detail + " — " + firstNonBlank(a.TargetDescription, "consequential app action")
	}
	// Pointer movement, scrolling, navigation, and ordinary edits are part of
	// the already-approved Computer Use session. They remain visible in the
	// activity stream but do not each interrupt the user with an approval card.
	return RiskYellow, "Computer Use " + detail + " — " + firstNonBlank(a.TargetDescription, "routine app interaction")
}

func computerActionIsConsequential(action, key, target string) bool {
	// Never echo or inspect the actual value being typed; classify from the
	// action and target metadata only, which should not contain field values.
	combined := strings.ToLower(strings.Join([]string{action, key, target}, " "))
	for _, word := range []string{
		"password", "passcode", "credential", "api key", "access token", "secret", "private key",
		"submit", "send", "publish", "purchase", "checkout", "pay now", "place order", "delete", "remove permanently",
		"revoke", "transfer", "security setting", "permission", "two-factor", "2fa", "factory reset",
	} {
		if strings.Contains(combined, word) {
			return true
		}
	}
	// Return/Enter often submits a form; keep this boundary explicit even if
	// the target is a custom control that accessibility cannot name.
	return action == "key" && (strings.EqualFold(key, "return") || strings.EqualFold(key, "enter"))
}
func (t *computerAct) Call(ctx context.Context, args json.RawMessage) (string, error) {
	s, err := requireComputerScope(ctx)
	if err != nil {
		return "", err
	}
	// The tool schema's parameter is "action" (see Schema above), not the
	// "type" json tag computeruse.Action itself uses for the outbound wire
	// format to the native helper. Decode into the schema's own shape first,
	// then build the Action explicitly, so a schema/model call never lands
	// on Action's zero-value Type and gets rejected as "unsupported computer
	// action """.
	var parsed struct {
		Action            string  `json:"action"`
		X                 float64 `json:"x"`
		Y                 float64 `json:"y"`
		Text              string  `json:"text"`
		Key               string  `json:"key"`
		Delta             int     `json:"delta"`
		ObservationID     string  `json:"observation_id"`
		ElementID         string  `json:"element_id"`
		TargetDescription string  `json:"target_description"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	a := computeruse.Action{
		Type:              parsed.Action,
		X:                 parsed.X,
		Y:                 parsed.Y,
		Text:              parsed.Text,
		Key:               parsed.Key,
		Delta:             parsed.Delta,
		ObservationID:     parsed.ObservationID,
		ElementID:         parsed.ElementID,
		TargetDescription: parsed.TargetDescription,
	}
	r, err := t.manager.Act(ctx, s.ThreadID, a)
	return encodeComputerReport(r), err
}

type computerStop struct{ manager *computeruse.Manager }

func (*computerStop) Name() string { return "computer.stop" }
func (*computerStop) Description() string {
	return "Release this task's Computer Use session. It does not quit the user's application and keeps screenshot artifacts reviewable."
}
func (*computerStop) Schema() json.RawMessage { return schema(`{"type":"object","properties":{}}`) }
func (*computerStop) Assess(json.RawMessage) (Risk, string) {
	return RiskGreen, "Stop Computer Use session"
}
func (t *computerStop) Call(ctx context.Context, _ json.RawMessage) (string, error) {
	s, err := requireComputerScope(ctx)
	if err != nil {
		return "", err
	}
	t.manager.Stop(s.ThreadID)
	return `{"status":"stopped","framework":"umcode-computer-use","artifacts":[]}`, nil
}

func requireComputerScope(ctx context.Context) (*Scope, error) {
	s := ScopeFrom(ctx)
	if s == nil || s.Root == "" || s.ThreadID == "" {
		return nil, ErrNoProject
	}
	return s, nil
}

func encodeComputerReport(r computeruse.Report) string {
	b, _ := json.Marshal(r)
	return string(b)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
