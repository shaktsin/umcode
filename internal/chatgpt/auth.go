// Package chatgpt implements "Sign in with ChatGPT": the OAuth device-code
// flow and token refresh used by OpenAI's own Codex client, so a ChatGPT plan
// can power OpenAI model calls instead of an API key.
//
// This is not a documented public API for third parties; OpenAI may change or
// restrict it. UMCode keeps its own tokens (it never reads another tool's
// login files) and treats the sign-in as one more OpenAI credential.
package chatgpt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// ClientID is the public OAuth client id of the Codex CLI.
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// Issuer is the OpenAI auth server.
	Issuer = "https://auth.openai.com"
)

// Tokens are the credentials produced by a sign-in.
type Tokens struct {
	IDToken      string    `json:"idToken"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	AccountID    string    `json:"accountId"`
	Email        string    `json:"email,omitempty"`
	Plan         string    `json:"plan,omitempty"`
}

// Expiring reports whether the access token is expired or about to be.
func (t Tokens) Expiring(now time.Time, margin time.Duration) bool {
	return t.AccessToken == "" || (!t.ExpiresAt.IsZero() && now.Add(margin).After(t.ExpiresAt))
}

// DeviceCode is a pending device-code sign-in.
type DeviceCode struct {
	VerificationURL string
	UserCode        string
	deviceAuthID    string
	interval        time.Duration
}

// Client talks to the auth server. The zero value uses production endpoints.
type Client struct {
	Issuer   string
	ClientID string
	HTTP     *http.Client
	// PollWait overrides the server-suggested polling interval (tests).
	PollWait time.Duration
	// MaxWait bounds device-code polling (default 15 minutes).
	MaxWait time.Duration
}

func (c *Client) issuer() string {
	if c.Issuer != "" {
		return strings.TrimRight(c.Issuer, "/")
	}
	return Issuer
}

func (c *Client) clientID() string {
	if c.ClientID != "" {
		return c.ClientID
	}
	return ClientID
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// ErrRefreshRejected means the refresh token is no longer valid: the user
// has to sign in again.
var ErrRefreshRejected = errors.New("ChatGPT sign-in expired; sign in again")

func (c *Client) postJSON(ctx context.Context, url string, in any) (*http.Response, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.http().Do(req)
}

// StartDeviceCode requests a one-time code the user enters in a browser.
func (c *Client) StartDeviceCode(ctx context.Context) (*DeviceCode, error) {
	resp, err := c.postJSON(ctx, c.issuer()+"/api/accounts/deviceauth/usercode", map[string]string{"client_id": c.clientID()})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("ChatGPT device sign-in is not available right now")
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("ChatGPT sign-in could not start (HTTP %d)", resp.StatusCode)
	}
	var r struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
		UserCode2    string `json:"usercode"`
		Interval     any    `json:"interval"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, errors.New("ChatGPT sign-in: unexpected response")
	}
	code := r.UserCode
	if code == "" {
		code = r.UserCode2
	}
	if r.DeviceAuthID == "" || code == "" {
		return nil, errors.New("ChatGPT sign-in: unexpected response")
	}
	iv := 5 * time.Second
	switch v := r.Interval.(type) {
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			iv = time.Duration(n) * time.Second
		}
	case float64:
		if v > 0 {
			iv = time.Duration(v * float64(time.Second))
		}
	}
	if c.PollWait > 0 {
		iv = c.PollWait
	}
	return &DeviceCode{VerificationURL: c.issuer() + "/codex/device", UserCode: code, deviceAuthID: r.DeviceAuthID, interval: iv}, nil
}

// Complete polls until the user approves the code, then exchanges it for tokens.
func (c *Client) Complete(ctx context.Context, dc *DeviceCode) (Tokens, error) {
	maxWait := c.MaxWait
	if maxWait <= 0 {
		maxWait = 15 * time.Minute
	}
	deadline := time.Now().Add(maxWait)
	var grant struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeChallenge     string `json:"code_challenge"`
		CodeVerifier      string `json:"code_verifier"`
	}
	for {
		resp, err := c.postJSON(ctx, c.issuer()+"/api/accounts/deviceauth/token",
			map[string]string{"device_auth_id": dc.deviceAuthID, "user_code": dc.UserCode})
		if err != nil {
			return Tokens{}, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			if err := json.Unmarshal(body, &grant); err != nil || grant.AuthorizationCode == "" {
				return Tokens{}, errors.New("ChatGPT sign-in: unexpected response")
			}
			break
		}
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			return Tokens{}, fmt.Errorf("ChatGPT sign-in failed (HTTP %d)", resp.StatusCode)
		}
		if time.Now().Add(dc.interval).After(deadline) {
			return Tokens{}, errors.New("ChatGPT sign-in timed out; start again")
		}
		select {
		case <-ctx.Done():
			return Tokens{}, ctx.Err()
		case <-time.After(dc.interval):
		}
	}
	form := strings.NewReader(formEncode(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     c.clientID(),
		"code":          grant.AuthorizationCode,
		"redirect_uri":  c.issuer() + "/deviceauth/callback",
		"code_verifier": grant.CodeVerifier,
	}))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.issuer()+"/oauth/token", form)
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http().Do(req)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return Tokens{}, fmt.Errorf("ChatGPT sign-in: token exchange failed (HTTP %d)", resp.StatusCode)
	}
	return parseTokens(body, Tokens{})
}

// Refresh exchanges a refresh token for fresh tokens.
func (c *Client) Refresh(ctx context.Context, old Tokens) (Tokens, error) {
	if old.RefreshToken == "" {
		return Tokens{}, ErrRefreshRejected
	}
	resp, err := c.postJSON(ctx, c.issuer()+"/oauth/token", map[string]string{
		"client_id": c.clientID(), "grant_type": "refresh_token", "refresh_token": old.RefreshToken,
	})
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return Tokens{}, ErrRefreshRejected
	}
	if resp.StatusCode/100 != 2 {
		return Tokens{}, fmt.Errorf("ChatGPT token refresh failed (HTTP %d)", resp.StatusCode)
	}
	return parseTokens(body, old)
}

func parseTokens(body []byte, prev Tokens) (Tokens, error) {
	var r struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.AccessToken == "" {
		return Tokens{}, errors.New("ChatGPT sign-in: unexpected token response")
	}
	t := prev
	t.AccessToken = r.AccessToken
	if r.RefreshToken != "" {
		t.RefreshToken = r.RefreshToken
	}
	if r.IDToken != "" {
		t.IDToken = r.IDToken
	}
	if exp := jwtClaims(t.AccessToken).exp(); !exp.IsZero() {
		t.ExpiresAt = exp
	} else if r.ExpiresIn > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)
	} else {
		t.ExpiresAt = time.Now().Add(time.Hour)
	}
	id := jwtClaims(t.IDToken)
	acc := jwtClaims(t.AccessToken)
	if v := firstNonEmpty(id.authString("chatgpt_account_id"), acc.authString("chatgpt_account_id")); v != "" {
		t.AccountID = v
	}
	if v := firstNonEmpty(id.str("email"), id.profileString("email")); v != "" {
		t.Email = v
	}
	if v := firstNonEmpty(id.authString("chatgpt_plan_type"), acc.authString("chatgpt_plan_type")); v != "" {
		t.Plan = v
	}
	if t.AccountID == "" {
		return Tokens{}, errors.New("ChatGPT sign-in: no ChatGPT account on this login")
	}
	return t, nil
}

type claims map[string]any

func jwtClaims(tok string) claims {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return claims{}
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return claims{}
	}
	var c claims
	if json.Unmarshal(b, &c) != nil {
		return claims{}
	}
	return c
}

func (c claims) exp() time.Time {
	if v, ok := c["exp"].(float64); ok && v > 0 {
		return time.Unix(int64(v), 0)
	}
	return time.Time{}
}

func (c claims) str(k string) string { s, _ := c[k].(string); return s }

func (c claims) authString(k string) string {
	m, _ := c["https://api.openai.com/auth"].(map[string]any)
	s, _ := m[k].(string)
	return s
}

func (c claims) profileString(k string) string {
	m, _ := c["https://api.openai.com/profile"].(map[string]any)
	s, _ := m[k].(string)
	return s
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func formEncode(m map[string]string) string {
	var b strings.Builder
	for k, v := range m {
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(urlEscape(k) + "=" + urlEscape(v))
	}
	return b.String()
}

func urlEscape(s string) string { return url.QueryEscape(s) }
