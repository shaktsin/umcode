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
