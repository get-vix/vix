package daemon

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/protocol"
	"github.com/get-vix/vix/internal/protocol/protoschema"
)

// TestProtocolConformanceLiveSocket is the protocol end-to-end guard: it drives
// a real Server over a real Unix socket with a real client handshake, then
// validates every event the daemon actually emits against the generated schema
// (internal/protocol/schema/vix-protocol.schema.json) — the exact contract the
// native Swift client (apps/vix-mac) decodes.
//
// The tmux e2e suite is screen-oriented and cannot inspect raw events, so this
// lives in the daemon package where a real in-process Server + socket are cheap.
// It exercises the live surface (thread lifecycle, init, and an input turn);
// the full per-type surface is covered by protoschema.TestRoundTrip.
func TestProtocolConformanceLiveSocket(t *testing.T) {
	// Empty daemon version disables the version gate, so an empty client
	// version is accepted (in-process embedding convention).
	srv := newInstanceTestServer(t)
	_, cancel := serve(t, srv)
	defer cancel()

	conn, err := net.Dial("unix", srv.sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	send := func(cmd protocol.ThreadCommand) {
		t.Helper()
		payload, _ := json.Marshal(cmd)
		payload = append(payload, '\n')
		if _, err := conn.Write(payload); err != nil {
			t.Fatalf("write %s: %v", cmd.Type, err)
		}
	}

	dec := json.NewDecoder(conn)
	readEvent := func() (protocol.ThreadEvent, bool) {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var ev protocol.ThreadEvent
		if err := dec.Decode(&ev); err != nil {
			return protocol.ThreadEvent{}, false
		}
		return ev, true
	}

	validated := map[string]bool{}
	check := func(ev protocol.ThreadEvent) {
		t.Helper()
		if err := protoschema.ValidateEvent(ev.Type, ev.Data); err != nil {
			t.Errorf("event %q does not conform to schema: %v", ev.Type, err)
		}
		validated[ev.Type] = true
	}

	// Start the thread and drain to thread_started, validating each event.
	startData, _ := json.Marshal(protocol.ThreadStartData{CWD: t.TempDir()})
	send(protocol.ThreadCommand{Type: "thread.start", Data: startData})

	gotStarted := false
	for i := 0; i < 50 && !gotStarted; i++ {
		ev, ok := readEvent()
		if !ok {
			break
		}
		check(ev)
		if ev.Type == "event.thread_started" {
			gotStarted = true
		}
	}
	if !gotStarted {
		t.Fatal("did not receive event.thread_started")
	}

	// Drive an input turn. Without configured credentials the daemon emits an
	// error notice followed by agent_done — both real events over the wire.
	inputData, _ := json.Marshal(protocol.ThreadInputData{Text: "hello"})
	send(protocol.ThreadCommand{Type: "thread.input", Data: inputData})

	gotDone := false
	for i := 0; i < 200 && !gotDone; i++ {
		ev, ok := readEvent()
		if !ok {
			break
		}
		check(ev)
		if ev.Type == "event.agent_done" {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatal("did not receive event.agent_done after input")
	}

	// Sanity: we actually saw and validated the core lifecycle events.
	for _, want := range []string{"event.thread_started", "event.agent_done"} {
		if !validated[want] {
			t.Errorf("expected to validate %q but never saw it", want)
		}
	}
}

// TestProtocolConformanceThreadListRPC drives the one-shot thread.list RPC
// over a real socket against seeded records and validates every returned
// ThreadSummary against the generated schema — the RPC-projection analog of the
// event conformance guard.
func TestProtocolConformanceThreadListRPC(t *testing.T) {
	dir := t.TempDir()
	const cwd = "/work"
	paths := config.NewVixPaths(dir, "", cwd)

	user := sampleRecord()
	user.ID = "user-1"
	user.CWD = cwd

	vixRun := sampleRecord()
	vixRun.ID = "vix-1"
	vixRun.CWD = "/elsewhere"
	vixRun.Origin = "vix"
	vixRun.Trigger = &protocol.TriggerInfo{Type: "cron", Ref: "job-1"}

	for _, r := range []threadRecord{user, vixRun} {
		if err := saveThreadRecord(paths, r); err != nil {
			t.Fatalf("save %s: %v", r.ID, err)
		}
	}

	srv := newInstanceTestServer(t)
	RegisterBuiltinHandlers(srv)
	_, cancel := serve(t, srv)
	defer cancel()

	conn, err := net.Dial("unix", srv.sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req, _ := json.Marshal(map[string]any{"command": "thread.list", "cwd": cwd, "config_dir": dir})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		t.Fatalf("write thread.list: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var resp map[string]json.RawMessage
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("read thread.list response: %v", err)
	}
	var status string
	json.Unmarshal(resp["status"], &status)
	if status != "ok" {
		t.Fatalf("thread.list status = %q, want ok", status)
	}

	var threads []map[string]any
	if err := json.Unmarshal(resp["threads"], &threads); err != nil {
		t.Fatalf("decode threads: %v", err)
	}
	if len(threads) == 0 {
		t.Fatal("expected at least one thread summary from seeded records")
	}
	for i, s := range threads {
		if err := protoschema.ValidateRPC("ThreadSummary", s); err != nil {
			t.Errorf("thread[%d] does not conform to ThreadSummary schema: %v", i, err)
		}
	}
}
