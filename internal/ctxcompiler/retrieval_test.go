package ctxcompiler

import (
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/retrieval"
	"reflect"
	"strings"
	"testing"
)

func TestCompilerRetrievalPreservesRequiredState(t *testing.T) {
	in := designedInput(designedDetail())
	in.Window = 20000
	base, ok := Compile(in)
	if !ok {
		t.Fatal(base.Report)
	}
	in.Retrieval = []retrieval.Candidate{{ID: "item:old", Kind: "conversation", Historical: true, Body: "OLDER_PARSER_CONSTRAINT", Distance: 3}}
	got, ok := Compile(in)
	if !ok {
		t.Fatal(got.Report)
	}
	if !strings.Contains(got.Messages[0].JoinedText(), "OLDER_PARSER_CONSTRAINT") {
		t.Fatal("retrieval absent")
	}
	if !strings.HasPrefix(got.Messages[0].JoinedText(), base.Messages[0].JoinedText()) || !reflect.DeepEqual(got.Messages[1:], base.Messages[1:]) {
		t.Fatal("required state or tail changed")
	}
	if got.Report.RetrievalTokens <= 0 || textTokens(got.Messages[0].JoinedText()) > packetBudget(in.Window) {
		t.Fatal(got.Report)
	}
	in.Window = 100
	a, aok := Compile(in)
	in.Retrieval = nil
	b, bok := Compile(in)
	if aok != bok || !reflect.DeepEqual(a.Messages, b.Messages) {
		t.Fatal("tiny window changed fallback")
	}
}

func TestCompilerRetrievalTailDeduplication(t *testing.T) {
	in := designedInput(designedDetail())
	in.Items = []protocol.Item{{ID: "recent", TurnID: "old", Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: "RECENT_CONSTRAINT"}}
	in.Retrieval = []retrieval.Candidate{{ID: "item:recent", Kind: "conversation", Historical: true, Body: "RECENT_CONSTRAINT"}, {ID: "item:older", Kind: "conversation", Historical: true, Body: "OLDER_CONSTRAINT"}}
	got, ok := Compile(in)
	if !ok {
		t.Fatal(got.Report)
	}
	text := ""
	for _, m := range got.Messages {
		text += m.JoinedText()
	}
	if strings.Count(text, "RECENT_CONSTRAINT") != 1 || !strings.Contains(text, "OLDER_CONSTRAINT") {
		t.Fatal(text)
	}
}

func TestCompilerRetrievalMalformedFallback(t *testing.T) {
	in := designedInput(designedDetail())
	base, _ := Compile(in)
	in.Retrieval = []retrieval.Candidate{{ID: "bad", Kind: "instruction", Body: "bad"}}
	got, ok := Compile(in)
	if !ok || !reflect.DeepEqual(got.Messages, base.Messages) {
		t.Fatal("malformed retrieval changed base")
	}
}

func TestReviewRenderedPacketBudget(t *testing.T) {
	for goalLen := 1500; goalLen < 2300; goalLen++ {
		in := Input{Detail: protocol.WorkDetail{Work: protocol.Work{Goal: strings.Repeat("g", goalLen)}}, Window: 4000, Retrieval: []retrieval.Candidate{{ID: "item:old", Kind: "conversation", Historical: true, Body: "relevant old context"}}}
		got, ok := Compile(in)
		if ok && got.Report.RetrievalTokens > 0 && estimate(got.Messages[:1]) > packetBudget(in.Window) {
			t.Fatalf("goalLen=%d packet text=%d rendered cost=%d budget=%d", goalLen, textTokens(got.Messages[0].JoinedText()), estimate(got.Messages[:1]), packetBudget(in.Window))
		}
	}
}
func TestReviewCanonicalPassDedupe(t *testing.T) {
	ev := protocol.Evidence{ID: "e", Kind: protocol.EvidenceVerificationOutput, SourceURI: "go test ./...", Summary: "PASS unique detailed result"}
	in := Input{Detail: protocol.WorkDetail{Work: protocol.Work{Goal: "g"}, Evidence: []protocol.Evidence{ev}, Attempts: []protocol.VerificationAttempt{{EvidenceID: "e", Status: protocol.AttemptPassed}}}, Window: 20000, Retrieval: []retrieval.Candidate{{ID: "evidence:e", Kind: "evidence", Body: ev.Summary}}}
	got, ok := Compile(in)
	if !ok {
		t.Fatal(got.Report)
	}
	if len(got.Report.RetrievalIDs) > 0 {
		t.Fatalf("evidence already represented by - ok line selected again: %s", got.Messages[0].JoinedText())
	}
}

func TestCompilerRetrievalDiagnostics(t *testing.T) {
	in := designedInput(designedDetail())
	c := retrieval.Candidate{ID: "item:old", Kind: "conversation", Historical: true, Body: "older relevant data"}
	in.Retrieval = []retrieval.Candidate{c, c}
	got, ok := Compile(in)
	if !ok {
		t.Fatal(got.Report)
	}
	r := got.Report.Retrieval
	if r.Candidates != 2 || r.Selected != 1 || r.SelectedSources["conversation"] != 1 || r.Drops["duplicate"] != 1 || r.Tokens != got.Report.RetrievalTokens {
		t.Fatalf("missing bounded diagnostics: %+v", r)
	}
}
