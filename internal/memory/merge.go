package memory

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
)

type MergeInput struct {
	Current     []byte
	Active      []protocol.ProjectMemory
	Proposals   []Proposal
	TargetBytes int
	// TargetPath is the canonical repository-relative destination. Callers
	// should always supply it; omission only checks consistency among rows.
	TargetPath string
}

type MergeResult struct {
	After                         []byte
	Inserted, Replaced, Unchanged []string // Candidate node IDs, in graph-ID order.
	BeforeHash, AfterHash         string
}

const (
	ReasonManagedSectionConflict = "managed_section_conflict"
	ReasonOwnershipConflict      = "ownership_conflict"
	ReasonSizeLimit              = "size_limit"
	managedHeading               = "## Verified project memory"
	managedMarker                = "<!-- umcode:generated -->"
)

type memoryLine struct {
	start, end, next int
	text             string
}

type memorySection struct {
	lines       []memoryLine
	marker, end int // Line indexes; end is exclusive.
	insert      int // Byte offset before trailing blank lines.
}

// Merge is a deterministic, I/O-free reconciliation. Any conflict or size
// failure returns a zero result; input slices and unrelated bytes are untouched.
// Success has a zero Outcome, matching Qualify and ResolvePlacement.
func Merge(in MergeInput) (MergeResult, Outcome) {
	conflict := func(reason string) (MergeResult, Outcome) {
		return MergeResult{}, Outcome{Status: protocol.MemoryOutcomeConflicted, Reason: reason}
	}
	section, ok := parseMemorySection(in.Current)
	if !ok {
		return conflict(ReasonManagedSectionConflict)
	}
	seen := map[string]int{}
	for _, line := range section.lines {
		seen[line.text]++
	}
	rows, semantic := map[string]protocol.ProjectMemory{}, map[string]string{}
	positions := map[string]memoryLine{}
	target := in.TargetPath
	if target != "" && !memoryTarget(target) {
		return conflict(ReasonOwnershipConflict)
	}
	project := ""
	for _, row := range in.Active {
		if target == "" {
			target = row.TargetPath
		}
		if project == "" {
			project = row.ProjectID
		}
		if !owned(row) || !memoryTarget(row.TargetPath) || row.TargetPath != target || row.ProjectID != project || rows[row.ID].ID != "" || semantic[row.SemanticKey] != "" || seen[row.Text] != 1 {
			return conflict(ReasonOwnershipConflict)
		}
		found := false
		for i := section.marker + 1; section.marker >= 0 && i < section.end; i++ {
			line := section.lines[i]
			if line.text != row.Text {
				continue
			}
			// Prove the whole item is still the recorded one-line bullet.
			// Blank lines do not end a Markdown list item: indented prose
			// or a nested list after them still belongs to the user-extended item.
			for j := i + 1; j < section.end; j++ {
				next := section.lines[j].text
				if strings.HasPrefix(next, "- ") {
					break
				}
				if strings.TrimSpace(next) != "" {
					return conflict(ReasonOwnershipConflict)
				}
			}
			positions[row.ID], found = line, true
		}
		if !found {
			return conflict(ReasonOwnershipConflict)
		}
		rows[row.ID], semantic[row.SemanticKey] = row, row.ID
	}

	proposals := append([]Proposal(nil), in.Proposals...)
	sort.Slice(proposals, func(i, j int) bool { return proposals[i].CandidateNodeID < proposals[j].CandidateNodeID })
	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	var additions []string
	result := MergeResult{BeforeHash: memoryHash(in.Current)}
	candidates, keys, replaced := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, proposal := range proposals {
		id := proposal.CandidateNodeID
		if id == "" || candidates[id] || !strings.HasPrefix(proposal.Text, "- ") || strings.TrimSpace(proposal.Text) != proposal.Text || strings.ContainsAny(proposal.Text, "\r\n") || strings.Contains(strings.ToLower(proposal.Text), "umcode:") || keys[proposal.SemanticKey] && proposal.SemanticKey != "" {
			return conflict(ReasonSemanticConflict)
		}
		candidates[id], keys[proposal.SemanticKey] = true, true
		oldID := semantic[proposal.SemanticKey]
		if proposal.ReplacesMemory != "" {
			if oldID != proposal.ReplacesMemory || replaced[oldID] || rows[oldID].ID == "" {
				return conflict(ReasonReplacementConflict)
			}
			replaced[oldID] = true
		} else if oldID != "" && rows[oldID].Text != proposal.Text {
			return conflict(ReasonSemanticConflict)
		}
		if seen[proposal.Text] > 0 {
			result.Unchanged = append(result.Unchanged, id)
			continue
		}
		if proposal.ReplacesMemory != "" {
			line := positions[oldID]
			edits = append(edits, edit{line.start, line.end, proposal.Text})
			seen[rows[oldID].Text]--
			result.Replaced = append(result.Replaced, id)
		} else {
			additions = append(additions, proposal.Text)
			result.Inserted = append(result.Inserted, id)
		}
		seen[proposal.Text]++
	}
	if len(additions) > 0 {
		eol := memoryEOL(in.Current)
		insert := section.insert
		text := strings.Join(additions, eol)
		if section.marker < 0 {
			insert = len(in.Current)
			prefix := ""
			if insert > 0 {
				if in.Current[insert-1] != '\n' {
					prefix += eol
				}
				if !bytes.HasSuffix(in.Current, []byte(eol+eol)) {
					prefix += eol
				}
			}
			text = prefix + managedHeading + eol + eol + managedMarker + eol + text
		} else if insert > 0 && in.Current[insert-1] != '\n' {
			text = eol + text
		}
		if len(in.Current) == 0 || insert < len(in.Current) || in.Current[len(in.Current)-1] == '\n' {
			text += eol
		}
		edits = append(edits, edit{insert, insert, text})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var after bytes.Buffer
	previous := 0
	for _, edit := range edits {
		after.Write(in.Current[previous:edit.start])
		after.WriteString(edit.text)
		previous = edit.end
	}
	after.Write(in.Current[previous:])
	if after.Len() > in.TargetBytes {
		return MergeResult{}, Outcome{Status: protocol.MemoryOutcomePending, Reason: ReasonSizeLimit}
	}
	result.After = after.Bytes()
	result.AfterHash = memoryHash(result.After)
	return result, Outcome{}
}

func memoryHash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func memoryTarget(target string) bool {
	return placementPath(target) && path.Base(target) == "UMCODE.md"
}

func memoryEOL(current []byte) string {
	if i := bytes.IndexByte(current, '\n'); i > 0 && current[i-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// Parsing keeps raw offsets; only CRLF line terminators are normalized for
// comparison. Ambiguous managed syntax is never repaired automatically.
func parseMemorySection(current []byte) (memorySection, bool) {
	s := memorySection{marker: -1}
	heading := -1
	var fence byte
	fenceLength := 0
	for start := 0; start < len(current); {
		next := len(current)
		if i := bytes.IndexByte(current[start:], '\n'); i >= 0 {
			next = start + i + 1
		}
		end := next
		if end > start && current[end-1] == '\n' {
			end--
			if end > start && current[end-1] == '\r' {
				end--
			}
		}
		line := memoryLine{start, end, next, string(current[start:end])}
		i := len(s.lines)
		s.lines = append(s.lines, line)
		text := strings.TrimSpace(line.text)
		if len(text) > 0 && (text[0] == '`' || text[0] == '~') {
			length := 0
			for length < len(text) && text[length] == text[0] {
				length++
			}
			if fence == 0 && length >= 3 {
				fence, fenceLength = text[0], length
			} else if text[0] == fence && length >= fenceLength && strings.TrimSpace(text[length:]) == "" {
				fence, fenceLength = 0, 0
			}
		}
		if strings.HasPrefix(strings.TrimSpace(line.text), managedHeading) {
			if line.text != managedHeading || heading >= 0 || fence != 0 {
				return memorySection{}, false
			}
			heading = i
		}
		lower := strings.ToLower(line.text)
		if strings.Contains(lower, "umcode:generated") || strings.Contains(lower, "<!--") && strings.Contains(lower, "umcode:") {
			if line.text != managedMarker || s.marker >= 0 || heading < 0 || fence != 0 {
				return memorySection{}, false
			}
			s.marker = i
		}
		start = next
	}
	// Appending a new section to an unclosed example would put generated
	// instructions inside user code, so it is ambiguous even without a marker.
	if fence != 0 {
		return memorySection{}, false
	}
	s.end = len(s.lines)
	if heading < 0 {
		return s, s.marker < 0
	}
	if s.marker <= heading {
		return memorySection{}, false
	}
	for i := heading + 1; i < s.marker; i++ {
		if strings.TrimSpace(s.lines[i].text) != "" {
			return memorySection{}, false
		}
	}
	s.insert = s.lines[s.marker].next
	for i := s.marker + 1; i < len(s.lines); i++ {
		text := strings.TrimSpace(s.lines[i].text)
		if strings.HasPrefix(text, "# ") || strings.HasPrefix(text, "## ") {
			s.end = i
			break
		}
		if strings.HasPrefix(text, "###") || strings.HasPrefix(text, "```") || strings.HasPrefix(text, "~~~") {
			return memorySection{}, false
		}
		if text != "" {
			s.insert = s.lines[i].next
		}
	}
	return s, true
}
