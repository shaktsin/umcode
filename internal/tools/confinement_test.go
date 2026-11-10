package tools

import (
	"os/exec"
	"testing"

	"github.com/shaktsin/umcode/internal/sandbox"
)

type fakeSandbox struct{}

func (fakeSandbox) Name() string                         { return "fake" }
func (fakeSandbox) Wrap(*exec.Cmd, sandbox.Policy) error { return nil }

func TestShellConfinementTable(t *testing.T) {
	var sb sandbox.Sandbox = fakeSandbox{}
	cases := []struct {
		name    string
		scope   *Scope
		vm      bool
		sandbox sandbox.Sandbox
		want    Confinement
	}{
		{"nil scope", nil, true, sb, ConfNone},
		{"vm on, available, sandbox", &Scope{UseCompute: true}, true, sb, ConfMicroVM},
		{"vm on, available, no sandbox", &Scope{UseCompute: true}, true, nil, ConfMicroVM},
		{"vm on, unavailable, sandbox present", &Scope{UseCompute: true}, false, sb, ConfNone},
		{"vm on, unavailable, no sandbox", &Scope{UseCompute: true}, false, nil, ConfNone},
		{"vm off, sandbox", &Scope{}, false, sb, ConfOSSandbox},
		{"vm off, vm available, sandbox", &Scope{}, true, sb, ConfOSSandbox},
		{"vm off, no sandbox", &Scope{}, true, nil, ConfNone},
	}
	for _, c := range cases {
		if got := ShellConfinement(c.scope, c.vm, c.sandbox); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	if ConfMicroVM != "microvm" || ConfOSSandbox != "os-sandbox" || ConfNone != "none" {
		t.Error("confinement strings changed")
	}
}
