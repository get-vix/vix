package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/get-vix/vix/internal/config"
)

// webFetchImpl fetches a URL and returns its content as text.
func webFetchImpl(rawURL, selector string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("only http and https URLs are supported, got %q", u.Scheme)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Vix/1.0)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}

	// Limit body to 1MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("failed to read response: %v", err)
	}

	ct := resp.Header.Get("Content-Type")
	result := string(body)

	// JSON or plain text: return directly
	if strings.Contains(ct, "application/json") || strings.Contains(ct, "text/plain") {
		if len(result) > maxOutput {
			result = result[:maxOutput] + "\n... (truncated)"
		}
		return result, nil
	}

	// HTML: convert to text
	if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml") {
		result = htmlToText(result, selector)
	}

	if len(result) > maxOutput {
		result = result[:maxOutput] + "\n... (truncated)"
	}
	return result, nil
}

// skipTags are HTML elements whose text content should be skipped.
var skipTags = map[atom.Atom]bool{
	atom.Script:   true,
	atom.Style:    true,
	atom.Nav:      true,
	atom.Footer:   true,
	atom.Header:   true,
	atom.Svg:      true,
	atom.Iframe:   true,
	atom.Noscript: true,
}

// blockTags are elements that should have newlines around them.
var blockTags = map[atom.Atom]bool{
	atom.P:          true,
	atom.Div:        true,
	atom.Br:         true,
	atom.H1:         true,
	atom.H2:         true,
	atom.H3:         true,
	atom.H4:         true,
	atom.H5:         true,
	atom.H6:         true,
	atom.Li:         true,
	atom.Tr:         true,
	atom.Blockquote: true,
	atom.Pre:        true,
	atom.Section:    true,
	atom.Article:    true,
}

// htmlToText parses HTML and extracts readable text content.
func htmlToText(htmlStr, selector string) string {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return htmlStr
	}

	// Find the root node to extract from
	root := findContentRoot(doc, selector)
	if root == nil {
		root = doc
	}

	var b strings.Builder
	extractText(root, &b)

	// Collapse multiple blank lines
	result := b.String()
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(result)
}

// findContentRoot locates the best content node based on selector hint.
func findContentRoot(doc *html.Node, selector string) *html.Node {
	// Try selector-based targets
	targets := []atom.Atom{atom.Main, atom.Article}
	if selector != "" {
		switch strings.ToLower(selector) {
		case "main":
			targets = []atom.Atom{atom.Main}
		case "article":
			targets = []atom.Atom{atom.Article}
		case "body":
			targets = []atom.Atom{atom.Body}
		}
	}

	for _, tag := range targets {
		if n := findElement(doc, tag); n != nil {
			return n
		}
	}

	// Default to body
	if n := findElement(doc, atom.Body); n != nil {
		return n
	}
	return nil
}

// findElement finds the first element with the given tag in DFS order.
func findElement(n *html.Node, tag atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

// extractText walks the HTML tree and writes text content to the builder.
func extractText(n *html.Node, b *strings.Builder) {
	if n.Type == html.ElementNode && skipTags[n.DataAtom] {
		return
	}

	if n.Type == html.ElementNode && blockTags[n.DataAtom] {
		b.WriteString("\n")
	}

	if n.Type == html.TextNode {
		text := strings.TrimSpace(n.Data)
		if text != "" {
			b.WriteString(text)
			b.WriteString(" ")
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extractText(c, b)
	}

	if n.Type == html.ElementNode && blockTags[n.DataAtom] {
		b.WriteString("\n")
	}
}

// webSearchImpl performs a web search using the Brave Search API.
func webSearchImpl(query string, count int) (string, error) {
	apiKey, _ := config.ResolveStoredSecret("brave-search-api-key")
	if apiKey == "" {
		return "", fmt.Errorf("Brave Search API key is not stored; add vix/brave-search-api-key with daz-secrets")
	}

	if count <= 0 {
		count = 5
	}
	if count > 20 {
		count = 20
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	searchURL := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d",
		url.QueryEscape(query), count)

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("X-Subscription-Token", apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("search request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("brave search API error (HTTP %d): %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("failed to read response: %v", err)
	}

	var searchResp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}

	if err := json.Unmarshal(body, &searchResp); err != nil {
		return "", fmt.Errorf("failed to parse search results: %v", err)
	}

	if len(searchResp.Web.Results) == 0 {
		return "No results found.", nil
	}

	var b strings.Builder
	for i, r := range searchResp.Web.Results {
		b.WriteString(strconv.Itoa(i+1) + ". " + r.Title + "\n")
		b.WriteString("   " + r.URL + "\n")
		if r.Description != "" {
			b.WriteString("   " + r.Description + "\n")
		}
		b.WriteString("\n")
	}

	result := b.String()
	if len(result) > maxOutput {
		result = result[:maxOutput] + "\n... (truncated)"
	}
	return result, nil
}
