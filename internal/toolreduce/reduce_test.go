package toolreduce

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReducePassesThroughSmallAndUnsupportedResults(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input Input
	}{
		{name: "small", input: Input{Name: "shell.run", Output: "small result", Budget: 100}},
		{name: "unsupported", input: Input{Name: "custom.tool", Output: strings.Repeat("x", 400), Budget: 20}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, report, applied := Reduce(tc.input)
			if got != tc.input.Output || applied {
				t.Fatalf("Reduce() = (%q, %v), want byte-identical pass-through and false", got, applied)
			}
			if tc.name == "unsupported" && report.Declined != "unsupported_tool" {
				t.Fatalf("Declined = %q, want unsupported_tool", report.Declined)
			}
		})
	}
}

func TestReduceDispatchesOnlyOwnedCanonicalNames(t *testing.T) {
	owned := map[string]string{
		"verification.run": "verification", "browser.verify": "verification",
		"shell.run": "shell", "exec.start": "shell", "exec.write": "shell", "exec.stop": "shell",
		"file.search": "search", "web.search": "search",
		"visual.start": "report", "visual.inspect": "report", "visual.act": "report",
		"computer.list": "report", "computer.start": "report", "computer.inspect": "report", "computer.act": "report", "computer.stop": "report",
	}
	for _, name := range []string{"verification.run", "browser.verify", "shell.run", "exec.start", "exec.write", "exec.stop", "file.search", "web.search", "visual.start", "visual.inspect", "visual.act", "computer.list", "computer.start", "computer.inspect", "computer.act", "computer.stop", "web.fetch", "file.read", "visual.stop", "custom.tool"} {
		t.Run(name, func(t *testing.T) {
			_, report, applied := Reduce(Input{Name: name, Output: strings.Repeat("x", 400), Budget: 20})
			if applied {
				t.Fatal("empty foundation reducer unexpectedly applied")
			}
			if strategy, isOwned := owned[name]; isOwned {
				if report.Strategy != strategy || report.Declined != "empty_candidate" {
					t.Fatalf("Report = %+v, want owned strategy %q and empty candidate decline", report, strategy)
				}
			} else if report.Strategy != "" || report.Declined != "unsupported_tool" {
				t.Fatalf("Report = %+v, want unsupported tool decline", report)
			}
		})
	}
}

func TestAcceptCandidateRejectsEmptyInvalidLargerAndLowSavingText(t *testing.T) {
	input := Input{Name: "shell.run", Output: strings.Repeat("o", 80), Budget: 100}
	tests := []struct {
		name      string
		candidate candidate
		declined  string
	}{
		{name: "empty", candidate: candidate{}, declined: "empty_candidate"},
		{name: "invalid_utf8", candidate: candidate{text: string([]byte{0xff})}, declined: "invalid_utf8"},
		{name: "larger", candidate: candidate{text: strings.Repeat("l", 100)}, declined: "candidate_not_smaller"},
		{name: "low_saving", candidate: candidate{text: strings.Repeat("s", 73)}, declined: "insufficient_savings"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, report, applied := acceptCandidate(input, "test", tc.candidate)
			if applied || got != input.Output || report.Declined != tc.declined {
				t.Fatalf("acceptCandidate() = (%q, %+v, %v), want pass-through declined %q", got, report, applied, tc.declined)
			}
		})
	}
}

func TestAcceptCandidateRequiresEveryMandatoryMarker(t *testing.T) {
	in := Input{Name: "shell.run", Output: strings.Repeat("x", 80), Budget: 100}
	got, report, applied := acceptCandidate(in, "shell", candidate{text: "summary " + strings.Repeat("y", 30), omitted: map[string]int{"lines": 4}, required: []string{"EXIT_CODE: 0", "stderr"}})
	if applied || got != in.Output || report.Declined != "missing_required_marker" {
		t.Fatalf("acceptCandidate() = (%q, %+v, %v), want missing marker decline and pass-through", got, report, applied)
	}
}

func TestAcceptCandidateKeepsValidUTF8AtBudget(t *testing.T) {
	in := Input{Name: "shell.run", Args: json.RawMessage(`{"cmd":"go test"}`), Output: strings.Repeat("o", 800), Budget: 50}
	// EstimateTokens is (bytes+3)/4. This candidate is exactly 50 tokens and saves 75%.
	text := strings.Repeat("é", 100)
	got, report, applied := acceptCandidate(in, "shell", candidate{text: text, omitted: map[string]int{"lines": 3}})
	if !applied || got != text || report.Strategy != "shell" || report.OriginalTokens != 200 || report.SentTokens != 50 || report.Omitted["lines"] != 3 || report.Declined != "" {
		t.Fatalf("acceptCandidate() = (%q, %+v, %v), want accepted exact-budget candidate with exact accounting", got, report, applied)
	}
}

func TestErrorInputUsesFailureBudget(t *testing.T) {
	out := strings.Repeat("e", 12000)
	got, report, applied := Reduce(Input{Name: "custom.tool", Output: out, IsError: true})
	if got != out || applied || report.OriginalTokens != 3000 || report.Declined != "" {
		t.Fatalf("error Reduce() = (%q, %+v, %v), want unchanged result below 4,000-token failure budget", got, report, applied)
	}
	_, regularReport, _ := Reduce(Input{Name: "custom.tool", Output: out})
	if regularReport.Declined != "unsupported_tool" {
		t.Fatalf("regular Declined = %q, want unsupported_tool above 2,000-token default budget", regularReport.Declined)
	}
}

func TestCropUTF8KeepsRuneBoundaries(t *testing.T) {
	got := cropUTF8("aé🙂z", 4)
	if got != "aé" || !utf8.ValidString(got) {
		t.Fatalf("cropUTF8() = %q, want valid UTF-8 prefix %q", got, "aé")
	}
}
