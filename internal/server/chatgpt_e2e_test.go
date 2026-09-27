package server_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/chatgpt"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

func fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

// A ChatGPT sign-in and an API key are both OpenAI credentials; the engine
// uses whichever the user picked (here: pinned by credential id) for the call.
func TestOpenAIChatWithChatGPTSignInOrAPIKey(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := map[string]any{"exp": time.Now().Add(time.Hour).Unix(),
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-e2e", "chatgpt_plan_type": "plus"}}
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			w.Write([]byte(`{"device_auth_id":"d","user_code":"E2E-CODE","interval":"1"}`))
		case "/api/accounts/deviceauth/token":
			w.Write([]byte(`{"authorization_code":"c","code_challenge":"x","code_verifier":"v"}`))
		case "/oauth/token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": fakeJWT(claims), "refresh_token": "r",
				"id_token": fakeJWT(map[string]any{"email": "e2e@example.com", "https://api.openai.com/auth": claims["https://api.openai.com/auth"]})})
		default:
			http.NotFound(w, r)
		}
	}))
	defer auth.Close()

	var mu sync.Mutex
	var seen []string // "<path> <authorization> <account>"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization")+" "+r.Header.Get("chatgpt-account-id"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		switch r.URL.Path {
		case "/responses": // ChatGPT backend (Responses API)
			w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"from chatgpt\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n"))
		case "/v1/chat/completions": // API key (Chat Completions)
			w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"from api key\"}}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	old := llm.ChatGPTBase
	llm.ChatGPTBase = api.URL
	defer func() { llm.ChatGPTBase = old }()

	h := newHarness(t, nil)
	h.eng.Creds.ChatGPT = &chatgpt.Client{Issuer: auth.URL, PollWait: 5 * time.Millisecond}

	var key protocol.Credential
	h.call(protocol.MethodCredentialAdd, protocol.CredentialAddParams{Provider: "openai", Label: "key", Secret: "sk-e2e-1234", BaseURL: api.URL + "/v1"}, &key)

	var start protocol.ChatGPTSignInStart
	h.call(protocol.MethodChatGPTSignInStart, struct{}{}, &start)
	if start.UserCode != "E2E-CODE" {
		t.Fatalf("start = %+v", start)
	}
	var signed protocol.ChatGPTSignInResult
	timeout := time.After(10 * time.Second)
wait:
	for {
		select {
		case <-timeout:
			t.Fatal("no sign-in notification")
		case n := <-h.c.Notifications():
			if n.Method == protocol.NotifyChatGPTSignIn {
				json.Unmarshal(n.Params, &signed)
				break wait
			}
		}
	}
	if !signed.OK || signed.Credential == nil || signed.Credential.Kind != "chatgpt" {
		t.Fatalf("signed = %+v", signed)
	}
	var list protocol.CredentialListResult
	h.call(protocol.MethodCredentialList, nil, &list)
	kinds := map[string]int{}
	for _, c := range list.Credentials {
		kinds[c.Kind]++
	}
	if kinds["api_key"] != 1 || kinds["chatgpt"] != 1 {
		t.Fatalf("credentials = %+v", list.Credentials)
	}
	var test protocol.CredentialTestResult
	h.call(protocol.MethodCredentialTest, protocol.CredentialIDParams{CredentialID: signed.Credential.ID}, &test)
	if !test.OK {
		t.Fatalf("test = %+v", test)
	}

	ask := func(credID string) (string, protocol.Turn) {
		var th protocol.Thread
		h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID}, &th)
		var res protocol.TurnStartResult
		h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "hi",
			Override: protocol.ModelSelection{Provider: "openai", Model: "gpt-5.6-terra", CredentialID: credID}}, &res)
		turn, text, _ := h.waitTurn(res.Turn.ID, nil)
		return text, turn
	}
	text, turn := ask(signed.Credential.ID)
	if text != "from chatgpt" || turn.Resolved.CredentialID != signed.Credential.ID || turn.Usage.CostUSD != 0 {
		t.Fatalf("chatgpt turn: text=%q resolved=%+v usage=%+v", text, turn.Resolved, turn.Usage)
	}
	text, turn = ask(key.ID)
	if text != "from api key" || turn.Resolved.CredentialID != key.ID || turn.Usage.CostUSD == 0 {
		t.Fatalf("api key turn: text=%q resolved=%+v usage=%+v", text, turn.Resolved, turn.Usage)
	}
	mu.Lock()
	defer mu.Unlock()
	var sawChatGPT, sawKey bool
	for _, s := range seen {
		if len(s) > 10 && s[:10] == "/responses" && s[10:] != "" {
			sawChatGPT = sawChatGPT || contains(s, "acct-e2e")
		}
		if contains(s, "/v1/chat/completions Bearer sk-e2e-1234") {
			sawKey = true
		}
	}
	if !sawChatGPT || !sawKey {
		t.Fatalf("requests = %v", seen)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
