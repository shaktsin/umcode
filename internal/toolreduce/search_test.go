package toolreduce

import (
	"fmt"
	"strings"
	"testing"
)

func fileSearchFixture() string {
	var b strings.Builder
	for i := 1; i <= 35; i++ {
		fmt.Fprintf(&b, "a.go:%d: needle %s\n", i, strings.Repeat("a", 35))
	}
	b.WriteString("b.go-9- before\nb.go:10: needle B\nb.go-11- after\n")
	b.WriteString("c.go:3: needle C\n\n37 match(es) in 3 file(s); stopped at 37 matches — narrow the search with path or glob")
	return b.String()
}

func TestFileSearchReducerRoundRobinsAcrossFiles(t *testing.T) {
	got, report, applied := Reduce(Input{Name: "file.search", Output: fileSearchFixture(), Budget: 140})
	if !applied {
		t.Fatalf("reduction declined: %+v", report)
	}
	for _, want := range []string{"a.go:1:", "b.go:10:", "c.go:3:"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing first hit for %s: %q", want, got)
		}
	}
	if report.Omitted["matches"] == 0 || !strings.Contains(got, "narrow path, glob, or pattern") {
		t.Fatalf("missing omissions/retrieval: %q %+v", got, report)
	}
}

func TestFileSearchReducerKeepsContextWithItsMatch(t *testing.T) {
	got, report, applied := Reduce(Input{Name: "file.search", Output: fileSearchFixture(), Budget: 140})
	if !applied {
		t.Fatalf("reduction declined: %+v", report)
	}
	for _, want := range []string{"b.go-9- before", "b.go:10: needle B", "b.go-11- after"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing grouped context %q", want)
		}
	}
}

func TestFileSearchReducerKeepsSharedContextWhenEarlierHitIsOmitted(t *testing.T) {
	out := "a.go:10: " + strings.Repeat("oversized", 90) + "\na.go-11- shared context\na.go:12: second hit\n"
	for i := 0; i < 50; i++ {
		out += fmt.Sprintf("b.go:%d: filler content\n", i+1)
	}
	out += "\n52 match(es) in 2 file(s)"
	got, report, applied := Reduce(Input{Name: "file.search", Output: out, Budget: 100})
	if !applied {
		t.Fatalf("reduction declined: %+v", report)
	}
	if !strings.Contains(got, "a.go:12: second hit") || !strings.Contains(got, "a.go-11- shared context") {
		t.Fatalf("second hit lost its adjacent context: %q", got)
	}
}

func TestFileSearchReducerKeepsTotalsAndTruncation(t *testing.T) {
	got, report, applied := Reduce(Input{Name: "file.search", Output: fileSearchFixture(), Budget: 140})
	if !applied {
		t.Fatalf("reduction declined: %+v", report)
	}
	if !strings.Contains(got, "37 match(es) in 3 file(s)") || !strings.Contains(got, "stopped at 37 matches") {
		t.Fatalf("totals or truncation lost: %q", got)
	}
}

func TestWebSearchReducerKeepsWholeResultRecords(t *testing.T) {
	var records []string
	for i := 0; i < 8; i++ {
		records = append(records, fmt.Sprintf(`{"title":"Title %d","url":"https://example.org/%d","snippet":"%s"}`, i, i, strings.Repeat("word ", 30)))
	}
	out := "[" + strings.Join(records, ",") + "]"
	got, report, applied := Reduce(Input{Name: "web.search", Output: out, Budget: 110})
	if !applied {
		t.Fatalf("reduction declined: %+v", report)
	}
	if !strings.Contains(got, "Title 0") || !strings.Contains(got, "https://example.org/0") || !strings.Contains(got, strings.Repeat("word ", 30)) {
		t.Fatalf("first record was split: %q", got)
	}
	if report.Omitted["records"] <= 0 || !strings.Contains(got, "narrow or repeat web.search") {
		t.Fatalf("missing omission notice: %q %+v", got, report)
	}
}

func TestWebFetchAndArbitraryStructuredResultsStayUnchanged(t *testing.T) {
	for _, name := range []string{"web.fetch", "file.read", "plugin.search"} {
		out := strings.Repeat(`{"important":"opaque"}`, 80)
		got, _, applied := Reduce(Input{Name: name, Output: out, Budget: 30})
		if applied || got != out {
			t.Errorf("%s was reduced", name)
		}
	}
	for _, out := range []string{`[{"title":"T","url":"https://example.org","snippet":"S","future":"important"}]`, `[{"title":"T","url":"https://example.org","snippet":"S"},]`} {
		got, _, applied := Reduce(Input{Name: "web.search", Output: out, Budget: 5})
		if applied || got != out {
			t.Errorf("unknown/malformed web shape was reduced: %q", got)
		}
	}
	badTotals := strings.Replace(fileSearchFixture(), "37 match(es)", "38 match(es)", 1)
	got, _, applied := Reduce(Input{Name: "file.search", Output: badTotals, Budget: 140})
	if applied || got != badTotals {
		t.Fatal("inconsistent file search totals were reduced")
	}
	badFiles := strings.Replace(fileSearchFixture(), "3 file(s)", "4 file(s)", 1)
	got, _, applied = Reduce(Input{Name: "file.search", Output: badFiles, Budget: 140})
	if applied || got != badFiles {
		t.Fatal("inconsistent file search file count was reduced")
	}
}
