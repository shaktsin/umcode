// Package server exposes the engine over the UMCode protocol: newline-
// delimited JSON-RPC on a Unix socket, and the same messages as WebSocket text
// frames on loopback (bearer-token authenticated).
package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/shaktsin/umcode/internal/engine"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/version"
)

// Server serves the protocol.
type Server struct {
	eng      *engine.Engine
	log      *slog.Logger
	handlers map[string]handler
	nextID   atomic.Int64

	mu        sync.Mutex
	listeners []net.Listener
	httpSrv   *http.Server
	conns     map[*conn]struct{}
	token     string
}

// New returns a server for eng.
func New(eng *engine.Engine, log *slog.Logger) *Server {
	s := &Server{eng: eng, log: log, conns: map[*conn]struct{}{}}
	s.handlers = s.routes()
	return s
}

// ListenUnix serves on a Unix socket at path (mode 0600).
func (s *Server) ListenUnix(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		c.Close()
		return fmt.Errorf("another engine is already listening on %s", path)
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, ln)
	s.mu.Unlock()
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					s.log.Error("accept", "err", err)
				}
				return
			}
			go s.serveStream(nc)
		}
	}()
	s.log.Info("listening", "socket", path)
	return nil
}

// AllowedOrigins are the browser origins (host patterns) that may open the
// WebSocket, besides same-origin requests: the Mac app's webview (wails://)
// and local dev servers. The bearer token is still required.
var AllowedOrigins = []string{"wails", "wails.localhost", "localhost", "localhost:*", "127.0.0.1:*", "[::1]:*"}

// ListenWebSocket serves the protocol on ws://host:port/ws. The bearer token is
// written to tokenPath (0600) so local clients can read it.
func (s *Server) ListenWebSocket(host string, port int, tokenPath string) error {
	if port == 0 {
		return nil
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	s.token = hex.EncodeToString(b[:])
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tokenPath, []byte(s.token+"\n"), 0o600); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok\n") })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	s.mu.Lock()
	s.httpSrv = srv
	s.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("websocket server", "err", err)
		}
	}()
	s.log.Info("listening", "websocket", "ws://"+ln.Addr().String()+"/ws")
	return nil
}

// Close stops listeners and disconnects clients.
func (s *Server) Close() {
	s.mu.Lock()
	lns, srv := s.listeners, s.httpSrv
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, ln := range lns {
		ln.Close()
	}
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		srv.Shutdown(ctx)
		cancel()
	}
	for _, c := range conns {
		c.close()
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	if s.token == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: AllowedOrigins})
	if err != nil {
		return
	}
	ws.SetReadLimit(32 << 20)
	ctx := r.Context()
	c := s.newConn(func(b []byte) error {
		wctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return ws.Write(wctx, websocket.MessageText, b)
	}, func() { ws.Close(websocket.StatusNormalClosure, "bye") })
	defer s.dropConn(c)
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		s.handleMessage(c, data)
	}
}

func (s *Server) serveStream(nc net.Conn) {
	var wmu sync.Mutex
	c := s.newConn(func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err := nc.Write(append(b, '\n'))
		return err
	}, func() { nc.Close() })
	defer s.dropConn(c)
	r := bufio.NewReaderSize(nc, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			s.handleMessage(c, line)
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) newConn(write func([]byte) error, closeFn func()) *conn {
	c := &conn{id: fmt.Sprintf("client-%d", s.nextID.Add(1)), out: make(chan []byte, 2048), closeFn: closeFn,
		threads: map[string]bool{}, log: s.log}
	go c.writer(write)
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	return c
}

func (s *Server) dropConn(c *conn) {
	s.eng.Bus.Remove(c.id)
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	c.close()
}

func (s *Server) handleMessage(c *conn, data []byte) {
	var m protocol.Message
	if err := json.Unmarshal(data, &m); err != nil {
		c.send(protocol.NewError(nil, protocol.Errorf(protocol.CodeParseError, "invalid JSON")))
		return
	}
	if !m.IsRequest() {
		if m.IsNotification() {
			return // clients have no notifications for the engine yet
		}
		c.send(protocol.NewError(m.ID, protocol.Errorf(protocol.CodeInvalidRequest, "expected a request")))
		return
	}
	h, ok := s.handlers[m.Method]
	if !ok {
		c.send(protocol.NewError(m.ID, protocol.Errorf(protocol.CodeMethodNotFound, "unknown method %s", m.Method)))
		return
	}
	if m.Method != protocol.MethodInitialize && !c.initialized.Load() {
		c.send(protocol.NewError(m.ID, protocol.Errorf(protocol.CodeInvalidRequest, "call initialize first")))
		return
	}
	// Requests run concurrently so a slow call does not block the connection;
	// initialize runs inline so it completes before anything that follows it.
	run := func(f func()) { go f() }
	if m.Method == protocol.MethodInitialize {
		run = func(f func()) { f() }
	}
	run(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := h(ctx, c, m.Params)
		if err != nil {
			c.send(protocol.NewError(m.ID, toRPCError(err)))
			return
		}
		msg, merr := protocol.NewResult(m.ID, res)
		if merr != nil {
			c.send(protocol.NewError(m.ID, protocol.Errorf(protocol.CodeInternal, "encode result: %v", merr)))
			return
		}
		c.send(msg)
	})
}

func toRPCError(err error) *protocol.Error {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe
	}
	if errors.Is(err, store.ErrNotFound) {
		return protocol.Errorf(protocol.CodeNotFound, "%v", err)
	}
	return protocol.Errorf(protocol.CodeInternal, "%v", err)
}

// ---- connection ----

type conn struct {
	id          string
	out         chan []byte
	closeFn     func()
	closeOnce   sync.Once
	sendMu      sync.Mutex
	closed      atomic.Bool
	initialized atomic.Bool
	admin       atomic.Bool
	log         *slog.Logger

	mu      sync.RWMutex
	all     bool
	threads map[string]bool
}

func (c *conn) ID() string    { return c.id }
func (c *conn) IsAdmin() bool { return c.admin.Load() }
func (c *conn) Wants(threadID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.all || c.threads[threadID]
}

func (c *conn) subscribe(p protocol.SubscribeParams) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.All {
		c.all = true
	}
	for _, id := range p.ThreadIDs {
		c.threads[id] = true
	}
}

func (c *conn) follow(threadID string) {
	c.mu.Lock()
	c.threads[threadID] = true
	c.mu.Unlock()
}

func (c *conn) Notify(method string, params any) {
	m, err := protocol.NewNotification(method, params)
	if err != nil {
		return
	}
	c.send(m)
}

func (c *conn) send(m *protocol.Message) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.closed.Load() {
		return
	}
	select {
	case c.out <- b:
	default:
		c.log.Warn("client too slow; disconnecting", "client", c.id)
		go c.close()
	}
}

// writer delivers queued messages, then closes the transport once the queue
// is closed and drained.
func (c *conn) writer(write func([]byte) error) {
	failed := false
	for b := range c.out {
		if failed {
			continue
		}
		if err := write(b); err != nil {
			failed = true
			go c.close()
		}
	}
	c.closeFn()
}

func (c *conn) close() {
	c.closeOnce.Do(func() {
		c.sendMu.Lock()
		c.closed.Store(true)
		close(c.out)
		c.sendMu.Unlock()
	})
}

// engineVersion is reported by initialize.
func engineVersion() string { return version.Version }
