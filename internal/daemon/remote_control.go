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

const remoteControlBusyMessage = "vix remote control is busy; try again after the current run finishes."

type remoteControl struct {
	server    *Server
	cfg       RemoteControlConfig
	http      remoteHTTPClient
	runs      chan struct{}
	runPrompt func(context.Context, string, string, string) (string, error)
}

func newRemoteControl(server *Server, cfg RemoteControlConfig, hc remoteHTTPClient) *remoteControl {
	if hc == nil {
		hc = http.DefaultClient
	}
	rc := &remoteControl{
		server: server,
		cfg:    cfg,
		http:   hc,
		runs:   make(chan struct{}, cfg.maxConcurrentRuns()),
	}
	if server != nil {
		rc.runPrompt = server.runRemotePrompt
	}
	return rc
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
	rc := newRemoteControl(s, cfg, nil)
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
	if !rc.tryAcquireRun() {
		if err := msg.Reply(ctx, remoteControlBusyMessage); err != nil {
			LogError("remote control: reply to %s %s failed: %v", msg.Provider, msg.SenderID, err)
		}
		return
	}

	LogInfo("remote control: received %s message from %s", msg.Provider, msg.SenderID)
	var result string
	var err error
	func() {
		defer rc.releaseRun()
		result, err = rc.runPrompt(ctx, rc.cfg.CWD, rc.cfg.Workflow, text)
	}()
	if err != nil {
		result = "vix remote control error: " + err.Error()
		LogError("remote control: %s", err)
	}
	if err := msg.Reply(ctx, result); err != nil {
		LogError("remote control: reply to %s %s failed: %v", msg.Provider, msg.SenderID, err)
	}
}

func (rc *remoteControl) tryAcquireRun() bool {
	select {
	case rc.runs <- struct{}{}:
		return true
	default:
		return false
	}
}

func (rc *remoteControl) releaseRun() {
	select {
	case <-rc.runs:
	default:
	}
}

func (s *Server) runRemotePrompt(ctx context.Context, cwd, workflow, prompt string) (string, error) {
	req := unattendedRunRequest{
		Model:     s.model,
		CWD:       cwd,
		Title:     "Remote control - " + time.Now().Format(jobTitleTimeFormat),
		Trigger:   &protocol.TriggerInfo{Type: "remote_control", Ref: "remote"},
		Prompt:    prompt,
		AutoWrite: false,
		AutoDirs:  false,
	}
	if workflow != "" {
		req.Workflow.Name = workflow
	}
	res := s.runUnattendedSession(ctx, req, remoteUnattendedPolicy)
	return remotePromptResultFromUnattended(res)
}

func remotePromptResultFromUnattended(res unattendedRunResult) (string, error) {
	if res.HadError {
		if strings.TrimSpace(res.FinalText) != "" && res.ErrSource == "agent" {
			return res.FinalText, nil
		}
		if strings.TrimSpace(res.Err) == "" {
			return res.FinalText, errors.New("remote control run failed")
		}
		return res.FinalText, errors.New(res.Err)
	}
	return res.FinalText, nil
}

func remoteUnattendedPolicy(ctx context.Context, session *Session, ev protocol.SessionEvent) (bool, error) {
	cmd, err := remoteUnattendedPolicyCommand(ev)
	if err != nil {
		return true, err
	}
	return session.pushCommand(ctx, cmd), nil
}

func remoteUnattendedPolicyCommand(ev protocol.SessionEvent) (protocol.SessionCommand, error) {
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
