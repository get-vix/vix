package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// httpClient is the client used for all OAuth HTTP calls. It is a package
// variable so tests could substitute a custom transport; the per-request
// timeout mirrors pi's 30s fetch timeout.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// httpRequest performs a single HTTP request and returns the status code and
// full response body. Every call is traced (method, URL, status, duration);
// request and success-response bodies are never logged (they carry secrets),
// but error-response bodies are logged truncated to aid diagnosis. Headers
// (which carry bearer tokens) are never logged.
func httpRequest(ctx context.Context, method, rawURL string, headers map[string]string, body io.Reader) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		lg().Error("http: build request failed", "method", method, "url", rawURL, "err", err)
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := httpClient.Do(req)
	durMS := time.Since(start).Milliseconds()
	if err != nil {
		lg().Error("http: request failed", "method", method, "url", rawURL, "dur_ms", durMS, "err", err)
		return 0, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		lg().Error("http: read body failed", "method", method, "url", rawURL, "status", resp.StatusCode, "dur_ms", durMS, "err", err)
		return resp.StatusCode, nil, err
	}

	if resp.StatusCode >= 400 {
		lg().Warn("http: non-2xx response", "method", method, "url", rawURL, "status", resp.StatusCode, "dur_ms", durMS, "resp_bytes", len(data), "body", truncate(string(data), 512))
	} else {
		lg().Debug("http: response", "method", method, "url", rawURL, "status", resp.StatusCode, "dur_ms", durMS, "resp_bytes", len(data))
	}
	return resp.StatusCode, data, nil
}

// postJSONForToken POSTs a JSON body and returns the raw response body,
// erroring on any non-2xx status. Mirrors pi's anthropic postJson helper.
func postJSONForToken(ctx context.Context, rawURL string, body map[string]any) ([]byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	status, data, err := httpRequest(ctx, http.MethodPost, rawURL, map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
	}, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("HTTP request failed. status=%d; url=%s; body=%s", status, rawURL, string(data))
	}
	return data, nil
}

// postFormJSON POSTs a form-encoded body and decodes a JSON response into out.
// It errors on non-2xx, matching pi's fetchJson ("status statusText: body").
func postFormJSON(ctx context.Context, rawURL string, form url.Values, headers map[string]string, out any) error {
	h := map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/x-www-form-urlencoded",
	}
	for k, v := range headers {
		h[k] = v
	}
	status, data, err := httpRequest(ctx, http.MethodPost, rawURL, h, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%d %s: %s", status, http.StatusText(status), string(data))
	}
	return json.Unmarshal(data, out)
}

// getJSON performs a GET and decodes a JSON response into out, erroring on
// non-2xx. Mirrors pi's fetchJson for GET requests.
func getJSON(ctx context.Context, rawURL string, headers map[string]string, out any) error {
	status, data, err := httpRequest(ctx, http.MethodGet, rawURL, headers, nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%d %s: %s", status, http.StatusText(status), string(data))
	}
	return json.Unmarshal(data, out)
}
