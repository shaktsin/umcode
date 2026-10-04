package toolreduce

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func shellFixture() string {
	var b strings.Builder
	b.WriteString("exit_code: 1\n--- stdout ---\ncommand: go test ./...\nworking_directory: /repo\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "progress %03d %s\n", i, strings.Repeat("x", 24))
	}
	b.WriteString("pkg/main.go:42: error: undefined symbol\n[sandbox: writes are limited to the project folder]\n--- stderr ---\nFAIL example/pkg\n")
	return b.String()
}

func TestShellReducerKeepsMetadataDiagnosticsAndTail(t *testing.T) {
	in := Input{Name: "shell.run", Output: shellFixture(), Budget: 200, IsError: true}
	got, report, applied := Reduce(in)
	if !applied {
		t.Fatalf("reduction declined: %+v", report)
	}
	for _, want := range []string{"exit_code: 1", "command: go test ./...", "working_directory: /repo", "pkg/main.go:42: error: undefined symbol", "[sandbox: writes are limited to the project folder]", "FAIL example/pkg", "omitted middle output:", "rerun shell.run"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Index(got, "exit_code: 1") > strings.Index(got, "FAIL example/pkg") {
		t.Fatal("selected lines lost original order")
	}
	if report.Omitted["lines"] <= 0 || report.Omitted["bytes"] <= 0 {
		t.Fatalf("missing omission counts: %+v", report.Omitted)
	}
	wantLines := 80 - strings.Count(got, "progress ")
	if report.Omitted["lines"] != wantLines || report.Omitted["bytes"] != wantLines*38 {
		t.Fatalf("omission counts %+v, want %d lines and %d bytes", report.Omitted, wantLines, wantLines*38)
	}
}

func TestShellReducerCollapsesConsecutiveProgressOnly(t *testing.T) {
	var b strings.Builder
	b.WriteString("exit_code: 0\n--- stdout ---\n")
	for i := 0; i < 60; i++ {
		b.WriteString("progress 50%\n")
	}
	b.WriteString("unique\nprogress 50%\nDONE\n")
	in := Input{Name: "shell.run", Output: b.String(), Budget: 80}
	got, report, applied := Reduce(in)
	if !applied {
		t.Fatalf("reduction declined: %+v", report)
	}
	if strings.Count(got, "progress 50%") != 2 || !strings.Contains(got, "repeated 60 times") || !strings.Contains(got, "DONE") {
		t.Fatalf("wrong progress collapse: %q", got)
	}
	if report.Omitted["progress_lines"] != 59 {
		t.Fatalf("collapsed %d lines, want 59", report.Omitted["progress_lines"])
	}
}

func TestShellReducerFindsANSIFailureInCRLFOutput(t *testing.T) {
	var b strings.Builder
	b.WriteString("exit_code: 1\r\n--- stdout ---\r\n")
	for i := 0; i < 70; i++ {
		fmt.Fprintf(&b, "routine %03d\r\n", i)
	}
	b.WriteString("\x1b[31mFAIL\x1b[0m case A\r\nfinal verdict\r\n")
	got, report, applied := Reduce(Input{Name: "shell.run", Output: b.String(), Budget: 90, IsError: true})
	if !applied {
		t.Fatalf("reduction declined: %+v", report)
	}
	if !strings.Contains(got, "\x1b[31mFAIL\x1b[0m case A") || !strings.Contains(got, "final verdict") {
		t.Fatalf("ANSI diagnostic or tail lost: %q", got)
	}
}

func TestShellReducerKeepsUTF8ValidWithOneOversizedLine(t *testing.T) {
	out := "exit_code: 0\n--- stdout ---\n" + strings.Repeat("中🙂", 600) + "\n"
	got, _, applied := Reduce(Input{Name: "shell.run", Output: out, Budget: 100})
	if applied && !utf8.ValidString(got) {
		t.Fatal("reduced output is invalid UTF-8")
	}
	if !applied && got != out {
		t.Fatal("decline changed original output")
	}
}

func TestExecReducerDeclinesUnknownControlFormat(t *testing.T) {
	for _, state := range []string{"status: suspended (unknown control protocol)", "status: running (use exec.write to send input or poll, exec.stop to end it) and extra control"} {
		out := "session_id: exec_1234\n" + state + "\n--- output ---\n" + strings.Repeat("ordinary line\n", 100)
		got, _, applied := Reduce(Input{Name: "exec.write", Output: out, Budget: 80})
		if applied || got != out {
			t.Fatalf("unknown session control was changed: %q", got)
		}
	}
}

func TestShellReducerReportsExactOmittedLineAndByteCounts(t *testing.T) {
	var b strings.Builder
	b.WriteString("exit_code: 0\n--- stdout ---\n")
	for i := 0; i < 60; i++ {
		b.WriteString("ordinary output line\n")
	}
	b.WriteString("DONE\n")
	got, report, applied := Reduce(Input{Name: "shell.run", Output: b.String(), Budget: 80})
	if !applied {
		t.Fatalf("reduction declined: %+v", report)
	}
	// One repeated line survives with an explicit count; the other 59
	// original 21-byte lines are accounted for as repeated progress.
	if report.Omitted["progress_lines"] != 59 || report.Omitted["progress_bytes"] != 59*21 {
		t.Fatalf("wrong progress accounting: %+v", report.Omitted)
	}
	if !strings.Contains(got, "omitted repeated progress: 59 line(s), 1239 byte(s)") {
		t.Fatalf("missing exact omission notice: %q", got)
	}
}

func TestShellNonzeroExitUsesFailureBudget(t *testing.T) {
	out := "exit_code: 2\n--- stdout ---\n" + strings.Repeat("diagnostic detail\n", 700)
	got, report, applied := Reduce(Input{Name: "shell.run", Output: out})
	if applied || got != out || report.OriginalTokens <= defaultBudgetTokens || report.OriginalTokens >= failureBudgetTokens {
		t.Fatalf("nonzero exit should pass through within failure budget: %+v", report)
	}
}

func TestExecReducerKeepsSessionStateAndDroppedNotice(t *testing.T) {
	var b strings.Builder
	b.WriteString("session_id: exec_abcd1234\nstatus: running (use exec.write to send input or poll, exec.stop to end it)\n[50 bytes of earlier output were dropped]\n--- output ---\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "progress %03d\n", i)
	}
	b.WriteString("server ready\n")
	got, report, applied := Reduce(Input{Name: "exec.write", Output: b.String(), Budget: 120})
	if !applied {
		t.Fatalf("reduction declined: %+v", report)
	}
	for _, want := range []string{"session_id: exec_abcd1234", "status: running", "[50 bytes of earlier output were dropped]", "server ready", "omitted middle output:"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
}
