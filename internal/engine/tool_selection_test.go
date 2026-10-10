package engine

import (
	"encoding/json"
	"github.com/shaktsin/umcode/internal/tools"
	"slices"
	"testing"
)

func TestSelectionRequiredExecOwnership(t *testing.T) {
	e, th, _, _ := pluginHookEngine(t)
	m := tools.NewExecManager()
	t.Cleanup(m.Close)
	e.Exec = m
	e.Cfg.Tools.HostSandbox = "off"
	tools.RegisterBuiltins(e.Tools, e.Cfg, tools.NewWorkspaces(e.Cfg), nil, tools.BuiltinServices{Exec: m})
	ctx := tools.WithScope(t.Context(), &tools.Scope{ThreadID: th.ID, AllowNet: true, Root: t.TempDir()})
	tool, ok := e.Tools.Get("exec.start")
	if !ok {
		t.Fatal("exec missing")
	}
	if _, err := tool.Call(ctx, json.RawMessage(`{"command":"sleep 30","yield_seconds":1}`)); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(e.requiredToolFamilies(th.ID), "core") || len(e.requiredToolFamilies("foreign")) != 0 {
		t.Fatal("required lifecycle ownership lost")
	}
}
