package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	maxWebBody    = 1 << 20
	maxWebOutput  = 24 << 10
	maxWebResults = 8
)

var (
	resultAnchor  = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	resultSnippet = regexp.MustCompile(`(?is)<(?:a|div)[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</(?:a|div)>`)
	tagPattern    = regexp.MustCompile(`(?s)<[^>]*>`)
	blockedHTML   = regexp.MustCompile(`(?is)<(?:script|style|noscript|svg|iframe|form|nav|footer|header)\b[^>]*>.*?</(?:script|style|noscript|svg|iframe|form|nav|footer|header)\s*>`)
	blockHTML     = regexp.MustCompile(`(?i)</?(?:p|div|article|section|li|ul|ol|h[1-6]|br|tr|table|blockquote|pre)[^>]*>`)
	entityHTML    = regexp.MustCompile(`&(#x[0-9a-fA-F]+|#[0-9]+|amp|quot|apos|lt|gt);`)
	punctSpace    = regexp.MustCompile(`\s+([.,!?;:])`)
)

type webSearch struct {
	client   *http.Client
	endpoint string
}

func newWebSearch() *webSearch {
	return &webSearch{client: webClient(), endpoint: "https://html.duckduckgo.com/html/"}
}

func (*webSearch) Name() string { return "web.search" }
func (*webSearch) Description() string {
	return "Search the public web for current information. The query is sent to DuckDuckGo. Returns a short list of result titles, URLs, and snippets; use web.fetch to read and verify a result."
}
func (*webSearch) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"query":{"type":"string","description":"What to search for"},"max_results":{"type":"integer","description":"Maximum results, 1-8 (default 5)"}},"required":["query"]}`)
}
func (*webSearch) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct{ Query string }](args)
	return RiskGreen, "Search the web for " + clip(a.Query)
}
func (t *webSearch) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}](args)
	if err != nil {
		return "", err
	}
	a.Query = strings.TrimSpace(a.Query)
	if a.Query == "" {
		return "", errors.New("search query is required")
	}
	if len(a.Query) > 500 {
		return "", errors.New("search query is too long (maximum 500 characters)")
	}
	limit := a.MaxResults
	if limit <= 0 {
		limit = 5
	}
	if limit > maxWebResults {
		limit = maxWebResults
	}
	u, _ := url.Parse(t.endpoint)
	q := u.Query()
	q.Set("q", a.Query)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "UMCode/1.0 (web search)")
	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web search failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("web search returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWebBody))
	if err != nil {
		return "", fmt.Errorf("read search results: %w", err)
	}
	results := parseSearchResults(string(body), limit)
	if len(results) == 0 {
		return "No web results were returned. Try a different search query.", nil
	}
	out, err := json.Marshal(results)
	return string(out), err
}

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

func parseSearchResults(body string, limit int) []searchResult {
	anchors := resultAnchor.FindAllStringSubmatch(body, -1)
	snippets := resultSnippet.FindAllStringSubmatch(body, -1)
	results := make([]searchResult, 0, min(len(anchors), limit))
	seen := map[string]bool{}
	for i, a := range anchors {
		if len(results) >= limit {
			break
		}
		link := htmlText(a[1])
		if parsed, err := url.Parse(link); err == nil && parsed.Host == "duckduckgo.com" && parsed.Path == "/l/" {
			link = parsed.Query().Get("uddg")
		}
		parsed, err := url.Parse(link)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || seen[parsed.String()] {
			continue
		}
		seen[parsed.String()] = true
		item := searchResult{Title: htmlText(a[2]), URL: parsed.String()}
		if i < len(snippets) {
			item.Snippet = htmlText(snippets[i][1])
		}
		results = append(results, item)
	}
	return results
}

type webFetch struct{ client *http.Client }

func newWebFetch() *webFetch   { return &webFetch{client: webClient()} }
func (*webFetch) Name() string { return "web.fetch" }
func (*webFetch) Description() string {
	return "Fetch a public web page and extract its readable text. Only HTTP and HTTPS pages on public hosts are allowed. Use this to verify or cite information from search results."
}
func (*webFetch) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{"url":{"type":"string","description":"Public HTTP or HTTPS URL to read"}},"required":["url"]}`)
}
func (*webFetch) Assess(args json.RawMessage) (Risk, string) {
	a, _ := decode[struct {
		URL string `json:"url"`
	}](args)
	return RiskGreen, "Read public web page " + clip(a.URL)
}
func (t *webFetch) Call(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decode[struct {
		URL string `json:"url"`
	}](args)
	if err != nil {
		return "", err
	}
	u, err := validatePublicURL(ctx, a.URL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; UMCode/1.0)")
	req.Header.Set("Accept", "text/html, text/plain, application/xhtml+xml;q=0.9, */*;q=0.1")
	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch page: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("page returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxWebBody {
		return "", fmt.Errorf("page is larger than the %d-byte limit", maxWebBody)
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.Contains(contentType, "text/") && !strings.Contains(contentType, "html") && !strings.Contains(contentType, "xml") {
		return "", fmt.Errorf("unsupported page content type %q", contentType)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWebBody+1))
	if err != nil {
		return "", fmt.Errorf("read page: %w", err)
	}
	if len(body) > maxWebBody {
		body = body[:maxWebBody]
	}
	text := htmlText(string(body))
	if text == "" {
		return "", errors.New("page contained no readable text")
	}
	if len(text) > maxWebOutput {
		text = text[:maxWebOutput] + "\n… [page text truncated]"
	}
	return fmt.Sprintf("URL: %s\nContent-Type: %s\n\n%s", resp.Request.URL.String(), contentType, text), nil
}

func webClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialPublic
	return &http.Client{Timeout: 20 * time.Second, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("stopped after 5 redirects")
		}
		if _, err := validatePublicURL(req.Context(), req.URL.String()); err != nil {
			return err
		}
		return nil
	}}
}

func validatePublicURL(ctx context.Context, raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("a valid public HTTP or HTTPS URL is required")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("only HTTP and HTTPS URLs are supported")
	}
	if u.User != nil {
		return nil, errors.New("URLs containing credentials are not allowed")
	}
	if port := u.Port(); port != "" && !((u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443")) {
		return nil, errors.New("only the standard HTTP and HTTPS ports are allowed")
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	if ip := net.ParseIP(host); ip != nil {
		if !publicIP(ip) {
			return nil, errors.New("private or local network addresses are not allowed")
		}
		return u, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve host: %w", err)
	}
	if len(ips) == 0 {
		return nil, errors.New("host did not resolve to a public address")
	}
	for _, addr := range ips {
		if !publicIP(addr.IP) {
			return nil, errors.New("private or local network addresses are not allowed")
		}
	}
	return u, nil
}

// dialPublic resolves again at connection time and dials only a validated IP,
// preventing a hostname from changing to a private address between validation
// and the actual connection.
func dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, strings.TrimSuffix(host, "."))
	if err != nil {
		return nil, err
	}
	var lastErr error
	dialer := net.Dialer{Timeout: 10 * time.Second}
	for _, addr := range ips {
		if !publicIP(addr.IP) {
			return nil, errors.New("private or local network addresses are not allowed")
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("host did not resolve to a public address")
	}
	return nil, lastErr
}

func publicIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

func htmlText(s string) string {
	s = blockedHTML.ReplaceAllString(s, " ")
	s = blockHTML.ReplaceAllString(s, "\n")
	s = entityHTML.ReplaceAllStringFunc(s, decodeEntity)
	s = tagPattern.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		line = strings.Join(strings.FieldsFunc(line, unicode.IsSpace), " ")
		line = punctSpace.ReplaceAllString(line, "$1")
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func decodeEntity(entity string) string {
	name := strings.TrimSuffix(strings.TrimPrefix(entity, "&"), ";")
	switch name {
	case "amp":
		return "&"
	case "quot":
		return `"`
	case "apos":
		return "'"
	case "lt":
		return "<"
	case "gt":
		return ">"
	}
	if strings.HasPrefix(name, "#x") {
		if n, err := strconv.ParseInt(name[2:], 16, 32); err == nil {
			return string(rune(n))
		}
	} else if strings.HasPrefix(name, "#") {
		if n, err := strconv.ParseInt(name[1:], 10, 32); err == nil {
			return string(rune(n))
		}
	}
	return entity
}
