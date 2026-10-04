package toolreduce

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/llm"
)

var (
	ansiColor        = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	compilerLocation = regexp.MustCompile(`(?:^|[ /])[^ :]+\.[A-Za-z0-9]+:[0-9]+(?::[0-9]+)?:`)
)

func reduceShell(in Input) candidate {
	if !utf8.ValidString(in.Output) {
		return candidate{}
	}
	lines := strings.Split(in.Output, "\n")
	if len(lines) < 3 {
		return candidate{}
	}
	clean := func(s string) string { return strings.TrimSuffix(s, "\r") }
	switch in.Name {
	case "shell.run":
		first := clean(lines[0])
		if !strings.HasPrefix(first, "exit_code: ") {
			return candidate{}
		}
		if _, err := strconv.Atoi(strings.TrimPrefix(first, "exit_code: ")); err != nil || clean(lines[1]) != "--- stdout ---" {
			return candidate{}
		}
		for i := 2; i < len(lines); i++ {
			line := clean(lines[i])
			if strings.HasPrefix(line, "--- ") && line != "--- stderr ---" {
				return candidate{}
			}
		}
	case "exec.start", "exec.write", "exec.stop":
		if !strings.HasPrefix(clean(lines[0]), "session_id: ") || strings.TrimSpace(strings.TrimPrefix(clean(lines[0]), "session_id: ")) == "" {
			return candidate{}
		}
		state := clean(lines[1])
		if state != "status: running (use exec.write to send input or poll, exec.stop to end it)" {
			if !strings.HasPrefix(state, "status: exited (exit_code ") || !strings.HasSuffix(state, ")") {
				return candidate{}
			}
			if _, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(state, "status: exited (exit_code "), ")")); err != nil {
				return candidate{}
			}
		}
		i := 2
		if strings.HasPrefix(clean(lines[i]), "[") && strings.HasSuffix(clean(lines[i]), " bytes of earlier output were dropped]") {
			i++
		}
		if i >= len(lines) || clean(lines[i]) != "--- output ---" {
			return candidate{}
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(clean(lines[j]), "--- ") {
				return candidate{}
			}
		}
	default:
		return candidate{}
	}
	budget := effectiveBudget(in)
	c := selectText(lines, budget, in.IsError)
	if c.text != "" && (c.omitted["lines"] > 0 || c.omitted["progress_lines"] > 0) {
		c.text = strings.Replace(c.text, "rerun this tool", "rerun "+in.Name, 1)
	}
	return c
}

func diagnosticLine(line string) bool {
	plain := strings.ToLower(ansiColor.ReplaceAllString(strings.TrimSuffix(line, "\r"), ""))
	for _, signal := range []string{"error", "fail", "panic", "fatal", "denied", "blocked", "timeout", "timed out", "warning", "truncated", "dropped", "sandbox:", "policy:"} {
		if strings.Contains(plain, signal) {
			return true
		}
	}
	return compilerLocation.MatchString(plain)
}

func mandatoryTextLine(line string) bool {
	plain := strings.ToLower(ansiColor.ReplaceAllString(strings.TrimSuffix(line, "\r"), ""))
	if diagnosticLine(line) {
		return true
	}
	for _, prefix := range []string{"exit_code: ", "session_id: ", "status: ", "command: ", "working_directory: ", "directory: ", "cwd: ", "--- stdout ---", "--- stderr ---", "--- output ---", "[sandbox:", "[policy:"} {
		if strings.HasPrefix(plain, prefix) {
			return true
		}
	}
	return false
}

// selectText keeps required lines, a bounded start and finish, and reports
// omissions from the original line stream. It never slices a line or rune.
func selectText(lines []string, budget int, failure bool) candidate {
	if len(lines) == 0 {
		return candidate{}
	}
	if budget <= 0 {
		budget = defaultBudgetTokens
		if failure {
			budget = failureBudgetTokens
		}
	}
	type line struct {
		text            string
		source, repeats int
	}
	var compact []line
	for i := 0; i < len(lines); {
		j := i + 1
		if lines[i] != "" && !mandatoryTextLine(lines[i]) {
			for j < len(lines) && lines[j] == lines[i] {
				j++
			}
		}
		v := lines[i]
		if j-i > 1 {
			v = fmt.Sprintf("%s (repeated %d times)", v, j-i)
		}
		compact = append(compact, line{text: v, source: i, repeats: j - i})
		i = j
	}
	selected := make([]bool, len(compact))
	optional := make([]int, 0, 8)
	var required []string
	last := len(compact) - 1
	for last > 0 && compact[last].text == "" {
		last--
	}
	for i, item := range compact {
		if mandatoryTextLine(lines[item.source]) || i == last {
			selected[i] = true
			if item.text != "" {
				required = append(required, item.text)
			}
		} else if i < 5 || i >= len(compact)-5 {
			selected[i] = true
			optional = append(optional, i)
		}
	}
	render := func() candidate {
		omittedLines, omittedBytes, progressLines, progressBytes := 0, 0, 0, 0
		for i, item := range compact {
			start := item.source
			if selected[i] {
				start++
			}
			for source := start; source < item.source+item.repeats; source++ {
				if source == len(lines)-1 && lines[source] == "" {
					continue
				}
				count := len(lines[source])
				if source < len(lines)-1 {
					count++
				}
				if selected[i] {
					progressLines++
					progressBytes += count
				} else {
					omittedLines++
					omittedBytes += count
				}
			}
		}
		var b strings.Builder
		markerWritten := false
		for i, item := range compact {
			if selected[i] {
				if !markerWritten && omittedLines > 0 && i > 0 {
					fmt.Fprintf(&b, "omitted middle output: %d line(s), %d byte(s); rerun this tool with a narrower command or fresh session read.\n", omittedLines, omittedBytes)
					markerWritten = true
				}
				b.WriteString(item.text)
				b.WriteByte('\n')
			}
		}
		if omittedLines > 0 && !markerWritten {
			fmt.Fprintf(&b, "omitted middle output: %d line(s), %d byte(s); rerun this tool with a narrower command or fresh session read.\n", omittedLines, omittedBytes)
		}
		if progressLines > 0 {
			fmt.Fprintf(&b, "omitted repeated progress: %d line(s), %d byte(s); rerun this tool for fresh output.\n", progressLines, progressBytes)
		}
		return candidate{text: b.String(), omitted: map[string]int{"lines": omittedLines, "bytes": omittedBytes, "progress_lines": progressLines, "progress_bytes": progressBytes}, required: required}
	}
	for {
		c := render()
		if int(llm.EstimateTokens(c.text)) <= budget {
			return c
		}
		if len(optional) == 0 {
			return candidate{}
		}
		i := optional[len(optional)-1]
		optional = optional[:len(optional)-1]
		selected[i] = false
	}
}
