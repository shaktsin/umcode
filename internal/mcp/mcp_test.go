package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/tools"
)

// The test binary doubles as a fake stdio MCP server.
func TestMain(m *testing.M) {
	if os.Getenv("UMCODE_FAKE_MCP") == "1" {
		fakeStdioServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeStdioServer() {
	in := bufio.NewReader(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	pinged := false
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return
		}
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		method, _ := m["method"].(string)
		id := m["id"]
		if id == nil && method == "" {
			continue // response to our ping
		}
		reply := func(result any) { out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}) }
		switch method {
		case "initialize":
			fmt.Fprintln(os.Stderr, "fake server starting")
			reply(map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "fake", "version": "0.1"}, "capabilities": map[string]any{}})
		case "tools/list":
			params, _ := m["params"].(map[string]any)
			if params["cursor"] == nil {
				reply(map[string]any{"tools": []any{
					map[string]any{"name": "echo", "description": "Echo", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}},
				}, "nextCursor": "p2"})
			} else {
				reply(map[string]any{"tools": []any{
					map[string]any{"name": "delete", "description": "Delete things", "annotations": map[string]any{"destructiveHint": true}},
					map[string]any{"name": "secret", "description": "filtered"},
				}})
			}
		case "tools/call":
			if !pinged {
				pinged = true
				out.Encode(map[string]any{"jsonrpc": "2.0", "id": 999, "method": "ping"})
			}
			params, _ := m["params"].(map[string]any)
			if params["name"] == "delete" {
				reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": "nothing to delete"}}, "isError": true})
				continue
			}
			args, _ := json.Marshal(params["arguments"])
			reply(map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "echo " + string(args) + " token=" + os.Getenv("FAKE_TOKEN") + " leaked=" + os.Getenv("OPENAI_API_KEY")},
				map[string]any{"type": "image", "mimeType": "image/png", "data": "AAAA"},
			}})
		case "crash":
			os.Exit(3)
		default:
			if id != nil {
				out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "nope"}})
			}
		}
	}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func findTool(t *testing.T, m *Manager, name string) tools.Tool {
	t.Helper()
	for _, tl := range m.Tools() {
		if tl.Name() == name {
			return tl
		}
	}
	t.Fatalf("tool %s not found", name)
	return nil
}

func TestStdioServer(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-must-not-leak")
	t.Setenv("UMCODE_FAKE_MCP", "0")
	cfg := config.MCPServerConfig{Name: "fake", Command: os.Args[0],
		Env: map[string]string{"UMCODE_FAKE_MCP": "1", "FAKE_TOKEN": "t0k"}, DisabledTools: []string{"sec*"}}
	m := NewManager([]config.MCPServerConfig{cfg}, quietLog())
	defer m.Close()
	if err := m.Start(true); err != nil {
		t.Fatal(err)
	}
	info := m.List()[0]
	if info.Status != StatusReady || info.ServerName != "fake" || strings.Join(info.Tools, ",") != "delete,echo" {
		t.Fatalf("info = %+v", info)
	}
	echo := findTool(t, m, "mcp_fake_echo")
	if r, _ := echo.Assess(nil); r != tools.RiskGreen {
		t.Errorf("echo risk = %s", r)
	}
	if r, _ := findTool(t, m, "mcp_fake_delete").Assess(nil); r != tools.RiskRed {
		t.Errorf("delete risk = %s", r)
	}
	out, err := echo.Call(context.Background(), json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `echo {"x":1} token=t0k leaked=`+"\n") {
		t.Errorf("env not isolated or args lost: %q", out)
	}
	if !strings.Contains(out, "[image image/png") {
		t.Errorf("image not summarised: %q", out)
	}
	if _, err := findTool(t, m, "mcp_fake_delete").Call(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "nothing to delete") {
		t.Errorf("isError not surfaced: %v", err)
	}
	// Kill the server; the next call reconnects.
	s := m.servers["fake"]
	s.mu.Lock()
	st := s.t.(*stdioTransport)
	s.mu.Unlock()
	st.cmd.Process.Kill()
	<-st.done
	if _, err := echo.Call(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatalf("reconnect failed: %v", err)
	}
}

func TestStdioFailure(t *testing.T) {
	m := NewManager([]config.MCPServerConfig{{Name: "bad", Command: "/nonexistent/mcp", Required: true}}, quietLog())
	defer m.Close()
	if err := m.Start(true); err == nil {
		t.Fatal("required server failure should be reported")
	}
	if l := m.List(); l[0].Status != StatusFailed || l[0].Error == "" {
		t.Fatalf("list = %+v", l)
	}
	if len(m.Tools()) != 0 {
		t.Fatal("failed server should expose no tools")
	}
}

func TestHTTPServer(t *testing.T) {
	var sawAuth, sawSession, sawVersion bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			return
		}
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		sawAuth = sawAuth || r.Header.Get("Authorization") == "Bearer secret-token"
		id := m["id"]
		switch m["method"] {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"protocolVersion": "2025-03-26", "serverInfo": map[string]any{"name": "httpfake"}}})
		case "notifications/initialized":
			sawSession = r.Header.Get("Mcp-Session-Id") == "sess-1"
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			sawVersion = r.Header.Get("MCP-Protocol-Version") == "2025-03-26"
			w.Header().Set("Content-Type", "text/event-stream")
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"tools": []any{map[string]any{"name": "search", "description": "Search"}}}})
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\nevent: message\ndata: %s\n\n", b)
		case "tools/call":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "found 3"}}}})
		}
	}))
	defer srv.Close()
	t.Setenv("MY_TOKEN", "secret-token")
	m := NewManager([]config.MCPServerConfig{{Name: "web", Transport: "http", URL: srv.URL, BearerTokenEnvVar: "MY_TOKEN", RiskLevel: "green", StartupTimeoutSec: 5}}, quietLog())
	defer m.Close()
	if err := m.Start(true); err != nil {
		t.Fatal(err)
	}
	tl := findTool(t, m, "mcp_web_search")
	if r, _ := tl.Assess(nil); r != tools.RiskGreen {
		t.Errorf("risk override = %s", r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := tl.Call(ctx, json.RawMessage(`{"q":"x"}`))
	if err != nil || out != "found 3" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if !sawAuth || !sawSession || !sawVersion {
		t.Fatalf("auth=%v session=%v version=%v", sawAuth, sawSession, sawVersion)
	}
}

func TestServerEnvCanDisableHostExpansionForPluginValues(t *testing.T) {
	t.Setenv("ENGINE_SECRET", "host-secret")
	environment := serverEnv(config.MCPServerConfig{Env: map[string]string{"TOKEN": "$ENGINE_SECRET"}, DisableHostEnvExpansion: true})
	values := map[string]string{}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	if values["TOKEN"] != "$ENGINE_SECRET" {
		t.Fatalf("TOKEN = %q", values["TOKEN"])
	}
}
