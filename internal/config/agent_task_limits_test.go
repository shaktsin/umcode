package config

import "testing"

func TestAgentTaskLimitsDefaultToUnlimited(t *testing.T) {
	c := Default(t.TempDir())
	if c.Models.AgentTaskLimits != (AgentTaskLimits{}) {
		t.Fatalf("default agent task limits = %+v, want none", c.Models.AgentTaskLimits)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentTaskLimitsValidation(t *testing.T) {
	for name, limits := range map[string]AgentTaskLimits{
		"cap set":         {MaxDurationMinutes: 600, MaxTokens: 50_000_000, MaxCostUSD: 500},
		"explicit zeroes": {},
	} {
		c := Default(t.TempDir())
		c.Models.AgentTaskLimits = limits
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, limits := range map[string]AgentTaskLimits{
		"negative minutes": {MaxDurationMinutes: -1},
		"tiny tokens":      {MaxTokens: 5},
		"negative cost":    {MaxCostUSD: -2},
	} {
		c := Default(t.TempDir())
		c.Models.AgentTaskLimits = limits
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
