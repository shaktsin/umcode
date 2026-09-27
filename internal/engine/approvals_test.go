package engine

import (
	"encoding/json"
	"testing"
)

func TestApprovalSignatureKeepsShellCommandsExact(t *testing.T) {
	first := ApprovalSignature("shell.run", json.RawMessage(`{"command":"npm run build"}`))
	second := ApprovalSignature("shell.run", json.RawMessage(`{"command":"npm run deploy"}`))
	if first != "npm run build" || second != "npm run deploy" || first == second {
		t.Fatalf("remembered command signatures were not exact: %q, %q", first, second)
	}
}

func TestApprovalSignatureTrimsCommandWhitespaceAndKeepsExactFilePath(t *testing.T) {
	command := ApprovalSignature("shell.run", json.RawMessage(`{"command":"  npm test  "}`))
	path := ApprovalSignature("file.write", json.RawMessage(`{"path":"src/main.ts"}`))
	if command != "npm test" || path != "src/main.ts" {
		t.Fatalf("signatures = command %q, path %q", command, path)
	}
}

func TestIsComputerUseToolNeverRemembered(t *testing.T) {
	for _, tool := range []string{"computer.act", "computer.start", "computer.inspect", "computer.stop"} {
		if !isComputerUseTool(tool) {
			t.Fatalf("%s should be recognized as a Computer Use tool", tool)
		}
	}
	for _, tool := range []string{"shell.run", "file.write", "computerlike.thing"} {
		if isComputerUseTool(tool) {
			t.Fatalf("%s should not be treated as a Computer Use tool", tool)
		}
	}
}
