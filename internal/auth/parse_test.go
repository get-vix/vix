package auth

import "testing"

func TestParseAuthorizationInput(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantCode  string
		wantState string
	}{
		{"empty", "", "", ""},
		{"bare code", "abc123", "abc123", ""},
		{"full url", "http://localhost:1455/auth/callback?code=xyz&state=st1", "xyz", "st1"},
		{"https url", "https://example.com/cb?code=c2&state=s2", "c2", "s2"},
		{"hash form", "thecode#thestate", "thecode", "thestate"},
		{"query form", "code=qcode&state=qstate", "qcode", "qstate"},
		{"whitespace trimmed", "   raw  ", "raw", ""},
		{"url no state", "https://example.com/cb?code=only", "only", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAuthorizationInput(tc.in)
			if got.code != tc.wantCode || got.state != tc.wantState {
				t.Errorf("parseAuthorizationInput(%q) = {code:%q state:%q}, want {code:%q state:%q}",
					tc.in, got.code, got.state, tc.wantCode, tc.wantState)
			}
		})
	}
}
