package engine

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
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

func TestToolAllowedWorkUpdateRegistration(t *testing.T) {
	var lists [][]string
	for _, enabled := range []bool{false, true} {
		cfg := config.Default(t.TempDir())
		cfg.Models.DesignedWorkflow = enabled
		st, err := store.Open(t.Context(), ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		e, err := New(t.Context(), Options{Config: cfg, Store: st, Secrets: secrets.NewFileStore(filepath.Join(cfg.Home, "secrets.json")), DisableScheduler: true, DisableMCP: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.Shutdown(context.Background()) })
		tool, exists := e.Tools.Get("work.update")
		if !exists || e.Work.DesignedWorkflow != enabled {
			t.Fatalf("enabled=%t registered=%t service flag=%t", enabled, exists, e.Work.DesignedWorkflow)
		}
		actual, _, err := e.permittedTurnCatalog(t.Context(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		visible := false
		for _, c := range actual {
			if c.tool.Name() == "work.update" {
				visible = true
			}
		}
		if visible != enabled {
			t.Fatalf("legacy enabled=%v visible=%v", enabled, visible)
		}
		var names []string
		for _, tool := range e.Tools.All() {
			if tool.Name() != "work.update" {
				names = append(names, tool.Name())
			}
		}
		lists = append(lists, names)
		if !enabled {
			continue
		}
		if !toolAllowed("work.update", nil) || !toolAllowed("work.update", &protocol.Project{}) {
			t.Fatal("internal graph tool hidden by project settings")
		}
		th, err := st.CreateThread(t.Context(), protocol.Thread{Title: "workflow"})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Work.Begin(t.Context(), th, "implement a small fix"); err != nil {
			t.Fatal(err)
		}
		w, ok, err := st.OpenWorkForThread(t.Context(), th.ID)
		if err != nil || !ok {
			t.Fatalf("open work: %v %v", ok, err)
		}
		_, err = tool.Call(tools.WithScope(t.Context(), &tools.Scope{ThreadID: th.ID}), []byte(`{"work_id":"`+w.ID+`","expected_revision":1,"workflow_depth":"guided","nodes":[{"ref":"task","kind":"task","title":"Fix"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if e.Work.Failures.Load() != 0 {
			t.Fatal("registered service rejected its own open work")
		}
	}
	if !reflect.DeepEqual(lists[0], lists[1]) {
		t.Fatalf("flag changed other tools: %v vs %v", lists[0], lists[1])
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
