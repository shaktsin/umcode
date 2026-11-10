package llm

import "testing"

func TestParseContextLimit(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"openai", `{"error":{"message":"This model's maximum context length is 32768 tokens. However, your messages resulted in 40000 tokens.","code":"context_length_exceeded"}}`, 32768},
		{"vllm", `maximum context length is 8192 tokens, however you requested 9000 tokens`, 8192},
		{"llama.cpp equals", `the request exceeds the available context size (n_ctx = 4096)`, 4096},
		{"llama.cpp json", `{"error":{"type":"exceed_context_size_error","n_prompt_tokens":9000,"n_ctx":8192}}`, 8192},
		{"lm studio", `Trying to keep the first 5000 tokens when context length of only 4096 tokens`, 4096},
		{"anthropic", `prompt is too long: 210000 tokens > 200000 maximum`, 200000},
		{"no number", `context window exceeded`, 0},
		{"tiny value rejected", `maximum context length is 12 tokens`, 0},
		{"huge value rejected", `maximum context length is 99999999999 tokens`, 0},
		{"empty", ``, 0},
	}
	for _, tc := range cases {
		if got := ParseContextLimit(tc.body); got != tc.want {
			t.Errorf("%s: ParseContextLimit = %d, want %d", tc.name, got, tc.want)
		}
	}
}
