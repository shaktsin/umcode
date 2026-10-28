package protocol

import (
	"encoding/json"
	"testing"
)

func TestNormalizeComplexity(t *testing.T) {
	cases := map[string]Complexity{
		"quick": ComplexityLow, "standard": ComplexityMedium, "auto": ComplexityMedium,
		"deep": ComplexityHigh, "": "", "HIGH": ComplexityHigh, " Low ": ComplexityLow,
		"medium": ComplexityMedium, "bogus": "bogus",
	}
	for in, want := range cases {
		if got := NormalizeComplexity(in); got != want {
			t.Errorf("NormalizeComplexity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComplexityUnmarshalLegacy(t *testing.T) {
	cases := map[string]Complexity{"deep": ComplexityHigh, "quick": ComplexityLow, "standard": ComplexityMedium, "auto": ComplexityMedium, "high": ComplexityHigh}
	for in, want := range cases {
		var sel ModelSelection
		if err := json.Unmarshal([]byte(`{"complexity":"`+in+`"}`), &sel); err != nil {
			t.Fatal(err)
		}
		if sel.Complexity != want {
			t.Errorf("%s -> %q, want %q", in, sel.Complexity, want)
		}
	}
}

func TestComplexityValid(t *testing.T) {
	for _, c := range []Complexity{"", ComplexityLow, ComplexityMedium, ComplexityHigh} {
		if !c.Valid() {
			t.Errorf("%q should be valid", c)
		}
	}
	for _, c := range []Complexity{"auto", "quick", "standard", "deep", "bogus"} {
		if c.Valid() {
			t.Errorf("%q should be invalid", c)
		}
	}
}

func TestComplexityUnmarshalNull(t *testing.T) {
	c := ComplexityHigh
	if err := json.Unmarshal([]byte("null"), &c); err != nil || c != ComplexityHigh {
		t.Fatalf("null changed value: %q err=%v", c, err)
	}
}
