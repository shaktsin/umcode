package memory

import (
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
)

// PlacementInput must use a successfully checked projects.InstructionInventory
// snapshot. ScopePaths and Existing are canonical slash-separated relative paths.
type PlacementInput struct {
	ProjectRoot string
	ScopePaths  []string
	Existing    []string
}

type Placement struct {
	RelativePath string
	CreateRoot   bool
}

const (
	ReasonScopeInvalid                = "scope_invalid"
	ReasonInstructionInventoryInvalid = "instruction_inventory_invalid"
)

// ResolvePlacement selects an existing common ancestor instruction file, or the
// root instruction file (the only file eligible for creation). It performs no
// I/O; callers must check scope existence and inventory containment separately.
func ResolvePlacement(in PlacementInput) (Placement, Outcome) {
	if !filepath.IsAbs(in.ProjectRoot) {
		return Placement{}, Outcome{Status: protocol.MemoryOutcomeRejected, Reason: ReasonProjectRequired}
	}
	scopes := append([]string(nil), in.ScopePaths...)
	existing := append([]string(nil), in.Existing...)
	sort.Strings(scopes)
	sort.Strings(existing)
	for _, scope := range scopes {
		if !placementPath(scope) {
			return Placement{}, Outcome{Status: protocol.MemoryOutcomeRejected, Reason: ReasonScopeInvalid}
		}
	}
	rootExists := false
	for _, file := range existing {
		if !placementPath(file) || path.Base(file) != "UMCODE.md" {
			return Placement{}, Outcome{Status: protocol.MemoryOutcomeRejected, Reason: ReasonInstructionInventoryInvalid}
		}
		rootExists = rootExists || file == "UMCODE.md"
	}
	best, depth := "UMCODE.md", 0
	if len(scopes) > 0 {
		for _, file := range existing {
			dir := path.Dir(file)
			if dir == "." {
				continue
			}
			common := true
			for _, scope := range scopes {
				if scope != dir && !strings.HasPrefix(scope, dir+"/") {
					common = false
					break
				}
			}
			if d := strings.Count(dir, "/") + 1; common && d > depth {
				best, depth = file, d
			}
		}
	}
	return Placement{RelativePath: best, CreateRoot: best == "UMCODE.md" && !rootExists}, Outcome{}
}

func placementPath(p string) bool {
	if p == "" || strings.TrimSpace(p) != p || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "~") || strings.ContainsAny(p, "\\\x00") || len(p) > 1 && p[1] == ':' {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "AGENTS.md" || part == "CLAUDE.md" || part == "AGENT.md" {
			return false
		}
	}
	return true
}
