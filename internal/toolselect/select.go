package toolselect

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/shaktsin/umcode/internal/llm"
	"strings"
	"sync"
	"unicode/utf8"
)

type Signals struct {
	Request, Depth, TaskID                                string
	ExplicitNames, RequiredFamilies, PlannedCheckFamilies []string
}
type Report struct {
	Catalog, Exposed, FullSchemaTokens, ExposedSchemaTokens, DiscoveryCalls, Additions int
	PhaseID, Fallback                                                                  string
}
type State struct {
	mu                       sync.Mutex
	catalog                  Catalog
	loaded, initial, allowed map[string]bool
	phase                    string
	report                   Report
}

func Start(c Catalog, signals Signals) (*State, Report, error) {
	frozen, err := NewCatalog(c.Entries)
	if err != nil {
		return nil, Report{}, err
	}
	s := &State{catalog: frozen, loaded: map[string]bool{}, initial: map[string]bool{}, allowed: map[string]bool{}}
	for _, e := range frozen.Entries {
		s.allowed[e.CanonicalName] = true
		if e.Family == "core" {
			s.loaded[e.CanonicalName] = true
		}
	}
	s.addSignals(signals, true)
	for name := range s.loaded {
		s.initial[name] = true
	}
	return s, s.reportLocked(), nil
}
func validSignals(s Signals) bool {
	if len(s.Request) > 8192 || !utf8.ValidString(s.Request) || len(s.Depth) > 4096 || len(s.TaskID) > 4096 || len(s.ExplicitNames) > 4096 || len(s.RequiredFamilies) > 4096 || len(s.PlannedCheckFamilies) > 4096 {
		return false
	}
	for _, xs := range [][]string{s.ExplicitNames, s.RequiredFamilies, s.PlannedCheckFamilies} {
		for _, x := range xs {
			if len(x) > 4096 || !utf8.ValidString(x) {
				return false
			}
		}
	}
	return true
}
func anyTerm(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}
func (s *State) addSignals(signals Signals, initial bool) {
	if !validSignals(signals) {
		s.fallbackLocked("input_bounds")
		return
	}
	phase := signals.Depth + "\x00" + signals.TaskID
	changed := initial || phase != s.phase
	families := map[string]bool{}
	for _, xs := range [][]string{signals.RequiredFamilies, signals.PlannedCheckFamilies} {
		for _, f := range xs {
			if !validFamily(f) {
				s.fallbackLocked("metadata")
				return
			}
			families[f] = true
		}
	}
	req := strings.ToLower(signals.Request)
	if changed {
		if anyTerm(req, "look up", "web search", "search web", "internet", "latest", "http://", "https://", "browse web") {
			families["web"] = true
		}
		if anyTerm(req, "frontend", "website", "landing page", "visual", "browser", "css", "html", "react", "preview", "screenshot") {
			families["browser"] = true
		}
		if anyTerm(req, "desktop", "computer use", "macos app", "click in", "open app") {
			families["computer"] = true
		}
	}
	for _, e := range s.catalog.Entries {
		if !s.allowed[e.CanonicalName] {
			continue
		}
		load := families[e.Family]
		for _, n := range signals.ExplicitNames {
			load = load || n == e.CanonicalName || n == e.WireName
		}
		if changed {
			load = load || strings.Contains(req, strings.ToLower(e.CanonicalName)) || strings.Contains(req, strings.ToLower(e.WireName))
			if strings.HasPrefix(e.Family, "mcp:") || strings.HasPrefix(e.Family, "plugin:") {
				identity := strings.SplitN(e.Family, ":", 2)[1]
				load = load || strings.Contains(req, strings.ToLower(identity))
			}
		}
		if load {
			s.loaded[e.CanonicalName] = true
		}
	}
	s.phase = phase
	sum := sha256.Sum256([]byte(phase))
	s.report.PhaseID = hex.EncodeToString(sum[:8])
}
func (s *State) Advance(signals Signals) Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addSignals(signals, false)
	return s.reportLocked()
}
func (s *State) Specs() []llm.ToolSpec { s.mu.Lock(); defer s.mu.Unlock(); return s.specsLocked() }
func (s *State) specsLocked() []llm.ToolSpec {
	var specs []llm.ToolSpec
	for _, e := range s.catalog.Entries {
		if s.loaded[e.CanonicalName] && s.allowed[e.CanonicalName] {
			specs = append(specs, cloneEntry(e).Spec)
		}
	}
	return specs
}
func schemaTokens(spec llm.ToolSpec) int {
	return int(llm.EstimateTokens(spec.Name) + llm.EstimateTokens(spec.Description) + llm.EstimateTokens(string(spec.Schema)))
}
func (s *State) reportLocked() Report {
	r := s.report
	r.Catalog = len(s.catalog.Entries)
	r.FullSchemaTokens = 0
	r.ExposedSchemaTokens = 0
	r.Exposed = 0
	r.Additions = 0
	for _, e := range s.catalog.Entries {
		r.FullSchemaTokens += schemaTokens(e.Spec)
		if s.loaded[e.CanonicalName] && s.allowed[e.CanonicalName] {
			r.Exposed++
			r.ExposedSchemaTokens += schemaTokens(e.Spec)
			if !s.initial[e.CanonicalName] {
				r.Additions++
			}
		}
	}
	return r
}
func (s *State) pinLocked(name string) error {
	for _, e := range s.catalog.Entries {
		if (e.CanonicalName == name || e.WireName == name) && s.allowed[e.CanonicalName] {
			s.loaded[e.CanonicalName] = true
			if s.reportLocked().Additions > 64 {
				s.fallbackLocked("addition_cap")
			}
			return nil
		}
	}
	return errors.New("tool unavailable")
}
func (s *State) Pin(name string) error { s.mu.Lock(); defer s.mu.Unlock(); return s.pinLocked(name) }
func (s *State) fallbackLocked(reason string) {
	switch reason {
	case "input_bounds", "metadata", "addition_cap", "catalog_error", "selection_error", "discovery_error":
	default:
		reason = "selection_error"
	}
	if s.report.Fallback == "" {
		s.report.Fallback = reason
	}
	for _, e := range s.catalog.Entries {
		if s.allowed[e.CanonicalName] {
			s.loaded[e.CanonicalName] = true
		}
	}
}
func (s *State) Fallback(reason string) { s.mu.Lock(); defer s.mu.Unlock(); s.fallbackLocked(reason) }
