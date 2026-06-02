package auth

import (
	"net/url"
	"strings"
)

// authInput is the result of parsing a pasted authorization code or redirect.
type authInput struct {
	code  string
	state string
}

// parseAuthorizationInput extracts an authorization code (and optional state)
// from a value the user pasted. It accepts, in order: a full redirect URL with
// query parameters, a "code#state" fragment, a raw "code=...&state=..." query
// string, or a bare code. Mirrors pi's parseAuthorizationInput.
func parseAuthorizationInput(input string) authInput {
	value := strings.TrimSpace(input)
	if value == "" {
		return authInput{}
	}

	// Absolute URL: pull code/state from the query string.
	if u, err := url.Parse(value); err == nil && u.Scheme != "" && u.Host != "" {
		q := u.Query()
		return authInput{code: q.Get("code"), state: q.Get("state")}
	}

	// "code#state" form.
	if strings.Contains(value, "#") {
		parts := strings.SplitN(value, "#", 2)
		return authInput{code: parts[0], state: parts[1]}
	}

	// Raw query string containing "code=".
	if strings.Contains(value, "code=") {
		if q, err := url.ParseQuery(value); err == nil {
			return authInput{code: q.Get("code"), state: q.Get("state")}
		}
	}

	return authInput{code: value}
}

// normalizeDomain extracts a bare hostname from a domain or URL the user
// entered, or returns "" if it cannot be parsed. Mirrors pi's normalizeDomain.
func normalizeDomain(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ""
	}
	raw := trimmed
	if !strings.Contains(trimmed, "://") {
		raw = "https://" + trimmed
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return u.Hostname()
}
