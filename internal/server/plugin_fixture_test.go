package server_test

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	switch os.Getenv("UMCODE_PLUGIN_TEST_HELPER") {
	case "skill":
		input, _ := io.ReadAll(os.Stdin)
		_, _ = os.Stdout.Write([]byte("skill-ok:" + string(input)))
		return
	case "hook":
		var envelope struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(os.Stdin).Decode(&envelope)
		result := map[string]any{"version": 1, "continue": true}
		if envelope.Event == "TurnStart" {
			result["context"] = []string{"FULL_PLUGIN_HOOK_CONTEXT"}
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		return
	case "block-hook":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"version": 1, "block": true, "reason": "blocked by fixture"})
		return
	case "mcp":
		runPluginMCPFixture()
		return
	}
	os.Exit(m.Run())
}

func runPluginMCPFixture() {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			response["result"] = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
		case "tools/list":
			response["result"] = map[string]any{"tools": []any{map[string]any{"name": "ping", "description": "fixture ping", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}}}}
		case "tools/call":
			response["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": "mcp-ok"}}}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "not found"}
		}
		_ = encoder.Encode(response)
	}
}
