package auth

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Auth-flow logging.
//
// Every OAuth flow always writes a detailed, structured, redacted trace so that
// a failure can be diagnosed after the fact without reproducing it. No env var
// or flag is needed to enable it. The trace goes to ~/.vix/logs/auth.log (see
// AuthLogPath), or VIX_AUTH_LOG=/path to redirect the file. Secrets (tokens,
// auth codes, PKCE verifiers, device codes) are never written — only their
// presence and length. `tail -f ~/.vix/logs/auth.log` to watch it live.
var (
	logMu      sync.Mutex
	authLogger *slog.Logger
)

// SetLogger overrides the logger used by the auth package. Pass nil to restore
// the default file logger. Used by the daemon to merge auth logs into its own
// stream, and by tests to capture or discard output.
func SetLogger(l *slog.Logger) {
	logMu.Lock()
	defer logMu.Unlock()
	authLogger = l
}

// lg returns the active logger, lazily building the default on first use.
func lg() *slog.Logger {
	logMu.Lock()
	defer logMu.Unlock()
	if authLogger == nil {
		authLogger = newDefaultLogger()
	}
	return authLogger
}

// AuthLogPath returns the path of the default auth log file, honouring
// VIX_AUTH_LOG, or "" if the home directory cannot be determined.
func AuthLogPath() string {
	if p := os.Getenv("VIX_AUTH_LOG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".vix", "logs", "auth.log")
}

func newDefaultLogger() *slog.Logger {
	// Always log full detail to the auth log file. Fall back to stderr only if
	// the file cannot be opened (e.g. an unwritable home directory).
	var w io.Writer = os.Stderr
	if f := openAuthLogFile(); f != nil {
		w = f
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func openAuthLogFile() io.Writer {
	p := AuthLogPath()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	return f
}

// redact returns a non-secret description of a sensitive value: its presence
// and length, never its contents.
func redact(secret string) string {
	if secret == "" {
		return "<empty>"
	}
	return fmt.Sprintf("<redacted len=%d>", len(secret))
}

// truncate bounds a string for logging (used for error response bodies, which
// never contain secrets — token success responses are never logged).
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}
