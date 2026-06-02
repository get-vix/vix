package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	if got := redact(""); got != "<empty>" {
		t.Errorf("redact(empty) = %q", got)
	}
	got := redact("supersecrettoken")
	if strings.Contains(got, "supersecrettoken") {
		t.Errorf("redact leaked the secret: %q", got)
	}
	if got != "<redacted len=16>" {
		t.Errorf("redact = %q, want <redacted len=16>", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 100); got != "short" {
		t.Errorf("truncate short = %q", got)
	}
	got := truncate(strings.Repeat("x", 50), 10)
	if !strings.HasSuffix(got, "(truncated)") || len(got) > 30 {
		t.Errorf("truncate long = %q", got)
	}
}

func TestHTTPErrorIsLogged(t *testing.T) {
	logs := captureLogs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()

	_, _ = postJSONForToken(context.Background(), srv.URL, map[string]any{"grant_type": "x"})

	out := logs.String()
	if !strings.Contains(out, "non-2xx") || !strings.Contains(out, "status=400") {
		t.Errorf("expected non-2xx status log, got:\n%s", out)
	}
	if !strings.Contains(out, "invalid_grant") {
		t.Errorf("expected error body in log, got:\n%s", out)
	}
}

func TestTokenExchangeLogsAreRedacted(t *testing.T) {
	logs := captureLogs(t)
	const secret = "supersecret-access-token-value"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"` + secret + `","refresh_token":"rrr-secret","expires_in":3600}`))
	}))
	defer srv.Close()

	p := newAnthropicProvider()
	p.tokenURL = srv.URL
	if _, err := p.exchangeAuthorizationCode(context.Background(), "c", "s", "v"); err != nil {
		t.Fatalf("exchange: %v", err)
	}

	out := logs.String()
	if !strings.Contains(out, "token exchange succeeded") {
		t.Errorf("expected success log, got:\n%s", out)
	}
	if strings.Contains(out, secret) || strings.Contains(out, "rrr-secret") {
		t.Errorf("logs leaked a token:\n%s", out)
	}
	if !strings.Contains(out, "<redacted len=") {
		t.Errorf("expected redaction marker, got:\n%s", out)
	}
}

func TestLoginFailureIsLogged(t *testing.T) {
	logs := captureLogs(t)
	registerStub(t, &stubProvider{
		id:   "stub",
		name: "Stub",
	}) // login func nil -> returns an error
	s := NewStorage(NewMemoryBackend())

	if err := s.Login(context.Background(), "stub", LoginCallbacks{}); err == nil {
		t.Fatal("expected login error")
	}
	out := logs.String()
	if !strings.Contains(out, "login: starting") || !strings.Contains(out, "login: flow failed") {
		t.Errorf("expected start + failure logs, got:\n%s", out)
	}
}
