package toolreduce

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/shaktsin/umcode/internal/llm"
)

// These types mirror the owned report JSON. Keeping them here prevents the
// reducer from depending on the tools that produce reports.
type reportArtifact struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	MimeType string `json:"mime_type"`
	Bytes    int64  `json:"bytes"`
}
type visualReport struct {
	Status      string           `json:"status"`
	Framework   string           `json:"framework"`
	DurationMS  int64            `json:"duration_ms"`
	Output      string           `json:"output,omitempty"`
	Diagnostics []string         `json:"diagnostics"`
	Artifacts   []reportArtifact `json:"artifacts"`
	Reason      string           `json:"reason,omitempty"`
	SessionID   string           `json:"session_id,omitempty"`
	Snapshot    string           `json:"snapshot,omitempty"`
}
type visualViewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}
type visualControl struct {
	Index    int    `json:"i"`
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	Type     string `json:"type"`
	Disabled bool   `json:"disabled"`
	Href     string `json:"href"`
}
type visualSnapshot struct {
	URL                string          `json:"url"`
	Title              string          `json:"title"`
	Viewport           visualViewport  `json:"viewport"`
	Text               string          `json:"text"`
	Controls           []visualControl `json:"controls"`
	BrokenImages       []string        `json:"brokenImages"`
	HorizontalOverflow bool            `json:"horizontalOverflow"`
}
type computerApp struct {
	Name     string `json:"name"`
	BundleID string `json:"bundle_id,omitempty"`
	PID      int    `json:"pid,omitempty"`
	Path     string `json:"path,omitempty"`
}
type computerWindow struct {
	ID          int     `json:"id"`
	Title       string  `json:"title,omitempty"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	PixelWidth  int     `json:"pixel_width,omitempty"`
	PixelHeight int     `json:"pixel_height,omitempty"`
}
type computerControl struct {
	ID         string  `json:"id"`
	Role       string  `json:"role,omitempty"`
	Label      string  `json:"label,omitempty"`
	Identifier string  `json:"identifier,omitempty"`
	X          float64 `json:"x,omitempty"`
	Y          float64 `json:"y,omitempty"`
	Width      float64 `json:"width,omitempty"`
	Height     float64 `json:"height,omitempty"`
	Enabled    bool    `json:"enabled,omitempty"`
	Focused    bool    `json:"focused,omitempty"`
}
type computerState struct {
	App        computerApp       `json:"app"`
	Window     computerWindow    `json:"window"`
	Permission string            `json:"permission,omitempty"`
	Controls   []computerControl `json:"controls,omitempty"`
}
type computerActionEvidence struct {
	Type              string  `json:"type"`
	TargetDescription string  `json:"target_description,omitempty"`
	ElementID         string  `json:"element_id,omitempty"`
	X                 float64 `json:"x,omitempty"`
	Y                 float64 `json:"y,omitempty"`
	ScreenshotWidth   int     `json:"screenshot_width,omitempty"`
	ScreenshotHeight  int     `json:"screenshot_height,omitempty"`
	ObservationID     string  `json:"observation_id,omitempty"`
	Result            string  `json:"result"`
}
type computerReport struct {
	Status        string                  `json:"status"`
	Framework     string                  `json:"framework"`
	SessionID     string                  `json:"session_id,omitempty"`
	State         *computerState          `json:"state,omitempty"`
	Apps          []computerApp           `json:"apps,omitempty"`
	Artifacts     []reportArtifact        `json:"artifacts"`
	Reason        string                  `json:"reason,omitempty"`
	DurationMS    int64                   `json:"duration_ms,omitempty"`
	ActionCount   int                     `json:"action_count,omitempty"`
	ObservationID string                  `json:"observation_id,omitempty"`
	LastAction    *computerActionEvidence `json:"last_action,omitempty"`
}

func reduceReport(in Input) candidate {
	if strings.HasPrefix(in.Name, "visual.") {
		return reduceVisualReport(in)
	}
	if strings.HasPrefix(in.Name, "computer.") {
		return reduceComputerReport(in)
	}
	return candidate{}
}

// strictReportJSON accepts the documented fields, including JSON omitempty
// fields, and declines any new field carrying non-empty semantics at any depth.
func strictReportJSON(data []byte, target any, mandatory ...string) bool {
	if !strictReportShape(data, reflect.TypeOf(target).Elem()) {
		return false
	}
	fields, ok := objectFields(data)
	if !ok {
		return false
	}
	for _, key := range mandatory {
		if !hasField(fields, key) {
			return false
		}
	}
	return json.Unmarshal(data, target) == nil
}

func strictReportShape(data []byte, typ reflect.Type) bool {
	if strings.TrimSpace(string(data)) == "null" {
		// The producer can represent absent collections as null. Pointers are
		// nullable by schema; call-specific identity checks below still reject
		// a null state or action when that result requires one.
		switch typ.Kind() {
		case reflect.Slice, reflect.Pointer:
			return true
		default:
			return false
		}
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		fields, ok := objectFields(data)
		if !ok {
			return false
		}
		type fieldSpec struct {
			typ      reflect.Type
			required bool
		}
		known := map[string]fieldSpec{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			parts := strings.Split(f.Tag.Get("json"), ",")
			name := parts[0]
			if name != "" && name != "-" {
				required := true
				for _, opt := range parts[1:] {
					if opt == "omitempty" {
						required = false
					}
				}
				known[name] = fieldSpec{f.Type, required}
			}
		}
		for name, spec := range known {
			if spec.required && !hasField(fields, name) {
				return false
			}
		}
		for name, raw := range fields {
			spec, exists := known[name]
			if !exists {
				if !emptyUnknownValue(raw) {
					return false
				}
				continue
			}
			if !strictReportShape(raw, spec.typ) {
				return false
			}
		}
		return true
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(data, &values) != nil {
			return false
		}
		for _, raw := range values {
			if !strictReportShape(raw, typ.Elem()) {
				return false
			}
		}
		return true
	default:
		v := reflect.New(typ).Interface()
		return json.Unmarshal(data, v) == nil
	}
}

func validReportStatus(status string) bool {
	switch status {
	case "passed", "failed", "blocked", "not_run", "dispatched", "stopped":
		return true
	}
	return false
}

func reduceVisualReport(in Input) candidate {
	var r visualReport
	if !strictReportJSON([]byte(in.Output), &r, "status", "framework", "diagnostics", "artifacts") || !validReportStatus(r.Status) || r.Framework != "umcode-visual-qa" || r.DurationMS < 0 {
		return candidate{}
	}
	needsVisualSession := in.Name == "visual.inspect" || in.Name == "visual.act" || (in.Name == "visual.start" && r.Status == "passed")
	if needsVisualSession && (r.SessionID == "" || r.Snapshot == "") {
		return candidate{}
	}
	var snap visualSnapshot
	if r.Snapshot != "" {
		if !strictReportJSON([]byte(r.Snapshot), &snap, "url", "title", "viewport", "text", "controls", "brokenImages", "horizontalOverflow") || snap.URL == "" || snap.Viewport.Width < 0 || snap.Viewport.Height < 0 {
			return candidate{}
		}
		for _, c := range snap.Controls {
			if c.Index < 0 || c.Tag == "" {
				return candidate{}
			}
		}
	}
	var b strings.Builder
	var required []string
	appendDetail(&b, "status", r.Status, &required)
	appendDetail(&b, "framework", r.Framework, &required)
	if r.SessionID != "" {
		appendDetail(&b, "session_id", r.SessionID, &required)
	}
	fmt.Fprintf(&b, "  duration_ms: %d\n", r.DurationMS)
	if r.Reason != "" {
		appendDetail(&b, "reason", r.Reason, &required)
	}
	for _, d := range r.Diagnostics {
		if d == "" {
			return candidate{}
		}
		appendDetail(&b, "diagnostic", d, &required)
	}
	if r.Snapshot != "" {
		appendDetail(&b, "url", snap.URL, &required)
		appendDetail(&b, "title", snap.Title, &required)
		fmt.Fprintf(&b, "  viewport: %dx%d\n", snap.Viewport.Width, snap.Viewport.Height)
		required = append(required, strconv.Itoa(snap.Viewport.Width), strconv.Itoa(snap.Viewport.Height))
		for _, src := range snap.BrokenImages {
			if src == "" {
				return candidate{}
			}
			appendDetail(&b, "broken image", src, &required)
		}
		fmt.Fprintf(&b, "  horizontalOverflow: %t\n", snap.HorizontalOverflow)
		if snap.HorizontalOverflow {
			required = append(required, "horizontalOverflow: true")
		}
	}
	if r.Status != "passed" && r.Output != "" {
		appendDetail(&b, "output", r.Output, &required)
	}
	if !appendReportArtifacts(&b, &required, r.Artifacts) {
		return candidate{}
	}
	base := b.String()
	selected := make([]bool, len(snap.Controls))
	var order []int
	for _, disabled := range []bool{false, true} {
		for i, c := range snap.Controls {
			if c.Disabled == disabled {
				order = append(order, i)
			}
		}
	}
	omittedOutput := 0
	if r.Status == "passed" {
		omittedOutput = len(r.Output)
	}
	for _, i := range order {
		line := fmt.Sprintf("  control[%d]: <%s> text=%q type=%q disabled=%t href=%q\n", snap.Controls[i].Index, snap.Controls[i].Tag, snap.Controls[i].Text, snap.Controls[i].Type, snap.Controls[i].Disabled, snap.Controls[i].Href)
		trial := base + line + visualOmissionNotice(len(snap.Controls)-1, len(snap.Text), omittedOutput)
		if int(llm.EstimateTokens(trial)) > effectiveBudget(in) {
			break
		}
		base += line
		selected[i] = true
	}
	omittedControls := 0
	for _, keep := range selected {
		if !keep {
			omittedControls++
		}
	}
	visiblePrefix := ""
	if snap.Text != "" {
		low, high := 0, len(snap.Text)
		for low <= high {
			mid := low + (high-low)/2
			prefix := cropUTF8(snap.Text, mid)
			line := ""
			if prefix != "" {
				line = "  visible_text: " + strconv.Quote(prefix) + "\n"
			}
			trial := base + line + visualOmissionNotice(omittedControls, len(snap.Text)-len(prefix), omittedOutput)
			if int(llm.EstimateTokens(trial)) <= effectiveBudget(in) {
				visiblePrefix = prefix
				low = mid + 1
			} else {
				high = mid - 1
			}
		}
	}
	if visiblePrefix != "" {
		base += "  visible_text: " + strconv.Quote(visiblePrefix) + "\n"
	}
	omittedText := len(snap.Text) - len(visiblePrefix)
	base += visualOmissionNotice(omittedControls, omittedText, omittedOutput)
	return candidate{text: base, required: required, omitted: map[string]int{"visual_controls": omittedControls, "visible_text_bytes": omittedText, "passing_output_bytes": omittedOutput}}
}

func appendReportArtifacts(b *strings.Builder, required *[]string, artifacts []reportArtifact) bool {
	for _, a := range artifacts {
		if a.Path == "" || a.Kind == "" || a.Bytes < 0 {
			return false
		}
		line := fmt.Sprintf("  artifact: %s | kind: %s | mime_type: %s | bytes: %d\n", a.Path, a.Kind, a.MimeType, a.Bytes)
		b.WriteString(line)
		*required = append(*required, a.Path, a.Kind)
		if a.MimeType != "" {
			*required = append(*required, a.MimeType)
		}
	}
	return true
}

func computerControlClass(c computerControl) string {
	if c.Focused {
		return "focused"
	}
	if c.Enabled {
		return "enabled"
	}
	switch strings.ToLower(c.Role) {
	case "text", "statictext", "label", "heading", "paragraph":
		return "static"
	}
	return "disabled"
}

func computerControlLine(c computerControl) string {
	return fmt.Sprintf("  control: id=%s role=%q label=%q identifier=%q enabled=%t focused=%t bounds=(%g,%g,%g,%g)\n", c.ID, c.Role, c.Label, c.Identifier, c.Enabled, c.Focused, c.X, c.Y, c.Width, c.Height)
}

func reduceComputerReport(in Input) candidate {
	var r computerReport
	if !strictReportJSON([]byte(in.Output), &r, "status", "framework", "artifacts") || !validReportStatus(r.Status) || r.Framework != "umcode-computer-use" || r.DurationMS < 0 || r.ActionCount < 0 {
		return candidate{}
	}
	needsObservation := in.Name == "computer.start" || in.Name == "computer.inspect" || in.Name == "computer.act"
	if needsObservation && (r.SessionID == "" || r.ObservationID == "" || r.State == nil) {
		return candidate{}
	}
	if in.Name == "computer.act" && (r.LastAction == nil || r.LastAction.Result == "" || r.LastAction.ObservationID == "") {
		return candidate{}
	}
	if r.LastAction != nil && (r.LastAction.Type == "" || r.LastAction.Result == "") {
		return candidate{}
	}
	if r.State != nil && (r.State.App.Name == "" || r.State.Window.ID == 0 || r.State.Window.PixelWidth < 0 || r.State.Window.PixelHeight < 0) {
		return candidate{}
	}
	if r.State != nil {
		for _, c := range r.State.Controls {
			if c.ID == "" {
				return candidate{}
			}
		}
	}
	var b strings.Builder
	var required []string
	appendDetail(&b, "status", r.Status, &required)
	appendDetail(&b, "framework", r.Framework, &required)
	if r.SessionID != "" {
		appendDetail(&b, "session_id", r.SessionID, &required)
	}
	if r.ObservationID != "" {
		appendDetail(&b, "observation_id", r.ObservationID, &required)
	}
	fmt.Fprintf(&b, "  duration_ms: %d\n  action_count: %d\n", r.DurationMS, r.ActionCount)
	if r.Reason != "" {
		appendDetail(&b, "reason", r.Reason, &required)
	}
	if r.State != nil {
		a := r.State.App
		w := r.State.Window
		fmt.Fprintf(&b, "  app: %s | bundle_id: %s | pid: %d | path: %s\n", a.Name, a.BundleID, a.PID, a.Path)
		for _, v := range []string{a.Name, a.BundleID, a.Path} {
			if v != "" {
				required = append(required, v)
			}
		}
		fmt.Fprintf(&b, "  window: id=%d title=%q bounds=(%g,%g,%g,%g) pixel_dimensions=%dx%d\n", w.ID, w.Title, w.X, w.Y, w.Width, w.Height, w.PixelWidth, w.PixelHeight)
		if w.Title != "" {
			required = append(required, w.Title)
		}
		required = append(required, strconv.Itoa(w.PixelWidth), strconv.Itoa(w.PixelHeight))
		if r.State.Permission != "" {
			appendDetail(&b, "permission", r.State.Permission, &required)
		}
	}
	for _, a := range r.Apps {
		if a.Name == "" {
			return candidate{}
		}
		fmt.Fprintf(&b, "  available app: %s | bundle_id: %s | pid: %d | path: %s\n", a.Name, a.BundleID, a.PID, a.Path)
		for _, v := range []string{a.Name, a.BundleID, a.Path} {
			if v != "" {
				required = append(required, v)
			}
		}
	}
	if r.LastAction != nil {
		a := r.LastAction
		fmt.Fprintf(&b, "  last_action: type=%s target=%q element_id=%s point=(%g,%g) screenshot_dimensions=%dx%d observation_id=%s result=%s\n", a.Type, a.TargetDescription, a.ElementID, a.X, a.Y, a.ScreenshotWidth, a.ScreenshotHeight, a.ObservationID, a.Result)
		for _, v := range []string{a.Type, a.TargetDescription, a.ElementID, a.ObservationID, a.Result, strconv.Itoa(a.ScreenshotWidth), strconv.Itoa(a.ScreenshotHeight)} {
			if v != "" {
				required = append(required, v)
			}
		}
	}
	if !appendReportArtifacts(&b, &required, r.Artifacts) {
		return candidate{}
	}
	base := b.String()
	if r.State == nil {
		return candidate{text: base, required: required}
	}
	controls := r.State.Controls
	order := make([]int, len(controls))
	for i := range controls {
		order[i] = i
	}
	priority := map[string]int{"focused": 0, "enabled": 1, "disabled": 2, "static": 3}
	sort.SliceStable(order, func(i, j int) bool {
		return priority[computerControlClass(controls[order[i]])] < priority[computerControlClass(controls[order[j]])]
	})
	omitted := map[string]int{"focused_controls": 0, "enabled_controls": 0, "disabled_controls": 0, "static_controls": 0}
	for _, c := range controls {
		omitted[computerControlClass(c)+"_controls"]++
	}
	for _, idx := range order {
		line := computerControlLine(controls[idx])
		class := computerControlClass(controls[idx]) + "_controls"
		omitted[class]--
		if int(llm.EstimateTokens(base+line+computerOmissionNotice(omitted))) > effectiveBudget(in) {
			omitted[class]++
			break
		}
		base += line
	}
	if omitted["focused_controls"] > 0 {
		return candidate{}
	}
	base += computerOmissionNotice(omitted)
	return candidate{text: base, required: required, omitted: omitted}
}

func sumControlOmissions(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func visualOmissionNotice(controls, textBytes, outputBytes int) string {
	if controls+textBytes+outputBytes == 0 {
		return ""
	}
	return fmt.Sprintf("omitted: %d visual controls, %d visible-text bytes, %d passing-output bytes; rerun visual.inspect for fresh context or inspect a narrower page.\n", controls, textBytes, outputBytes)
}

func computerOmissionNotice(omitted map[string]int) string {
	if sumControlOmissions(omitted) == 0 {
		return ""
	}
	return fmt.Sprintf("omitted: %d focused controls, %d enabled controls, %d disabled controls, %d static controls; rerun computer.inspect for a fresh observation or narrow the target app/window.\n", omitted["focused_controls"], omitted["enabled_controls"], omitted["disabled_controls"], omitted["static_controls"])
}
