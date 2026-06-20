package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const telegramAPIBase = "https://api.telegram.org"

type telegramUpdatesResponse struct {
	OK     bool             `json:"ok"`
	Result []telegramUpdate `json:"result"`
}

type telegramUpdate struct {
	UpdateID int64           `json:"update_id"`
	Message  telegramMessage `json:"message"`
}

type telegramMessage struct {
	Text string       `json:"text"`
	Chat telegramChat `json:"chat"`
}

type telegramChat struct {
	ID int64 `json:"id"`
}

func (rc *remoteControl) startTelegram(ctx context.Context) error {
	cfg := rc.cfg.Telegram
	if strings.TrimSpace(cfg.BotToken) == "" {
		return fmt.Errorf("remote control telegram: missing bot_token")
	}
	if len(cfg.AllowedChatIDs) == 0 {
		return fmt.Errorf("remote control telegram: missing allowed_chat_ids")
	}
	interval := time.Duration(cfg.PollIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = 2 * time.Second
	}
	go rc.pollTelegram(ctx, cfg, interval)
	LogInfo("remote control: telegram polling enabled")
	return nil
}

func (rc *remoteControl) pollTelegram(ctx context.Context, cfg TelegramRemoteConfig, interval time.Duration) {
	var offset int64
	for {
		updates, err := rc.getTelegramUpdates(ctx, cfg.BotToken, offset)
		if err != nil {
			LogError("remote control telegram: getUpdates failed: %v", err)
		} else {
			for _, upd := range updates {
				if upd.UpdateID >= offset {
					offset = upd.UpdateID + 1
				}
				chatID := strconv.FormatInt(upd.Message.Chat.ID, 10)
				if !authorizedRemoteID(chatID, cfg.AllowedChatIDs) || strings.TrimSpace(upd.Message.Text) == "" {
					continue
				}
				msg := remoteMessage{
					Provider: "telegram",
					SenderID: chatID,
					Text:     upd.Message.Text,
					Reply: func(replyCtx context.Context, text string) error {
						return rc.sendTelegramMessage(replyCtx, cfg.BotToken, chatID, text)
					},
				}
				go rc.handleMessage(ctx, msg)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (rc *remoteControl) getTelegramUpdates(ctx context.Context, token string, offset int64) ([]telegramUpdate, error) {
	endpoint := telegramAPIBase + "/bot" + token + "/getUpdates"
	q := url.Values{}
	q.Set("timeout", "20")
	if offset > 0 {
		q.Set("offset", strconv.FormatInt(offset, 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := rc.http.Do(req)
	if err != nil {
		return nil, redactTelegramError(err, token)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("telegram getUpdates returned %s", resp.Status)
	}
	var out telegramUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram getUpdates returned ok=false")
	}
	return out.Result, nil
}

func (rc *remoteControl) sendTelegramMessage(ctx context.Context, token, chatID, text string) error {
	endpoint := telegramAPIBase + "/bot" + token + "/sendMessage"
	if err := postForm(ctx, rc.http, endpoint, url.Values{"chat_id": {chatID}, "text": {text}}); err != nil {
		return redactTelegramError(err, token)
	}
	return nil
}

func redactTelegramError(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), token, "[REDACTED]"))
}
