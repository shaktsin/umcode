package engine

import (
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestComputerUseRequiresEffectiveProjectOptIn(t *testing.T) {
	project := &protocol.Project{Tools: protocol.ProjectTools{}}
	for _, tool := range []string{"shell.run", "verification.run", "browser.verify", "visual.start", "file.write", "web.search"} {
		if !toolAllowed(tool, project) {
			t.Errorf("first-party tool %s was hidden by project settings", tool)
		}
		if !toolAllowed(tool, nil) {
			t.Errorf("first-party tool %s was hidden in a chat without a project", tool)
		}
	}
	if toolAllowed("computer.act", project) || toolAllowed("computer.act", nil) {
		t.Fatal("Computer Use must be hidden unless enabled for the project")
	}
	enabled := true
	project.Tools.ComputerUse = &enabled
	if !toolAllowed("computer.act", project) {
		t.Fatal("project override did not enable Computer Use")
	}
}

func TestProjectMCPAllowlistStillApplies(t *testing.T) {
	project := &protocol.Project{Tools: protocol.ProjectTools{MCPServers: []string{"allowed"}}}
	if !toolAllowed("mcp_allowed_search", project) {
		t.Fatal("project MCP allowlist did not permit its selected server")
	}
	if toolAllowed("mcp_blocked_search", project) {
		t.Fatal("project MCP allowlist did not block an unselected server")
	}
}
