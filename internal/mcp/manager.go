package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/version"
)

// ProtocolVersion is the MCP revision the client asks for; servers may answer
// with an older one, which is accepted.
const ProtocolVersion = "2025-06-18"

// Status values.
const (
	StatusDisabled = "disabled"
	StatusStarting = "starting"
	StatusReady    = "ready"
	StatusFailed   = "failed"
	StatusStopped  = "stopped"
)

// ServerInfo describes a server for clients (mcp/list).
type ServerInfo struct {
	Name          string   `json:"name"`
	Transport     string   `json:"transport"`
	Status        string   `json:"status"`
	Error         string   `json:"error,omitempty"`
	ServerName    string   `json:"serverName,omitempty"`
	ServerVersion string   `json:"serverVersion,omitempty"`
	Tools         []string `json:"tools"`
}

type toolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations struct {
		ReadOnlyHint    *bool `json:"readOnlyHint"`
		DestructiveHint *bool `json:"destructiveHint"`
	} `json:"annotations"`
}

type server struct {
	cfg       config.MCPServerConfig
	connectMu sync.Mutex
	mu        sync.Mutex
	status    string
	err       string
	t         transport
	tools     []toolDef
	info      struct{ name, version string }
}

// Manager owns all configured MCP servers.
type Manager struct {
	log     *slog.Logger
	mu      sync.RWMutex
	servers map[string]*server
	order   []string
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewManager builds a manager for the configured servers (not started yet).
func NewManager(cfgs []config.MCPServerConfig, log *slog.Logger) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{log: log, servers: map[string]*server{}, ctx: ctx, cancel: cancel}
	for _, c := range cfgs {
		s := &server{cfg: c, status: StatusStopped}
		if !c.IsEnabled() {
			s.status = StatusDisabled
		}
		m.servers[c.Name] = s
		m.order = append(m.order, c.Name)
	}
	return m
}

// Start connects to every enabled server in the background. With wait=true it
// blocks until each attempt finishes (used for servers marked required).
func (m *Manager) Start(wait bool) error {
	var wg sync.WaitGroup
	errs := make(chan error, len(m.order))
	for _, name := range m.order {
		s := m.servers[name]
		if s.status == StatusDisabled {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.connect(s); err != nil && s.cfg.Required {
				errs <- fmt.Errorf("required MCP server %s: %w", s.cfg.Name, err)
			}
		}()
	}
	if wait {
		wg.Wait()
		close(errs)
		for err := range errs {
			return err
		}
	}
	return nil
}

func (m *Manager) connect(s *server) error {
	// connectMu serialises connection attempts; s.mu only guards fields, so
	// Tools() and List() never wait on a slow server.
	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	s.mu.Lock()
	old := s.t
	s.t, s.status, s.err, s.tools = nil, StatusStarting, "", nil
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	timeout := time.Duration(s.cfg.StartupTimeoutSec * float64(time.Second))
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()
	var t transport
	fail := func(err error) error {
		if t != nil {
			_ = t.Close()
		}
		s.mu.Lock()
		s.status, s.err = StatusFailed, err.Error()
		s.mu.Unlock()
		m.log.Warn("MCP server failed", "server", s.cfg.Name, "err", err)
		return err
	}
	if s.cfg.Transport == "http" {
		t = newHTTP(s.cfg)
	} else {
		st, err := startStdio(s.cfg)
		if err != nil {
			return fail(err)
		}
		t = st
	}
	res, err := t.Call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "umcode", "version": version.Version},
	})
	if err != nil {
		if st, ok := t.(*stdioTransport); ok {
			if tail := strings.TrimSpace(st.stderr.String()); tail != "" {
				err = fmt.Errorf("initialize: %w (stderr: %s)", err, lastLine(tail))
			}
		}
		return fail(err)
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	_ = json.Unmarshal(res, &init)
	if ht, ok := t.(*httpTransport); ok {
		ht.mu.Lock()
		ht.protoVer = init.ProtocolVersion
		ht.mu.Unlock()
	}
	if err := t.Notify(ctx, "notifications/initialized", nil); err != nil {
		return fail(fmt.Errorf("initialized notification: %w", err))
	}
	var all []toolDef
	cursor := ""
	for page := 0; page < 50; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := t.Call(ctx, "tools/list", params)
		if err != nil {
			return fail(fmt.Errorf("tools/list: %w", err))
		}
		var list struct {
			Tools      []toolDef `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &list); err != nil {
			return fail(fmt.Errorf("tools/list: %w", err))
		}
		for _, td := range list.Tools {
			if allowed(s.cfg, td.Name) {
				all = append(all, td)
			}
		}
		if list.NextCursor == "" {
			break
		}
		cursor = list.NextCursor
	}
	s.mu.Lock()
	s.t, s.tools, s.status = t, all, StatusReady
	s.info.name, s.info.version = init.ServerInfo.Name, init.ServerInfo.Version
	s.mu.Unlock()
	m.log.Info("MCP server ready", "server", s.cfg.Name, "tools", len(all))
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func allowed(cfg config.MCPServerConfig, tool string) bool {
	if len(cfg.EnabledTools) > 0 {
		ok := false
		for _, p := range cfg.EnabledTools {
			if m, _ := path.Match(p, tool); m {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for _, p := range cfg.DisabledTools {
		if m, _ := path.Match(p, tool); m {
			return false
		}
	}
	return true
}

// Restart reconnects one server.
func (m *Manager) Restart(name string) error {
	m.mu.RLock()
	s, ok := m.servers[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown MCP server %q", name)
	}
	if !s.cfg.IsEnabled() {
		return fmt.Errorf("MCP server %q is disabled in config", name)
	}
	return m.connect(s)
}

// Close stops all servers.
func (m *Manager) Close() {
	m.cancel()
	for _, name := range m.order {
		s := m.servers[name]
		s.mu.Lock()
		if s.t != nil {
			_ = s.t.Close()
			s.t = nil
		}
		if s.status != StatusDisabled {
			s.status = StatusStopped
		}
		s.mu.Unlock()
	}
}

// List describes all servers.
func (m *Manager) List() []ServerInfo {
	var out []ServerInfo
	for _, name := range m.order {
		s := m.servers[name]
		s.mu.Lock()
		info := ServerInfo{Name: name, Transport: or(s.cfg.Transport, "stdio"), Status: s.status, Error: s.err,
			ServerName: s.info.name, ServerVersion: s.info.version, Tools: []string{}}
		for _, t := range s.tools {
			info.Tools = append(info.Tools, t.Name)
		}
		s.mu.Unlock()
		sort.Strings(info.Tools)
		out = append(out, info)
	}
	return out
}

func or(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

// Tools implements tools.Source: the tools of every ready server.
func (m *Manager) Tools() []tools.Tool {
	var out []tools.Tool
	for _, name := range m.order {
		s := m.servers[name]
		s.mu.Lock()
		if s.status == StatusReady {
			for _, td := range s.tools {
				out = append(out, &mcpTool{m: m, s: s, def: td})
			}
		}
		s.mu.Unlock()
	}
	return out
}

type mcpTool struct {
	m   *Manager
	s   *server
	def toolDef
}

func (t *mcpTool) Name() string { return "mcp_" + t.s.cfg.Name + "_" + t.def.Name }
func (t *mcpTool) Description() string {
	d := strings.TrimSpace(t.def.Description)
	if d == "" {
		d = t.def.Name
	}
	return "[MCP " + t.s.cfg.Name + "] " + d
}
func (t *mcpTool) Schema() json.RawMessage {
	if len(t.def.InputSchema) == 0 || string(t.def.InputSchema) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return t.def.InputSchema
}

// Assess uses the server's risk_level if set, else the tool's annotations:
// read-only → green, destructive → red, otherwise yellow.
func (t *mcpTool) Assess(args json.RawMessage) (tools.Risk, string) {
	summary := fmt.Sprintf("%s on %s %s", t.def.Name, t.s.cfg.Name, compact(args))
	if r, ok := tools.ParseRisk(t.s.cfg.RiskLevel); ok {
		return r, summary
	}
	a := t.def.Annotations
	switch {
	case a.DestructiveHint != nil && *a.DestructiveHint && !(a.ReadOnlyHint != nil && *a.ReadOnlyHint):
		return tools.RiskRed, summary
	case a.ReadOnlyHint != nil && *a.ReadOnlyHint:
		return tools.RiskGreen, summary
	}
	return tools.RiskYellow, summary
}

func compact(raw json.RawMessage) string {
	s := strings.Join(strings.Fields(string(raw)), " ")
	if len(s) > 200 {
		s = s[:197] + "..."
	}
	return s
}

func (t *mcpTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	s := t.s
	s.mu.Lock()
	tr, status := s.t, s.status
	s.mu.Unlock()
	if tr == nil || !tr.Alive() || status != StatusReady {
		// One reconnect attempt, e.g. after the server process exited.
		if err := t.m.connect(s); err != nil {
			return "", fmt.Errorf("MCP server %s is unavailable: %w", s.cfg.Name, err)
		}
		s.mu.Lock()
		tr = s.t
		s.mu.Unlock()
	}
	timeout := time.Duration(s.cfg.ToolTimeoutSec * float64(time.Second))
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	res, err := tr.Call(cctx, "tools/call", map[string]any{"name": t.def.Name, "arguments": args})
	if err != nil {
		return "", err
	}
	return formatResult(res)
}

// formatResult turns an MCP CallToolResult into text for the model.
func formatResult(res json.RawMessage) (string, error) {
	var r struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Data     string `json:"data"`
			Resource struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"resource"`
			URI  string `json:"uri"`
			Name string `json:"name"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return "", fmt.Errorf("bad tools/call result: %w", err)
	}
	var parts []string
	for _, c := range r.Content {
		switch c.Type {
		case "text":
			parts = append(parts, c.Text)
		case "image", "audio":
			parts = append(parts, fmt.Sprintf("[%s %s, %d bytes base64]", c.Type, c.MimeType, len(c.Data)))
		case "resource":
			if c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
			} else {
				parts = append(parts, "[resource "+c.Resource.URI+"]")
			}
		case "resource_link":
			parts = append(parts, fmt.Sprintf("[link %s %s]", c.Name, c.URI))
		}
	}
	if len(parts) == 0 && len(r.StructuredContent) > 0 {
		parts = append(parts, string(r.StructuredContent))
	}
	out := strings.Join(parts, "\n")
	if r.IsError {
		if out == "" {
			out = "tool reported an error"
		}
		return "", fmt.Errorf("%s", out)
	}
	return out, nil
}

func (t *mcpTool) SelectionMetadata() (string, string) {
	return "mcp:" + t.s.cfg.Name, "mcp:" + t.s.cfg.Name
}
