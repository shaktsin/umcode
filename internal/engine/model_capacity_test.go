package engine

import (
	"github.com/shaktsin/umcode/internal/protocol"
	"testing"
)

func TestCapacitySettingsRejectInvalidWindow(t *testing.T) {
	e, _, _, _ := taskRuntimeFixture(t, "summary_only", false)
	cred, err := e.Creds.Resolve(t.Context(), "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	params := protocol.ModelCapacityParams{CredentialID: cred.Record.ID, Model: "deterministic"}
	initial, err := e.ModelCapacity(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	p := initial.Profile
	zero := 0
	p.ContextWindow = &zero
	params.Profile = &p
	if _, err = e.SetModelCapacity(t.Context(), params); err == nil {
		t.Fatal("zero context accepted")
	}
	positive := 32768
	p.ContextWindow = &positive
	p.EndpointID = "forged"
	if _, err = e.SetModelCapacity(t.Context(), params); err == nil {
		t.Fatal("foreign endpoint profile accepted")
	}
	p = initial.Profile
	p.ContextWindow = &positive
	params.Profile = &p
	saved, err := e.SetModelCapacity(t.Context(), params)
	if err != nil || saved.Profile.ContextWindow == nil || *saved.Profile.ContextWindow != 32768 {
		t.Fatalf("valid override rejected: %+v %v", saved, err)
	}
}
