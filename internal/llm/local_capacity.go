package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type RuntimeCapacity struct {
	ContextWindow   int
	MaxOutputTokens int
}

// RuntimeCapacityProvider lets an adapter supply verified serving metadata.
// It is optional; catalog maximums are not a local runtime allocation.
type RuntimeCapacityProvider interface {
	RuntimeCapacity(context.Context, Credential, string) (RuntimeCapacity, error)
}

var ErrRuntimeCapacityUnavailable = errors.New("runtime capacity unavailable; configure the endpoint's context window in Model settings")

func DiscoverRuntimeCapacity(ctx context.Context, client *http.Client, cred Credential, backend, model string) (RuntimeCapacity, error) {
	if backend != "ollama" {
		return RuntimeCapacity{}, ErrRuntimeCapacityUnavailable
	}
	u, err := url.Parse(cred.BaseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || strings.TrimRight(u.Path, "/") != "/v1" {
		return RuntimeCapacity{}, ErrRuntimeCapacityUnavailable
	}
	u.Path = "/api/ps"
	u.RawPath = ""
	u.Fragment = ""
	u.RawFragment = ""
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return RuntimeCapacity{}, ErrRuntimeCapacityUnavailable
	}
	if cred.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cred.APIKey)
	}
	if client == nil {
		client = http.DefaultClient
	}
	bounded := *client
	priorRedirect := client.CheckRedirect
	bounded.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !strings.EqualFold(next.URL.Scheme, u.Scheme) || !strings.EqualFold(next.URL.Host, u.Host) {
			return ErrRuntimeCapacityUnavailable
		}
		if priorRedirect != nil {
			return priorRedirect(next, via)
		}
		return nil
	}
	response, err := bounded.Do(req)
	if err != nil {
		return RuntimeCapacity{}, ErrRuntimeCapacityUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return RuntimeCapacity{}, ErrRuntimeCapacityUnavailable
	}
	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return RuntimeCapacity{}, ErrRuntimeCapacityUnavailable
	}
	var payload struct {
		Models []struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return RuntimeCapacity{}, ErrRuntimeCapacityUnavailable
	}
	match := func(name string) bool {
		return name == model || (!strings.Contains(model, ":") && name == model+":latest")
	}
	for _, loaded := range payload.Models {
		if (match(loaded.Name) || match(loaded.Model)) && loaded.ContextLength > 0 && int64(loaded.ContextLength) <= 2147483647 {
			return RuntimeCapacity{ContextWindow: loaded.ContextLength}, nil
		}
	}
	return RuntimeCapacity{}, ErrRuntimeCapacityUnavailable
}
