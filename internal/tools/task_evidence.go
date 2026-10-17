package tools

// Only these built-in implementations provide engine-verifiable result contracts.
func IsVerificationSource(tool Tool) bool {
	switch tool.(type) {
	case *verificationRun, *browserVerify:
		return true
	}
	return false
}
func IsAgentControlSource(tool Tool) bool {
	switch tool.(type) {
	case *agentTool, *agentWorkflowTool:
		return true
	}
	return false
}
