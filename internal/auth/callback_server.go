package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
)

// callbackResult carries the authorization code (and state) delivered to the
// local OAuth redirect endpoint.
type callbackResult struct {
	code  string
	state string
}

// callbackHost returns the interface the local OAuth callback server binds to.
// It honours pi's PI_OAUTH_CALLBACK_HOST override and defaults to 127.0.0.1.
func callbackHost() string {
	if h := os.Getenv("PI_OAUTH_CALLBACK_HOST"); h != "" {
		return h
	}
	return "127.0.0.1"
}

// writeHTML writes an HTML response with the given status code.
func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// newCallbackHandler builds the redirect handler. It validates the path,
// surfaces an `error` query parameter, requires a code, and enforces that the
// returned state matches expectedState before accepting. The first accepted
// callback is delivered (once) to results. Factoring the handler out lets the
// validation logic be unit-tested with httptest, without binding a fixed port.
func newCallbackHandler(path, expectedState, successMsg string, results chan<- callbackResult) http.HandlerFunc {
	var once sync.Once
	deliver := func(r callbackResult) { once.Do(func() { results <- r }) }

	return func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != path {
			lg().Debug("callback: request to unexpected path", "path", req.URL.Path, "want", path)
			writeHTML(w, http.StatusNotFound, oauthErrorHTML("Callback route not found.", ""))
			return
		}
		q := req.URL.Query()
		if e := q.Get("error"); e != "" {
			lg().Warn("callback: provider returned error", "error", e, "error_description", q.Get("error_description"))
			writeHTML(w, http.StatusBadRequest, oauthErrorHTML("Authentication did not complete.", "Error: "+e))
			return
		}
		code := q.Get("code")
		state := q.Get("state")
		if code == "" {
			lg().Warn("callback: missing authorization code")
			writeHTML(w, http.StatusBadRequest, oauthErrorHTML("Missing authorization code.", ""))
			return
		}
		if state != expectedState {
			lg().Warn("callback: state mismatch", "got_state_present", state != "")
			writeHTML(w, http.StatusBadRequest, oauthErrorHTML("State mismatch.", ""))
			return
		}
		lg().Info("callback: authorization code received", "code", redact(code))
		writeHTML(w, http.StatusOK, oauthSuccessHTML(successMsg))
		deliver(callbackResult{code: code, state: state})
	}
}

// callbackServer is a local HTTP server that receives the OAuth redirect.
type callbackServer struct {
	redirectURI string

	server  *http.Server
	results chan callbackResult

	cancelOnce sync.Once
	cancelCh   chan struct{}
}

// startCallbackServer binds host:port and serves the redirect handler at path.
// redirectURI is reported as http://localhost:port/path to match pi.
func startCallbackServer(host string, port int, path, successMsg, expectedState string) (*callbackServer, error) {
	results := make(chan callbackResult, 1)
	handler := newCallbackHandler(path, expectedState, successMsg, results)

	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		lg().Error("callback server: bind failed", "host", host, "port", port, "err", err)
		return nil, err
	}

	srv := &http.Server{Handler: handler}
	cs := &callbackServer{
		redirectURI: fmt.Sprintf("http://localhost:%d%s", port, path),
		server:      srv,
		results:     results,
		cancelCh:    make(chan struct{}),
	}
	lg().Debug("callback server: listening", "addr", ln.Addr().String(), "redirect_uri", cs.redirectURI)
	go func() { _ = srv.Serve(ln) }()
	return cs, nil
}

// waitForCode blocks until a callback is received, ctx is cancelled, or
// cancelWait is called. ok is false in the latter two cases.
func (cs *callbackServer) waitForCode(ctx context.Context) (res callbackResult, ok bool) {
	select {
	case r := <-cs.results:
		return r, true
	case <-cs.cancelCh:
		return callbackResult{}, false
	case <-ctx.Done():
		return callbackResult{}, false
	}
}

// cancelWait unblocks a pending waitForCode without delivering a code. Used
// when a racing manual-input path wins.
func (cs *callbackServer) cancelWait() {
	cs.cancelOnce.Do(func() { close(cs.cancelCh) })
}

// close shuts the server down.
func (cs *callbackServer) close() {
	_ = cs.server.Close()
}

// waitForAuthorizationCode resolves the authorization code for a callback-based
// flow. When OnManualCodeInput is provided it is raced against the local
// callback server (whichever resolves first wins), mirroring pi's behaviour.
// A returned empty code means the caller should fall back to OnPrompt.
func waitForAuthorizationCode(ctx context.Context, server *callbackServer, cb LoginCallbacks, expectedState string) (code, state string, err error) {
	if cb.OnManualCodeInput == nil {
		res, ok := server.waitForCode(ctx)
		if ok && res.code != "" {
			return res.code, res.state, nil
		}
		return "", "", nil
	}

	type manualResult struct {
		in  string
		err error
	}
	manualCh := make(chan manualResult, 1)
	go func() {
		in, err := cb.OnManualCodeInput()
		manualCh <- manualResult{in: in, err: err}
		server.cancelWait()
	}()

	if res, ok := server.waitForCode(ctx); ok && res.code != "" {
		return res.code, res.state, nil
	}

	select {
	case <-ctx.Done():
		return "", "", errors.New(deviceCancelMessage)
	case m := <-manualCh:
		if m.err != nil {
			return "", "", m.err
		}
		if m.in == "" {
			return "", "", nil
		}
		parsed := parseAuthorizationInput(m.in)
		if parsed.state != "" && parsed.state != expectedState {
			return "", "", errors.New("OAuth state mismatch")
		}
		st := parsed.state
		if st == "" {
			st = expectedState
		}
		return parsed.code, st, nil
	}
}
