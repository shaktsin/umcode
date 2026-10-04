// Package toolreduce conservatively reduces selected tool results before they
// are sent to a model. It has no model or provider dependencies.
package toolreduce

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/llm"
)

const (
	defaultBudgetTokens   = 2000
	failureBudgetTokens   = 4000
	minimumSavingsPercent = 10
)

type Input struct {
	Name    string
	Args    json.RawMessage
	Output  string
	IsError bool
	Budget  int
}

type Report struct {
	Strategy                   string
	OriginalTokens, SentTokens int
	Omitted                    map[string]int
	Declined                   string
}

type candidate struct {
	text     string
	omitted  map[string]int
	required []string
}

// Reduce returns a conservative model-facing result and an accounting report.
// Unsupported tools and any candidate that fails a guard preserve the original
// output byte-for-byte.
func Reduce(in Input) (text string, report Report, applied bool) {
	budget := in.Budget
	if budget <= 0 {
		budget = defaultBudgetTokens
		if in.IsError {
			budget = failureBudgetTokens
		}
	}
	report.OriginalTokens = int(llm.EstimateTokens(in.Output))
	report.SentTokens = report.OriginalTokens
	if report.OriginalTokens <= budget {
		return in.Output, report, false
	}

	var strategy string
	var c candidate
	switch in.Name {
	case "verification.run", "browser.verify":
		strategy, c = "verification", reduceVerification(in)
	case "shell.run", "exec.start", "exec.write", "exec.stop":
		strategy, c = "shell", reduceShell(in)
	case "file.search", "web.search":
		strategy, c = "search", reduceSearch(in)
	case "visual.start", "visual.inspect", "visual.act", "computer.list", "computer.start", "computer.inspect", "computer.act", "computer.stop":
		strategy, c = "report", reduceReport(in)
	default:
		report.Declined = "unsupported_tool"
		return in.Output, report, false
	}
	return acceptCandidate(in, strategy, c)
}

func acceptCandidate(in Input, strategy string, c candidate) (string, Report, bool) {
	report := Report{Strategy: strategy, OriginalTokens: int(llm.EstimateTokens(in.Output)), SentTokens: int(llm.EstimateTokens(in.Output))}
	budget := in.Budget
	if budget <= 0 {
		budget = defaultBudgetTokens
		if in.IsError {
			budget = failureBudgetTokens
		}
	}
	decline := func(reason string) (string, Report, bool) {
		report.Declined = reason
		return in.Output, report, false
	}
	if c.text == "" {
		return decline("empty_candidate")
	}
	if !utf8.ValidString(c.text) {
		return decline("invalid_utf8")
	}
	for _, marker := range c.required {
		if marker == "" || !strings.Contains(c.text, marker) {
			return decline("missing_required_marker")
		}
	}
	sent := int(llm.EstimateTokens(c.text))
	report.SentTokens = sent
	if sent >= report.OriginalTokens {
		return decline("candidate_not_smaller")
	}
	if sent > budget {
		return decline("over_budget")
	}
	if (report.OriginalTokens-sent)*100 < report.OriginalTokens*minimumSavingsPercent {
		return decline("insufficient_savings")
	}
	report.Omitted = c.omitted
	return c.text, report, true
}

func reduceVerification(Input) candidate { return candidate{} }
func reduceShell(Input) candidate        { return candidate{} }
func reduceSearch(Input) candidate       { return candidate{} }
func reduceReport(Input) candidate       { return candidate{} }

// cropUTF8 returns a prefix no longer than maxBytes without splitting a rune.
func cropUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
