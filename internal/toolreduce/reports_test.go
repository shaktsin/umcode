package toolreduce

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func visualFixture(t *testing.T) string {
	t.Helper()
	snapshot, err := json.Marshal(map[string]any{
		"url": "http://127.0.0.1:4173/settings", "title": "Settings", "viewport": map[string]int{"width": 1280, "height": 720},
		"text":         strings.Repeat("ordinary page copy ", 600),
		"controls":     []map[string]any{{"i": 0, "tag": "button", "text": "Save", "type": "button", "disabled": false, "href": ""}},
		"brokenImages": []string{"http://127.0.0.1:4173/missing.png"}, "horizontalOverflow": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{
		"status": "failed", "framework": "umcode-visual-qa", "duration_ms": 43, "output": "snapshot completed", "reason": "layout check failed",
		"session_id": "visual-abc", "snapshot": string(snapshot), "diagnostics": []string{"console error: save failed", "request failed: /api/save"},
		"artifacts": []map[string]any{{"path": ".umcode/artifacts/visual-qa/visual-abc/inspection.png", "kind": "screenshot", "mime_type": "image/png", "bytes": 54321}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func computerFixture(t *testing.T) string {
	t.Helper()
	controls := []map[string]any{
		{"id": "static-1", "role": "text", "label": "ordinary prose", "identifier": "", "x": 0, "y": 0, "width": 20, "height": 12, "enabled": false, "focused": false},
		{"id": "enabled-1", "role": "button", "label": "Save", "identifier": "save-button", "x": 20, "y": 30, "width": 90, "height": 20, "enabled": true, "focused": false},
		{"id": "focused-1", "role": "textbox", "label": "Name", "identifier": "name-field", "x": 20, "y": 60, "width": 190, "height": 20, "enabled": true, "focused": true},
		{"id": "disabled-1", "role": "button", "label": "Delete", "identifier": "delete-button", "x": 20, "y": 90, "width": 90, "height": 20, "enabled": false, "focused": false},
		{"id": "enabled-2", "role": "button", "label": "Cancel", "identifier": "cancel-button", "x": 20, "y": 120, "width": 90, "height": 20, "enabled": true, "focused": false},
	}
	for i := 0; i < 80; i++ {
		controls = append(controls, map[string]any{"id": fmt.Sprintf("static-%d", i+2), "role": "text", "label": strings.Repeat("routine static copy ", 4), "enabled": false, "focused": false})
	}
	b, err := json.Marshal(map[string]any{
		"status": "dispatched", "framework": "umcode-computer-use", "session_id": "computer-abc", "observation_id": "computer-abc-9", "action_count": 4, "duration_ms": 21,
		"state": map[string]any{"app": map[string]any{"name": "Safari", "bundle_id": "com.apple.Safari", "pid": 321, "path": "/Applications/Safari.app"},
			"window":     map[string]any{"id": 12, "title": "Settings - Safari", "x": 10, "y": 20, "width": 960, "height": 540, "pixel_width": 1920, "pixel_height": 1080},
			"permission": "granted", "controls": controls},
		"apps": []any{}, "reason": "", "artifacts": []map[string]any{{"path": ".umcode/artifacts/computer-use/computer-abc/action.png", "kind": "screenshot", "mime_type": "image/png", "bytes": 123456}},
		"last_action": map[string]any{"type": "click", "target_description": "Save settings", "element_id": "enabled-1", "x": 70, "y": 40, "screenshot_width": 1920, "screenshot_height": 1080, "observation_id": "computer-abc-8", "result": "dispatched; verify against this fresh screenshot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestVisualReducerKeepsSessionSnapshotFailuresAndArtifacts(t *testing.T) {
	out := visualFixture(t)
	got, report, applied := Reduce(Input{Name: "visual.inspect", Output: out, Budget: 450})
	if !applied {
		t.Fatalf("declined: %s", report.Declined)
	}
	for _, want := range []string{"failed", "umcode-visual-qa", "visual-abc", "http://127.0.0.1:4173/settings", "1280", "720", "missing.png", "horizontalOverflow", "console error: save failed", "request failed: /api/save", "layout check failed", "inspection.png", "screenshot"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if !strings.Contains(got, "omitted:") || !strings.Contains(got, "visual.inspect") {
		t.Fatalf("missing retrieval notice: %s", got)
	}
}

func TestVisualReducerUsesRemainingBudgetForVisibleText(t *testing.T) {
	out := visualFixture(t)
	got, report, applied := Reduce(Input{Name: "visual.inspect", Output: out, Budget: 900})
	if !applied {
		t.Fatalf("declined: %s", report.Declined)
	}
	if !strings.Contains(got, `visible_text: "ordinary page copy`) {
		t.Fatalf("visible text omitted despite available budget: %s", got)
	}
	if report.Omitted["visible_text_bytes"] <= 0 || report.Omitted["visible_text_bytes"] >= len(strings.Repeat("ordinary page copy ", 600)) {
		t.Fatalf("wrong visible text omission count: %#v", report.Omitted)
	}
}

func TestVisualReducerAcceptsProducerNilDiagnostics(t *testing.T) {
	out := strings.Replace(visualFixture(t), `"diagnostics":["console error: save failed","request failed: /api/save"]`, `"diagnostics":null`, 1)
	if !strings.Contains(out, `"diagnostics":null`) {
		t.Fatal("fixture replacement failed")
	}
	got, report, applied := Reduce(Input{Name: "visual.inspect", Output: out, Budget: 450})
	if !applied || !strings.Contains(got, "visual-abc") {
		t.Fatalf("producer nil diagnostics declined: %s", report.Declined)
	}
}

func TestComputerReducerPrioritizesFocusedThenEnabledControls(t *testing.T) {
	got, report, applied := Reduce(Input{Name: "computer.act", Output: computerFixture(t), Budget: 470})
	if !applied {
		t.Fatalf("declined: %s", report.Declined)
	}
	for _, want := range []string{"focused-1", "enabled-1", "enabled-2"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s", want)
		}
	}
	if !(strings.Index(got, "control: id=focused-1") < strings.Index(got, "control: id=enabled-1") && strings.Index(got, "control: id=enabled-1") < strings.Index(got, "control: id=enabled-2")) {
		t.Fatalf("wrong priority: %s", got)
	}
}

func TestComputerReducerKeepsObservationAndLastAction(t *testing.T) {
	got, report, applied := Reduce(Input{Name: "computer.act", Output: computerFixture(t), Budget: 470})
	if !applied {
		t.Fatalf("declined: %s", report.Declined)
	}
	for _, want := range []string{"dispatched", "umcode-computer-use", "computer-abc-9", "computer-abc-8", "Safari", "com.apple.Safari", "Settings - Safari", "1920", "1080", "Save settings", "dispatched; verify against this fresh screenshot", "action.png", "screenshot"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestComputerReducerCountsOmittedControlClasses(t *testing.T) {
	got, report, applied := Reduce(Input{Name: "computer.act", Output: computerFixture(t), Budget: 470})
	if !applied {
		t.Fatalf("declined: %s", report.Declined)
	}
	if report.Omitted["static_controls"] != 78 || report.Omitted["enabled_controls"] != 0 || report.Omitted["focused_controls"] != 0 || report.Omitted["disabled_controls"] != 0 {
		t.Fatalf("wrong class omissions: %#v", report.Omitted)
	}
	if !strings.Contains(got, "static controls") || !strings.Contains(got, "computer.inspect") {
		t.Fatalf("missing omission notice: %s", got)
	}
}

func TestReportReducerDeclinesMissingIdentityOrUnknownSchema(t *testing.T) {
	computer := computerFixture(t)
	visual := visualFixture(t)
	cases := []struct{ name, tool, out string }{
		{"missing observation", "computer.act", strings.Replace(computer, `"observation_id":"computer-abc-9",`, "", 1)},
		{"unknown report", "computer.act", strings.Replace(computer, `"action_count":4,`, `"future_failure":"serious","action_count":4,`, 1)},
		{"unknown control", "computer.act", strings.Replace(computer, `"id":"focused-1",`, `"future_failure":"serious","id":"focused-1",`, 1)},
		{"unknown visual snapshot", "visual.inspect", strings.Replace(visual, `\"horizontalOverflow\":true`, `\"newFailure\":true,\"horizontalOverflow\":true`, 1)},
		{"missing visual session", "visual.inspect", strings.Replace(visual, `"session_id":"visual-abc",`, "", 1)},
		{"unknown artifact", "computer.act", strings.Replace(computer, `"kind":"screenshot",`, `"new_failure":"serious","kind":"screenshot",`, 1)},
		{"unknown last action", "computer.act", strings.Replace(computer, `"result":"dispatched; verify against this fresh screenshot"`, `"new_failure":"serious","result":"dispatched; verify against this fresh screenshot"`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := computer
			if strings.HasPrefix(tc.tool, "visual.") {
				base = visual
			}
			if tc.out == base {
				t.Fatal("fixture mutation had no effect")
			}
			got, _, applied := Reduce(Input{Name: tc.tool, Output: tc.out, Budget: 470})
			if applied || got != tc.out {
				t.Fatal("wanted byte-identical decline")
			}
		})
	}
}

func TestReportReducerDeclinesMissingWindowGeometry(t *testing.T) {
	out := computerFixture(t)
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	window := raw["state"].(map[string]any)["window"].(map[string]any)
	delete(window, "width")
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, _, applied := Reduce(Input{Name: "computer.act", Output: string(b), Budget: 470})
	if applied || got != string(b) {
		t.Fatal("missing window width did not decline")
	}
}

func TestComputerReducerRecognizesStoppedReport(t *testing.T) {
	c := reduceReport(Input{Name: "computer.stop", Output: `{"status":"stopped","framework":"umcode-computer-use","artifacts":[]}`})
	if !strings.Contains(c.text, "status: stopped") {
		t.Fatalf("stopped report declined: %#v", c)
	}
}

func TestReportReducerDeclinesWhenMandatoryDataCannotFit(t *testing.T) {
	out := computerFixture(t)
	got, _, applied := Reduce(Input{Name: "computer.act", Output: out, Budget: 60})
	if applied || got != out {
		t.Fatal("mandatory report data was dropped")
	}
}
