package engine

import (
	"context"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/toolselect"
	"strings"
)

func (e *Engine) permittedTurnCatalog(ctx context.Context, snapshot pluginSnapshot, project *protocol.Project) ([]turnTool, toolselect.Catalog, error) {
	var actual []turnTool
	var entries []toolselect.Entry
	for _, c := range e.turnTools(snapshot) {
		if !e.permittedToolFor(ctx, c, project) {
			continue
		}
		if c.tool.Name() == "work.update" && (e.Cfg == nil || !e.optimizationPolicy(ctx).DesignedWorkflow) {
			continue
		}
		family, origin := catalogMetadata(c)
		c.spec = llm.ToolSpec{Name: tools.ToWire(c.tool.Name()), Description: toolDescription(ctx, c.tool), Schema: append([]byte(nil), c.tool.Schema()...)}
		actual = append(actual, c)
		entries = append(entries, toolselect.Entry{CanonicalName: c.tool.Name(), WireName: tools.ToWire(c.tool.Name()), Family: family, Origin: origin, Spec: c.spec})
	}
	catalog, err := toolselect.NewCatalog(entries)
	return actual, catalog, err
}

func catalogMetadata(c turnTool) (string, string) {
	if m, ok := c.tool.(tools.SelectionMetadata); ok {
		return m.SelectionMetadata()
	}
	if c.plugin {
		return "other", "plugin:snapshot"
	}
	n := c.tool.Name()
	switch {
	case strings.HasPrefix(n, "file."), strings.HasPrefix(n, "exec."), strings.HasPrefix(n, "skill."), n == "shell.run", n == "verification.plan", n == "verification.run", n == "work.update":
		return "core", "registry"
	case strings.HasPrefix(n, "web."):
		return "web", "registry"
	case strings.HasPrefix(n, "preview."), strings.HasPrefix(n, "visual."), n == "browser.verify":
		return "browser", "registry"
	case strings.HasPrefix(n, "computer."):
		return "computer", "registry"
	}
	return "other", "registry"
}

func catalogToolAllowed(c turnTool, p *protocol.Project) bool {
	// Plugin MCP policy is fixed by the acquired project snapshot. Computer use
	// still requires the same project opt-in as every other computer tool.
	if c.plugin && !strings.HasPrefix(c.tool.Name(), "computer.") {
		return true
	}
	if m, ok := c.tool.(tools.SelectionMetadata); ok {
		family, _ := m.SelectionMetadata()
		if strings.HasPrefix(family, "mcp:") && p != nil && p.Tools.MCPServers != nil {
			for _, name := range p.Tools.MCPServers {
				if name == strings.TrimPrefix(family, "mcp:") {
					return true
				}
			}
			return false
		}
	}
	return toolAllowed(c.tool.Name(), p)
}

func toolDescription(ctx context.Context, t tools.Tool) string {
	if contextual, ok := t.(interface{ DescriptionContext(context.Context) string }); ok {
		return contextual.DescriptionContext(ctx)
	}
	return t.Description()
}
