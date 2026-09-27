package chatgpt

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func fakeAuthServer(t *testing.T, polls *int32, refreshed *int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts/deviceauth/usercode", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["client_id"] != ClientID {
			http.Error(w, "bad client", 400)
			return
		}
		w.Write([]byte(`{"device_auth_id":"dev-1","user_code":"ABCD-1234","interval":"1"}`))
	})
	mux.HandleFunc("/api/accounts/deviceauth/token", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["device_auth_id"] != "dev-1" || in["user_code"] != "ABCD-1234" {
			http.Error(w, "bad", 400)
			return
		}
		if atomic.AddInt32(polls, 1) < 3 {
			http.Error(w, "pending", http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"authorization_code":"code-1","code_challenge":"c","code_verifier":"verifier-1"}`))
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		id, access := fakeTokenPair("acct-1", "me@example.com", time.Now().Add(time.Hour))
		if r.Header.Get("Content-Type") == "application/json" {
			var in map[string]string
			json.Unmarshal(body, &in)
			if in["grant_type"] != "refresh_token" || in["refresh_token"] != "refresh-1" {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
			atomic.AddInt32(refreshed, 1)
			json.NewEncoder(w).Encode(map[string]any{"id_token": id, "access_token": access, "refresh_token": "refresh-2"})
			return
		}
		f, _ := url.ParseQuery(string(body))
		if f.Get("grant_type") != "authorization_code" || f.Get("code") != "code-1" || f.Get("code_verifier") != "verifier-1" ||
			f.Get("redirect_uri") == "" || f.Get("client_id") != ClientID {
			http.Error(w, "bad exchange "+string(body), 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id_token": id, "access_token": access, "refresh_token": "refresh-1"})
	})
	return httptest.NewServer(mux)
}

func TestDeviceCodeSignInAndRefresh(t *testing.T) {
	var polls, refreshed int32
	srv := fakeAuthServer(t, &polls, &refreshed)
	defer srv.Close()
	c := &Client{Issuer: srv.URL, PollWait: 10 * time.Millisecond}
	ctx := context.Background()
	dc, err := c.StartDeviceCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dc.UserCode != "ABCD-1234" || dc.VerificationURL != srv.URL+"/codex/device" {
		t.Fatalf("dc = %+v", dc)
	}
	tok, err := c.Complete(ctx, dc)
	if err != nil {
		t.Fatal(err)
	}
	if polls != 3 || tok.AccountID != "acct-1" || tok.Email != "me@example.com" || tok.Plan != "plus" ||
		tok.RefreshToken != "refresh-1" || tok.ExpiresAt.Before(time.Now().Add(30*time.Minute)) {
		t.Fatalf("polls=%d tok=%+v", polls, tok)
	}
	if tok.Expiring(time.Now(), 5*time.Minute) {
		t.Fatal("fresh token reported expiring")
	}
	if !tok.Expiring(time.Now().Add(58*time.Minute), 5*time.Minute) {
		t.Fatal("near-expiry token not reported expiring")
	}
	nt, err := c.Refresh(ctx, tok)
	if err != nil || nt.RefreshToken != "refresh-2" || nt.AccountID != "acct-1" || refreshed != 1 {
		t.Fatalf("refresh: %+v %v", nt, err)
	}
	if _, err := c.Refresh(ctx, Tokens{RefreshToken: "revoked"}); err != ErrRefreshRejected {
		t.Fatalf("revoked refresh err = %v", err)
	}
}

func TestDeviceCodeCancelAndTimeout(t *testing.T) {
	var polls, refreshed int32
	srv := fakeAuthServer(t, &polls, &refreshed)
	defer srv.Close()
	c := &Client{Issuer: srv.URL, PollWait: 20 * time.Millisecond, MaxWait: 30 * time.Millisecond}
	dc, err := c.StartDeviceCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Never approved (needs 3 polls; the window allows fewer): times out.
	if _, err := c.Complete(context.Background(), dc); err == nil {
		t.Fatal("expected timeout")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	atomic.StoreInt32(&polls, -100)
	if _, err := c.Complete(ctx, dc); err == nil {
		t.Fatal("expected cancel")
	}
}
