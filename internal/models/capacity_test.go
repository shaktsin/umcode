package models

import (
	"strings"
	"testing"
)

func TestEndpointIdentityRedactsSecrets(t *testing.T) {
	a, err := EndpointIdentity("openai_compatible", "http://alice:secret@LOCALHOST:8080/v1/?token=hidden#fragment")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 64 || strings.Contains(a, "secret") || strings.Contains(a, "hidden") {
		t.Fatalf("identity exposes URL material: %q", a)
	}
	b, err := EndpointIdentity("openai_compatible", "http://alice:secret@localhost:8080/v1?token=hidden")
	if err != nil || a != b {
		t.Fatal("host/trailing-slash normalization is unstable", err)
	}
	c, _ := EndpointIdentity("openai_compatible", "http://localhost:8080/proxy/v1")
	if c == a {
		t.Fatal("proxy path conflated")
	}
	if _, err := EndpointIdentity("openai_compatible", "file:///private/secret"); err == nil {
		t.Fatal("non-http endpoint accepted")
	}
}

func TestEndpointIdentityNormalizesScheme(t *testing.T) {
	a, err := EndpointIdentity("openai_compatible", "HTTP://LOCALHOST:80/v1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := EndpointIdentity("openai_compatible", "http://localhost/v1/")
	if err != nil || a != b {
		t.Fatal("equivalent default endpoint identities differ", err)
	}
}
