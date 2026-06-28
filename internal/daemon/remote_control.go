package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/protocol"
)

// RemoteControlConfig configures opt-in remote control from chat services.
type RemoteControlConfig struct {
	Enabled           bool                 `json:"enabled"`
	CWD               string               `json:"cwd"`
	Workflow          string               `json:"workflow,omitempty"`
	MaxConcurrentRuns int                  `json:"max_concurrent_runs,omitempty"`
	Telegram          TelegramRemoteConfig `json:"telegram,omitempty"`
	WhatsApp          WhatsAppRemoteConfig `json:"whatsapp,omitempty"`
}

func (cfg RemoteControlConfig) maxConcurrentRuns() int {
	if cfg.MaxConcurrentRuns <= 0 {
		return 1
	}
	return cfg.MaxConcurrentRuns
}

type TelegramRemoteConfig struct {
	Enabled        bool     `json:"enabled"`
	BotToken       string   `json:"bot_token"`
	AllowedChatIDs []string `json:"allowed_chat_ids"`
	PollIntervalMs int      `json:"poll_interval_ms,omitempty"`
}

type WhatsAppRemoteConfig struct {
	Enabled         bool     `json:"enabled"`
	AccessToken     string   `json:"access_token"`
	AppSecret       string   `json:"app_secret"`
	PhoneNumberID   string   `json:"phone_number_id"`
	VerifyToken     string   `json:"verify_token"`
	GraphAPIVersion string   `json:"graph_api_version,omitempty"`
	WebhookAddr     string   `json:"webhook_addr,omitempty"`
	AllowedContacts []string `json:"allowed_contacts"`
}

type remoteReplyFunc func(ctx context.Context, text string) error

type remoteMessage struct {
	Provider string
	SenderID string
	Text     string
	Reply    remoteReplyFunc
}

type remoteHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type remoteControl struct {
	server *Server
	cfg    RemoteControlConfig
	http   remoteHTTPClient
}

func LoadRemoteControlConfig() (RemoteControlConfig, error) {
	p := filepath.Join(config.HomeVixDir(), "settings.json")
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return RemoteControlConfig{}, nil
		}
		return RemoteControlConfig{}, err
	}
	var cfg struct {
		RemoteControl RemoteControlConfig `json:"remote_control"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return RemoteControlConfig{}, err
	}
	return cfg.RemoteControl, nil
}

func (s *Server) StartRemoteControl(ctx context.Context, cfg RemoteControlConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(cfg.CWD) == "" {
		return fmt.Errorf("remote control: missing cwd")
	}
	rc := &remoteControl{server: s, cfg: cfg, http: http.DefaultClient}
	started := false
	if cfg.Telegram.Enabled {
		if err := rc.startTelegram(ctx); err != nil {
			return err
		}
		started = true
	}
	if cfg.WhatsApp.Enabled {
		if err := rc.startWhatsApp(ctx); err != nil {
			return err
		}
		started = true
	}
	if !started {
		return fmt.Errorf("remote control: enabled but no provider is enabled")
	}
	return nil
}

func (rc *remoteControl) handleMessage(ctx context.Context, msg remoteMessage) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	LogInfo("remote control: received %s message from %s", msg.Provider, msg.SenderID)
	result, err := rc.server.runRemotePrompt(ctx, rc.cfg.CWD, rc.cfg.Workflow, text)
	if err != nil {
		result = "vix remote control error: " + err.Error()
		LogError("remote control: %s", err)
	}
	if err := msg.Reply(ctx, result); err != nil {
		LogError("remote control: reply to %s %s failed: %v", msg.Provider, msg.SenderID, err)
	}
}

func (s *Server) runRemotePrompt(ctx context.Context, cwd, workflow, prompt string) (string, error) {
	runID := generateSessionID()
	session := NewSession(runID, s, nil, s.model, cwd, "", false, false, false, true, ctx)
	session.origin = "vix"
	session.trigger = &protocol.TriggerInfo{Type: "remote_control", Ref: "remote"}
	session.title = "Remote control - " + time.Now().Format(jobTitleTimeFormat)

	s.sessionMu.Lock()
	s.sessions[runID] = session
	s.sessionMu.Unlock()
	s.broadcastSessionsChanged()
	defer func() {
		s.sessionMu.Lock()
		delete(s.sessions, runID)
		s.sessionMu.Unlock()
		session.cancel()
		s.broadcastSessionsChanged()
	}()

	go session.Run()

	var startCmd protocol.SessionCommand
	if workflow != "" {
		data, _ := json.Marshal(protocol.SessionWorkflowData{Name: workflow, Text: prompt})
		startCmd = protocol.SessionCommand{Type: "session.workflow", Data: data}
	} else {
		data, _ := json.Marshal(protocol.SessionInputData{Text: prompt})
		startCmd = protocol.SessionCommand{Type: "session.input", Data: data}
	}
	if !session.pushCommand(ctx, startCmd) {
		return "", fmt.Errorf("session refused start command")
	}

	var final strings.Builder
	var hadError bool
	var errMsg string
	for {
		select {
		case ev := <-session.eventChan:
			switch ev.Type {
			case "event.stream_chunk":
				final.WriteString(decodeRemoteEvent[protocol.EventStreamChunk](ev.Data).Text)
			case "event.confirm_request", "event.user_question", "event.plan_proposed":
				cmd, err := remoteCommandForUnattendedEvent(ev)
				if err != nil {
					session.persist()
					return "", err
				}
				session.pushCommand(ctx, cmd)
			case "event.error":
				hadError = true
				errMsg = decodeRemoteEvent[protocol.EventError](ev.Data).Message
			case "event.agent_done":
				if hadError && strings.TrimSpace(final.String()) == "" {
					return "", errors.New(errMsg)
				}
				session.persist()
				return final.String(), nil
			}
		case <-ctx.Done():
			session.persist()
			return "", ctx.Err()
		case <-session.ctx.Done():
			if hadError && strings.TrimSpace(final.String()) == "" {
				return "", errors.New(errMsg)
			}
			return final.String(), nil
		}
	}
}

func remoteCommandForUnattendedEvent(ev protocol.SessionEvent) (protocol.SessionCommand, error) {
	switch ev.Type {
	case "event.confirm_request":
		data, _ := json.Marshal(protocol.SessionConfirmData{Approved: false})
		return protocol.SessionCommand{Type: "session.confirm", Data: data}, nil
	case "event.user_question":
		return protocol.SessionCommand{}, fmt.Errorf("remote control requires an interactive answer")
	case "event.plan_proposed":
		return protocol.SessionCommand{}, fmt.Errorf("remote control requires interactive approval")
	default:
		return protocol.SessionCommand{}, fmt.Errorf("unsupported unattended event: %s", ev.Type)
	}
}

func decodeRemoteEvent[T any](data any) T {
	var out T
	raw, _ := json.Marshal(data)
	_ = json.Unmarshal(raw, &out)
	return out
}

func authorizedRemoteID(id string, allowed []string) bool {
	if len(allowed) == 0 {
		return false
	}
	for _, v := range allowed {
		if strings.TrimSpace(v) == id {
			return true
		}
	}
	return false
}

func postForm(ctx context.Context, hc remoteHTTPClient, endpoint string, form url.Values) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("remote provider returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}
