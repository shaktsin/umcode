package work

import (
	"encoding/json"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
)

// summaryLimit caps stored verification output and error text.
const summaryLimit = 2048

// goalLimit caps the stored goal text so large pastes are not persisted.
const goalLimit = 2000

func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

type plannedCheck struct {
	Label     string `json:"label"`
	Command   string `json:"command"`
	Directory string `json:"directory"`
	Reason    string `json:"reason"`
}

type runResult struct {
	Label     string `json:"label"`
	Command   string `json:"command"`
	Directory string `json:"directory"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code"`
	Output    string `json:"output"`
	Error     string `json:"error"`
}

type browserResult struct {
	Status   string `json:"status"`
	Command  string `json:"command"`
	ExitCode *int   `json:"exit_code"`
	Output   string `json:"output"`
	Reason   string `json:"reason"`
}

// parsePlan extracts planned checks from verification.plan output; malformed
// output yields none.
func parsePlan(output string) []plannedCheck {
	var v struct {
		Checks []plannedCheck `json:"checks"`
	}
	if json.Unmarshal([]byte(output), &v) != nil {
		return nil
	}
	var out []plannedCheck
	for _, c := range v.Checks {
		if strings.TrimSpace(c.Command) != "" {
			out = append(out, c)
		}
	}
	return out
}

// parseRun extracts per-check results from verification.run output.
func parseRun(output string) []runResult {
	var v struct {
		Results []runResult `json:"results"`
	}
	if json.Unmarshal([]byte(output), &v) != nil {
		return nil
	}
	var out []runResult
	for _, r := range v.Results {
		if validAttemptStatus(r.Status) {
			out = append(out, r)
		}
	}
	return out
}

// parseBrowser extracts the single result of browser.verify.
func parseBrowser(output string) (browserResult, bool) {
	var v browserResult
	if json.Unmarshal([]byte(output), &v) != nil || !validAttemptStatus(v.Status) {
		return v, false
	}
	return v, true
}

func validAttemptStatus(s string) bool {
	switch s {
	case protocol.AttemptPassed, protocol.AttemptFailed, protocol.AttemptBlocked, protocol.AttemptNotRun:
		return true
	}
	return false
}

// criterionCommand reads the command stored in a criterion node.
func criterionCommand(n protocol.WorkNode) string {
	var c struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(n.Content, &c)
	return c.Command
}
