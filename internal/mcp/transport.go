// Package mcp connects to Model Context Protocol servers (stdio or Streamable
// HTTP) and exposes their tools to the agent as mcp_<server>_<tool>.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shaktsin/umcode/internal/config"
)

// rpcMessage is a JSON-RPC 2.0 message.
type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message) }

// transport sends requests and notifications to one server.
type transport interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	Notify(ctx context.Context, method string, params any) error
	Alive() bool
	Close() error
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	return json.Marshal(params)
}

// ---- stdio ----

type stdioTransport struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	wmu     sync.Mutex
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan rpcMessage
	done    chan struct{}
	err     error
	stderr  *ringBuffer
}

func startStdio(cfg config.MCPServerConfig) (*stdioTransport, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = serverEnv(cfg)
	if cfg.Cwd != "" {
		cmd.Dir = cfg.Cwd
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	t := &stdioTransport{cmd: cmd, stdin: stdin, pending: map[int64]chan rpcMessage{}, done: make(chan struct{}), stderr: newRing(8 << 10)}
	cmd.Stderr = t.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cfg.Command, err)
	}
	go t.read(stdout)
	go func() {
		err := cmd.Wait()
		t.mu.Lock()
		if t.err == nil {
			t.err = fmt.Errorf("server exited: %v; stderr: %s", err, strings.TrimSpace(t.stderr.String()))
		}
		t.mu.Unlock()
	}()
	return t, nil
}

// serverEnv is the environment for a stdio server: a minimal base (no engine
// secrets), forwarded env_vars, then the configured env.
func serverEnv(cfg config.MCPServerConfig) []string {
	env := map[string]string{}
	for _, k := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR", "SHELL", "TERM"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	env["PATH"] = env["PATH"] + ":/opt/homebrew/bin:/usr/local/bin"
	for _, k := range cfg.EnvVars {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	for k, v := range cfg.Env {
		if cfg.DisableHostEnvExpansion {
			env[k] = v
		} else {
			env[k] = os.ExpandEnv(v)
		}
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func (t *stdioTransport) read(r io.Reader) {
	defer func() {
		t.mu.Lock()
		if t.err == nil {
			t.err = errors.New("server closed its output")
		}
		for id, ch := range t.pending {
			close(ch)
			delete(t.pending, id)
		}
		t.mu.Unlock()
		close(t.done)
	}()
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var m rpcMessage
			if json.Unmarshal(line, &m) == nil {
				t.dispatch(m)
			}
		}
		if err != nil {
			return
		}
	}
}

func (t *stdioTransport) dispatch(m rpcMessage) {
	switch {
	case m.ID != nil && m.Method != "":
		// Server → client request. We support ping; everything else is declined.
		resp := rpcMessage{JSONRPC: "2.0", ID: m.ID}
		if m.Method == "ping" {
			resp.Result = json.RawMessage("{}")
		} else {
			resp.Error = &rpcError{Code: -32601, Message: "method not supported by client"}
		}
		_ = t.write(resp)
	case m.ID != nil:
		id, err := strconv.ParseInt(string(*m.ID), 10, 64)
		if err != nil {
			return
		}
		t.mu.Lock()
		ch := t.pending[id]
		delete(t.pending, id)
		t.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
}

func (t *stdioTransport) write(m rpcMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_, err = t.stdin.Write(append(b, '\n'))
	return err
}

func (t *stdioTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	id := t.nextID.Add(1)
	idRaw := json.RawMessage(strconv.FormatInt(id, 10))
	ch := make(chan rpcMessage, 1)
	t.mu.Lock()
	if t.err != nil {
		err := t.err
		t.mu.Unlock()
		return nil, err
	}
	t.pending[id] = ch
	t.mu.Unlock()
	if err := t.write(rpcMessage{JSONRPC: "2.0", ID: &idRaw, Method: method, Params: raw}); err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			t.mu.Lock()
			err := t.err
			t.mu.Unlock()
			return nil, err
		}
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		_ = t.write(rpcMessage{JSONRPC: "2.0", Method: "notifications/cancelled",
			Params: json.RawMessage(fmt.Sprintf(`{"requestId":%d,"reason":"timeout"}`, id))})
		return nil, ctx.Err()
	}
}

func (t *stdioTransport) Notify(_ context.Context, method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	return t.write(rpcMessage{JSONRPC: "2.0", Method: method, Params: raw})
}

func (t *stdioTransport) Alive() bool {
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}

func (t *stdioTransport) Close() error {
	t.stdin.Close()
	select {
	case <-t.done:
	case <-time.After(2 * time.Second):
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
	}
	return nil
}

// ringBuffer keeps the last n bytes written (server stderr, for error messages).
type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	n   int
}

func newRing(n int) *ringBuffer { return &ringBuffer{n: n} }

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.n {
		r.buf = r.buf[len(r.buf)-r.n:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

// ---- Streamable HTTP ----

type httpTransport struct {
	url      string
	headers  map[string]string
	client   *http.Client
	nextID   atomic.Int64
	mu       sync.Mutex
	session  string
	protoVer string
	closed   atomic.Bool
}

func newHTTP(cfg config.MCPServerConfig) *httpTransport {
	h := map[string]string{}
	for k, v := range cfg.HTTPHeaders {
		h[k] = v
	}
	for header, envName := range cfg.EnvHTTPHeaders {
		if v := os.Getenv(envName); v != "" {
			h[header] = v
		}
	}
	if cfg.BearerTokenEnvVar != "" {
		if _, set := h["Authorization"]; !set {
			if tok := os.Getenv(cfg.BearerTokenEnvVar); tok != "" {
				h["Authorization"] = "Bearer " + tok
			}
		}
	}
	return &httpTransport{url: cfg.URL, headers: h, client: &http.Client{}}
}

func (t *httpTransport) post(ctx context.Context, m rpcMessage) (*http.Response, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	t.mu.Lock()
	if t.session != "" {
		req.Header.Set("Mcp-Session-Id", t.session)
	}
	if t.protoVer != "" {
		req.Header.Set("MCP-Protocol-Version", t.protoVer)
	}
	t.mu.Unlock()
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.session = sid
		t.mu.Unlock()
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

func (t *httpTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	id := t.nextID.Add(1)
	idRaw := json.RawMessage(strconv.FormatInt(id, 10))
	resp, err := t.post(ctx, rpcMessage{JSONRPC: "2.0", ID: &idRaw, Method: method, Params: raw})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	match := func(m rpcMessage) (json.RawMessage, bool, error) {
		if m.ID == nil || string(*m.ID) != string(idRaw) {
			return nil, false, nil
		}
		if m.Error != nil {
			return nil, true, m.Error
		}
		return m.Result, true, nil
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		br := bufio.NewReaderSize(resp.Body, 1<<20)
		var data []string
		for {
			line, err := br.ReadString('\n')
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(line[5:]))
			} else if line == "" && len(data) > 0 {
				var m rpcMessage
				if json.Unmarshal([]byte(strings.Join(data, "\n")), &m) == nil {
					if res, ok, rerr := match(m); ok {
						return res, rerr
					}
				}
				data = nil
			}
			if err != nil {
				return nil, fmt.Errorf("event stream ended without a response to %s", method)
			}
		}
	}
	var m rpcMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", method, err)
	}
	res, ok, rerr := match(m)
	if !ok {
		return nil, fmt.Errorf("unexpected response to %s", method)
	}
	return res, rerr
}

func (t *httpTransport) Notify(ctx context.Context, method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	resp, err := t.post(ctx, rpcMessage{JSONRPC: "2.0", Method: method, Params: raw})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (t *httpTransport) Alive() bool { return !t.closed.Load() }

func (t *httpTransport) Close() error {
	t.closed.Store(true)
	t.mu.Lock()
	sid := t.session
	t.mu.Unlock()
	if sid != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil); err == nil {
			req.Header.Set("Mcp-Session-Id", sid)
			if resp, err := t.client.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}
	return nil
}
