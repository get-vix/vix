package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/protocol"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestAuthorizedRemoteIDRequiresAllowlist(t *testing.T) {
	if authorizedRemoteID("123", nil) {
		t.Fatal("empty allowlist must deny")
	}
	if authorizedRemoteID("123", []string{"456"}) {
		t.Fatal("unlisted sender must deny")
	}
	if !authorizedRemoteID("123", []string{" 123 "}) {
		t.Fatal("listed sender should be authorized")
	}
}

func TestRemoteSessionOptionsDisableAutomaticPermissions(t *testing.T) {
	autoWrite, autoDirs := remoteSessionAutomaticPermissions()
	if autoWrite || autoDirs {
		t.Fatalf("remote automatic permissions = write:%v dirs:%v, want both false", autoWrite, autoDirs)
	}
}

func TestRemoteUnattendedEventsDoNotAutoApprove(t *testing.T) {
	cmd, err := remoteCommandForUnattendedEvent(protocol.SessionEvent{Type: "event.confirm_request"})
	if err != nil {
		t.Fatalf("confirm request handling: %v", err)
	}
	if cmd.Type != "session.confirm" || !strings.Contains(string(cmd.Data), `"approved":false`) {
		t.Fatalf("confirm request command = %+v data=%s, want denial", cmd, string(cmd.Data))
	}

	_, err = remoteCommandForUnattendedEvent(protocol.SessionEvent{Type: "event.plan_proposed"})
	if err == nil || !strings.Contains(err.Error(), "requires interactive approval") {
		t.Fatalf("plan proposal err = %v, want interactive approval error", err)
	}

	_, err = remoteCommandForUnattendedEvent(protocol.SessionEvent{Type: "event.user_question"})
	if err == nil || !strings.Contains(err.Error(), "requires an interactive answer") {
		t.Fatalf("user question err = %v, want interactive answer error", err)
	}
}

func TestTelegramSendMessageRedactsBotTokenFromErrors(t *testing.T) {
	secret := "123456:secret-token"
	rc := &remoteControl{http: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Post", URL: req.URL.String(), Err: fmt.Errorf("dial failed")}
	})}

	err := rc.sendTelegramMessage(context.Background(), secret, "42", "hello")
	if err == nil {
		t.Fatal("sendTelegramMessage error = nil, want redacted error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked bot token: %v", err)
	}
}

func TestWhatsAppStartFailsWhenWebhookAddrAlreadyBound(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	rc := &remoteControl{cfg: RemoteControlConfig{WhatsApp: WhatsAppRemoteConfig{
		Enabled:         true,
		AccessToken:     "access",
		AppSecret:       "secret",
		PhoneNumberID:   "phone",
		VerifyToken:     "verify",
		WebhookAddr:     ln.Addr().String(),
		AllowedContacts: []string{"15551234567"},
	}}}
	if err := rc.startWhatsApp(context.Background()); err == nil {
		t.Fatal("startWhatsApp error = nil, want bind failure")
	}
}

func TestTelegramSendMessagePostsChatIDAndText(t *testing.T) {
	var gotPath, gotBody string
	rc := &remoteControl{http: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotPath = req.URL.Path
		b, _ := io.ReadAll(req.Body)
		gotBody = string(b)
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}

	if err := rc.sendTelegramMessage(context.Background(), "token", "42", "hello vix"); err != nil {
		t.Fatalf("sendTelegramMessage: %v", err)
	}
	if gotPath != "/bottoken/sendMessage" {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, "chat_id=42") || !strings.Contains(gotBody, "text=hello+vix") {
		t.Fatalf("unexpected body %q", gotBody)
	}
}

func TestTelegramGetUpdatesRedactsBotTokenFromErrors(t *testing.T) {
	secret := "123456:secret-token"
	rc := &remoteControl{http: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: req.URL.String(), Err: fmt.Errorf("dial failed")}
	})}

	_, err := rc.getTelegramUpdates(context.Background(), secret, 0)
	if err == nil {
		t.Fatal("getTelegramUpdates error = nil, want redacted error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked bot token: %v", err)
	}
}

func TestWhatsAppWebhookVerificationAndAllowlist(t *testing.T) {
	handled := make(chan remoteMessage, 1)
	rc := &remoteControl{}
	cfg := WhatsAppRemoteConfig{VerifyToken: "verify", AppSecret: "secret", AllowedContacts: []string{"15551234567"}}
	h := rc.whatsAppWebhookHandlerWithHandle(context.Background(), cfg, func(_ context.Context, msg remoteMessage) {
		handled <- msg
	})

	req := httptest.NewRequest(http.MethodGet, "/whatsapp?hub.mode=subscribe&hub.verify_token=verify&hub.challenge=abc", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "abc" {
		t.Fatalf("verification response = %d %q", w.Code, w.Body.String())
	}

	body := `{"entry":[{"changes":[{"value":{"messages":[{"from":"15551234567","type":"text","text":{"body":"run tests"}},{"from":"blocked","type":"text","text":{"body":"ignore"}}]}}]}]}`
	req = httptest.NewRequest(http.MethodPost, "/whatsapp", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", whatsappTestSignature(body, "secret"))
	w = httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("post response = %d", w.Code)
	}
	select {
	case msg := <-handled:
		if msg.SenderID != "15551234567" || msg.Text != "run tests" {
			t.Fatalf("unexpected message: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("handled 0 messages, want 1")
	}
	select {
	case msg := <-handled:
		t.Fatalf("unexpected extra message: %+v", msg)
	default:
	}
}

func whatsappTestSignature(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
