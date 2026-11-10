package server_test

import (
	"github.com/shaktsin/umcode/internal/protocol"
	"strings"
	"testing"
)

func TestModelCapacitySettingsRPC(t *testing.T) {
	h := newHarness(t, nil)
	key := h.addKey("claude", "capacity", "test")
	params := protocol.ModelCapacityParams{CredentialID: key.ID, Model: "claude-sonnet-5"}
	var result protocol.ModelCapacityResult
	h.call(protocol.MethodModelCapacityGet, params, &result)
	if len(result.Profile.EndpointID) != 64 || strings.Contains(result.Profile.EndpointID, "http") {
		t.Fatal("endpoint identity was exposed")
	}
	n := 32768
	result.Profile.ContextWindow = &n
	params.Profile = &result.Profile
	h.call(protocol.MethodModelCapacitySet, params, &result)
	if result.Profile.ContextWindow == nil || *result.Profile.ContextWindow != 32768 {
		t.Fatal("capacity not persisted")
	}
	params.Profile.EndpointID = "unrelated"
	if err := h.callErr(protocol.MethodModelCapacitySet, params); err == nil {
		t.Fatal("capacity endpoint binding could be forged")
	}
}
