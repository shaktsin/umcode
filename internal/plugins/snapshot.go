package plugins

import (
	"sync"

	"github.com/shaktsin/umcode/internal/hooks"
	"github.com/shaktsin/umcode/internal/mcp"
	"github.com/shaktsin/umcode/internal/skills"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// Snapshot is one immutable project capability generation. The manager owns
// one reference while the snapshot is current; every acquired turn owns one
// additional reference.
type Snapshot struct {
	generation      string
	tools           []tools.Tool
	byTool          map[string]tools.Tool
	skills          *skills.Snapshot
	hooks           hooks.Set
	mcp             *mcp.Manager
	installations   []store.PluginInstallation
	componentHealth map[string]map[string]ComponentHealth

	mu      sync.Mutex
	refs    int
	closed  bool
	onClose func()
}

type ComponentHealth struct {
	Status string
	Error  string
}

func (s *Snapshot) Tools() []tools.Tool {
	if s == nil {
		return nil
	}
	return append([]tools.Tool(nil), s.tools...)
}

func (s *Snapshot) Tool(name string) (tools.Tool, bool) {
	if s == nil {
		return nil, false
	}
	tool, ok := s.byTool[name]
	if ok {
		return tool, true
	}
	for _, candidate := range s.tools {
		if tools.ToWire(candidate.Name()) == name {
			return candidate, true
		}
	}
	return nil, false
}

func (s *Snapshot) SkillSnapshot() *skills.Snapshot {
	if s == nil {
		return nil
	}
	return s.skills
}

func (s *Snapshot) Hooks() hooks.Set {
	if s == nil {
		return hooks.Set{}
	}
	return hooks.Set{Declarations: append([]hooks.Declaration(nil), s.hooks.Declarations...)}
}

func (s *Snapshot) addRef() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.refs++
	return true
}

func (s *Snapshot) Release() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.refs > 0 {
		s.refs--
	}
	closeNow := s.refs == 0 && !s.closed
	if closeNow {
		s.closed = true
	}
	s.mu.Unlock()
	if closeNow {
		if s.mcp != nil {
			s.mcp.Close()
		}
		if s.onClose != nil {
			s.onClose()
		}
	}
}
