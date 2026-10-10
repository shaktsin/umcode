package toolselect

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func selectionCatalog(t *testing.T) Catalog {
	t.Helper()
	var es []Entry
	for _, n := range []string{"file.list", "file.search", "file.read", "file.write", "file.edit", "shell.run", "exec.start", "exec.write", "exec.stop", "verification.plan", "verification.run", "skill.get_instructions", "skill.run_script", "work.update"} {
		es = append(es, entry(n, "core"))
	}
	d := entry("tools.discover", "core")
	d.Origin = "engine"
	es = append(es, d, entry("web.search", "web"), entry("visual.stop", "browser"), entry("computer.stop", "computer"), entry("mcp_server_with_underscores_lookup", "mcp:server_with_underscores"), entry("plugin.special", "plugin:sample-plugin"), entry("odd.action", "other"))
	c, err := NewCatalog(es)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func specNames(s *State) map[string]bool {
	m := map[string]bool{}
	for _, sp := range s.Specs() {
		m[sp.Name] = true
	}
	return m
}
func TestInitialCoreAndFamilySelection(t *testing.T) {
	c := selectionCatalog(t)
	s, _, err := Start(c, Signals{Request: "implement a parser"})
	if err != nil {
		t.Fatal(err)
	}
	names := specNames(s)
	for _, e := range c.Entries {
		if names[e.WireName] != (e.Family == "core") {
			t.Fatalf("unknown intent names=%v", names)
		}
	}
	for _, tc := range []struct{ request, family string }{{"look up the latest documentation", "web"}, {"build a frontend website", "browser"}, {"operate a desktop app", "computer"}, {"use server_with_underscores", "mcp:server_with_underscores"}, {"use sample-plugin", "plugin:sample-plugin"}, {"call odd.action", "other"}} {
		s, _, err := Start(c, Signals{Request: tc.request})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range c.Entries {
			if e.Family == tc.family && specNames(s)[e.WireName] {
				found = true
			}
		}
		if !found {
			t.Fatal(tc)
		}
	}
	s, _, _ = Start(c, Signals{PlannedCheckFamilies: []string{"browser"}})
	if !specNames(s)["visual__stop"] {
		t.Fatal("planned browser check omitted")
	}
}
func TestSelectionPhaseStable(t *testing.T) {
	c := selectionCatalog(t)
	s, _, _ := Start(c, Signals{Request: "look up documentation", Depth: "guided", TaskID: "one"})
	before, _ := json.Marshal(s.Specs())
	s.Advance(Signals{Request: "operate desktop", Depth: "guided", TaskID: "one"})
	after, _ := json.Marshal(s.Specs())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("same phase changed schemas")
	}
	s.Advance(Signals{Request: "operate desktop", Depth: "guided", TaskID: "two"})
	names := specNames(s)
	if !names["web__search"] || !names["computer__stop"] {
		t.Fatal("phase union lost tools")
	}
	next, _, _ := Start(c, Signals{Request: "implement parser"})
	if specNames(next)["web__search"] || specNames(next)["computer__stop"] {
		t.Fatal("next turn did not prune")
	}
	if err := s.Pin("odd__action"); err != nil || !specNames(s)["odd__action"] {
		t.Fatal("pin failed", err)
	}
	s.Fallback("selection_error")
	if len(s.Specs()) != len(c.Entries) {
		t.Fatal("fallback not full")
	}
}
func TestSelectionRequiredSessions(t *testing.T) {
	s, _, _ := Start(selectionCatalog(t), Signals{RequiredFamilies: []string{"browser", "computer"}})
	names := specNames(s)
	if !names["visual__stop"] || !names["computer__stop"] {
		t.Fatal("required lifecycle omitted")
	}
}
func TestSelectionBoundedMetadata(t *testing.T) {
	for _, signals := range []Signals{{Request: strings.Repeat("x", 8193)}, {RequiredFamilies: []string{"invented"}}} {
		c := selectionCatalog(t)
		s, r, err := Start(c, signals)
		if err != nil {
			t.Fatal(err)
		}
		if r.Fallback == "" || len(s.Specs()) != len(c.Entries) {
			t.Fatal("unsafe selection did not fallback")
		}
	}
}
