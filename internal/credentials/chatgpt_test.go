package credentials

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/chatgpt"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
)

// keychainLike mimics the macOS Keychain store's limits: no quotes or
// backslashes, and short values.
type keychainLike struct{ secrets.Store }

func (k keychainLike) Set(key, value string) error {
	if strings.ContainsAny(value, "\"\\\n\r") || len(value) > 2000 {
		return errors.New("keychain: secret contains unsupported characters or is too long")
	}
	return k.Store.Set(key, value)
}

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

func pair(exp time.Time) (id, access string) {
	auth := map[string]any{"chatgpt_account_id": "acct-9", "chatgpt_plan_type": "pro"}
	return jwt(map[string]any{"email": "u@example.com", "https://api.openai.com/auth": auth}),
		jwt(map[string]any{"exp": exp.Unix(), "https://api.openai.com/auth": auth})
}

func TestChatGPTSignInStoresCredentialAndRefreshes(t *testing.T) {
	var refreshes int32
	var exchangeExp atomic.Int64
	exchangeExp.Store(time.Now().Add(-time.Minute).Unix()) // first token is already expired
	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts/deviceauth/usercode", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"device_auth_id":"d","user_code":"CODE-1","interval":"1"}`))
	})
	mux.HandleFunc("/api/accounts/deviceauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"authorization_code":"c","code_challenge":"x","code_verifier":"v"}`))
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		exp := time.Unix(exchangeExp.Load(), 0)
		if r.Header.Get("Content-Type") == "application/json" {
			atomic.AddInt32(&refreshes, 1)
			exp = time.Now().Add(time.Hour)
		}
		id, access := pair(exp)
		json.NewEncoder(w).Encode(map[string]any{"id_token": id, "access_token": access, "refresh_token": "r-next"})
	})
	auth := httptest.NewServer(mux)
	defer auth.Close()

	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(st, keychainLike{secrets.NewMemoryStore()}, llm.NewRegistry())
	s.ChatGPT = &chatgpt.Client{Issuer: auth.URL, PollWait: 5 * time.Millisecond}
	results := make(chan protocol.ChatGPTSignInResult, 2)
	s.OnSignIn = func(r protocol.ChatGPTSignInResult) { results <- r }

	// An API key exists first; the sign-in is a second openai credential.
	key, err := s.Add(ctx, protocol.CredentialAddParams{Provider: "openai", Label: "key", Secret: "sk-abcd1234"})
	if err != nil || key.Kind != "api_key" {
		t.Fatalf("key = %+v err=%v", key, err)
	}
	start, err := s.StartChatGPTSignIn(ctx)
	if err != nil || start.UserCode != "CODE-1" || start.SessionID == "" {
		t.Fatalf("start = %+v err=%v", start, err)
	}
	var res protocol.ChatGPTSignInResult
	select {
	case res = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("sign-in did not finish")
	}
	if !res.OK || res.Credential == nil || res.Credential.Kind != "chatgpt" || res.Credential.Provider != "openai" ||
		res.Credential.IsDefault || res.Credential.Last4 != "" {
		t.Fatalf("result = %+v", res)
	}

	// Both credentials are usable; the default (the key) is still first.
	all, err := s.Usable(ctx, "openai")
	if err != nil || len(all) != 2 {
		t.Fatalf("usable = %d err=%v", len(all), err)
	}
	if all[0].Material.Kind == llm.KindChatGPT || all[0].Material.APIKey != "sk-abcd1234" {
		t.Fatalf("default = %+v", all[0].Material)
	}
	// The stored token was expired, so resolving it refreshed it.
	r, err := s.Resolve(ctx, "openai", res.Credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Material.Kind != llm.KindChatGPT || r.Material.AccountID != "acct-9" || r.Material.APIKey == "" || atomic.LoadInt32(&refreshes) < 1 {
		t.Fatalf("material = %+v refreshes=%d", r.Material, refreshes)
	}
	before := atomic.LoadInt32(&refreshes)
	if _, err := s.Resolve(ctx, "openai", res.Credential.ID); err != nil || atomic.LoadInt32(&refreshes) != before {
		t.Fatalf("refreshed again although the token is fresh (err=%v)", err)
	}

	// Signing in again with the same account renews rather than duplicates.
	if _, err := s.StartChatGPTSignIn(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case res2 := <-results:
		if !res2.OK || res2.Credential.ID != res.Credential.ID {
			t.Fatalf("re-sign-in = %+v", res2)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second sign-in did not finish")
	}
	if list, _ := s.List(ctx); len(list) != 2 {
		t.Fatalf("credentials = %d", len(list))
	}
	if _, err := s.Rotate(ctx, res.Credential.ID, "sk-x"); err == nil {
		t.Fatal("rotating a ChatGPT sign-in should fail")
	}
	// Signing out removes it (and its tokens).
	if err := s.Delete(ctx, res.Credential.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(ctx); len(list) != 1 || list[0].Kind != "api_key" {
		t.Fatalf("after delete: %+v", list)
	}
}
