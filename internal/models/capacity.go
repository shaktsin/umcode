package models

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
)

type Capacity struct {
	Window     int
	MaxOutput  int
	Source     string
	ObservedAt time.Time
	EndpointID string
}

// EndpointIdentity is opaque: authentication/query values can distinguish
// endpoints but never appear in diagnostics or settings responses.
func EndpointIdentity(provider, baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("model endpoint must be an absolute HTTP or HTTPS URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	u.RawQuery = u.Query().Encode()
	u.Fragment = ""
	u.RawFragment = ""
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(provider)) + "\x00" + u.String()))
	return hex.EncodeToString(sum[:]), nil
}
