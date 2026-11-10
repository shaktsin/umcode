package tools

// Only these built-in implementations provide engine-verifiable result contracts.
func IsVerificationSource(tool Tool) bool {
	switch tool.(type) {
	case *verificationRun, *browserVerify:
		return true
	}
	return false
}
// IsAgentControlSource reports the engine-owned agent runtime tools, whose
// succeeded calls are engine control actions rather than model claims.
func IsAgentControlSource(tool Tool) bool {
	switch tool.(type) {
	case *runtimeAgentTool, *runtimeIntegrateTool, *agentSuggestTool, *agentProposePlanTool:
		return true
	}
	return false
}
