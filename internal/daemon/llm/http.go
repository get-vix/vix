package llm

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"
)

// sharedHTTPTransport is used by every adapter so we can close idle pool
// connections between retry attempts. A poisoned half-open connection
// pinned in this pool would otherwise silently fail every retry.
var sharedHTTPTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	return t
}()

// SharedHTTPClient returns the package-wide HTTP client. Adapters install
// per-instance wrappers (header strip/set, lifecycle logging) by composing
// transports on top of this client's Transport, then passing the wrapped
// client to their SDK via that SDK's WithHTTPClient option.
func SharedHTTPClient() *http.Client {
	return &http.Client{Transport: sharedHTTPTransport}
}

// CloseIdleHTTPConnections drops all pooled connections in the shared
// transport. Called between retries so a poisoned conn doesn't get reused
// on the next attempt.
func CloseIdleHTTPConnections() {
	sharedHTTPTransport.CloseIdleConnections()
}

// sessionIDCtxKey is the context key for the stable session identifier
// (thread UUID) that is sent as the x-opencode-session header on every
// outbound LLM request.
type sessionIDCtxKey struct{}

// WithSessionID returns a new context that carries the given session ID.
// The HTTP transport stamps this ID as x-opencode-session on every request.
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDCtxKey{}, id)
}

// SessionIDFromContext returns the session ID stamped on ctx by
// WithSessionID, or "" if none.
func SessionIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(sessionIDCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// NewPluginHTTPClient returns an *http.Client whose Transport applies the
// plugin's header set/strip rules and the session header to every outgoing
// request, then delegates to the shared transport. The lifecycle-logging
// transport is composed on the outside (see NewLoggingTransport) so the
// log ordering is:
//
//	request → loggingTransport → headerStripperTransport → sharedHTTPTransport
//
// Returns SharedHTTPClient() unchanged when pc has no headers.
func NewPluginHTTPClient(pc PluginConfig) *http.Client {
	if len(pc.Headers) == 0 {
		return &http.Client{Transport: NewLoggingTransport(&headerStripperTransport{base: sharedHTTPTransport, sessionHeader: pc.SessionHeader})}
	}

	set := make(map[string]string)
	var strip []string
	for name, val := range pc.Headers {
		canonical := http.CanonicalHeaderKey(name)
		if val == nil {
			strip = append(strip, canonical)
		} else {
			set[canonical] = *val
		}
	}

	header := &headerStripperTransport{
		base:          sharedHTTPTransport,
		set:           set,
		strip:         strip,
		sessionHeader: pc.SessionHeader,
	}
	return &http.Client{Transport: NewLoggingTransport(header)}
}

// headerStripperTransport applies plugin header mutations and the
// session header to every outgoing request before delegating to base.
type headerStripperTransport struct {
	base          http.RoundTripper
	set           map[string]string
	strip         []string
	sessionHeader string // header key for x-opencode-session; empty = no injection
}

func (h *headerStripperTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.set {
		req.Header.Set(k, v)
	}
	for _, k := range h.strip {
		req.Header.Del(k)
	}
	if h.sessionHeader != "" {
		if sid := SessionIDFromContext(req.Context()); sid != "" {
			req.Header.Set(h.sessionHeader, sid)
		}
	}
	return h.base.RoundTrip(req)
}

// NewLoggingTransport returns a RoundTripper that attaches an
// httptrace.ClientTrace and logs a compact lifecycle summary per request.
// When VIX_STREAM_DEBUG=1 it also wraps the response body to count bytes
// and log when the first body byte arrives.
//
// This is the provider-agnostic replacement for streamDebugMiddleware
// (formerly typed for anthropic-sdk-go's option.Middleware).
func NewLoggingTransport(base http.RoundTripper) http.RoundTripper {
	return &loggingTransport{base: base}
}

type loggingTransport struct {
	base http.RoundTripper
}

func (l *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	reqID := RequestIDFromContext(req.Context())
	if reqID == "" {
		reqID = NewRequestID()
	}
	t0 := time.Now()

	var (
		dnsStart, dnsDone   time.Time
		connStart, connDone time.Time
		tlsStart, tlsDone   time.Time
		wroteReq            time.Time
		firstByte           time.Time
		reused              bool
		remoteAddr          string
		connectErr, tlsErr  error
	)
	trace := &httptrace.ClientTrace{
		DNSStart:     func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:      func(httptrace.DNSDoneInfo) { dnsDone = time.Now() },
		ConnectStart: func(_, _ string) { connStart = time.Now() },
		ConnectDone: func(_, _ string, err error) {
			connDone = time.Now()
			connectErr = err
		},
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			tlsDone = time.Now()
			tlsErr = err
		},
		GotConn: func(info httptrace.GotConnInfo) {
			reused = info.Reused
			if info.Conn != nil {
				remoteAddr = info.Conn.RemoteAddr().String()
			}
		},
		WroteRequest:         func(httptrace.WroteRequestInfo) { wroteReq = time.Now() },
		GotFirstResponseByte: func() { firstByte = time.Now() },
	}
	ctx := httptrace.WithClientTrace(req.Context(), trace)
	req = req.WithContext(ctx)

	resp, err := l.base.RoundTrip(req)

	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	log.Printf("[httpx req=%s] dns=%s connect=%s tls=%s wrote_req=%s first_byte=%s status=%d reused=%v remote=%s err=%v connect_err=%v tls_err=%v",
		reqID,
		DurStr(dnsStart, dnsDone),
		DurStr(connStart, connDone),
		DurStr(tlsStart, tlsDone),
		DurStr(t0, wroteReq),
		DurStr(t0, firstByte),
		status, reused, remoteAddr, err, connectErr, tlsErr,
	)

	if resp != nil && StreamDebugVerbose() {
		resp.Body = &countingBody{
			ReadCloser: resp.Body,
			reqID:      reqID,
			t0:         t0,
		}
	}
	return resp, err
}

// countingBody wraps an SSE response body to log byte counts and
// first-byte latency. Only installed when VIX_STREAM_DEBUG=1 to keep prod
// log volume low.
type countingBody struct {
	io.ReadCloser
	reqID     string
	t0        time.Time
	firstByte time.Time
	bytes     int64
	closed    atomic.Bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 && b.firstByte.IsZero() {
		b.firstByte = time.Now()
		log.Printf("[httpx req=%s] stream_first_body_byte=%dms",
			b.reqID, time.Since(b.t0).Milliseconds())
	}
	b.bytes += int64(n)
	return n, err
}

func (b *countingBody) Close() error {
	if b.closed.Swap(true) {
		return b.ReadCloser.Close()
	}
	fb := "never"
	if !b.firstByte.IsZero() {
		fb = DurStr(b.t0, b.firstByte)
	}
	log.Printf("[httpx req=%s] stream_body_close bytes=%d first_byte=%s elapsed=%s",
		b.reqID, b.bytes, fb, time.Since(b.t0))
	return b.ReadCloser.Close()
}
