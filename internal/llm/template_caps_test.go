package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestParseTemplateCapsQwen38(t *testing.T) {
	b, err := os.ReadFile("testdata/qwen38_props.json")
	if err != nil {
		t.Fatal(err)
	}
	got := ParseTemplateCaps(b)
	want := TemplateCaps{EnableThinking: true, ReasoningEffort: true, PreserveThinking: true}
	if got != want {
		t.Fatalf("caps = %+v, want %+v", got, want)
	}
}

func TestParseTemplateCapsUndetected(t *testing.T) {
	for _, in := range []string{``, `not json`, `{}`, `{"chat_template":"{{ messages }}"}`} {
		if got := ParseTemplateCaps([]byte(in)); got != (TemplateCaps{}) {
			t.Fatalf("%q -> %+v, want zero", in, got)
		}
	}
}

func TestTemplateKwargsLevels(t *testing.T) {
	all := TemplateCaps{EnableThinking: true, ReasoningEffort: true, PreserveThinking: true}
	cases := []struct {
		caps  TemplateCaps
		level string
		want  map[string]any
	}{
		{all, "off", map[string]any{"enable_thinking": false, "preserve_thinking": false}},
		{all, "low", map[string]any{"enable_thinking": true, "reasoning_effort": "low", "preserve_thinking": false}},
		{all, "medium", map[string]any{"enable_thinking": true, "reasoning_effort": "medium", "preserve_thinking": false}},
		{all, "high", map[string]any{"enable_thinking": true, "reasoning_effort": "xhigh", "preserve_thinking": false}},
		{TemplateCaps{EnableThinking: true}, "off", map[string]any{"enable_thinking": false}},
		{TemplateCaps{EnableThinking: true}, "high", map[string]any{"enable_thinking": true}},
		{TemplateCaps{ReasoningEffort: true}, "high", map[string]any{"reasoning_effort": "xhigh"}},
	}
	for _, c := range cases {
		if got := TemplateKwargs(c.caps, c.level); !reflect.DeepEqual(got, c.want) {
			t.Errorf("TemplateKwargs(%+v,%q) = %v, want %v", c.caps, c.level, got, c.want)
		}
	}
	if got := TemplateKwargs(TemplateCaps{}, "high"); got != nil {
		t.Fatalf("undetected server must get no kwargs, got %v", got)
	}
}

func TestNeverSendsNone(t *testing.T) {
	all := TemplateCaps{EnableThinking: true, ReasoningEffort: true, PreserveThinking: true}
	for _, lvl := range []string{"off", "low", "medium", "high", "", "bogus"} {
		if v, ok := TemplateKwargs(all, lvl)["reasoning_effort"]; ok && v == "none" {
			t.Fatalf("level %q produced reasoning_effort none", lvl)
		}
	}
}

func TestDiscoverTemplateCaps(t *testing.T) {
	b, _ := os.ReadFile("testdata/qwen38_props.json")
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.URL.Path != "/props" {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	got, err := DiscoverTemplateCaps(context.Background(), srv.Client(), Credential{BaseURL: srv.URL + "/v1"})
	if err != nil || !got.EnableThinking || !got.ReasoningEffort || !got.PreserveThinking {
		t.Fatalf("got %+v err %v", got, err)
	}
	if path != "/props" {
		t.Fatalf("path %q", path)
	}
	// A server without /props is undetected, not an error that sends fields.
	bad := httptest.NewServer(http.NotFoundHandler())
	defer bad.Close()
	got, err = DiscoverTemplateCaps(context.Background(), bad.Client(), Credential{BaseURL: bad.URL + "/v1"})
	if err != ErrRuntimeCapacityUnavailable || got != (TemplateCaps{}) {
		t.Fatalf("undetected: got %+v err %v", got, err)
	}
	if _, err = DiscoverTemplateCaps(context.Background(), nil, Credential{BaseURL: "ftp://x/v1"}); err != ErrRuntimeCapacityUnavailable {
		t.Fatalf("non-http base: %v", err)
	}
}

func TestApplyCustomKwargsRejectsProtected(t *testing.T) {
	for _, k := range []string{"model", "messages", "stream", "tools"} {
		if _, err := ApplyCustomKwargs(map[string]any{"a": 1}, map[string]any{k: 1}); err == nil || !strings.Contains(err.Error(), k) {
			t.Fatalf("key %q not rejected: %v", k, err)
		}
	}
	base := map[string]any{"a": 1, "b": 2}
	got, err := ApplyCustomKwargs(base, map[string]any{"b": 3, "c": 4})
	if err != nil || !reflect.DeepEqual(got, map[string]any{"a": 1, "b": 3, "c": 4}) {
		t.Fatalf("got %v err %v", got, err)
	}
	if base["b"] != 2 {
		t.Fatal("base mutated")
	}
}

func TestBuildRequestTemplateKwargs(t *testing.T) {
	req := testRequest()
	req.Reasoning = ReasoningHigh
	req.TemplateKwargs = TemplateKwargs(TemplateCaps{EnableThinking: true, ReasoningEffort: true}, "high")
	body := (&OpenAI{compatible: true}).buildRequest(req)
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatalf("top-level reasoning_effort sent: %v", body["reasoning_effort"])
	}
	kw, ok := body["chat_template_kwargs"].(map[string]any)
	if !ok || kw["reasoning_effort"] != "xhigh" || kw["enable_thinking"] != true {
		t.Fatalf("chat_template_kwargs = %v", body["chat_template_kwargs"])
	}
	off := testRequest()
	off.Reasoning = ReasoningOff
	off.TemplateKwargs = TemplateKwargs(TemplateCaps{EnableThinking: true, ReasoningEffort: true}, "off")
	body = (&OpenAI{compatible: true}).buildRequest(off)
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatal("off must not send reasoning_effort")
	}
	if kw := body["chat_template_kwargs"].(map[string]any); kw["enable_thinking"] != false {
		t.Fatalf("off kwargs = %v", kw)
	}
}

func TestTemplateStyleWithoutKwargsSendsNothing(t *testing.T) {
	for _, lvl := range []string{ReasoningOff, ReasoningLow, ReasoningMedium, ReasoningHigh} {
		req := testRequest()
		req.Reasoning, req.ReasoningStyle = lvl, "template"
		body := (&OpenAI{compatible: true}).buildRequest(req)
		if _, ok := body["reasoning_effort"]; ok {
			t.Fatalf("level %s: undetected template server got reasoning_effort %v", lvl, body["reasoning_effort"])
		}
		if _, ok := body["chat_template_kwargs"]; ok {
			t.Fatalf("level %s: undetected template server got chat_template_kwargs", lvl)
		}
	}
}

func TestDiscoverTemplateCapsFailurePaths(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"chat_template":"enable_thinking"}`))
	}))
	defer other.Close()
	cases := map[string]http.HandlerFunc{
		"non-200": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) },
		"timeout": func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		},
		"off-host redirect": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/props", http.StatusFound)
		},
		"oversize": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"chat_template":"enable_thinking ` + strings.Repeat("x", 1<<20+10) + `"}`))
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			got, err := DiscoverTemplateCaps(context.Background(), srv.Client(), Credential{BaseURL: srv.URL + "/v1"})
			if got != (TemplateCaps{}) || err != ErrRuntimeCapacityUnavailable {
				t.Fatalf("got %+v err %v, want zero caps and ErrRuntimeCapacityUnavailable", got, err)
			}
		})
	}
}
