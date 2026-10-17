package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/plugins"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/skills"
)

// Actual global installs can share a package name. Their picker commands must
// retain installed package identity even after namespaces become source-qualified.
func TestDelegationDuplicateGlobalSkillIdentity(t *testing.T) {
	e, th, _, p := delegationFixture(t)
	for _, pkg := range []string{"superpowers", "superpowers", "other-superpowers"} {
		root := t.TempDir()
		dir := filepath.Join(root, "skills", "subagent-driven-development")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"` + pkg + `","version":"1.0.0"}`
		if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0600); err != nil {
			t.Fatal(err)
		}
		// Skill text cannot forge trusted package provenance from another manifest.
		body := "---\nname: subagent-driven-development\ndescription: Bounded delegated development\npackageName: superpowers\noriginalName: subagent-driven-development\n---\nUse a bounded implementer and independent review."
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		inspected, err := e.Installer.Inspect(t.Context(), plugins.InspectRequest{Source: root, Mode: plugins.InstallManaged})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = e.Installer.Install(t.Context(), inspected.Token, ""); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := e.Plugins.Acquire(t.Context(), th.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := skills.WithSnapshot(t.Context(), snapshot.SkillSnapshot())
	var qualified []string
	var unrelated string
	for _, skill := range e.Skills.ListContext(ctx) {
		if strings.HasSuffix(skill.Name, ".superpowers:subagent-driven-development") {
			qualified = append(qualified, skill.Name)
		}
		if skill.Name == "other-superpowers:subagent-driven-development" {
			unrelated = skill.Name
		}
	}
	snapshot.Release()
	if len(qualified) != 2 || unrelated == "" {
		t.Fatalf("real picker IDs missing: %v / %s", qualified, unrelated)
	}
	for _, text := range []string{
		"/" + unrelated + " implement",
		"/uninstalled.superpowers:subagent-driven-development implement",
		"/subagent-driven-development implement",
		"/superpowers:subagent-driven-development implement", // canonical no longer resolves with duplicate sources
		"Mention /" + qualified[0] + " in prose",
	} {
		delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: text})
		if p.last() != "" {
			t.Fatalf("untrusted suffix/prose/unresolved identity activated: %s", text)
		}
	}
	for _, name := range qualified {
		parent, err := e.StartThread(t.Context(), protocol.ThreadStartParams{ProjectID: th.ProjectID, Title: name})
		if err != nil {
			t.Fatal(err)
		}
		tr := delegationTurn(t, e, protocol.TurnStartParams{ThreadID: parent.ID, Text: "/" + name + " implement"})
		task, err := e.Store.AgentTaskForRootTurn(t.Context(), parent.ID, tr.ID)
		if err != nil || task.Activation != protocol.AgentActivationInstalledSkill || p.last() != task.ID {
			t.Errorf("actual installed picker identity failed activation %s: %+v %v", name, task, err)
		}
	}
}
