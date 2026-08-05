package daemon

import (
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptrace"
	"os"
	"sync/atomic"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
)

type reqIDCtxKey struct{}

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, reqIDCtxKey{}, id)
}

func requestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(reqIDCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// newRequestID returns a short random hex ID for correlating one logical
// LLM turn (all retry attempts share the turn root; each attempt appends
// ".<n>" in the caller).
func newRequestID() string {
	var b [6]byte
	if _, err := crand.Read(b[:]); err != nil {
		return fmt.Sprintf("noncrypto-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// streamDebugVerbose returns true when VIX_STREAM_DEBUG=1 is set, enabling
// per-response-body-byte tracing in addition to always-on HTTP lifecycle
// logging.
func streamDebugVerbose() bool {
	return os.Getenv("VIX_STREAM_DEBUG") == "1"
}

// sharedHTTPTransport is used by all LLM instances so we can close idle
// pool connections between retry attempts. A poisoned half-open connection
// pinned in this pool would otherwise silently fail every retry.
var sharedHTTPTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	return t
}()

var sharedHTTPClient = &http.Client{Transport: sharedHTTPTransport}

// CloseIdleHTTPConnections drops all pooled connections in the shared
// transport. Called between retries in AgentRunner.Send and streamWithRetry
// so a poisoned conn doesn't get reused on the next attempt.
func CloseIdleHTTPConnections() {
	sharedHTTPTransport.CloseIdleConnections()
}

// streamDebugHTTPClient returns an option that wires the shared HTTP client
// (and therefore the shared pool we can drop on retry).
func streamDebugHTTPClient() option.RequestOption {
	return option.WithHTTPClient(sharedHTTPClient)
}

// streamDebugMiddleware is an anthropic-sdk-go middleware that attaches an
// httptrace.ClientTrace to each outgoing request and logs a compact lifecycle
// summary to the daemon log. When VIX_STREAM_DEBUG=1, it also wraps the
// response body to log when the first body byte arrives and how many bytes
// total flowed before Close.
//
// Goals: make it possible to distinguish "DNS/connect/TLS hung",
// "connection established but server never wrote headers", "headers arrived
// but no body bytes", and "body started then stalled" from a single log
// greppable by request ID.
func streamDebugMiddleware(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	reqID := requestIDFromContext(req.Context())
	if reqID == "" {
		reqID = newRequestID()
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

	resp, err := next(req)

	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	log.Printf("[httpx req=%s] dns=%s connect=%s tls=%s wrote_req=%s first_byte=%s status=%d reused=%v remote=%s err=%v connect_err=%v tls_err=%v",
		reqID,
		durStr(dnsStart, dnsDone),
		durStr(connStart, connDone),
		durStr(tlsStart, tlsDone),
		durStr(t0, wroteReq),
		durStr(t0, firstByte),
		status, reused, remoteAddr, err, connectErr, tlsErr,
	)

	if resp != nil && streamDebugVerbose() {
		resp.Body = &countingBody{
			ReadCloser: resp.Body,
			reqID:      reqID,
			t0:         t0,
		}
	}
	return resp, err
}

func durStr(a, b time.Time) string {
	if a.IsZero() || b.IsZero() {
		return "—"
	}
	return fmt.Sprintf("%dms", b.Sub(a).Milliseconds())
}

// countingBody wraps an SSE response body to log byte counts and first-byte
// latency. Only installed when VIX_STREAM_DEBUG=1 to keep prod log volume low.
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
		fb = fmt.Sprintf("%dms", b.firstByte.Sub(b.t0).Milliseconds())
	}
	log.Printf("[httpx req=%s] stream_body_close bytes=%d first_byte=%s elapsed=%s",
		b.reqID, b.bytes, fb, time.Since(b.t0))
	return b.ReadCloser.Close()
}
