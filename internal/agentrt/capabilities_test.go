package agentrt

import (
	"reflect"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

var (
	allFiles  = []protocol.FileAccess{protocol.FilesNone, protocol.FilesRead, protocol.FilesReadWrite}
	allShells = []protocol.ShellAccess{protocol.ShellNone, protocol.ShellReadOnly, protocol.ShellReadWrite}
)

func allCaps(files []protocol.FileAccess) []protocol.AgentCapabilities {
	var out []protocol.AgentCapabilities
	for _, f := range files {
		for _, s := range allShells {
			for _, n := range []bool{false, true} {
				for _, m := range []int{0, 2} {
					out = append(out, protocol.AgentCapabilities{Files: f, Shell: s, Network: n, SpawnMax: m})
				}
			}
		}
	}
	return out
}

func TestResolveNeverExceedsParent(t *testing.T) {
	caps := allCaps(allFiles)
	requests := append(allCaps(allFiles),
		protocol.AgentCapabilities{Files: "root", Shell: "", Network: true, SpawnMax: 2},
		protocol.AgentCapabilities{Files: "", Shell: "admin", Network: true, SpawnMax: 5},
	)
	for _, parent := range caps {
		for _, entry := range caps {
			for _, req := range requests {
				g, _ := Resolve(parent, entry, req, ResolveOptions{ShellReadOnlySupported: true})
				if !Within(g, parent) || !Within(g, entry) {
					t.Fatalf("widened: parent=%+v entry=%+v req=%+v granted=%+v", parent, entry, req, g)
				}
				if filesRank(g.Files) > filesRank(req.Files) || shellRank(g.Shell) > shellRank(req.Shell) ||
					(g.Network && !req.Network) || g.SpawnMax > req.SpawnMax {
					t.Fatalf("exceeds request: req=%+v granted=%+v", req, g)
				}
			}
		}
	}
}

func hasRejection(rs []protocol.CapabilityRejection, field string) *protocol.CapabilityRejection {
	for i := range rs {
		if rs[i].Field == field {
			return &rs[i]
		}
	}
	return nil
}

func TestResolveReportsDroppedNetwork(t *testing.T) {
	parent := protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone, Network: false}
	req := protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone, Network: true}
	g, rs := Resolve(parent, RootCapabilities(), req, ResolveOptions{ShellReadOnlySupported: true})
	if g.Network {
		t.Fatal("network granted")
	}
	if hasRejection(rs, "capabilities.network") == nil {
		t.Fatalf("missing network rejection: %+v", rs)
	}
}

func TestResolveDowngradesReadOnlyShellWhenUnsupported(t *testing.T) {
	c := protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellReadOnly}
	g, rs := Resolve(c, c, c, ResolveOptions{ShellReadOnlySupported: false})
	if g.Shell != protocol.ShellNone {
		t.Fatalf("shell = %q", g.Shell)
	}
	if hasRejection(rs, "capabilities.shell") == nil {
		t.Fatalf("missing shell rejection: %+v", rs)
	}
	g, rs = Resolve(c, c, c, ResolveOptions{ShellReadOnlySupported: true})
	if g.Shell != protocol.ShellReadOnly || len(rs) != 0 {
		t.Fatalf("got %+v %+v", g, rs)
	}
}

func TestResolveTreatsUnknownStringsAsNone(t *testing.T) {
	req := protocol.AgentCapabilities{Files: "root", Shell: ""}
	g, rs := Resolve(RootCapabilities(), RootCapabilities(), req, ResolveOptions{ShellReadOnlySupported: true})
	if g.Files != protocol.FilesNone || g.Shell != protocol.ShellNone {
		t.Fatalf("granted %+v", g)
	}
	for _, f := range []string{"capabilities.files", "capabilities.shell"} {
		r := hasRejection(rs, f)
		if r == nil || !strings.Contains(r.Reason, "unknown value") {
			t.Fatalf("%s rejection = %+v", f, r)
		}
	}
}

func TestAllowedToolsFollowCapabilities(t *testing.T) {
	got := AllowedTools(protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone})
	want := []string{"file.list", "file.read", "file.search", "skill.get_instructions"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	got = AllowedTools(protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadOnly, SpawnMax: 1})
	want = []string{"agent.cancel", "agent.list", "agent.message", "agent.spawn", "agent.status", "agent.wait",
		"file.edit", "file.list", "file.read", "file.search", "file.write", "shell.run", "skill.get_instructions", "verification.run"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	for _, tool := range AllowedTools(protocol.AgentCapabilities{Files: protocol.FilesNone}) {
		if strings.HasPrefix(tool, "file.") {
			t.Fatalf("unexpected %s", tool)
		}
	}
}
