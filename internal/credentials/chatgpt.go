package credentials

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/chatgpt"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

const signInWindow = 15 * time.Minute

// Secret stores (the macOS Keychain in particular) reject quotes and limit
// line length, so sign-in tokens are saved as base64 text in short chunks.
const chunkSize = 1200

const chunkMarker = "umcode-chunks:"

func (s *Service) setLargeSecret(key, value string) error {
	enc := base64.RawURLEncoding.EncodeToString([]byte(value))
	var parts []string
	for len(enc) > 0 {
		n := min(chunkSize, len(enc))
		parts = append(parts, enc[:n])
		enc = enc[n:]
	}
	for i, p := range parts {
		if err := s.secrets.Set(fmt.Sprintf("%s#%d", key, i), p); err != nil {
			return err
		}
	}
	// The index is written last, so a half-written set is never read.
	return s.secrets.Set(key, chunkMarker+strconv.Itoa(len(parts)))
}

func (s *Service) getLargeSecret(key string) (string, error) {
	head, err := s.secrets.Get(key)
	if err != nil {
		return "", err
	}
	rest, ok := strings.CutPrefix(head, chunkMarker)
	if !ok {
		return head, nil
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 || n > 64 {
		return "", errors.New("stored sign-in is unreadable")
	}
	var enc strings.Builder
	for i := 0; i < n; i++ {
		p, err := s.secrets.Get(fmt.Sprintf("%s#%d", key, i))
		if err != nil {
			return "", err
		}
		enc.WriteString(p)
	}
	b, err := base64.RawURLEncoding.DecodeString(enc.String())
	return string(b), err
}

// deleteSecret removes a secret and any chunks that belong to it.
func (s *Service) deleteSecret(key string) {
	if head, err := s.secrets.Get(key); err == nil {
		if rest, ok := strings.CutPrefix(head, chunkMarker); ok {
			n, _ := strconv.Atoi(rest)
			for i := 0; i < n && i < 64; i++ {
				_ = s.secrets.Delete(fmt.Sprintf("%s#%d", key, i))
			}
		}
	}
	_ = s.secrets.Delete(key)
}

func (s *Service) loadTokens(id string) (chatgpt.Tokens, error) {
	raw, err := s.getLargeSecret(secretKey(id))
	if err != nil {
		return chatgpt.Tokens{}, err
	}
	var t chatgpt.Tokens
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return chatgpt.Tokens{}, errors.New("stored ChatGPT sign-in is unreadable; sign in again")
	}
	return t, nil
}

func (s *Service) saveTokens(id string, t chatgpt.Tokens) error {
	t.IDToken = "" // only used to read the account details, which are kept separately
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return s.setLargeSecret(secretKey(id), string(b))
}

// chatGPTMaterial returns a usable access token, refreshing it when it is
// close to expiry. Refreshes are serialised so concurrent calls don't spend
// the (rotating) refresh token twice.
func (s *Service) chatGPTMaterial(c protocol.Credential) (llm.Credential, error) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	t, err := s.loadTokens(c.ID)
	if err != nil {
		return llm.Credential{}, fmt.Errorf("read ChatGPT sign-in for %s: %w", c.Label, err)
	}
	if t.Expiring(s.now(), 5*time.Minute) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		nt, err := s.ChatGPT.Refresh(ctx, t)
		if err != nil {
			return llm.Credential{}, fmt.Errorf("%s: %w", c.Label, err)
		}
		t = nt
		if err := s.saveTokens(c.ID, t); err != nil {
			return llm.Credential{}, fmt.Errorf("save refreshed ChatGPT sign-in: %w", err)
		}
	}
	return llm.Credential{Kind: llm.KindChatGPT, APIKey: t.AccessToken, AccountID: t.AccountID, BaseURL: c.BaseURL}, nil
}

// StartChatGPTSignIn begins a device-code sign-in. The result is delivered to
// OnSignIn when the user finishes (or the attempt fails, times out or is cancelled).
func (s *Service) StartChatGPTSignIn(ctx context.Context) (protocol.ChatGPTSignInStart, error) {
	dc, err := s.ChatGPT.StartDeviceCode(ctx)
	if err != nil {
		return protocol.ChatGPTSignInStart{}, err
	}
	sid := store.NewID("signin")
	runCtx, cancel := context.WithTimeout(context.Background(), signInWindow)
	s.signMu.Lock()
	s.signins[sid] = cancel
	s.signMu.Unlock()
	go func() {
		defer cancel()
		defer func() {
			s.signMu.Lock()
			delete(s.signins, sid)
			s.signMu.Unlock()
		}()
		res := protocol.ChatGPTSignInResult{SessionID: sid}
		t, err := s.ChatGPT.Complete(runCtx, dc)
		if err == nil {
			var c protocol.Credential
			if c, err = s.saveChatGPT(runCtx, t); err == nil {
				res.OK, res.Credential = true, &c
			}
		}
		if err != nil {
			res.Error = err.Error()
			if errors.Is(err, context.Canceled) {
				res.Error = "sign-in cancelled"
			}
		}
		if s.OnSignIn != nil {
			s.OnSignIn(res)
		}
	}()
	return protocol.ChatGPTSignInStart{SessionID: sid, VerificationURL: dc.VerificationURL, UserCode: dc.UserCode,
		ExpiresInSec: int(signInWindow.Seconds())}, nil
}

// CancelChatGPTSignIn abandons a pending sign-in.
func (s *Service) CancelChatGPTSignIn(sessionID string) {
	s.signMu.Lock()
	cancel := s.signins[sessionID]
	s.signMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// saveChatGPT stores a signed-in account as an openai credential. Signing in
// again with the same account refreshes that credential instead of adding a copy.
func (s *Service) saveChatGPT(ctx context.Context, t chatgpt.Tokens) (protocol.Credential, error) {
	existing, err := s.st.ListCredentials(ctx, "openai")
	if err != nil {
		return protocol.Credential{}, err
	}
	label := "ChatGPT"
	if t.Email != "" {
		label += " · " + t.Email
	}
	if t.Plan != "" {
		label += " (" + strings.ToLower(t.Plan) + ")"
	}
	for _, c := range existing {
		if c.Kind != llm.KindChatGPT {
			continue
		}
		if old, err := s.loadTokens(c.ID); err == nil && old.AccountID == t.AccountID {
			if err := s.saveTokens(c.ID, t); err != nil {
				return c, err
			}
			_ = s.st.Audit(ctx, "credential.signin", map[string]any{"id": c.ID, "provider": "openai", "kind": "chatgpt", "renewed": true})
			return c, nil
		}
	}
	c := protocol.Credential{ID: store.NewID("key"), Provider: "openai", Label: label, Kind: llm.KindChatGPT}
	if err := s.saveTokens(c.ID, t); err != nil {
		return c, fmt.Errorf("save sign-in: %w", err)
	}
	c, err = s.st.CreateCredential(ctx, c)
	if err != nil {
		s.deleteSecret(secretKey(c.ID))
		return c, err
	}
	_ = s.st.Audit(ctx, "credential.signin", map[string]any{"id": c.ID, "provider": "openai", "kind": "chatgpt"})
	return c, nil
}
