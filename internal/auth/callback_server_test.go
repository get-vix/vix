package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCallbackHandlerSuccess(t *testing.T) {
	results := make(chan callbackResult, 1)
	h := newCallbackHandler("/callback", "expected-state", "done", results)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/callback?code=thecode&state=expected-state")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	select {
	case r := <-results:
		if r.code != "thecode" || r.state != "expected-state" {
			t.Errorf("delivered %+v", r)
		}
	default:
		t.Fatal("expected a delivered result")
	}
}

func TestCallbackHandlerStateMismatch(t *testing.T) {
	results := make(chan callbackResult, 1)
	h := newCallbackHandler("/callback", "expected-state", "done", results)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/callback?code=thecode&state=wrong")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if len(results) != 0 {
		t.Errorf("should not deliver on state mismatch")
	}
}

func TestCallbackHandlerMissingCode(t *testing.T) {
	results := make(chan callbackResult, 1)
	h := newCallbackHandler("/callback", "st", "done", results)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/callback?state=st")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestCallbackHandlerErrorParam(t *testing.T) {
	results := make(chan callbackResult, 1)
	h := newCallbackHandler("/callback", "st", "done", results)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/callback?error=access_denied")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), "access_denied") {
		t.Errorf("expected error detail in body")
	}
}

func TestCallbackHandlerWrongPath(t *testing.T) {
	results := make(chan callbackResult, 1)
	h := newCallbackHandler("/callback", "st", "done", results)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/nope?code=c&state=st")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func newTestCallbackServer() *callbackServer {
	return &callbackServer{
		results:  make(chan callbackResult, 1),
		cancelCh: make(chan struct{}),
	}
}

func TestWaitForCodeDelivers(t *testing.T) {
	cs := newTestCallbackServer()
	cs.results <- callbackResult{code: "c", state: "s"}
	r, ok := cs.waitForCode(context.Background())
	if !ok || r.code != "c" {
		t.Errorf("got %+v ok=%v", r, ok)
	}
}

func TestWaitForCodeCancelWait(t *testing.T) {
	cs := newTestCallbackServer()
	cs.cancelWait()
	if _, ok := cs.waitForCode(context.Background()); ok {
		t.Errorf("expected ok=false after cancelWait")
	}
}

func TestWaitForCodeContextDone(t *testing.T) {
	cs := newTestCallbackServer()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := cs.waitForCode(ctx); ok {
		t.Errorf("expected ok=false after ctx cancel")
	}
}

func TestWaitForAuthorizationCodeServerWins(t *testing.T) {
	cs := newTestCallbackServer()
	cs.results <- callbackResult{code: "browsercode", state: "st"}
	code, state, err := waitForAuthorizationCode(context.Background(), cs, LoginCallbacks{}, "st")
	if err != nil || code != "browsercode" || state != "st" {
		t.Errorf("code=%q state=%q err=%v", code, state, err)
	}
}

func TestWaitForAuthorizationCodeManualWins(t *testing.T) {
	cs := newTestCallbackServer()
	cb := LoginCallbacks{
		OnManualCodeInput: func() (string, error) { return "manualcode", nil },
	}
	code, state, err := waitForAuthorizationCode(context.Background(), cs, cb, "st")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if code != "manualcode" || state != "st" {
		t.Errorf("code=%q state=%q", code, state)
	}
}

func TestWaitForAuthorizationCodeManualError(t *testing.T) {
	cs := newTestCallbackServer()
	cb := LoginCallbacks{
		OnManualCodeInput: func() (string, error) { return "", errors.New("paste failed") },
	}
	_, _, err := waitForAuthorizationCode(context.Background(), cs, cb, "st")
	if err == nil || err.Error() != "paste failed" {
		t.Errorf("err = %v, want paste failed", err)
	}
}

func TestWaitForAuthorizationCodeManualStateMismatch(t *testing.T) {
	cs := newTestCallbackServer()
	cb := LoginCallbacks{
		OnManualCodeInput: func() (string, error) { return "code#otherstate", nil },
	}
	_, _, err := waitForAuthorizationCode(context.Background(), cs, cb, "st")
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Errorf("err = %v, want state mismatch", err)
	}
}
