package chatgpt

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

func fakeJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// FakeTokens builds an id/access token pair for tests.
func fakeTokenPair(account, email string, exp time.Time) (id, access string) {
	auth := map[string]any{"chatgpt_account_id": account, "chatgpt_plan_type": "plus"}
	id = fakeJWT(map[string]any{"email": email, "https://api.openai.com/auth": auth})
	access = fakeJWT(map[string]any{"exp": exp.Unix(), "https://api.openai.com/auth": auth})
	return
}
