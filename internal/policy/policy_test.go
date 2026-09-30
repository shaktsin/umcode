package policy

import (
	"testing"

	"github.com/shaktsin/umcode/internal/tools"
)

func TestGate(t *testing.T) {
	g := New()
	cases := []struct {
		tool     string
		risk     tools.Risk
		listener bool
		mode     string
		want     Decision
	}{
		{"file.read", tools.RiskGreen, false, "", Allow},
		{"shell.run", tools.RiskRed, false, "", Ask},
		{"file.write", tools.RiskRed, false, "", Ask},
		{"file.write", tools.RiskRed, true, "", Ask},
		{"gmail.send", tools.RiskYellow, false, "", Allow},
		// auto_workspace auto-approves red workspace actions...
		{"shell.run", tools.RiskRed, false, ApprovalAutoWorkspace, Allow},
		// ...but never Computer Use, and never for a listener-triggered call.
		{"computer.act", tools.RiskRed, false, ApprovalAutoWorkspace, Ask},
		{"shell.run", tools.RiskRed, true, ApprovalAutoWorkspace, Ask},
		// auto_all also auto-approves Computer Use.
		{"computer.act", tools.RiskRed, false, ApprovalAutoAll, Allow},
		{"computer.act", tools.RiskRed, true, ApprovalAutoAll, Ask},
	}
	for _, c := range cases {
		if got, _ := g.Check(c.tool, c.risk, c.listener, c.mode); got != c.want {
			t.Errorf("%s/%s/%v/%s = %s, want %s", c.tool, c.risk, c.listener, c.mode, got, c.want)
		}
	}
}
