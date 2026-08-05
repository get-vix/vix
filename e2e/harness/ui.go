package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/get-vix/vix/e2e/artifact"
)

// UI drives the real vix TUI inside a tmux session. tmux is a complete, faithful
// terminal emulator (the same one VHS uses), so Bubble Tea v2 renders to it
// perfectly — no PTY/emulator handshake races. Input is delivered with
// `send-keys`; the rendered screen is read with `capture-pane`.
type UI struct {
	h       *Harness
	sock    string // dedicated tmux server socket (per test, for isolation)
	session string
	cols    int
	rows    int
}

// startTUI launches vix inside a fresh, isolated tmux server + session.
func (h *Harness) startTUI(cfg *config) {
	t := h.t
	bin, err := vixBinary()
	if err != nil {
		t.Fatalf("e2e: %v", err)
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("e2e: tmux not found (required by the TUI driver): %v", err)
	}

	logDir := filepath.Dir(h.logFile)
	h.clientLog = filepath.Join(logDir, "vix-client.log")

	// A launcher script sets the isolated env and execs vix, with stderr (logs)
	// to a file so they don't corrupt the rendered screen.
	script := filepath.Join(logDir, "launch-vix.sh")
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	for _, kv := range h.daemonEnv(cfg, map[string]string{"VIX_TEST_RENDER": "1"}) {
		if i := strings.IndexByte(kv, '='); i > 0 {
			sb.WriteString("export " + shellQuote(kv[:i]) + "=" + shellQuote(kv[i+1:]) + "\n")
		}
	}
	sb.WriteString("cd " + shellQuote(h.workdir) + "\n")
	sb.WriteString("exec " + shellQuote(bin) +
		" -socket-path " + shellQuote(h.socket) +
		" -workdir " + shellQuote(h.workdir))
	for _, a := range cfg.tuiArgs {
		sb.WriteString(" " + shellQuote(a))
	}
	sb.WriteString(" 2> " + shellQuote(h.clientLog) + "\n")
	mustWrite(t, script, sb.String())
	_ = os.Chmod(script, 0o755)

	ui := &UI{
		h:       h,
		sock:    filepath.Join(logDir, "tmux.sock"),
		session: "vix",
		cols:    cfg.cols,
		rows:    cfg.rows,
	}
	out, err := ui.tmux("new-session", "-d", "-s", ui.session,
		"-x", itoa(cfg.cols), "-y", itoa(cfg.rows), "sh "+script).CombinedOutput()
	if err != nil {
		t.Fatalf("e2e: tmux new-session: %v\n%s", err, out)
	}
	h.UI = ui
	ui.waitReady()
}

// tmux builds a command against this UI's private tmux server socket.
func (u *UI) tmux(args ...string) *exec.Cmd {
	return exec.Command("tmux", append([]string{"-S", u.sock}, args...)...)
}

func (u *UI) close() {
	if u.sock != "" {
		_ = u.tmux("kill-server").Run()
	}
}

// waitReady blocks until the TUI chrome has painted, then settles briefly.
func (u *UI) waitReady() {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if u.Contains("Threads") && u.Contains("Workspace") {
			select {
			case <-u.h.ctx.Done():
			case <-time.After(300 * time.Millisecond):
			}
			return
		}
		select {
		case <-u.h.ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// --- input ---

// Type sends literal text to the TUI.
func (u *UI) Type(s string) { _ = u.tmux("send-keys", "-t", u.session, "-l", s).Run() }

// Enter submits the current input line.
func (u *UI) Enter() { _ = u.tmux("send-keys", "-t", u.session, "Enter").Run() }

// Ctrl sends a Ctrl-<letter> key chord (e.g. Ctrl('c')).
func (u *UI) Ctrl(letter byte) {
	_ = u.tmux("send-keys", "-t", u.session, "C-"+strings.ToLower(string(letter))).Run()
}

// Key sends a named special key (tmux key name).
func (u *UI) Key(name string) {
	k, ok := tmuxKeys[name]
	if !ok {
		u.h.t.Fatalf("e2e: unknown key %q", name)
	}
	_ = u.tmux("send-keys", "-t", u.session, k).Run()
}

// Paste sends text as a bracketed paste.
func (u *UI) Paste(s string) {
	_ = u.tmux("send-keys", "-t", u.session, "-l", "\x1b[200~"+s+"\x1b[201~").Run()
}

// Slash sends a slash command (the name without the leading "/") and submits it,
// e.g. Slash("compact") for /compact.
func (u *UI) Slash(name string) {
	u.Type("/" + name)
	u.Enter()
}

// SendRaw sends raw bytes as a literal key sequence.
func (u *UI) SendRaw(b []byte) { _ = u.tmux("send-keys", "-t", u.session, "-l", string(b)).Run() }

var tmuxKeys = map[string]string{
	"esc": "Escape", "tab": "Tab", "enter": "Enter", "backspace": "BSpace",
	"space": "Space",
	"up":    "Up", "down": "Down", "right": "Right", "left": "Left",
	"home": "Home", "end": "End", "pgup": "PageUp", "pgdn": "PageDown",
	"delete": "DC", "shift-tab": "BTab",
	// Function keys F1–F12 (tmux uses the same names). F1–F6 switch tabs.
	"f1": "F1", "f2": "F2", "f3": "F3", "f4": "F4", "f5": "F5", "f6": "F6",
	"f7": "F7", "f8": "F8", "f9": "F9", "f10": "F10", "f11": "F11", "f12": "F12",
}

// --- screen reads ---

// Snapshot returns the current rendered pane as plain text.
func (u *UI) Snapshot() string {
	out, err := u.tmux("capture-pane", "-t", u.session, "-p").Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

// snapshotANSI returns the pane with ANSI styling (for the report PNG).
func (u *UI) snapshotANSI() string {
	out, err := u.tmux("capture-pane", "-t", u.session, "-e", "-p").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// Contains reports whether the current screen contains substr.
func (u *UI) Contains(substr string) bool { return strings.Contains(u.Snapshot(), substr) }

// FgColorOf returns the SGR foreground parameters active where label first
// appears on the styled screen (e.g. "38;5;155" for a 256-color fg, "38;2;r;g;b"
// for truecolor, "31" for a basic color, or "" for the terminal default). ok is
// false when label isn't currently on screen. Used to assert tab-title tinting
// (highlight / blink) without hard-coding a specific palette index.
func (u *UI) FgColorOf(label string) (code string, ok bool) {
	return fgColorOf(u.snapshotANSI(), label)
}

// fgColorOf walks an ANSI-styled string, tracking the current SGR foreground,
// and returns the foreground in effect at the first visible occurrence of label.
func fgColorOf(ansi, label string) (string, bool) {
	if label == "" {
		return "", false
	}
	var vis strings.Builder
	var fgAt []string // foreground in effect for each visible rune
	cur := ""         // "" = terminal default
	rs := []rune(ansi)
	for i := 0; i < len(rs); {
		// CSI escape: ESC '[' params 'm' (we only care about SGR 'm').
		if rs[i] == 0x1b && i+1 < len(rs) && rs[i+1] == '[' {
			j := i + 2
			for j < len(rs) && !(rs[j] >= '@' && rs[j] <= '~') {
				j++
			}
			if j < len(rs) && rs[j] == 'm' {
				cur = applySGR(cur, string(rs[i+2:j]))
			}
			if j < len(rs) {
				i = j + 1
			} else {
				i = len(rs)
			}
			continue
		}
		vis.WriteRune(rs[i])
		fgAt = append(fgAt, cur)
		i++
	}
	idx := strings.Index(vis.String(), label)
	if idx < 0 {
		return "", false
	}
	// idx is a byte offset; map it to the rune index in fgAt.
	runeIdx := len([]rune(vis.String()[:idx]))
	if runeIdx >= len(fgAt) {
		return "", false
	}
	return fgAt[runeIdx], true
}

// applySGR folds one SGR parameter list into the running foreground state.
// Recognizes reset (0 / empty), default (39), basic (30–37, 90–97), and
// extended (38;5;n / 38;2;r;g;b) foregrounds; other attributes are ignored.
func applySGR(cur, params string) string {
	toks := strings.Split(params, ";")
	for i := 0; i < len(toks); i++ {
		switch t := toks[i]; {
		case t == "" || t == "0":
			cur = ""
		case t == "39":
			cur = ""
		case t == "38":
			if i+1 < len(toks) && toks[i+1] == "5" && i+2 < len(toks) {
				cur = "38;5;" + toks[i+2]
				i += 2
			} else if i+1 < len(toks) && toks[i+1] == "2" && i+4 < len(toks) {
				cur = "38;2;" + toks[i+2] + ";" + toks[i+3] + ";" + toks[i+4]
				i += 4
			}
		case len(t) == 2 && (t[0] == '3' || t[0] == '9') && t[1] >= '0' && t[1] <= '7':
			cur = t
		}
	}
	return cur
}

// WaitFor blocks until substr appears on screen, or fails the test on deadline.
func (u *UI) WaitFor(substr string) {
	u.h.t.Helper()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if u.Contains(substr) {
			return
		}
		select {
		case <-u.h.ctx.Done():
			u.h.t.Fatalf("e2e: timed out waiting for %q on screen", substr)
		case <-tick.C:
		}
	}
}

// WaitStable blocks until the screen stops changing for quiet (bounded).
func (u *UI) WaitStable(quiet time.Duration) {
	u.h.t.Helper()
	prev := u.Snapshot()
	last := time.Now()
	cap := time.Now().Add(5 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-u.h.ctx.Done():
			return
		case <-tick.C:
			cur := u.Snapshot()
			if cur != prev {
				prev = cur
				last = time.Now()
				if time.Now().After(cap) {
					return
				}
				continue
			}
			if time.Since(last) >= quiet {
				return
			}
		}
	}
}

// Shot records a labelled capture for the report: plain text + a best-effort
// PNG rendered via `freeze` from the ANSI capture.
func (u *UI) Shot(label string) {
	text := u.Snapshot()
	ansiOut := u.snapshotANSI()

	slug := u.h.result.Slug()
	order := len(u.h.result.Screenshots)
	dir := filepath.Join(artifact.ImagesDir(u.h.root), slug)
	_ = os.MkdirAll(dir, 0o755)

	base := itoa2(order) + "-" + sanitizeLabel(label)
	_ = os.WriteFile(filepath.Join(dir, base+".txt"), []byte(text), 0o644)
	ansiPath := filepath.Join(dir, base+".ans")
	_ = os.WriteFile(ansiPath, []byte(ansiOut), 0o644)

	pngRel := ""
	pngPath := filepath.Join(dir, base+".png")
	if renderPNG(ansiPath, pngPath) {
		if rel, err := filepath.Rel(u.h.root, pngPath); err == nil {
			pngRel = rel
		}
	}
	u.h.addScreenshot(artifact.Screenshot{Label: label, Text: text, PNGPath: pngRel})
}

// renderPNG runs `freeze` to turn an ANSI capture into a PNG (best-effort).
func renderPNG(ansiPath, pngPath string) bool {
	bin := os.Getenv("FREEZE_BIN")
	if bin == "" {
		if p, err := exec.LookPath("freeze"); err == nil {
			bin = p
		} else {
			return false
		}
	}
	if err := exec.Command(bin, ansiPath, "--language", "ansi", "-o", pngPath).Run(); err != nil {
		return false
	}
	_, err := os.Stat(pngPath)
	return err == nil
}

func sanitizeLabel(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "shot"
	}
	return out
}

// shellQuote single-quotes a string for safe use in the launcher script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// itoa2 formats n as a zero-padded 2-digit string for screenshot ordering.
func itoa2(n int) string {
	s := itoa(n)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}
