package toolreduce

import (
	"fmt"
	"strings"
	"testing"
)

func TestVerificationReducerKeepsEveryVerdictAndFailureDetail(t *testing.T) {
	passOutput := strings.Repeat("ordinary passing output\n", 200)
	output := fmt.Sprintf(`{"status":"failed","results":[{"label":"typecheck","command":"npm run check","directory":"web","reason":"check changed types","status":"passed","duration_ms":90,"output":%q},{"label":"unit tests","command":"go test ./...","directory":".","reason":"test changed packages","status":"failed","exit_code":2,"duration_ms":340,"output":"exit_code: 2\nfirst diagnostic\nASSERTION_FAILED: widget mismatch","error":"runner reported failure"}]}`, passOutput)
	got, report, applied := Reduce(Input{Name: "verification.run", Output: output, Budget: 300})
	if !applied || report.Strategy != "verification" {
		t.Fatalf("Reduce() applied=%v report=%+v, want verification reduction", applied, report)
	}
	for _, marker := range []string{"verification: failed", "PASS", "typecheck", "npm run check", "FAIL", "unit tests", "go test ./...", "directory: .", "exit_code: 2", "runner reported failure", "ASSERTION_FAILED: widget mismatch"} {
		if !strings.Contains(got, marker) {
			t.Errorf("reduced result omitted %q: %s", marker, got)
		}
	}
	if strings.Contains(got, "ordinary passing output") {
		t.Errorf("passing command output was retained: %s", got)
	}
}

func TestVerificationReducerCollapsesPassingOutput(t *testing.T) {
	passOutput := strings.Repeat("PASS package/example\n", 250)
	output := fmt.Sprintf(`{"status":"passed","results":[{"label":"unit","command":"go test ./...","directory":".","status":"passed","duration_ms":245,"output":%q}]}`, passOutput)
	got, _, applied := Reduce(Input{Name: "verification.run", Output: output, Budget: 100})
	if !applied {
		t.Fatalf("passing verification was not reduced: %s", got)
	}
	if !strings.Contains(got, "verification: passed") || !strings.Contains(got, "PASS unit") || !strings.Contains(got, "go test ./...") || strings.Contains(got, "PASS package/example") {
		t.Fatalf("passing result lost verdict/command or kept output: %s", got)
	}
	if strings.Count(got, "PASS unit") != 1 {
		t.Fatalf("passing check should have one line: %s", got)
	}
	for _, marker := range []string{"omitted: passing command output", fmt.Sprintf("%d bytes", len(passOutput)), "rerun verification.run", "go test ./..."} {
		if !strings.Contains(got, marker) {
			t.Errorf("passing-output omission lacks %q: %s", marker, got)
		}
	}
}

func TestVerificationReducerKeepsBlockedAndNotRunReasons(t *testing.T) {
	output := fmt.Sprintf(`{"status":"blocked","results":[{"label":"lint","command":"npm run lint","directory":"web","reason":"static analysis","status":"blocked","duration_ms":12,"error":"compute unavailable"},{"label":"browser","command":"npm run e2e","status":"not_run","duration_ms":0,"error":"required command is unavailable: npm"},{"label":"build","command":"npm run build","status":"passed","duration_ms":84,"output":%q}]}`, strings.Repeat("built successfully\n", 150))
	got, _, applied := Reduce(Input{Name: "verification.run", Output: output, Budget: 200})
	if !applied {
		t.Fatalf("blocked verification was not reduced: %s", got)
	}
	for _, marker := range []string{"verification: blocked", "BLOCKED lint", "npm run lint", "directory: web", "static analysis", "compute unavailable", "NOT_RUN browser", "npm run e2e", "required command is unavailable: npm", "PASS build", "npm run build"} {
		if !strings.Contains(got, marker) {
			t.Errorf("reduced result omitted %q: %s", marker, got)
		}
	}
}

func TestBrowserReducerKeepsDiagnosticsAndArtifactsOnPass(t *testing.T) {
	passOutput := strings.Repeat("ordinary browser runner output\n", 250)
	output := fmt.Sprintf(`{"status":"passed","framework":"playwright","command":"npm run test:e2e","directory":"web","duration_ms":1430,"output":%q,"diagnostics":["console.error: recovered state","requestfailed: /api/retry"],"artifacts":[{"path":"test-results/flow/screenshot.png","kind":"screenshot","mime_type":"image/png","bytes":3072},{"path":"test-results/flow/trace.zip","kind":"trace","mime_type":"application/zip","bytes":4096}]}`, passOutput)
	got, _, applied := Reduce(Input{Name: "browser.verify", Output: output, Budget: 200})
	if !applied {
		t.Fatalf("browser verification was not reduced: %s", got)
	}
	for _, marker := range []string{"browser verification: passed", "playwright", "npm run test:e2e", "directory: web", "console.error: recovered state", "requestfailed: /api/retry", "test-results/flow/screenshot.png", "screenshot", "test-results/flow/trace.zip", "trace"} {
		if !strings.Contains(got, marker) {
			t.Errorf("browser reduction omitted %q: %s", marker, got)
		}
	}
	if strings.Contains(got, "ordinary browser runner output") {
		t.Errorf("passing browser output was retained: %s", got)
	}
	for _, marker := range []string{"omitted: passing browser output", fmt.Sprintf("%d bytes", len(passOutput)), "rerun browser.verify", "npm run test:e2e"} {
		if !strings.Contains(got, marker) {
			t.Errorf("browser-output omission lacks %q: %s", marker, got)
		}
	}
}

func TestVerificationReducerPreservesFullFailureOutputWhenItFits(t *testing.T) {
	failureOutput := "EARLY_COMPILER_DIAGNOSTIC\n… [truncated 4096 bytes from the middle] …\n" + strings.Repeat("later diagnostic line\n", 58) + "FINAL_FAILURE_MARKER"
	output := fmt.Sprintf(`{"status":"failed","results":[{"label":"typecheck","command":"npm run check","status":"passed","duration_ms":5,"output":%q},{"label":"unit","command":"go test ./...","status":"failed","exit_code":2,"duration_ms":8,"output":%q}]}`, strings.Repeat("passing output\n", 350), failureOutput)
	got, _, applied := Reduce(Input{Name: "verification.run", Output: output, Budget: 600})
	if !applied {
		t.Fatalf("failure output that fits was not reduced: %s", got)
	}
	for _, marker := range []string{"EARLY_COMPILER_DIAGNOSTIC", "[truncated 4096 bytes from the middle]", "FINAL_FAILURE_MARKER"} {
		if !strings.Contains(got, marker) {
			t.Errorf("available failure evidence %q was lost: %s", marker, got)
		}
	}
}

func TestBrowserReducerPreservesFullFailureOutputWhenItFits(t *testing.T) {
	failureOutput := "EARLY_BROWSER_DIAGNOSTIC\n… [truncated 2048 bytes from the middle] …\n" + strings.Repeat(strings.Repeat("\\", 12)+"\n", 250) + "FINAL_BROWSER_FAILURE"
	output := fmt.Sprintf(`{"status":"failed","framework":"playwright","command":"npm run e2e","exit_code":1,"duration_ms":9,"output":%q,"diagnostics":["console.error: broken widget"],"artifacts":[]}`, failureOutput)
	got, report, applied := Reduce(Input{Name: "browser.verify", Output: output, Budget: 1000})
	if !applied {
		t.Fatalf("browser failure output that fits was not reduced: %+v / %s", report, got)
	}
	for _, marker := range []string{"EARLY_BROWSER_DIAGNOSTIC", "[truncated 2048 bytes from the middle]", "FINAL_BROWSER_FAILURE", "console.error: broken widget"} {
		if !strings.Contains(got, marker) {
			t.Errorf("available browser failure evidence %q was lost: %s", marker, got)
		}
	}
}

func TestVerificationReducerDeclinesMalformedOrUnknownSchema(t *testing.T) {
	tests := []struct{ name, tool, output string }{
		{"malformed", "verification.run", `{"status":"failed","results":[`},
		{"top_unknown", "verification.run", `{"status":"passed","results":[{"label":"unit","command":"go test","status":"passed","duration_ms":1}],"new_detail":"important"}`},
		{"entry_unknown", "verification.run", `{"status":"passed","results":[{"label":"unit","command":"go test","status":"passed","duration_ms":1,"new_detail":"important"}]}`},
		{"browser_unknown", "browser.verify", `{"status":"passed","diagnostics":[],"artifacts":[],"new_detail":"important"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, applied := Reduce(Input{Name: tc.tool, Output: tc.output, Budget: 1})
			if applied || got != tc.output {
				t.Fatalf("Reduce() = (%q, %v), want byte-identical fallback", got, applied)
			}
		})
	}
}

func TestVerificationReducerDeclinesInconsistentVerdictsAndNullBrowserCollections(t *testing.T) {
	longOutput := strings.Repeat("ordinary passing output\n", 200)
	tests := []struct{ name, tool, output string }{
		{"inconsistent_verdict", "verification.run", fmt.Sprintf(`{"status":"passed","results":[{"label":"unit","command":"go test","status":"failed","exit_code":2,"duration_ms":1,"output":%q}]}`, longOutput)},
		{"null_diagnostics", "browser.verify", fmt.Sprintf(`{"status":"passed","framework":"playwright","command":"npm run e2e","output":%q,"diagnostics":null,"artifacts":[]}`, longOutput)},
		{"null_artifacts", "browser.verify", fmt.Sprintf(`{"status":"passed","framework":"playwright","command":"npm run e2e","output":%q,"diagnostics":[],"artifacts":null}`, longOutput)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, applied := Reduce(Input{Name: tc.tool, Output: tc.output, Budget: 500})
			if applied || got != tc.output {
				t.Fatalf("Reduce() = (%q, %v), want byte-identical fallback", got, applied)
			}
		})
	}
}

func TestVerificationReducerDeclinesWhenMandatoryFailureExceedsBudget(t *testing.T) {
	output := fmt.Sprintf(`{"status":"failed","results":[{"label":"critical integration test","command":"go test ./integration/...","status":"failed","exit_code":9,"duration_ms":950,"error":"unrecoverable failure","output":%q}]}`, strings.Repeat("diagnostic context\n", 1000)+"FINAL_FAILURE_MARKER")
	got, report, applied := Reduce(Input{Name: "verification.run", Output: output, Budget: 25})
	if applied || got != output || report.Declined != "over_budget" {
		t.Fatalf("Reduce() = (applied=%v, declined=%q), want byte-identical over-budget fallback", applied, report.Declined)
	}
}
