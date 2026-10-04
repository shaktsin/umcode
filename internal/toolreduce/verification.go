package toolreduce

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const verificationTailBytes = 1024

type verificationCheck struct {
	Label      string `json:"label"`
	Command    string `json:"command"`
	Directory  string `json:"directory"`
	Reason     string `json:"reason"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output"`
	Error      string `json:"error"`
}

type verificationPayload struct {
	Status  string              `json:"status"`
	Results []verificationCheck `json:"results"`
}

type browserArtifactResult struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	MimeType string `json:"mime_type"`
	Bytes    int64  `json:"bytes"`
}

type browserPayload struct {
	Status      string                  `json:"status"`
	Framework   string                  `json:"framework"`
	Command     string                  `json:"command"`
	Directory   string                  `json:"directory"`
	DurationMS  int64                   `json:"duration_ms"`
	ExitCode    int                     `json:"exit_code"`
	Output      string                  `json:"output"`
	Diagnostics []string                `json:"diagnostics"`
	Artifacts   []browserArtifactResult `json:"artifacts"`
	Reason      string                  `json:"reason"`
}

func reduceVerification(in Input) candidate {
	if !utf8.ValidString(in.Output) {
		return candidate{}
	}
	if in.Name == "browser.verify" {
		return reduceBrowserVerification(in.Output)
	}
	return reduceCheckVerification(in.Output)
}

func reduceCheckVerification(output string) candidate {
	var payload verificationPayload
	if !decodeOwnedObject([]byte(output), &payload, "status", "results") || !validVerificationStatus(payload.Status) || len(payload.Results) == 0 {
		return candidate{}
	}
	var raw struct {
		Results []json.RawMessage `json:"results"`
	}
	if json.Unmarshal([]byte(output), &raw) != nil || len(raw.Results) != len(payload.Results) {
		return candidate{}
	}
	var b strings.Builder
	var required []string
	expectedStatus := "passed"
	for _, check := range payload.Results {
		if check.Status != "passed" {
			expectedStatus = check.Status
			break
		}
	}
	if payload.Status != expectedStatus {
		return candidate{}
	}
	b.WriteString("verification: ")
	b.WriteString(payload.Status)
	b.WriteByte('\n')
	required = append(required, "verification: "+payload.Status)
	omitted := 0
	for i, check := range payload.Results {
		if !decodeOwnedObject(raw.Results[i], &check, "label", "command", "status", "duration_ms") ||
			!validVerificationStatus(check.Status) || check.Label == "" || check.Command == "" || check.DurationMS < 0 {
			return candidate{}
		}
		fields, _ := objectFields(raw.Results[i])
		if check.Status == "failed" && (!hasField(fields, "exit_code") || check.ExitCode == 0) {
			return candidate{}
		}
		if (check.Status == "blocked" || check.Status == "not_run") && check.Error == "" {
			return candidate{}
		}
		label := verificationStatusLabel(check.Status)
		line := fmt.Sprintf("%s %s | command: %s | directory: %s | duration_ms: %d", label, check.Label, check.Command, displayDirectory(check.Directory), check.DurationMS)
		b.WriteString(line)
		b.WriteByte('\n')
		if check.Status != "passed" {
			required = append(required, label+" "+check.Label, check.Command)
			if check.Reason != "" {
				appendDetail(&b, "reason", check.Reason, &required)
			}
			if check.Error != "" {
				appendDetail(&b, "error", check.Error, &required)
			}
			if hasField(fields, "exit_code") {
				appendDetail(&b, "exit_code", strconv.Itoa(check.ExitCode), &required)
			}
			if check.Output != "" {
				appendDetail(&b, "diagnostic tail", diagnosticTail(check.Output), &required)
			}
		} else {
			omitted += len(check.Output)
		}
	}
	return candidate{text: b.String(), omitted: map[string]int{"passing_output_bytes": omitted}, required: required}
}

func reduceBrowserVerification(output string) candidate {
	var payload browserPayload
	if !decodeOwnedObject([]byte(output), &payload, "status", "diagnostics", "artifacts") || !validVerificationStatus(payload.Status) {
		return candidate{}
	}
	var raw struct {
		Artifacts []json.RawMessage `json:"artifacts"`
	}
	if json.Unmarshal([]byte(output), &raw) != nil || len(raw.Artifacts) != len(payload.Artifacts) {
		return candidate{}
	}
	fields, _ := objectFields([]byte(output))
	if payload.Status == "failed" && (!hasField(fields, "exit_code") || payload.ExitCode == 0) {
		return candidate{}
	}
	if (payload.Status == "blocked" || payload.Status == "not_run") && payload.Reason == "" {
		return candidate{}
	}
	var b strings.Builder
	var required []string
	b.WriteString("browser verification: ")
	b.WriteString(payload.Status)
	b.WriteByte('\n')
	required = append(required, "browser verification: "+payload.Status)
	for _, field := range []struct{ label, value string }{{"framework", payload.Framework}, {"command", payload.Command}, {"directory", displayDirectory(payload.Directory)}, {"duration_ms", strconv.FormatInt(payload.DurationMS, 10)}} {
		appendDetail(&b, field.label, field.value, &required)
	}
	if payload.Reason != "" {
		appendDetail(&b, "reason", payload.Reason, &required)
	}
	if hasField(fields, "exit_code") {
		appendDetail(&b, "exit_code", strconv.Itoa(payload.ExitCode), &required)
	}
	if payload.Status != "passed" && payload.Output != "" {
		appendDetail(&b, "diagnostic tail", diagnosticTail(payload.Output), &required)
	}
	for _, diagnostic := range payload.Diagnostics {
		if diagnostic == "" {
			return candidate{}
		}
		appendDetail(&b, "diagnostic", diagnostic, &required)
	}
	for i, artifact := range payload.Artifacts {
		if !decodeOwnedObject(raw.Artifacts[i], &artifact, "path", "kind", "bytes") || artifact.Path == "" || artifact.Kind == "" || artifact.Bytes < 0 {
			return candidate{}
		}
		line := fmt.Sprintf("artifact: %s | kind: %s | bytes: %d", artifact.Path, artifact.Kind, artifact.Bytes)
		if artifact.MimeType != "" {
			line += " | mime_type: " + artifact.MimeType
		}
		b.WriteString(line)
		b.WriteByte('\n')
		required = append(required, artifact.Path, artifact.Kind)
	}
	omitted := 0
	if payload.Status == "passed" {
		omitted = len(payload.Output)
	}
	return candidate{text: b.String(), omitted: map[string]int{"passing_output_bytes": omitted}, required: required}
}

func decodeOwnedObject(data []byte, target any, mandatory ...string) bool {
	fields, ok := objectFields(data)
	if !ok {
		return false
	}
	var known map[string]json.RawMessage
	if marshaled, err := json.Marshal(target); err != nil || json.Unmarshal(marshaled, &known) != nil {
		return false
	}
	for key, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
		if _, exists := known[key]; !exists && !emptyUnknownValue(value) {
			return false
		}
	}
	for _, key := range mandatory {
		if !hasField(fields, key) {
			return false
		}
	}
	return json.Unmarshal(data, target) == nil
}

func objectFields(data []byte) (map[string]json.RawMessage, bool) {
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

func hasField(fields map[string]json.RawMessage, key string) bool {
	_, ok := fields[key]
	return ok
}

func emptyUnknownValue(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case bool:
		return !v
	case float64:
		return v == 0
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

func validVerificationStatus(status string) bool {
	switch status {
	case "passed", "failed", "blocked", "not_run":
		return true
	}
	return false
}

func verificationStatusLabel(status string) string {
	switch status {
	case "passed":
		return "PASS"
	case "failed":
		return "FAIL"
	case "blocked":
		return "BLOCKED"
	default:
		return "NOT_RUN"
	}
}

func displayDirectory(directory string) string {
	if directory == "" {
		return "."
	}
	return directory
}

func appendDetail(b *strings.Builder, label, value string, required *[]string) {
	b.WriteString("  ")
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteByte('\n')
	if value != "" {
		*required = append(*required, value)
	}
}

func diagnosticTail(output string) string {
	if len(output) <= verificationTailBytes {
		return output
	}
	start := len(output) - verificationTailBytes
	for start < len(output) && !utf8.RuneStart(output[start]) {
		start++
	}
	return output[start:]
}
