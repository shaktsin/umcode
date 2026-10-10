package toolselect

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/vault"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

type DiscoverArgs struct {
	Query  string   `json:"query"`
	Names  []string `json:"names"`
	Cursor string   `json:"cursor"`
}
type DiscoveryEntry struct {
	CanonicalName string `json:"canonical_name"`
	WireName      string `json:"wire_name"`
	Family        string `json:"family"`
	Description   string `json:"description"`
	Loaded        bool   `json:"loaded"`
}
type DiscoverResult struct {
	Entries     []DiscoveryEntry `json:"entries"`
	NextCursor  string           `json:"next_cursor,omitempty"`
	Message     string           `json:"message"`
	Suggestions []string         `json:"suggestions,omitempty"`
}
type cursorData struct {
	Version                      int
	Generation, QueryHash, Nonce string
	Offset, PermissionRevision   int
}

func shortDescription(text string) string {
	b, _ := vault.Redact([]byte(text))
	if len(b) > 256 {
		b = b[:256]
		for !utf8.Valid(b) {
			b = b[:len(b)-1]
		}
	}
	return string(b)
}
func (s *State) cursor(queryHash string, offset int) string {
	b, _ := json.Marshal(cursorData{Version: 1, Generation: s.catalog.Generation, QueryHash: queryHash, Nonce: hex.EncodeToString(s.nonce[:]), Offset: offset, PermissionRevision: s.permissionRevision})
	mac := hmac.New(sha256.New, s.cursorKey[:])
	_, _ = mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *State) offset(cursor, queryHash string) (int, error) {
	invalid := errors.New("invalid discovery cursor")
	parts := strings.Split(cursor, ".")
	if len(parts) != 2 {
		return 0, invalid
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return 0, invalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, invalid
	}
	mac := hmac.New(sha256.New, s.cursorKey[:])
	_, _ = mac.Write(b)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return 0, invalid
	}
	var d cursorData
	if json.Unmarshal(b, &d) != nil || d.Version != 1 || d.Generation != s.catalog.Generation || d.QueryHash != queryHash || d.Nonce != hex.EncodeToString(s.nonce[:]) || d.PermissionRevision != s.permissionRevision || d.Offset < 0 {
		return 0, invalid
	}
	return d.Offset, nil
}
func (s *State) Discover(raw json.RawMessage) (result DiscoverResult, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if recover() != nil {
			s.fallbackLocked("discovery_error")
			result = DiscoverResult{}
			err = errors.New("discovery unavailable")
		}
	}()
	s.report.DiscoveryCalls++
	invalid := errors.New("invalid discovery arguments")
	if len(raw) > 65536 || !utf8.Valid(raw) {
		return result, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var args DiscoverArgs
	if decoder.Decode(&args) != nil {
		return result, invalid
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return result, invalid
	}
	if len(args.Query) > 512 || len(args.Names) > 8 || len(args.Cursor) > 2048 {
		return result, invalid
	}
	query := strings.ToLower(strings.Join(strings.Fields(args.Query), " "))
	if query == "" && len(args.Names) == 0 {
		return result, invalid
	}
	for _, n := range args.Names {
		if n == "" || len(n) > 4096 || !utf8.ValidString(n) {
			return result, invalid
		}
	}
	names := append([]string(nil), args.Names...)
	sort.Strings(names)
	binding, _ := json.Marshal(struct {
		Query string
		Names []string
	}{query, names})
	sum := sha256.Sum256(binding)
	queryHash := hex.EncodeToString(sum[:])
	offset := 0
	if args.Cursor != "" {
		offset, err = s.offset(args.Cursor, queryHash)
		if err != nil {
			return result, err
		}
	}
	type match struct {
		entry Entry
		score int
	}
	var matches []match
	terms := strings.Fields(query)
	for _, e := range s.catalog.Entries {
		if !s.allowed[e.CanonicalName] {
			continue
		}
		exact := false
		for _, n := range names {
			if n == e.CanonicalName || n == e.WireName {
				exact = true
				break
			}
		}
		// Search redacted metadata; query and full descriptions never enter results.
		body, _ := vault.Redact([]byte(e.CanonicalName + " " + e.WireName + " " + e.Family + " " + e.Origin + " " + e.Spec.Description))
		text := strings.ToLower(string(body))
		score := 0
		for _, term := range terms {
			if strings.Contains(text, term) {
				score++
			}
		}
		if query != "" && (query == strings.ToLower(e.CanonicalName) || query == strings.ToLower(e.WireName)) {
			score += 1000
		}
		if query != "" && query == strings.ToLower(e.Family) {
			score += 500
		}
		if exact {
			score += 2000
		}
		if score > 0 {
			matches = append(matches, match{e, score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].entry.CanonicalName < matches[j].entry.CanonicalName
	})
	if offset > len(matches) {
		return result, errors.New("invalid discovery cursor")
	}
	result = DiscoverResult{Entries: []DiscoveryEntry{}, Message: "Loaded schemas are available on the next provider request."}
	end := min(offset+8, len(matches))
	for _, m := range matches[offset:end] {
		if err = s.pinLocked(m.entry.CanonicalName); err != nil {
			s.fallbackLocked("discovery_error")
			return DiscoverResult{}, errors.New("discovery unavailable")
		}
		e := m.entry
		result.Entries = append(result.Entries, DiscoveryEntry{CanonicalName: e.CanonicalName, WireName: e.WireName, Family: e.Family, Description: shortDescription(e.Spec.Description), Loaded: true})
	}
	if end < len(matches) {
		result.NextCursor = s.cursor(queryHash, end)
	}
	if len(matches) == 0 {
		result.Suggestions = []string{"web", "browser", "computer", "mcp", "plugin", "other"}
	}
	return result, nil
}
