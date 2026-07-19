package daemon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const defaultWhatsAppGraphAPIVersion = "v20.0"

type whatsappWebhookPayload struct {
	Entry []struct {
		Changes []struct {
			Value struct {
				Messages []struct {
					From string `json:"from"`
					Text struct {
						Body string `json:"body"`
					} `json:"text"`
					Type string `json:"type"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

func (cfg WhatsAppRemoteConfig) graphAPIVersion() string {
	v := strings.TrimSpace(cfg.GraphAPIVersion)
	if v == "" {
		return defaultWhatsAppGraphAPIVersion
	}
	return v
}

func (rc *remoteControl) startWhatsApp(ctx context.Context) error {
	cfg := rc.cfg.WhatsApp
	if strings.TrimSpace(cfg.AccessToken) == "" {
		return fmt.Errorf("remote control whatsapp: missing access_token")
	}
	if strings.TrimSpace(cfg.AppSecret) == "" {
		return fmt.Errorf("remote control whatsapp: missing app_secret")
	}
	if strings.TrimSpace(cfg.PhoneNumberID) == "" {
		return fmt.Errorf("remote control whatsapp: missing phone_number_id")
	}
	if strings.TrimSpace(cfg.VerifyToken) == "" {
		return fmt.Errorf("remote control whatsapp: missing verify_token")
	}
	if len(cfg.AllowedContacts) == 0 {
		return fmt.Errorf("remote control whatsapp: missing allowed_contacts")
	}
	addr := strings.TrimSpace(cfg.WebhookAddr)
	if addr == "" {
		addr = "127.0.0.1:1340"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("remote control whatsapp: listen %s: %w", addr, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/whatsapp", rc.whatsAppWebhookHandler(ctx, cfg))
	server := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		LogInfo("remote control: whatsapp webhook listening on %s", addr)
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			LogError("remote control whatsapp: webhook server failed: %v", err)
		}
	}()
	return nil
}

func (rc *remoteControl) whatsAppWebhookHandler(ctx context.Context, cfg WhatsAppRemoteConfig) http.HandlerFunc {
	return rc.whatsAppWebhookHandlerWithHandle(ctx, cfg, rc.handleMessage)
}

func (rc *remoteControl) whatsAppWebhookHandlerWithHandle(ctx context.Context, cfg WhatsAppRemoteConfig, handle func(context.Context, remoteMessage)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			mode := r.URL.Query().Get("hub.mode")
			verifyToken := r.URL.Query().Get("hub.verify_token")
			challenge := r.URL.Query().Get("hub.challenge")
			if mode == "subscribe" && verifyToken == cfg.VerifyToken {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(challenge))
				return
			}
			http.Error(w, "forbidden", http.StatusForbidden)
		case http.MethodPost:
			defer r.Body.Close()
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if !validWhatsAppSignature(body, r.Header.Get("X-Hub-Signature-256"), cfg.AppSecret) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			var payload whatsappWebhookPayload
			if err := json.Unmarshal(body, &payload); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			for _, entry := range payload.Entry {
				for _, change := range entry.Changes {
					for _, wm := range change.Value.Messages {
						if wm.Type != "text" || !authorizedRemoteID(wm.From, cfg.AllowedContacts) || strings.TrimSpace(wm.Text.Body) == "" {
							continue
						}
						from := wm.From
						msg := remoteMessage{
							Provider: "whatsapp",
							SenderID: from,
							Text:     wm.Text.Body,
							Reply: func(replyCtx context.Context, text string) error {
								return rc.sendWhatsAppMessage(replyCtx, cfg, from, text)
							},
						}
						go handle(ctx, msg)
					}
				}
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func validWhatsAppSignature(body []byte, signature, appSecret string) bool {
	if !strings.HasPrefix(signature, "sha256=") || appSecret == "" {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(appSecret))
	_, _ = mac.Write(body)
	want := mac.Sum(nil)
	return hmac.Equal(got, want)
}

func (rc *remoteControl) sendWhatsAppMessage(ctx context.Context, cfg WhatsAppRemoteConfig, to, text string) error {
	endpoint := fmt.Sprintf("https://graph.facebook.com/%s/%s/messages", cfg.graphAPIVersion(), cfg.PhoneNumberID)
	payload := map[string]any{
		"messaging_product": "whatsapp",
		"to":                to,
		"type":              "text",
		"text": map[string]string{
			"body": text,
		},
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := rc.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("whatsapp send returned %s", resp.Status)
	}
	return nil
}
