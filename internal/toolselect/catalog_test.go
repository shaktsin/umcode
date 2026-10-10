package toolselect

import (
	"encoding/json"
	"github.com/shaktsin/umcode/internal/llm"
	"reflect"
	"strings"
	"testing"
)

func entry(name, family string) Entry {
	return Entry{CanonicalName: name, WireName: strings.ReplaceAll(name, ".", "__"), Family: family, Origin: "registry", Spec: llm.ToolSpec{Name: strings.ReplaceAll(name, ".", "__"), Description: "Description of " + name, Schema: json.RawMessage(`{"type":"object"}`)}}
}
func TestCatalogOrderAndCollisions(t *testing.T) {
	a, b := entry("file.read", "core"), entry("web.search", "web")
	x, err := NewCatalog([]Entry{b, a})
	if err != nil {
		t.Fatal(err)
	}
	y, err := NewCatalog([]Entry{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if x.Generation == "" || !reflect.DeepEqual(x, y) {
		t.Fatal("catalog not canonical")
	}
	b.WireName = a.WireName
	b.Spec.Name = a.WireName
	if _, err = NewCatalog([]Entry{a, b}); err == nil {
		t.Fatal("wire collision accepted")
	}
	reserved := entry("tools.discover", "core")
	if _, err = NewCatalog([]Entry{reserved}); err == nil {
		t.Fatal("discovery origin not reserved")
	}
	a = entry("file.read", "core")
	x, err = NewCatalog([]Entry{a})
	if err != nil {
		t.Fatal(err)
	}
	a.Spec.Schema[0] = 'x'
	if x.Entries[0].Spec.Schema[0] == 'x' {
		t.Fatal("schema aliased")
	}
}
func TestCatalogBounds(t *testing.T) {
	for _, change := range []func(*Entry){func(e *Entry) { e.Origin = strings.Repeat("x", 4097) }, func(e *Entry) { e.Spec.Schema = json.RawMessage(strings.Repeat(" ", 256*1024+1)) }, func(e *Entry) { e.Family = "invented" }} {
		e := entry("file.read", "core")
		change(&e)
		if _, err := NewCatalog([]Entry{e}); err == nil {
			t.Fatal("unsafe metadata accepted")
		}
	}
}

func TestCatalogGenerationIncludesExactSchemaBytes(t *testing.T) {
	a := entry("file.read", "core")
	x, err := NewCatalog([]Entry{a})
	if err != nil {
		t.Fatal(err)
	}
	a.Spec.Schema = json.RawMessage(`{ "type": "object" }`)
	y, err := NewCatalog([]Entry{a})
	if err != nil {
		t.Fatal(err)
	}
	if x.Generation == y.Generation {
		t.Fatal("exact schema byte change lost in generation")
	}
}
