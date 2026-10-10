package memory

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestMergeCreationAndPreservation(t *testing.T) {
	// Losing a byte outside the managed section, changing newline style, or
	// sorting preexisting bullets must fail these hand-written fixtures.
	for _, tc := range []struct{ name, before, want string }{
		{"empty root", "", "## Verified project memory\n\n<!-- umcode:generated -->\n- Run repository checks.\n"},
		{"append LF", "# Project\nUser text.\n", "# Project\nUser text.\n\n## Verified project memory\n\n<!-- umcode:generated -->\n- Run repository checks.\n"},
		{"ordinary umcode text", "Use the umcode:cli label.\n", "Use the umcode:cli label.\n\n## Verified project memory\n\n<!-- umcode:generated -->\n- Run repository checks.\n"},
		{"append CRLF", "# Project\r\nUser text.\r\n", "# Project\r\nUser text.\r\n\r\n## Verified project memory\r\n\r\n<!-- umcode:generated -->\r\n- Run repository checks.\r\n"},
		{"no final newline", "# Project", "# Project\n\n## Verified project memory\n\n<!-- umcode:generated -->\n- Run repository checks."},
		{"preserve surrounding bytes", "preamble  \n## Verified project memory\n\n<!-- umcode:generated -->\n- Zebra stays first.\n\n## User section\nuser  \ttext", "preamble  \n## Verified project memory\n\n<!-- umcode:generated -->\n- Zebra stays first.\n- Run repository checks.\n\n## User section\nuser  \ttext"},
		{"existing CRLF without final newline", "## Verified project memory\r\n\r\n<!-- umcode:generated -->\r\n- Zebra stays first.", "## Verified project memory\r\n\r\n<!-- umcode:generated -->\r\n- Zebra stays first.\r\n- Run repository checks."},
		{"marker without blank", "## Verified project memory\n<!-- umcode:generated -->", "## Verified project memory\n<!-- umcode:generated -->\n- Run repository checks."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := []byte(tc.before)
			got, out := Merge(MergeInput{Current: current, Proposals: []Proposal{{CandidateNodeID: "candidate", SemanticKey: "checks", Text: "- Run repository checks."}}, TargetBytes: 4096})
			if out != (Outcome{}) || string(got.After) != tc.want || !reflect.DeepEqual(got.Inserted, []string{"candidate"}) {
				t.Fatalf("Merge = %+v, %v; want %q", got, out, tc.want)
			}
			if got.BeforeHash != fmt.Sprintf("%x", sha256.Sum256(current)) || got.AfterHash != fmt.Sprintf("%x", sha256.Sum256([]byte(tc.want))) {
				t.Fatalf("incorrect full-file hashes: %+v", got)
			}
			if string(current) != tc.before {
				t.Fatal("mutated caller bytes")
			}
		})
	}
}

func TestMergeDuplicatesAnywhere(t *testing.T) {
	for _, before := range []string{
		"- Run repository checks.",
		"# User\r\n- Run repository checks.\r\n## Next\r\n",
		"## Verified project memory\n\n<!-- umcode:generated -->\n- Run repository checks.\n",
		"## Verified project memory\n\n<!-- umcode:generated -->\n\n## User\n- Run repository checks.\n",
	} {
		got, out := Merge(MergeInput{Current: []byte(before), Proposals: []Proposal{{CandidateNodeID: "candidate", SemanticKey: "checks", Text: "- Run repository checks."}}, TargetBytes: 4096})
		if out != (Outcome{}) || string(got.After) != before || !reflect.DeepEqual(got.Unchanged, []string{"candidate"}) || len(got.Inserted)+len(got.Replaced) != 0 || got.BeforeHash != got.AfterHash {
			t.Fatalf("duplicate changed file: %+v, %v", got, out)
		}
	}
}

func TestMergeDeterministicCandidateOrder(t *testing.T) {
	proposals := []Proposal{{CandidateNodeID: "z", SemanticKey: "z", Text: "- Last."}, {CandidateNodeID: "a", SemanticKey: "a", Text: "- First."}, {CandidateNodeID: "b", SemanticKey: "b", Text: "- First."}}
	got, out := Merge(MergeInput{Proposals: proposals, TargetBytes: 4096})
	if out != (Outcome{}) || string(got.After) != "## Verified project memory\n\n<!-- umcode:generated -->\n- First.\n- Last.\n" || !reflect.DeepEqual(got.Inserted, []string{"a", "z"}) || !reflect.DeepEqual(got.Unchanged, []string{"b"}) || proposals[0].CandidateNodeID != "z" {
		t.Fatalf("nondeterministic merge or mutated proposals: %+v, %v", got, out)
	}
}

func mergeRecorded(text string) protocol.ProjectMemory {
	return protocol.ProjectMemory{ID: "old", ProjectID: "project", SemanticKey: "checks", Category: "command", TargetPath: "UMCODE.md", Text: text, TextHash: fmt.Sprintf("%x", sha256.Sum256([]byte(text))), Status: protocol.MemoryStatusActive}
}

func TestMergeExactReplacement(t *testing.T) {
	before := "user  \r\n## Verified project memory\r\n\r\n<!-- umcode:generated -->\r\n- Old checks.\r\n- User bullet.\r\n\r\n# User footer\r\n"
	got, out := Merge(MergeInput{Current: []byte(before), Active: []protocol.ProjectMemory{mergeRecorded("- Old checks.")}, Proposals: []Proposal{{CandidateNodeID: "new", SemanticKey: "checks", Text: "- New checks.", ReplacesMemory: "old"}}, TargetPath: "UMCODE.md", TargetBytes: 4096})
	want := "user  \r\n## Verified project memory\r\n\r\n<!-- umcode:generated -->\r\n- New checks.\r\n- User bullet.\r\n\r\n# User footer\r\n"
	if out != (Outcome{}) || string(got.After) != want || !reflect.DeepEqual(got.Replaced, []string{"new"}) || len(got.Inserted)+len(got.Unchanged) != 0 {
		t.Fatalf("replacement = %+v, %v; want %q", got, out, want)
	}
}

func TestMergeOwnershipConflicts(t *testing.T) {
	section := "## Verified project memory\n\n<!-- umcode:generated -->\n"
	for _, tc := range []struct {
		name, before string
		mutate       func(*protocol.ProjectMemory)
	}{
		{"edited", section + "- Edited checks.\n", nil},
		{"moved", "- Old checks.\n" + section, nil},
		{"deleted", section, nil},
		{"duplicated inside", section + "- Old checks.\n- Old checks.\n", nil},
		{"duplicated outside", "- Old checks.\n" + section + "- Old checks.\n", nil},
		{"hash mismatch", section + "- Old checks.\n", func(r *protocol.ProjectMemory) { r.TextHash = "wrong" }},
		{"target mismatch", section + "- Old checks.\n", func(r *protocol.ProjectMemory) { r.TargetPath = "nested/UMCODE.md" }},
		{"invalid target", section + "- Old checks.\n", func(r *protocol.ProjectMemory) { r.TargetPath = "../UMCODE.md" }},
		{"superseded row", section + "- Old checks.\n", func(r *protocol.ProjectMemory) { r.SupersededBy = "other" }},
		{"indented heading continuation", section + "- Old checks.\n\n    ## Local caveat\n    Only use with integration service.\n", nil},
		{"raw HTML relocation", "<pre>\n" + section + "- Old checks.\n## Example footer\n</pre>\n", nil},
		{"continued bullet", section + "- Old checks.\n  User continuation.\n", nil},
		{"blank before continuation", section + "- Old checks.\n\n  User continuation.\n", nil},
		{"multiple blanks before tab continuation", section + "- Old checks.\n\n \t\n\tUser continuation.\n- User bullet.\n", nil},
		{"blank before nested bullet", section + "- Old checks.\n\n  - User child bullet.\n", nil},
		{"fenced text", section + "```markdown\n- Old checks.\n\n```\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := mergeRecorded("- Old checks.")
			if tc.mutate != nil {
				tc.mutate(&row)
			}
			before := []byte(tc.before)
			got, out := Merge(MergeInput{Current: before, Active: []protocol.ProjectMemory{row}, Proposals: []Proposal{{CandidateNodeID: "new", SemanticKey: "checks", Text: "- New checks.", ReplacesMemory: "old"}}, TargetPath: "UMCODE.md", TargetBytes: 4096})
			if out.Status != protocol.MemoryOutcomeConflicted || out.Reason == "" || !reflect.DeepEqual(got, MergeResult{}) || string(before) != tc.before {
				t.Fatalf("unsafe ownership: %+v, %v", got, out)
			}
		})
	}
}

func TestMergeManagedExampleInsideOuterFenceConflicts(t *testing.T) {
	for _, tc := range []struct{ name, before string }{
		{"backticks with level two boundary", "```markdown\n## Verified project memory\n\n<!-- umcode:generated -->\n- Old checks.\n## Example footer\n```\n"},
		{"tildes with level one boundary CRLF", "~~~markdown\r\n## Verified project memory\r\n\r\n<!-- umcode:generated -->\r\n- Old checks.\r\n# Example footer\r\n~~~\r\n"},
		{"long fence with shorter apparent close", "````markdown\n```\n## Verified project memory\n\n<!-- umcode:generated -->\n- Old checks.\n# Example footer\n````\n"},
		{"unclosed enclosing fence", "```markdown\n## Verified project memory\n\n<!-- umcode:generated -->\n- Old checks.\n## Example footer\n"},
		{"four-space false delimiters", "```markdown\n    ```\n## Verified project memory\n\n<!-- umcode:generated -->\n- Old checks.\n## Example footer\n    ```\n```\n"},
		{"tab-indented false delimiters", "```markdown\n\t```\n## Verified project memory\n\n<!-- umcode:generated -->\n- Old checks.\n## Example footer\n\t```\n```\n"},
		{"space-and-tab false delimiters", "~~~markdown\n \t~~~\n## Verified project memory\n\n<!-- umcode:generated -->\n- Old checks.\n# Example footer\n \t~~~\n~~~\n"},
		{"non-ASCII trailing whitespace false delimiters", "```markdown\n```\u00a0\n## Verified project memory\n\n<!-- umcode:generated -->\n- Old checks.\n## Example footer\n```\u00a0\n```\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := []byte(tc.before)
			got, out := Merge(MergeInput{Current: before, Active: []protocol.ProjectMemory{mergeRecorded("- Old checks.")}, Proposals: []Proposal{{CandidateNodeID: "new", SemanticKey: "checks", Text: "- New checks.", ReplacesMemory: "old"}}, TargetPath: "UMCODE.md", TargetBytes: 4096})
			if out.Status != protocol.MemoryOutcomeConflicted || !reflect.DeepEqual(got, MergeResult{}) || string(before) != tc.before {
				t.Fatalf("writable fenced example: %+v, %v", got, out)
			}
		})
	}
}

func TestMergeReplacementPreservesBlankItemSeparators(t *testing.T) {
	before := "```markdown\n# Ordinary example\n   ``` \t\n\n## Verified project memory\n\n<!-- umcode:generated -->\n- Old checks.\n\n- User bullet.\n\n## User footer\n"
	want := "```markdown\n# Ordinary example\n   ``` \t\n\n## Verified project memory\n\n<!-- umcode:generated -->\n- New checks.\n\n- User bullet.\n\n## User footer\n"
	got, out := Merge(MergeInput{Current: []byte(before), Active: []protocol.ProjectMemory{mergeRecorded("- Old checks.")}, Proposals: []Proposal{{CandidateNodeID: "new", SemanticKey: "checks", Text: "- New checks.", ReplacesMemory: "old"}}, TargetPath: "UMCODE.md", TargetBytes: 4096})
	if out != (Outcome{}) || string(got.After) != want || !reflect.DeepEqual(got.Replaced, []string{"new"}) {
		t.Fatalf("safe replacement = %+v, %v; want %q", got, out, want)
	}
}

func TestMergeMalformedSections(t *testing.T) {
	for _, before := range []string{
		"## Verified project memory\n",
		"<!-- umcode:generated -->\n",
		"<!-- umcode:generated -->\n## Verified project memory\n",
		"## Verified project memory\n\n<!-- umcode:generated -->\n## Verified project memory\n<!-- umcode:generated -->\n",
		"## Verified project memory\n<!-- umcode:generated -->\n<!-- umcode:generated -->\n",
		"## Verified project memory\n<!-- umcode:generated -->\n### Nested heading\n",
		"## Verified project memory\n<!-- umcode:generated -->\n<!-- umcode:generated extra -->\n",
		"## Verified project memory\nUser text before marker.\n<!-- umcode:generated -->\n",
		"## Verified project memory\n## User section\n<!-- umcode:generated -->\n",
		" ## Verified project memory\n<!-- umcode:generated -->\n",
		"## Verified project memory\n<!-- umcode:generated --> trailing text\n",
		"```markdown\n# Unclosed user example\n",
	} {
		got, out := Merge(MergeInput{Current: []byte(before), Proposals: []Proposal{{CandidateNodeID: "new", Text: "- New."}}, TargetBytes: 4096})
		if out.Status != protocol.MemoryOutcomeConflicted || out.Reason == "" || !reflect.DeepEqual(got, MergeResult{}) {
			t.Fatalf("malformed section accepted: %q => %+v, %v", before, got, out)
		}
	}
}

func TestMergeByteCeiling(t *testing.T) {
	want := "## Verified project memory\n\n<!-- umcode:generated -->\n- 日本語.\n"
	in := MergeInput{Proposals: []Proposal{{CandidateNodeID: "new", Text: "- 日本語."}}, TargetBytes: len([]byte(want)) - 1}
	got, out := Merge(in)
	if out != (Outcome{Status: protocol.MemoryOutcomePending, Reason: "size_limit"}) || !reflect.DeepEqual(got, MergeResult{}) {
		t.Fatalf("byte overflow = %+v, %v", got, out)
	}
	in.TargetBytes++
	got, out = Merge(in)
	if out != (Outcome{}) || string(got.After) != want {
		t.Fatalf("exact limit rejected: %+v, %v", got, out)
	}
}

func TestMergeReconcilesEveryActiveRow(t *testing.T) {
	old := mergeRecorded("- Old checks.")
	other := mergeRecorded("- Other checks.")
	other.ID, other.SemanticKey = "other", "other"
	before := "## Verified project memory\n\n<!-- umcode:generated -->\n- Old checks.\n- Other checks.\n"
	for _, tc := range []struct {
		name   string
		mutate func(*protocol.ProjectMemory)
	}{
		{"different target without explicit target", func(r *protocol.ProjectMemory) { r.TargetPath = "nested/UMCODE.md" }},
		{"unrelated edited row", func(r *protocol.ProjectMemory) {
			r.Text = "- Deleted checks."
			r.TextHash = fmt.Sprintf("%x", sha256.Sum256([]byte(r.Text)))
		}},
		{"duplicate ID", func(r *protocol.ProjectMemory) { r.ID = "old" }},
		{"duplicate semantic identity", func(r *protocol.ProjectMemory) { r.SemanticKey = "checks" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := other
			tc.mutate(&row)
			got, out := Merge(MergeInput{Current: []byte(before), Active: []protocol.ProjectMemory{old, row}, Proposals: []Proposal{{CandidateNodeID: "new", SemanticKey: "new", Text: "- New checks."}}, TargetBytes: 4096})
			if out.Status != protocol.MemoryOutcomeConflicted || !reflect.DeepEqual(got, MergeResult{}) {
				t.Fatalf("unreconciled active row: %+v, %v", got, out)
			}
		})
	}
}

func TestMergeBatchConflictHasNoPartialOutput(t *testing.T) {
	for _, proposals := range [][]Proposal{
		{{CandidateNodeID: "a", SemanticKey: "checks", Text: "- First."}, {CandidateNodeID: "z", SemanticKey: "checks", Text: "- Second."}},
		{{CandidateNodeID: "a", SemanticKey: "first", Text: "- First."}, {CandidateNodeID: "z", SemanticKey: "checks", Text: "- Second.", ReplacesMemory: "missing"}},
		{{CandidateNodeID: "a", SemanticKey: "first", Text: "- First."}, {CandidateNodeID: "a", SemanticKey: "second", Text: "- Second."}},
	} {
		got, out := Merge(MergeInput{Proposals: proposals, TargetBytes: 4096})
		if out.Status != protocol.MemoryOutcomeConflicted || !reflect.DeepEqual(got, MergeResult{}) {
			t.Fatalf("batch conflict leaked partial result: %+v, %v", got, out)
		}
	}
}

func TestMergeAlreadyCurrentAndReplacementDuplicate(t *testing.T) {
	for _, tc := range []struct{ name, text, suffix, replaces string }{
		{"active current", "- Old checks.", "", ""},
		{"replacement duplicate outside", "- New checks.", "\n## User\n- New checks.\n", "old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := "## Verified project memory\n\n<!-- umcode:generated -->\n- Old checks.\n" + tc.suffix
			current := []byte(before)
			got, out := Merge(MergeInput{Current: current, Active: []protocol.ProjectMemory{mergeRecorded("- Old checks.")}, Proposals: []Proposal{{CandidateNodeID: "new", SemanticKey: "checks", Text: tc.text, ReplacesMemory: tc.replaces}}, TargetBytes: 4096})
			if tc.replaces != "" {
				if out.Status != protocol.MemoryOutcomeConflicted || !reflect.DeepEqual(got, MergeResult{}) || string(current) != before {
					t.Fatalf("unsafe duplicate replacement: %+v %v", got, out)
				}
				return
			}
			if out != (Outcome{}) || string(got.After) != before || !reflect.DeepEqual(got.Unchanged, []string{"new"}) {
				t.Fatalf("already current changed bytes: %+v, %v", got, out)
			}
			got.After[0] = 'x'
			if string(current) != before {
				t.Fatal("result aliases input bytes")
			}
		})
	}
}
