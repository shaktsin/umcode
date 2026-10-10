package retrieval

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// BuildQuery uses a bounded literal OR vocabulary, never caller FTS syntax.
func BuildQuery(request string, taskTitles []string) Query {
	if !utf8.ValidString(request) {
		return Query{}
	}
	input := request
	for _, title := range taskTitles {
		if len(input) >= MaxInputBytes {
			break
		}
		if utf8.ValidString(title) {
			input += " " + title
		}
	}
	if len(input) > MaxInputBytes {
		input = input[:MaxInputBytes]
		for !utf8.ValidString(input) {
			input = input[:len(input)-1]
		}
	}
	var q Query
	seen := map[string]bool{}
	hints := strings.Fields(input)
	for _, h := range hints {
		h = strings.Trim(h, "\"'`(),;:")
		if len(h) > MaxTermBytes {
			continue
		}
		if strings.Contains(h, "/") && !strings.HasPrefix(h, "/") && !strings.Contains(h, "..") {
			q.Paths = appendUnique(q.Paths, h)
		}
		if strings.IndexFunc(h, unicode.IsUpper) >= 0 && strings.IndexFunc(h, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') }) < 0 {
			q.Symbols = appendUnique(q.Symbols, h)
		}
	}
	for _, term := range strings.FieldsFunc(input, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') }) {
		term = strings.ToLower(term)
		if len(term) < 2 || len(term) > MaxTermBytes || seen[term] || stopWord(term) {
			continue
		}
		seen[term] = true
		q.Terms = append(q.Terms, term)
		if len(q.Terms) == MaxTerms {
			break
		}
	}
	var parts []string
	for _, term := range q.Terms {
		parts = append(parts, `"`+term+`"`)
	}
	q.FTS = strings.Join(parts, " OR ")
	return q
}
func appendUnique(xs []string, s string) []string {
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	if len(xs) < MaxTerms {
		return append(xs, s)
	}
	return xs
}
func stopWord(s string) bool {
	switch s {
	case "and", "or", "not", "near", "the", "for", "with", "this", "that", "from", "please":
		return true
	}
	return false
}
