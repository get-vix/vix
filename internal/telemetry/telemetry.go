package telemetry

import (
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/posthog/posthog-go"
	"github.com/zalando/go-keyring"
)

const (
	posthogHost    = "https://us.i.posthog.com"
	keyringService = "vix"
	keyringUser    = "device-id"
)

// Config controls telemetry initialization.
type Config struct {
	Version string // build version
	Mode    string // "tui", "headless", "daemon"
	Enabled bool   // false disables all telemetry (from settings.json feature flag)
}

// embeddedAPIKey is the PostHog project key injected at build time via
// -ldflags "-X .../telemetry.embeddedAPIKey=<key>" (see script/build.sh, which
// reads VIX_POSTHOG_API_KEY from the environment or .env). It is empty in plain
// `go build` / dev builds, which keeps telemetry inert there. There is no
// runtime env var or .env fallback — the key lives only in the binary.
var embeddedAPIKey string

var (
	client    posthog.Client
	deviceID  string
	enabled   bool
	version   = "dev" // overwritten by Init(); "dev" means an unstamped local build
	mode      string
	initOnce  sync.Once
	closeOnce sync.Once
)

// Version returns the build version string set via Init (or "dev" if unset).
func Version() string {
	return version
}

// Init initializes the telemetry client. Safe to call multiple times; only the first call takes effect.
func Init(cfg Config) {
	initOnce.Do(func() {
		version = cfg.Version
		mode = cfg.Mode

		// Check opt-out: env var or settings.json feature flag
		if !cfg.Enabled {
			return
		}
		if isOptedOut() {
			return
		}

		// The analytics key is embedded at build time (see script/build.sh).
		// It is empty in plain dev builds, which keeps telemetry inert there.
		if embeddedAPIKey == "" {
			return
		}

		// Load or create device ID
		id, err := GetOrCreateDeviceID()
		if err != nil {
			logDebug("[telemetry] failed to get device ID: %v", err)
			return
		}
		deviceID = id

		c, err := posthog.NewWithConfig(embeddedAPIKey, posthog.Config{
			Endpoint:  posthogHost,
			BatchSize: 20,
			Interval:  30 * time.Second,
			// Bound Close() so a flush-on-crash can't hang the dying
			// process indefinitely on a dead network (Close waits forever
			// when ShutdownTimeout is unset).
			ShutdownTimeout: 3 * time.Second,
		})
		if err != nil {
			logDebug("[telemetry] failed to create PostHog client: %v", err)
			return
		}
		client = c
		enabled = true
		logDebug("[telemetry] initialized (device=%s, mode=%s)", deviceID, mode)
	})
}

// Shutdown flushes pending events and closes the client.
func Shutdown() {
	closeOnce.Do(func() {
		if client != nil {
			logDebug("[telemetry] flushing and shutting down")
			client.Close()
		}
	})
}

// Enabled reports whether telemetry is active.
func Enabled() bool {
	return enabled
}

// Track sends an event to PostHog with common properties merged in.
func Track(event string, properties map[string]interface{}) {
	if !enabled {
		return
	}

	props := commonProps()

	// Merge caller properties
	for k, v := range properties {
		props.Set(k, v)
	}

	client.Enqueue(posthog.Capture{
		DistinctId: deviceID,
		Event:      event,
		Properties: props,
	})
}

// commonProps returns the build/runtime properties attached to every event.
func commonProps() posthog.Properties {
	return posthog.NewProperties().
		Set("version", version).
		Set("os", runtime.GOOS).
		Set("arch", runtime.GOARCH).
		Set("mode", mode)
}

// CaptureException reports an error or panic to PostHog error tracking.
// typ is the title shown in the UI (e.g. "panic"); value is the description.
// extra carries additional context properties (e.g. the raw goroutine stack)
// that surface in the exception's Properties tab. Safe to call when telemetry
// is disabled (no-op).
//
// We build the $exception event by hand via posthog.Capture rather than
// posthog.NewDefaultException: the SDK's Exception type exposes no arbitrary
// properties map, so common/extra context could not otherwise reach the
// Properties tab. PostHog's error tracking ingests any event named "$exception"
// that carries an "$exception_list" property, so this is equivalent to what
// Exception.APIfy() emits, plus our extra properties.
func CaptureException(typ, value string, extra map[string]interface{}) {
	if !enabled {
		return
	}

	// GetStackTrace's skip is measured from the live stack at this point.
	// Call chain is runtime.Callers(0) -> GetStackTrace(1) -> CaptureException(2)
	// -> TrackPanic(3) -> the recover site(4), so skip=4 drops the telemetry
	// plumbing and starts at application code. This is faithful to the current
	// sole caller (TrackPanic); the raw debug.Stack() passed via extra remains
	// the authoritative trace regardless.
	stack := posthog.DefaultStackTraceExtractor{InAppDecider: posthog.SimpleInAppDecider}.GetStackTrace(4)

	props := commonProps()
	props.Set("$exception_list", []posthog.ExceptionItem{{
		Type:       typ,
		Value:      value,
		Stacktrace: stack,
	}})
	for k, v := range extra {
		props.Set(k, v)
	}

	client.Enqueue(posthog.Capture{
		DistinctId: deviceID,
		Event:      "$exception",
		Properties: props,
	})
}

// HashWorkflowName returns a truncated SHA256 hex digest of the workflow name.
func HashWorkflowName(name string) string {
	h := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%x", h[:6]) // 12 hex chars
}

// logDebug logs only when running a dev build.
func logDebug(format string, args ...any) {
	if version == "dev" {
		log.Printf(format, args...)
	}
}

// isOptedOut checks whether the user has disabled telemetry.
func isOptedOut() bool {
	v := strings.ToLower(os.Getenv("VIX_TELEMETRY"))
	return v == "off" || v == "false" || v == "0"
}

// GetOrCreateDeviceID loads the device ID from the system keychain,
// or generates and stores a new one if none exists.
func GetOrCreateDeviceID() (string, error) {
	id, err := keyring.Get(keyringService, keyringUser)
	if err == nil && id != "" {
		return id, nil
	}

	id = uuid.New().String()
	if err := keyring.Set(keyringService, keyringUser, id); err != nil {
		return "", fmt.Errorf("failed to store device ID: %w", err)
	}
	return id, nil
}
