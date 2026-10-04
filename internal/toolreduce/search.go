package toolreduce

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/llm"
)

var (
	searchHit            = regexp.MustCompile(`^(.+):([0-9]+): (.*)$`)
	searchContext        = regexp.MustCompile(`^(.+)-([0-9]+)- (.*)$`)
	searchMatchesSummary = regexp.MustCompile(`^([0-9]+) match\(es\) in ([0-9]+) file\(s\)(?:; .*)?$`)
	searchFilesSummary   = regexp.MustCompile(`^([0-9]+) file\(s\)(?:; .*)?$`)
)

func reduceSearch(in Input) candidate {
	if !utf8.ValidString(in.Output) {
		return candidate{}
	}
	switch in.Name {
	case "file.search":
		return reduceFileSearch(in)
	case "web.search":
		return reduceWebSearch(in)
	default:
		return candidate{}
	}
}

type searchGroup struct {
	path  string
	lines []string
	hits  int
}

func reduceFileSearch(in Input) candidate {
	lines := strings.Split(strings.TrimSuffix(in.Output, "\n"), "\n")
	if len(lines) < 3 {
		return candidate{}
	}
	summary := lines[len(lines)-1]
	matchMode := searchMatchesSummary.MatchString(summary)
	if !matchMode && !searchFilesSummary.MatchString(summary) {
		return candidate{}
	}
	lines = lines[:len(lines)-1]
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return candidate{}
	}
	var groups []searchGroup
	var current searchGroup
	flush := func() {
		if len(current.lines) > 0 {
			groups = append(groups, current)
			current = searchGroup{}
		}
	}
	for _, line := range lines {
		if line == "--" && matchMode {
			flush()
			continue
		}
		if line == "" {
			return candidate{}
		}
		if !matchMode {
			if strings.ContainsAny(line, "\r\n") {
				return candidate{}
			}
			groups = append(groups, searchGroup{path: line, lines: []string{line}, hits: 1})
			continue
		}
		var path string
		hit := searchHit.FindStringSubmatch(line)
		context := searchContext.FindStringSubmatch(line)
		switch {
		case hit != nil:
			path = hit[1]
		case context != nil:
			path = context[1]
		default:
			return candidate{}
		}
		if path == "" {
			return candidate{}
		}
		if current.path != "" && current.path != path {
			flush()
		}
		if hit != nil && current.hits > 0 {
			var shared []string
			for j := len(current.lines) - 1; j >= 0; j-- {
				if contextLine := searchContext.FindStringSubmatch(current.lines[j]); contextLine != nil && contextLine[1] == path {
					shared = append([]string{current.lines[j]}, shared...)
				} else {
					break
				}
			}
			flush()
			if len(shared) > 0 {
				current = searchGroup{path: path, lines: shared}
			}
		}
		if current.path == "" {
			current.path = path
		}
		current.lines = append(current.lines, line)
		if hit != nil {
			current.hits++
		}
	}
	flush()
	if len(groups) == 0 {
		return candidate{}
	}
	total := 0
	for _, group := range groups {
		if group.hits == 0 {
			return candidate{}
		}
		total += group.hits
	}
	declaredMatches, declaredFiles := 0, 0
	var err error
	if matchMode {
		parts := searchMatchesSummary.FindStringSubmatch(summary)
		declaredMatches, err = strconv.Atoi(parts[1])
		if err != nil {
			return candidate{}
		}
		declaredFiles, err = strconv.Atoi(parts[2])
	} else {
		parts := searchFilesSummary.FindStringSubmatch(summary)
		declaredMatches, err = strconv.Atoi(parts[1])
		declaredFiles = declaredMatches
	}
	if err != nil || declaredMatches != total {
		return candidate{}
	}
	// The tool returns one path at a time. Queue each path's groups and
	// interleave queues so early large files do not hide later files.
	paths := make([]string, 0)
	queues := map[string][]int{}
	for i, group := range groups {
		if _, ok := queues[group.path]; !ok {
			paths = append(paths, group.path)
		}
		queues[group.path] = append(queues[group.path], i)
	}
	if declaredFiles != len(paths) {
		return candidate{}
	}
	var order []int
	for round := 0; ; round++ {
		more := false
		for _, path := range paths {
			if round < len(queues[path]) {
				order = append(order, queues[path][round])
				more = true
			}
		}
		if !more {
			break
		}
	}
	budget := in.Budget
	if budget <= 0 {
		budget = defaultBudgetTokens
		if in.IsError {
			budget = failureBudgetTokens
		}
	}
	selected := make([]bool, len(groups))
	render := func() candidate {
		var b strings.Builder
		kept := 0
		for _, i := range order {
			if selected[i] {
				b.WriteString(strings.Join(groups[i].lines, "\n"))
				b.WriteByte('\n')
				kept += groups[i].hits
			}
		}
		b.WriteByte('\n')
		b.WriteString(summary)
		b.WriteByte('\n')
		omitted := total - kept
		if omitted > 0 {
			label := "matches with context"
			if !matchMode {
				label = "file records"
			}
			fmt.Fprintf(&b, "omitted: %d %s; narrow path, glob, or pattern and rerun file.search for fresh results.\n", omitted, label)
		}
		return candidate{text: b.String(), omitted: map[string]int{"matches": omitted}, required: []string{summary}}
	}
	for _, i := range order {
		selected[i] = true
		if int(llm.EstimateTokens(render().text)) > budget {
			selected[i] = false
		}
	}
	c := render()
	kept := false
	for _, chosen := range selected {
		if chosen {
			kept = true
			break
		}
	}
	if !kept || int(llm.EstimateTokens(c.text)) > budget {
		return candidate{}
	}
	return c
}

type webSearchRecord struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

func reduceWebSearch(in Input) candidate {
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(in.Output), &raw); err != nil || len(raw) == 0 {
		return candidate{}
	}
	records := make([]webSearchRecord, len(raw))
	for i, item := range raw {
		decoder := json.NewDecoder(bytes.NewReader(item))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&records[i]); err != nil || records[i].Title == "" || records[i].URL == "" {
			return candidate{}
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(item, &fields) != nil || len(fields) < 2 {
			return candidate{}
		}
		if _, ok := fields["title"]; !ok {
			return candidate{}
		}
		if _, ok := fields["url"]; !ok {
			return candidate{}
		}
		u, err := url.Parse(records[i].URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return candidate{}
		}
	}
	budget := in.Budget
	if budget <= 0 {
		budget = defaultBudgetTokens
		if in.IsError {
			budget = failureBudgetTokens
		}
	}
	selected := 0
	render := func(count int) candidate {
		var b strings.Builder
		b.WriteString("web.search results:\n")
		var required []string
		for _, record := range records[:count] {
			fmt.Fprintf(&b, "title: %s\nurl: %s\n", record.Title, record.URL)
			if record.Snippet != "" {
				fmt.Fprintf(&b, "snippet: %s\n", record.Snippet)
			}
			required = append(required, record.Title, record.URL)
		}
		omitted := len(records) - count
		if omitted > 0 {
			fmt.Fprintf(&b, "omitted: %d web result record(s); narrow or repeat web.search for fresh results.\n", omitted)
		}
		return candidate{text: b.String(), omitted: map[string]int{"records": omitted}, required: required}
	}
	for selected < len(records) && int(llm.EstimateTokens(render(selected+1).text)) <= budget {
		selected++
	}
	if selected == 0 {
		return candidate{}
	}
	return render(selected)
}
