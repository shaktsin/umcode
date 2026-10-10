package toolselect

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDiscoveryLoadsExactAndLexicalMatches(t *testing.T) {
	s, _, _ := Start(selectionCatalog(t), Signals{})
	for _, args := range []string{`{"names":["web.search","web__search"]}`, `{"query":"web"}`} {
		r, err := s.Discover(json.RawMessage(args))
		if err != nil || len(r.Entries) != 1 || !r.Entries[0].Loaded || !specNames(s)["web__search"] {
			t.Fatalf("r=%+v err=%v", r, err)
		}
	}
}
func TestDiscoveryStrictArguments(t *testing.T) {
	s, _, _ := Start(selectionCatalog(t), Signals{})
	for _, arg := range []string{`{}`, `{"unknown":true}`, `{"query":1}`, `{"query":"web"} {}`, `{"names":["a","b","c","d","e","f","g","h","i"]}`, `{"query":"` + strings.Repeat("x", 513) + `"}`} {
		if _, err := s.Discover(json.RawMessage(arg)); err == nil {
			t.Fatal("invalid args accepted", arg)
		}
	}
}
func discoveryCatalog(t *testing.T, n int) Catalog {
	var es []Entry
	for i := 0; i < n; i++ {
		es = append(es, entry(fmt.Sprintf("special.%03d", i), "other"))
	}
	c, err := NewCatalog(es)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestDiscoveryPaginationScoped(t *testing.T) {
	c := discoveryCatalog(t, 19)
	s, _, _ := Start(c, Signals{})
	foreign, _, _ := Start(c, Signals{})
	cursor := ""
	seen := map[string]bool{}
	for {
		args, _ := json.Marshal(DiscoverArgs{Query: "other", Cursor: cursor})
		r, err := s.Discover(args)
		if err != nil || len(r.Entries) == 0 || len(r.Entries) > 8 {
			t.Fatalf("%+v %v", r, err)
		}
		for _, e := range r.Entries {
			if seen[e.CanonicalName] {
				t.Fatal("duplicate page")
			}
			seen[e.CanonicalName] = true
		}
		if r.NextCursor == "" {
			break
		}
		if cursor == "" {
			for _, bad := range []DiscoverArgs{{Query: "other", Cursor: r.NextCursor + "x"}, {Query: "changed", Cursor: r.NextCursor}} {
				arg, _ := json.Marshal(bad)
				if _, err := s.Discover(arg); err == nil {
					t.Fatal("altered cursor accepted")
				}
			}
			arg, _ := json.Marshal(DiscoverArgs{Query: "other", Cursor: r.NextCursor})
			if _, err := foreign.Discover(arg); err == nil {
				t.Fatal("foreign turn accepted")
			}
		}
		cursor = r.NextCursor
	}
	if len(seen) != 19 {
		t.Fatal(seen)
	}
}
func TestDiscoveryPermissionAndCaps(t *testing.T) {
	c := discoveryCatalog(t, 73)
	s, _, _ := Start(c, Signals{})
	r, err := s.Discover(json.RawMessage(`{"names":["forbidden.secret"]}`))
	if err != nil || len(r.Entries) != 0 {
		t.Fatal("forbidden existence revealed")
	}
	cursor := ""
	for {
		arg, _ := json.Marshal(DiscoverArgs{Query: "other", Cursor: cursor})
		r, err = s.Discover(arg)
		if err != nil {
			t.Fatal(err)
		}
		cursor = r.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(s.Specs()) != 73 || s.Advance(Signals{}).Fallback != "addition_cap" {
		t.Fatal("discovery cap made tools unreachable")
	}
}
func TestDiscoverySecretMetadata(t *testing.T) {
	e := entry("special.check", "other")
	e.Spec.Description = "secret=sk-abcdefghijklmnopqrstuvwxyz1234567890 " + strings.Repeat("é", 300)
	c, err := NewCatalog([]Entry{e})
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ := Start(c, Signals{})
	r, err := s.Discover(json.RawMessage(`{"query":"other"}`))
	if err != nil || len(r.Entries) != 1 {
		t.Fatal(r, err)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "sk-abcdefghijklmnopqrstuvwxyz1234567890") || len(r.Entries[0].Description) > 256 {
		t.Fatal("unbounded or secret metadata", string(b))
	}
}

func TestDiscoveryRevocationCannotLoadOrFallback(t *testing.T) {
	c := selectionCatalog(t)
	s, _, _ := Start(c, Signals{})
	permitted := map[string]bool{}
	for _, e := range c.Entries {
		permitted[e.CanonicalName] = e.CanonicalName != "web.search"
	}
	s.Restrict(permitted)
	r, err := s.Discover(json.RawMessage(`{"names":["web.search"]}`))
	if err != nil || len(r.Entries) != 0 {
		t.Fatal("revoked entry discoverable", r, err)
	}
	s.Fallback("selection_error")
	if specNames(s)["web__search"] {
		t.Fatal("fallback restored revoked permission")
	}
}
