package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/openai/openai-go"
)

// rateLimitRetryAfter returns the server-suggested retry delay for a 429 error,
// reading Retry-After-Ms then Retry-After from the HTTP response headers.
// Falls back to 0 (caller uses its own backoff) if not present or not a 429.
func rateLimitRetryAfter(err error) time.Duration {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 429 || apiErr.Response == nil {
		return 0
	}
	h := apiErr.Response.Header
	if ms := h.Get("Retry-After-Ms"); ms != "" {
		if n, err := strconv.ParseInt(ms, 10, 64); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	if s := h.Get("Retry-After"); s != "" {
		if n, err := strconv.ParseFloat(s, 64); err == nil && n > 0 {
			return time.Duration(n * float64(time.Second))
		}
	}
	return 0
}

// isRateLimitError reports whether err is a 429 rate-limit response from any
// supported provider. Used by retry loops to apply a longer backoff cap for
// subscription-tier accounts whose rate-limit windows exceed 60 s.
func isRateLimitError(err error) bool {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 429
	}
	var oaiErr *openai.Error
	if errors.As(err, &oaiErr) {
		return oaiErr.StatusCode == 429
	}
	var bedrockErr *llm.BedrockHTTPError
	if errors.As(err, &bedrockErr) {
		return bedrockErr.Code == 429
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "rate_limit_error") ||
		strings.Contains(msg, "too many requests")
}

// extractStatusCodeAnthropic returns the HTTP status code from an
// anthropic.Error wrapped in err, or 0 otherwise. Anthropic-only — the
// retry layer dispatches to the active client's classifier once the
// OpenAI/MiniMax adapters land their own status extractors.
func extractStatusCodeAnthropic(err error) int {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// apiErrorMessage extracts the human-readable message from an Anthropic API
// error's raw JSON body. Returns empty string if extraction fails.
func apiErrorMessage(apiErr *anthropic.Error) string {
	raw := apiErr.RawJSON()
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &body) == nil && body.Error.Message != "" {
		return body.Error.Message
	}
	return ""
}

// openAIErrorDetail returns the most informative human-readable detail for an
// OpenAI API error. The SDK populates oaiErr.Message only when the body uses
// the standard {"error":{"message":...}} envelope. The ChatGPT/Codex backend
// instead returns shapes like {"detail":...}, which the SDK unwraps to nothing
// — leaving Message empty and discarding the reason. In that case we fall back
// to reading the original response body (still attached to oaiErr.Response) and
// parse it for "detail" or "error.message", finally degrading to a truncated
// raw body so the user always sees something actionable.
func openAIErrorDetail(oaiErr *openai.Error) string {
	if oaiErr.Message != "" {
		return oaiErr.Message
	}
	if oaiErr.Response == nil || oaiErr.Response.Body == nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(oaiErr.Response.Body, 4096))
	if err != nil || len(raw) == 0 {
		return ""
	}
	var body struct {
		Detail string `json:"detail"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil {
		if body.Detail != "" {
			return body.Detail
		}
		if body.Error.Message != "" {
			return body.Error.Message
		}
	}
	return strings.TrimSpace(string(raw))
}

// classifyError determines if an API error is retryable and returns a
// user-friendly description.
func classifyError(err error) (retryable bool, friendlyMsg string) {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		detail := apiErrorMessage(apiErr)
		withDetail := func(label string) string {
			if detail != "" {
				return fmt.Sprintf("%s: %s", label, detail)
			}
			return label
		}
		switch apiErr.StatusCode {
		case 429:
			return true, withDetail("Rate limited by API")
		case 500:
			return true, withDetail("API internal server error")
		case 502:
			return true, withDetail("API bad gateway")
		case 503:
			return true, withDetail("API temporarily unavailable")
		case 529:
			return true, withDetail("API overloaded")
		case 401:
			return false, withDetail("Invalid API key")
		case 400:
			return false, withDetail("Bad request")
		case 403:
			return false, withDetail("Permission denied")
		case 404:
			return false, withDetail("Model not found")
		default:
			if apiErr.StatusCode >= 500 {
				return true, withDetail("API server error")
			}
			return false, withDetail("API error")
		}
	}

	var oaiErr *openai.Error
	if errors.As(err, &oaiErr) {
		detail := openAIErrorDetail(oaiErr)
		withDetail := func(label string) string {
			if detail != "" {
				return fmt.Sprintf("%s: %s", label, detail)
			}
			return label
		}
		switch oaiErr.StatusCode {
		case 429:
			return true, withDetail("Rate limited by API")
		case 500:
			return true, withDetail("API internal server error")
		case 502:
			return true, "API bad gateway"
		case 503:
			return true, "API temporarily unavailable"
		case 401:
			return false, withDetail("Invalid API key")
		case 400, 422:
			return false, withDetail("Bad request")
		case 402:
			return false, withDetail("Billing error / out of credits")
		case 403:
			return false, withDetail("Permission denied")
		case 404:
			return false, withDetail("Model not found")
		default:
			if oaiErr.StatusCode >= 500 {
				return true, withDetail("API server error")
			}
			return false, withDetail("API error")
		}
	}

	var bedrockErr *llm.BedrockHTTPError
	if errors.As(err, &bedrockErr) {
		switch {
		case bedrockErr.Code == 429:
			return true, "Rate limited by API"
		case bedrockErr.Code >= 500:
			return true, "API server error"
		default:
			return false, "Bad request"
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true, "Network error"
	}

	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "broken pipe") ||
		strings.Contains(msg, "EOF") {
		return true, "Connection lost"
	}

	// Streaming errors may arrive as JSON blobs rather than a typed *anthropic.Error.
	// Anthropic's error envelope looks like:
	//   {"type":"error","error":{"type":"<kind>","message":"..."}}
	// The <kind> determines whether it's retryable. Check the non-retryable kinds
	// first so permanent failures (auth, bad request) fail fast instead of burning
	// through the outer retry loop.
	if detail, kind, ok := parseStreamErrorKind(msg); ok {
		switch kind {
		case "authentication_error":
			return false, withStreamDetail("Invalid API key", detail)
		case "permission_error":
			return false, withStreamDetail("Permission denied", detail)
		case "invalid_request_error":
			return false, withStreamDetail("Bad request", detail)
		case "not_found_error":
			return false, withStreamDetail("Not found", detail)
		case "request_too_large":
			return false, withStreamDetail("Request too large", detail)
		case "billing_error":
			return false, withStreamDetail("Billing error", detail)
		case "rate_limit_error":
			return true, withStreamDetail("Rate limited by API", detail)
		case "api_error":
			return true, withStreamDetail("API server error", detail)
		case "overloaded_error":
			return true, withStreamDetail("API overloaded", detail)
		}
	}

	// Stream idle timeout: the SSE connection went silent. Retryable because
	// it's a transient connection issue, not a permanent API failure.
	if errors.Is(err, ErrStreamIdleTimeout) {
		return true, "Stream idle timeout"
	}

	// Fallback string match for loosely-wrapped server errors.
	if strings.Contains(lower, "internal server error") ||
		strings.Contains(lower, "internal_error") ||
		strings.Contains(lower, "overloaded") ||
		strings.Contains(lower, "bad gateway") ||
		strings.Contains(lower, "service unavailable") {
		return true, "API server error (stream)"
	}

	return false, "Unexpected error"
}

// parseStreamErrorKind extracts the error.type and error.message from an
// Anthropic streaming error envelope embedded anywhere inside msg. Returns
// (detail, kind, true) on success, or ("", "", false) if no envelope is found.
func parseStreamErrorKind(msg string) (detail, kind string, ok bool) {
	start := strings.Index(msg, "{")
	for start != -1 {
		dec := json.NewDecoder(strings.NewReader(msg[start:]))
		var env struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := dec.Decode(&env); err == nil && env.Error.Type != "" {
			return env.Error.Message, env.Error.Type, true
		}
		next := strings.Index(msg[start+1:], "{")
		if next == -1 {
			break
		}
		start += 1 + next
	}
	return "", "", false
}

func withStreamDetail(label, detail string) string {
	if detail == "" {
		return label
	}
	return label + ": " + detail
}
