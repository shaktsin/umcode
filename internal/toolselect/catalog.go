// Package toolselect selects advertisements, never execution permissions.
package toolselect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shaktsin/umcode/internal/llm"
	"sort"
	"strings"
	"unicode/utf8"
)

type Entry struct {
	CanonicalName, WireName, Family, Origin string
	Spec                                    llm.ToolSpec
}
type Catalog struct {
	Generation string
	Entries    []Entry
}

func validFamily(f string) bool {
	switch f {
	case "core", "web", "browser", "computer", "other":
		return true
	}
	return strings.HasPrefix(f, "mcp:") && len(f) > 4 || strings.HasPrefix(f, "plugin:") && len(f) > 7
}
func cloneEntry(e Entry) Entry {
	e.Spec.Schema = append(json.RawMessage(nil), e.Spec.Schema...)
	return e
}
func NewCatalog(entries []Entry) (Catalog, error) {
	if len(entries) > 4096 {
		return Catalog{}, errors.New("catalog bounds")
	}
	c := Catalog{Entries: make([]Entry, 0, len(entries))}
	names, wires := map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		for _, v := range []string{e.CanonicalName, e.WireName, e.Family, e.Origin, e.Spec.Description} {
			if len(v) > 4096 || !utf8.ValidString(v) {
				return Catalog{}, errors.New("catalog metadata")
			}
		}
		if e.CanonicalName == "" || e.WireName == "" || len(e.WireName) > 64 || e.Origin == "" || !validFamily(e.Family) || e.Spec.Name != e.WireName || names[e.CanonicalName] || wires[e.WireName] || len(e.Spec.Schema) > 256*1024 || !json.Valid(e.Spec.Schema) || e.CanonicalName == "tools.discover" && e.Origin != "engine" {
			return Catalog{}, errors.New("catalog identity or schema")
		}
		names[e.CanonicalName] = true
		wires[e.WireName] = true
		c.Entries = append(c.Entries, cloneEntry(e))
	}
	sort.Slice(c.Entries, func(i, j int) bool { return c.Entries[i].CanonicalName < c.Entries[j].CanonicalName })
	h := sha256.New()
	for _, e := range c.Entries {
		metadata := []string{e.CanonicalName, e.WireName, e.Family, e.Origin, e.Spec.Name, e.Spec.Description}
		if err := json.NewEncoder(h).Encode(metadata); err != nil {
			return Catalog{}, errors.New("catalog encoding")
		}
		fmt.Fprintf(h, "%d:", len(e.Spec.Schema))
		_, _ = h.Write(e.Spec.Schema)
	}
	c.Generation = hex.EncodeToString(h.Sum(nil))
	return c, nil
}
