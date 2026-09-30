//go:build darwin

package main

/*
#cgo CFLAGS: -fblocks -x objective-c -mmacosx-version-min=14.0
#cgo LDFLAGS: -framework AppKit -framework ApplicationServices -framework CoreGraphics -framework ImageIO
#include <stdlib.h>
char *umcComputerUse(const char *command, const char *input, char **error);
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/shaktsin/umcode/internal/config"
)

const computerUseSocketName = "computer-use.sock"

type computerUseHost struct {
	path     string
	listener net.Listener
	server   *http.Server
}

func startComputerUseHost() (*computerUseHost, error) {
	home, err := config.HomeDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	socket := filepath.Join(home, computerUseSocketName)
	if st, err := os.Lstat(socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("Computer Use socket path is occupied by a non-socket file: %s", socket)
		}
		if conn, dialErr := net.DialTimeout("unix", socket, 150*time.Millisecond); dialErr == nil {
			_ = conn.Close()
			return nil, errors.New("another UMCode Computer Use host is already listening")
		}
		if err := os.Remove(socket); err != nil {
			return nil, fmt.Errorf("remove stale Computer Use socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("listen for Computer Use requests: %w", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(socket)
		return nil, fmt.Errorf("secure Computer Use socket: %w", err)
	}
	host := &computerUseHost{path: socket, listener: listener}
	host.server = &http.Server{Handler: http.HandlerFunc(host.serveHTTP), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = host.server.Serve(listener) }()
	return host, nil
}

func (h *computerUseHost) Close() {
	if h == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = h.server.Shutdown(ctx)
	_ = os.Remove(h.path)
}

func (h *computerUseHost) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/v1/") {
		http.NotFound(w, r)
		return
	}
	command := strings.TrimPrefix(r.URL.Path, "/v1/")
	if command != "list" && command != "inspect" && command != "act" {
		http.NotFound(w, r)
		return
	}
	input, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid Computer Use request"})
		return
	}
	output, err := inProcessComputerUse(command, input)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(output)
}

func inProcessComputerUse(command string, input []byte) ([]byte, error) {
	cmd := C.CString(command)
	defer C.free(unsafe.Pointer(cmd))
	data := C.CString(string(input))
	defer C.free(unsafe.Pointer(data))
	var message *C.char
	output := C.umcComputerUse(cmd, data, &message)
	if message != nil {
		defer C.free(unsafe.Pointer(message))
		return nil, errors.New(C.GoString(message))
	}
	if output == nil {
		return nil, errors.New("UMCode could not complete the Computer Use request")
	}
	defer C.free(unsafe.Pointer(output))
	result := []byte(C.GoString(output))
	if !json.Valid(result) {
		return nil, errors.New("UMCode returned an invalid Computer Use response")
	}
	return result, nil
}
