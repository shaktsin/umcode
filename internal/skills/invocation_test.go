package skills

import "testing"

func TestInvocation(t *testing.T) {
	for _, tc := range []struct {
		text, name string
		ok         bool
	}{
		{"/greet", "greet", true},
		{"/demo:greet write a message", "demo:greet", true},
		{"/demo:greet\nwrite a message", "demo:greet", true},
		{"/tmp/file", "", false},
		{"hello /greet", "", false},
		{"https://example.com", "", false},
		{"/", "", false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			name, ok := Invocation(tc.text)
			if name != tc.name || ok != tc.ok {
				t.Fatalf("got %q, %v; want %q, %v", name, ok, tc.name, tc.ok)
			}
		})
	}
}
