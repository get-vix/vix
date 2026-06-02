package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAICodexProviderBasics(t *testing.T) {
	p := newOpenAICodexProvider()
	if p.ID() != "openai-codex" {
		t.Errorf("ID = %q", p.ID())
	}
	if !p.UsesCallbackServer() {
		t.Errorf("expected UsesCallbackServer true")
	}
}

func TestOpenAICodexAccountID(t *testing.T) {
	token := makeJWT(t, map[string]any{
		openaiCodexJWTClaimPath: map[string]any{"chatgpt_account_id": "acc_xyz"},
	})
	if got := openaiCodexAccountID(token); got != "acc_xyz" {
		t.Errorf("accountID = %q, want acc_xyz", got)
	}
	if got := openaiCodexAccountID("not-a-jwt"); got != "" {
		t.Errorf("expected empty for non-JWT, got %q", got)
	}
	// JWT without the claim path.
	noClaim := makeJWT(t, map[string]any{"sub": "u"})
	if got := openaiCodexAccountID(noClaim); got != "" {
		t.Errorf("expected empty when claim absent, got %q", got)
	}
}

func TestDecodeJWTClaimsInvalid(t *testing.T) {
	if decodeJWTClaims("a.b") != nil {
		t.Errorf("expected nil for wrong segment count")
	}
	if decodeJWTClaims("a.!!!.c") != nil {
		t.Errorf("expected nil for undecodable payload")
	}
}

func TestCoerceInterval(t *testing.T) {
	cases := []struct {
		in   any
		want int
		ok   bool
	}{
		{float64(5), 5, true},
		{"7", 7, true},
		{"  9 ", 9, true},
		{"x", 0, false},
		{nil, 0, false},
		{true, 0, false},
	}
	for _, tc := range cases {
		got, ok := coerceInterval(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("coerceInterval(%v) = (%d,%v), want (%d,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCodexDeviceErrorCode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`{"error":"slow_down"}`, "slow_down"},
		{`{"error":{"code":"deviceauth_authorization_pending"}}`, "deviceauth_authorization_pending"},
		{`{}`, ""},
		{`not json`, ""},
	}
	for _, tc := range cases {
		if got := codexDeviceErrorCode([]byte(tc.in)); got != tc.want {
			t.Errorf("codexDeviceErrorCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCreateState(t *testing.T) {
	s, err := createState()
	if err != nil {
		t.Fatalf("createState: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(s) {
		t.Errorf("state = %q, want 32 hex chars", s)
	}
	s2, _ := createState()
	if s == s2 {
		t.Errorf("expected distinct states")
	}
}

func TestOpenAICodexExchange(t *testing.T) {
	setNow(t, 3_000_000)
	jwt := makeJWT(t, map[string]any{
		openaiCodexJWTClaimPath: map[string]any{"chatgpt_account_id": "acc_1"},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"` + jwt + `","refresh_token":"ref","expires_in":3600}`))
	}))
	defer srv.Close()

	p := newOpenAICodexProvider()
	p.tokenURL = srv.URL

	creds, err := p.exchangeAuthorizationCode(context.Background(), "code", "verifier", p.redirectURI)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if creds.Access != jwt || creds.Refresh != "ref" {
		t.Errorf("creds = %+v", creds)
	}
	if creds.StringExtra("accountId") != "acc_1" {
		t.Errorf("accountId = %q", creds.StringExtra("accountId"))
	}
	// Codex does not subtract a refresh buffer.
	if creds.Expires != 3_000_000+3600*1000 {
		t.Errorf("expires = %d", creds.Expires)
	}
}

func TestOpenAICodexExchangeMissingAccountID(t *testing.T) {
	jwt := makeJWT(t, map[string]any{"sub": "u"}) // no account id claim
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"` + jwt + `","refresh_token":"ref","expires_in":3600}`))
	}))
	defer srv.Close()

	p := newOpenAICodexProvider()
	p.tokenURL = srv.URL
	if _, err := p.exchangeAuthorizationCode(context.Background(), "c", "v", p.redirectURI); err == nil {
		t.Fatal("expected error when account id missing")
	}
}

func TestOpenAICodexExchangeMissingFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"x"}`)) // missing refresh + expires_in
	}))
	defer srv.Close()

	p := newOpenAICodexProvider()
	p.tokenURL = srv.URL
	if _, err := p.exchangeAuthorizationCode(context.Background(), "c", "v", p.redirectURI); err == nil {
		t.Fatal("expected error for missing fields")
	}
}

func TestOpenAICodexLoginDeviceCode(t *testing.T) {
	setNow(t, 4_000_000)
	setSleep(t, func(context.Context, time.Duration) error { return nil })

	jwt := makeJWT(t, map[string]any{
		openaiCodexJWTClaimPath: map[string]any{"chatgpt_account_id": "acc_d"},
	})
	var pollCount int32
	mux := http.NewServeMux()
	mux.HandleFunc("/usercode", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"device_auth_id":"dai","user_code":"UC","interval":1}`))
	})
	mux.HandleFunc("/devicetoken", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&pollCount, 1) == 1 {
			w.WriteHeader(http.StatusForbidden) // pending
			return
		}
		_, _ = w.Write([]byte(`{"authorization_code":"authcode","code_verifier":"cv"}`))
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"` + jwt + `","refresh_token":"ref","expires_in":60}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := newOpenAICodexProvider()
	p.deviceUserCodeURL = srv.URL + "/usercode"
	p.deviceTokenURL = srv.URL + "/devicetoken"
	p.tokenURL = srv.URL + "/token"

	cb := LoginCallbacks{OnDeviceCode: func(DeviceCodeInfo) {}}
	creds, err := p.loginDeviceCode(context.Background(), cb)
	if err != nil {
		t.Fatalf("loginDeviceCode: %v", err)
	}
	if creds.Access != jwt || creds.Refresh != "ref" {
		t.Errorf("creds = %+v", creds)
	}
	if creds.StringExtra("accountId") != "acc_d" {
		t.Errorf("accountId = %q", creds.StringExtra("accountId"))
	}
}

func TestOpenAICodexLoginSelectCancel(t *testing.T) {
	p := newOpenAICodexProvider()
	cb := LoginCallbacks{OnSelect: func(SelectPrompt) (string, error) { return "", nil }}
	if _, err := p.Login(context.Background(), cb); err == nil {
		t.Fatal("expected cancellation error when no method selected")
	}
}

func TestOpenAICodexLoginUnknownMethod(t *testing.T) {
	p := newOpenAICodexProvider()
	cb := LoginCallbacks{OnSelect: func(SelectPrompt) (string, error) { return "garbage", nil }}
	if _, err := p.Login(context.Background(), cb); err == nil {
		t.Fatal("expected error for unknown login method")
	}
}

func TestOpenAICodexDeviceAuthNotEnabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	p := newOpenAICodexProvider()
	p.deviceUserCodeURL = srv.URL
	if _, err := p.startDeviceAuth(context.Background()); err == nil {
		t.Fatal("expected error when device auth returns 404")
	}
}
