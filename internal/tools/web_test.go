package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/config"
)

func TestParseSearchResults(t *testing.T) {
	body := `<a class="result__a" href="https://example.com/page?a=1&amp;b=2"><b>Example</b> title</a><a class="result__snippet">A useful &amp; current <b>snippet</b>.</a>`
	got := parseSearchResults(body, 5)
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if got[0].Title != "Example title" {
		t.Fatalf("title = %q", got[0].Title)
	}
	if got[0].URL != "https://example.com/page?a=1&b=2" {
		t.Fatalf("URL = %q", got[0].URL)
	}
	if got[0].Snippet != "A useful & current snippet." {
		t.Fatalf("snippet = %q", got[0].Snippet)
	}
}

func TestHTMLTextDropsActiveAndHiddenContent(t *testing.T) {
	got := htmlText(`<h1>Weather</h1><script>secret()</script><p>Sunny &amp; warm</p><style>.x{}</style>`)
	if got != "Weather\nSunny & warm" {
		t.Fatalf("htmlText() = %q", got)
	}
}

func TestValidatePublicURLRejectsUnsafeTargets(t *testing.T) {
	for _, raw := range []string{
		"file:///etc/passwd", "http://127.0.0.1/", "http://10.0.0.1/", "http://[::1]/", "https://user:pass@example.com/",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := validatePublicURL(context.Background(), raw); err == nil {
				t.Fatalf("validatePublicURL(%q) unexpectedly succeeded", raw)
			}
		})
	}
}

func TestValidatePublicURLAcceptsPublicLiteral(t *testing.T) {
	u, err := validatePublicURL(context.Background(), "https://8.8.8.8/path")
	if err != nil {
		t.Fatal(err)
	}
	if u.Hostname() != "8.8.8.8" {
		t.Fatalf("host = %q", u.Hostname())
	}
}

func TestWebToolContracts(t *testing.T) {
	search, fetch := newWebSearch(), newWebFetch()
	if search.Name() != "web.search" || fetch.Name() != "web.fetch" {
		t.Fatalf("unexpected tool names: %s, %s", search.Name(), fetch.Name())
	}
	if !strings.Contains(search.Description(), "web.fetch") {
		t.Fatal("search tool does not explain how to fetch result pages")
	}
	if !json.Valid(search.Schema()) || !json.Valid(fetch.Schema()) {
		t.Fatal("tool schemas must be valid JSON")
	}
}

func TestRegisterBuiltinsRegisterShellToolsWhenProjectMayEnableThem(t *testing.T) {
	cfg := &config.Config{Home: t.TempDir()}
	registry := NewRegistry()
	RegisterBuiltins(registry, cfg, NewWorkspaces(cfg), nil)
	for _, name := range []string{"web.search", "web.fetch", "file.read", "file.list", "file.write", "shell.run", "verification.plan", "verification.run", "browser.verify"} {
		if _, ok := registry.Get(name); !ok {
			t.Errorf("built-in %q is not registered", name)
		}
	}
}
