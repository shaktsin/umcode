// Package client is a Go client for the UMCode engine protocol over the
// Unix socket. The CLI uses it; so can tests and other Go programs.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/version"
)

// Notification is an engine → client notification.
type Notification struct {
	Method string
	Params json.RawMessage
}

// Client is a connected protocol client.
type Client struct {
	nc      net.Conn
	wmu     sync.Mutex
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan *protocol.Message
	notes   chan Notification
	done    chan struct{}
	err     error
	// ClientID is assigned by the engine at initialize.
	ClientID string
}

// Dial connects to the engine socket and initializes the session.
func Dial(ctx context.Context, socketPath, name string, admin bool) (*Client, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("engine not reachable at %s (is `umcode engine` running?): %w", socketPath, err)
	}
	c := &Client{nc: nc, pending: map[int64]chan *protocol.Message{}, notes: make(chan Notification, 4096), done: make(chan struct{})}
	go c.read()
	var res protocol.InitializeResult
	if err := c.Call(ctx, protocol.MethodInitialize, protocol.InitializeParams{
		ClientName: name, ClientVersion: version.Version, ProtocolVersion: protocol.Version, Admin: admin,
	}, &res); err != nil {
		nc.Close()
		return nil, err
	}
	c.ClientID = res.ClientID
	return c, nil
}

// Notifications returns the notification stream. It is closed when the connection ends.
func (c *Client) Notifications() <-chan Notification { return c.notes }

// Close closes the connection.
func (c *Client) Close() error { return c.nc.Close() }

// Call sends a request and decodes the result into out (which may be nil).
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	id := c.nextID.Add(1)
	msg, err := protocol.NewRequest(id, method, params)
	if err != nil {
		return err
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ch := make(chan *protocol.Message, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	c.wmu.Lock()
	_, err = c.nc.Write(append(b, '\n'))
	c.wmu.Unlock()
	if err != nil {
		return err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if out != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	case <-c.done:
		if c.err != nil {
			return c.err
		}
		return errors.New("connection closed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) read() {
	defer close(c.done)
	defer close(c.notes)
	r := bufio.NewReaderSize(c.nc, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var m protocol.Message
			if json.Unmarshal(line, &m) == nil {
				switch {
				case m.IsNotification():
					select {
					case c.notes <- Notification{Method: m.Method, Params: m.Params}:
					default: // drop if the consumer is not reading
					}
				case m.ID != nil:
					id, _ := strconv.ParseInt(string(*m.ID), 10, 64)
					c.mu.Lock()
					ch := c.pending[id]
					c.mu.Unlock()
					if ch != nil {
						ch <- &m
					}
				}
			}
		}
		if err != nil {
			c.err = err
			return
		}
	}
}
