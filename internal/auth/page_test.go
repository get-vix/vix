package auth

import (
	"strings"
	"testing"
)

func TestOAuthSuccessHTML(t *testing.T) {
	html := oauthSuccessHTML("All done here.")
	if !strings.Contains(html, "Authentication successful") {
		t.Errorf("missing success heading")
	}
	if !strings.Contains(html, "All done here.") {
		t.Errorf("missing message")
	}
	if !strings.Contains(html, "<!doctype html>") {
		t.Errorf("missing doctype")
	}
}

func TestOAuthErrorHTML(t *testing.T) {
	html := oauthErrorHTML("Something broke.", "code=42")
	if !strings.Contains(html, "Authentication failed") {
		t.Errorf("missing failure heading")
	}
	if !strings.Contains(html, "Something broke.") {
		t.Errorf("missing message")
	}
	if !strings.Contains(html, "code=42") {
		t.Errorf("missing details")
	}
}

func TestEscapeHTMLInPage(t *testing.T) {
	html := oauthErrorHTML("<script>alert('x')</script>", "")
	if strings.Contains(html, "<script>alert('x')</script>") {
		t.Errorf("message was not escaped: %q", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("expected escaped script tag")
	}
}

func TestEscapeHTML(t *testing.T) {
	got := escapeHTML(`<>&"'`)
	want := "&lt;&gt;&amp;&quot;&#39;"
	if got != want {
		t.Errorf("escapeHTML = %q, want %q", got, want)
	}
}
