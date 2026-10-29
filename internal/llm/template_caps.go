package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// TemplateCaps records which thinking controls a llama.cpp chat template reads.
// The zero value means "not detected": the server receives no reasoning fields.
type TemplateCaps struct {
	EnableThinking   bool
	ReasoningEffort  bool
	PreserveThinking bool
}

// Any reports whether at least one thinking control was detected.
func (c TemplateCaps) Any() bool {
	return c.EnableThinking || c.ReasoningEffort || c.PreserveThinking
}

// ParseTemplateCaps detects template variables by substring search over the
// chat_template in a llama.cpp /props payload. It does not parse Jinja.
func ParseTemplateCaps(props []byte) TemplateCaps {
	var p struct {
		ChatTemplate string `json:"chat_template"`
	}
	if json.Unmarshal(props, &p) != nil {
		return TemplateCaps{}
	}
	return TemplateCaps{
		EnableThinking:   strings.Contains(p.ChatTemplate, "enable_thinking"),
		ReasoningEffort:  strings.Contains(p.ChatTemplate, "reasoning_effort"),
		PreserveThinking: strings.Contains(p.ChatTemplate, "preserve_thinking"),
	}
}

// DiscoverTemplateCaps reads <base without /v1>/props with a 2s timeout and the
// same redirect and size limits as DiscoverRuntimeCapacity.
func DiscoverTemplateCaps(ctx context.Context, client *http.Client, cred Credential) (TemplateCaps, error) {
	u, err := url.Parse(cred.BaseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || strings.TrimRight(u.Path, "/") != "/v1" {
		return TemplateCaps{}, ErrRuntimeCapacityUnavailable
	}
	u.Path, u.RawPath, u.Fragment, u.RawFragment, u.RawQuery = "/props", "", "", "", ""
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return TemplateCaps{}, ErrRuntimeCapacityUnavailable
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
	resp, err := bounded.Do(req)
	if err != nil {
		return TemplateCaps{}, ErrRuntimeCapacityUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return TemplateCaps{}, ErrRuntimeCapacityUnavailable
	}
	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return TemplateCaps{}, ErrRuntimeCapacityUnavailable
	}
	return ParseTemplateCaps(body), nil
}

// TemplateKwargs builds chat_template_kwargs for a level (off|low|medium|high).
// Off is enable_thinking=false; it is never expressed as reasoning_effort
// "none". High is sent as "xhigh". Fields the template does not read, and
// unknown levels, are omitted; an undetected server gets nil.
func TemplateKwargs(c TemplateCaps, level string) map[string]any {
	effort := ""
	switch level {
	case ReasoningOff:
	case ReasoningLow, ReasoningMedium:
		effort = level
	case ReasoningHigh:
		effort = "xhigh"
	default:
		return nil
	}
	out := map[string]any{}
	if c.EnableThinking {
		out["enable_thinking"] = level != ReasoningOff
	}
	if c.ReasoningEffort && effort != "" {
		out["reasoning_effort"] = effort
	}
	if c.PreserveThinking {
		out["preserve_thinking"] = false
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

var protectedBodyKeys = map[string]bool{"model": true, "messages": true, "stream": true, "tools": true}

// ApplyCustomKwargs overlays custom onto a copy of base, rejecting protected
// request keys.
func ApplyCustomKwargs(base, custom map[string]any) (map[string]any, error) {
	var bad []string
	for k := range custom {
		if protectedBodyKeys[k] {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, fmt.Errorf("custom override may not set protected key(s): %s", strings.Join(bad, ", "))
	}
	out := make(map[string]any, len(base)+len(custom))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range custom {
		out[k] = v
	}
	return out, nil
}
