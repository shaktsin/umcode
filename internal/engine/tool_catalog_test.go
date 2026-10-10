package engine

import (
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
	"testing"
)

func TestPermittedCatalogScope(t *testing.T) {
	e, _, _, _ := pluginHookEngine(t)
	static := &engineTestTool{name: "file.read", risk: tools.RiskGreen}
	e.Tools.Add(static)
	e.Tools.Add(&engineTestTool{name: "computer.act", risk: tools.RiskGreen})
	e.Tools.Add(&engineTestTool{name: "mcp_blocked_search", risk: tools.RiskGreen})
	snapshot := &fakePluginSnapshot{tool: &engineTestTool{name: "file.read", risk: tools.RiskRed}}
	p := &protocol.Project{Tools: protocol.ProjectTools{MCPServers: []string{"allowed"}}}
	actual, catalog, err := e.permittedTurnCatalog(t.Context(), snapshot, p)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range actual {
		if c.tool.Name() == "file.read" {
			found = true
			if c.tool != static {
				t.Fatal("static precedence lost")
			}
		}
		if c.tool.Name() == "computer.act" || c.tool.Name() == "mcp_blocked_search" {
			t.Fatal("disabled capability exposed")
		}
	}
	if !found || len(catalog.Entries) == 0 {
		t.Fatal("permitted catalog empty")
	}
}

type catalogMetadataTool struct {
	*engineTestTool
	family, origin string
}

func (t *catalogMetadataTool) SelectionMetadata() (string, string) { return t.family, t.origin }
func TestPermittedCatalogExactServerIdentity(t *testing.T) {
	e, _, _, _ := pluginHookEngine(t)
	e.Cfg.MCPServers = []config.MCPServerConfig{{Name: "allowed_server"}}
	e.Tools.Add(&catalogMetadataTool{engineTestTool: &engineTestTool{name: "mcp_allowed_server_lookup", risk: tools.RiskGreen}, family: "mcp:allowed_server", origin: "mcp:allowed_server"})
	for _, allowed := range []string{"allowed", "allowed_server"} {
		p := &protocol.Project{Tools: protocol.ProjectTools{MCPServers: []string{allowed}}}
		actual, catalog, err := e.permittedTurnCatalog(t.Context(), nil, p)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range actual {
			if c.tool.Name() == "mcp_allowed_server_lookup" {
				found = true
			}
		}
		if found != (allowed == "allowed_server") {
			t.Fatal("server identity split or broadened")
		}
		for _, c := range catalog.Entries {
			if c.CanonicalName == "mcp_allowed_server_lookup" && c.Family != "mcp:allowed_server" {
				t.Fatal(c)
			}
		}
	}
}
