package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGitHubCopilotProviderBasics(t *testing.T) {
	p := newGitHubCopilotProvider()
	if p.ID() != "github-copilot" {
		t.Errorf("ID = %q", p.ID())
	}
	if p.UsesCallbackServer() {
		t.Errorf("expected UsesCallbackServer false")
	}
	if p.clientID != "Iv1.b507a08c87ecfe98" {
		t.Errorf("clientID = %q", p.clientID)
	}
}

func TestGitHubCopilotBaseURL(t *testing.T) {
	cases := []struct {
		name       string
		token      string
		enterprise string
		want       string
	}{
		{"from token proxy-ep", "tid=x;exp=1;proxy-ep=proxy.individual.githubcopilot.com;y=2", "", "https://api.individual.githubcopilot.com"},
		{"enterprise fallback", "", "company.ghe.com", "https://copilot-api.company.ghe.com"},
		{"default", "", "", "https://api.individual.githubcopilot.com"},
		{"token without proxy falls back", "tid=x;exp=1", "", "https://api.individual.githubcopilot.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GitHubCopilotBaseURL(tc.token, tc.enterprise); got != tc.want {
				t.Errorf("GitHubCopilotBaseURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGitHubCopilotStartDeviceFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"UC-123","verification_uri":"https://github.com/login/device","interval":5,"expires_in":900}`))
	}))
	defer srv.Close()

	p := newGitHubCopilotProvider()
	p.resolveURLs = func(string) copilotURLs { return copilotURLs{deviceCode: srv.URL} }

	dc, err := p.startDeviceFlow(context.Background(), "github.com")
	if err != nil {
		t.Fatalf("startDeviceFlow: %v", err)
	}
	if dc.deviceCode != "dc" || dc.userCode != "UC-123" || dc.interval != 5 || dc.expiresIn != 900 {
		t.Errorf("device = %+v", dc)
	}
}

func TestGitHubCopilotLogin(t *testing.T) {
	setNow(t, 5_000_000)
	setSleep(t, func(context.Context, time.Duration) error { return nil })

	var pollCount int32
	mux := http.NewServeMux()
	mux.HandleFunc("/device/code", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"UC","verification_uri":"https://github.com/login/device","interval":1,"expires_in":900}`))
	})
	mux.HandleFunc("/access_token", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&pollCount, 1) == 1 {
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"gh_tok"}`))
	})
	mux.HandleFunc("/copilot_token", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer gh_tok" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"token":"cop_tok","expires_at":1000}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := newGitHubCopilotProvider()
	p.resolveURLs = func(string) copilotURLs {
		return copilotURLs{
			deviceCode:   srv.URL + "/device/code",
			accessToken:  srv.URL + "/access_token",
			copilotToken: srv.URL + "/copilot_token",
		}
	}

	cb := LoginCallbacks{
		OnPrompt:     func(Prompt) (string, error) { return "", nil }, // blank -> github.com
		OnDeviceCode: func(DeviceCodeInfo) {},
	}

	creds, err := p.Login(context.Background(), cb)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if creds.Access != "cop_tok" || creds.Refresh != "gh_tok" {
		t.Errorf("creds = %+v", creds)
	}
	if creds.Expires != 1000*1000-300000 {
		t.Errorf("expires = %d, want 700000", creds.Expires)
	}
	if creds.StringExtra("enterpriseUrl") != "" {
		t.Errorf("did not expect enterpriseUrl for github.com")
	}
}

func TestGitHubCopilotInvalidEnterprise(t *testing.T) {
	p := newGitHubCopilotProvider()
	cb := LoginCallbacks{
		OnPrompt: func(Prompt) (string, error) { return "not a domain with spaces", nil },
	}
	if _, err := p.Login(context.Background(), cb); err == nil {
		t.Fatal("expected error for invalid enterprise domain")
	}
}
