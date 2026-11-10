package llm

import (
	"regexp"
	"strconv"
	"strings"
)

// Provider overflow errors usually state the real serving limit. Parsing it
// lets UMCode learn an endpoint's window without asking the user to configure
// it. Patterns are ordered from most to least specific.
var contextLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`maximum context length is (\d+)`),
	regexp.MustCompile(`n_ctx\D{1,4}(\d+)`),
	regexp.MustCompile(`context (?:size|length|window) of (?:only )?(\d+)`),
	regexp.MustCompile(`>\s*(\d+)\s+maximum`),
}

const (
	minParsedContextLimit = 512
	maxParsedContextLimit = 2147483647
)

// ParseContextLimit extracts the serving context window, in tokens, from a
// provider's context-overflow error text. It returns 0 when no plausible
// limit is stated.
func ParseContextLimit(body string) int {
	lower := strings.ToLower(body)
	for _, pattern := range contextLimitPatterns {
		match := pattern.FindStringSubmatch(lower)
		if match == nil {
			continue
		}
		n, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || n < minParsedContextLimit || n > maxParsedContextLimit {
			continue
		}
		return int(n)
	}
	return 0
}
