package plugins

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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

func TestMain(m *testing.M) {
	if os.Getenv("UMCODE_PLUGIN_MCP_HELPER") == "hang" {
		_ = os.WriteFile(os.Getenv("PID_PATH"), []byte(strconv.Itoa(os.Getpid())), 0o600)
		_, _ = io.Copy(io.Discard, bufio.NewReader(os.Stdin))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestSnapshotIncludesNamespacedMCPTools(t *testing.T) {
	ctx := context.Background()
	server := pluginMCPServer(t, "v1")
	defer server.Close()
	manager, _, _, _ := pluginTestManager(t, ctx, writeManagerPlugin(t, "1.0.0", server.URL, false))
	defer manager.Close()
	snapshot, err := manager.Acquire(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	tool, ok := snapshot.Tool("mcp_sample-plugin__echo_echo")
	if !ok {
		t.Fatalf("snapshot tools = %v", toolNames(snapshot.Tools()))
	}
	out, err := tool.Call(ctx, json.RawMessage(`{"message":"hello"}`))
	if err != nil || out != "v1" {
		t.Fatalf("tool call = %q, %v", out, err)
	}
}

func TestSnapshotReferenceKeepsOldVersionAlive(t *testing.T) {
	ctx := context.Background()
	v1 := pluginMCPServer(t, "v1")
	defer v1.Close()
	manager, st, _, installation := pluginTestManager(t, ctx, writeManagerPlugin(t, "1.0.0", v1.URL, false))
	defer manager.Close()
	oldSnapshot, err := manager.Acquire(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	oldTool, _ := oldSnapshot.Tool("mcp_sample-plugin__echo_echo")

	v2 := pluginMCPServer(t, "v2")
	defer v2.Close()
	newRoot := writeManagerPlugin(t, "2.0.0", v2.URL, false)
	installation.Root = newRoot
	installation.Version = "2.0.0"
	installation.Digest, _ = digestTree(newRoot)
	if err := st.UpsertPluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate("project")
	newSnapshot, err := manager.Acquire(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	defer newSnapshot.Release()
	newTool, _ := newSnapshot.Tool("mcp_sample-plugin__echo_echo")
	if out, err := oldTool.Call(ctx, nil); err != nil || out != "v1" {
		t.Fatalf("old snapshot before release = %q, %v", out, err)
	}
	if out, err := newTool.Call(ctx, nil); err != nil || out != "v2" {
		t.Fatalf("new snapshot = %q, %v", out, err)
	}
	oldSnapshot.Release()
	if _, err := oldTool.Call(ctx, nil); err == nil {
		t.Fatal("retired MCP manager remained usable after final release")
	}
}

func TestDisableAffectsOnlyNewSnapshots(t *testing.T) {
	ctx := context.Background()
	server := pluginMCPServer(t, "enabled")
	defer server.Close()
	manager, st, project, installation := pluginTestManager(t, ctx, writeManagerPlugin(t, "1.0.0", server.URL, false))
	defer manager.Close()
	oldSnapshot, err := manager.Acquire(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer oldSnapshot.Release()
	oldTool, ok := oldSnapshot.Tool("mcp_sample-plugin__echo_echo")
	if !ok {
		t.Fatal("enabled snapshot missing tool")
	}
	if err := st.SetProjectPlugin(ctx, store.ProjectPlugin{ProjectID: project.ID, PluginID: installation.ID, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate(project.ID)
	newSnapshot, err := manager.Acquire(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer newSnapshot.Release()
	if len(newSnapshot.Tools()) != 0 {
		t.Fatalf("disabled snapshot tools = %v", toolNames(newSnapshot.Tools()))
	}
	if out, err := oldTool.Call(ctx, nil); err != nil || out != "enabled" {
		t.Fatalf("active old snapshot changed after disable: %q, %v", out, err)
	}
}

func TestActivationFailureRollsBackAndClosesProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process liveness assertion uses signal 0")
	}
	ctx := context.Background()
	server := pluginMCPServer(t, "stable")
	defer server.Close()
	oldRoot := writeManagerPlugin(t, "1.0.0", server.URL, false)
	manager, st, project, installation := pluginTestManager(t, ctx, oldRoot)
	defer manager.Close()
	stableSnapshot, err := manager.Acquire(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer stableSnapshot.Release()

	pidPath := filepath.Join(t.TempDir(), "pid")
	failingRoot := writeFailingManagerPlugin(t, pidPath)
	installation.Root = failingRoot
	installation.Version = "2.0.0"
	installation.Digest, _ = digestTree(failingRoot)
	if err := st.UpsertPluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate(project.ID)
	fallback, err := manager.Acquire(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer fallback.Release()
	tool, ok := fallback.Tool("mcp_sample-plugin__echo_echo")
	if !ok {
		t.Fatalf("fallback tools = %v", toolNames(fallback.Tools()))
	}
	if out, err := tool.Call(ctx, nil); err != nil || out != "stable" {
		t.Fatalf("fallback tool = %q, %v", out, err)
	}
	rolledBack, err := st.GetPluginInstallation(ctx, installation.ID)
	if err != nil || rolledBack.Root != oldRoot || rolledBack.Version != "1.0.0" {
		t.Fatalf("installation rollback = %#v, %v", rolledBack, err)
	}
	pidBytes, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("failed activation process %d is still alive", pid)
	}
}

func pluginTestManager(t *testing.T, ctx context.Context, root string) (*Manager, *store.Store, protocol.Project, store.PluginInstallation) {
	t.Helper()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	project, err := st.CreateProject(ctx, protocol.Project{ID: "project", Name: "project", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := digestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	installation := store.PluginInstallation{ID: "source/sample-plugin", Name: "sample-plugin", Version: "1.0.0", Format: "agent", Source: root, SourceID: "source", Mode: "linked", Root: root, Digest: digest, Active: true}
	if err := st.UpsertPluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectPlugin(ctx, store.ProjectPlugin{ProjectID: project.ID, PluginID: installation.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close(); st.Close() })
	return manager, st, project, installation
}

func writeManagerPlugin(t *testing.T, version, serverURL string, required bool) string {
	t.Helper()
	root := t.TempDir()
	writeInstallerManifest(t, root, version)
	requiredJSON := "false"
	if required {
		requiredJSON = "true"
	}
	writeInstallerFile(t, filepath.Join(root, "mcp.json"), fmt.Sprintf(`{"mcpServers":{"echo":{"type":"http","url":%q,"required":%s}}}`, serverURL, requiredJSON))
	return root
}

func writeFailingManagerPlugin(t *testing.T, pidPath string) string {
	t.Helper()
	root := t.TempDir()
	writeInstallerManifest(t, root, "2.0.0")
	writeInstallerFile(t, filepath.Join(root, "mcp.json"), fmt.Sprintf(`{"mcpServers":{"broken":{"type":"stdio","command":%q,"env":{"UMCODE_PLUGIN_MCP_HELPER":"hang","PID_PATH":%q},"required":true,"startup_timeout_sec":0.2}}}`, os.Args[0], pidPath))
	return root
}

func pluginMCPServer(t *testing.T, label string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		var message map[string]any
		_ = json.NewDecoder(request.Body).Decode(&message)
		id := message["id"]
		writer.Header().Set("Content-Type", "application/json")
		switch message["method"] {
		case "initialize":
			_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": label, "version": "1"}}})
		case "notifications/initialized":
			writer.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "Echo", "inputSchema": map[string]any{"type": "object"}}}}})
		case "tools/call":
			_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": label}}}})
		}
	}))
}

func toolNames(input []tools.Tool) []string {
	result := make([]string, 0, len(input))
	for _, tool := range input {
		result = append(result, tool.Name())
	}
	return result
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
