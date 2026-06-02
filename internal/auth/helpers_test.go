package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Keep auth-flow logs out of the real ~/.vix/logs/auth.log during tests.
func init() {
	SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// captureLogs routes auth logs to a buffer for the duration of the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	SetLogger(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil))) })
	return buf
}

// setNow installs a fixed clock for the duration of the test.
func setNow(t *testing.T, ms int64) {
	t.Helper()
	prev := nowMillis
	nowMillis = func() int64 { return ms }
	t.Cleanup(func() { nowMillis = prev })
}

// setNowFunc installs a custom clock function for the duration of the test.
func setNowFunc(t *testing.T, fn func() int64) {
	t.Helper()
	prev := nowMillis
	nowMillis = fn
	t.Cleanup(func() { nowMillis = prev })
}

// setSleep installs a custom sleep function for the duration of the test.
func setSleep(t *testing.T, fn func(context.Context, time.Duration) error) {
	t.Helper()
	prev := sleepCtx
	sleepCtx = fn
	t.Cleanup(func() { sleepCtx = prev })
}

// makeJWT builds an unsigned JWT whose payload is the given claims. Only the
// payload segment matters for the decoders under test.
func makeJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	pb, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	body := base64.RawURLEncoding.EncodeToString(pb)
	return header + "." + body + ".sig"
}
