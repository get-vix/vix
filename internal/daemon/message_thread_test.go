package daemon

import (
	"testing"

	"github.com/get-vix/vix/internal/config"
)

// newMessageTestServer builds a bare server with a temp home dir and the
// builtin handlers (which include message.create) registered.
func newMessageTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	s := &Server{handlers: make(map[string]HandlerFunc), homeVixDir: home}
	RegisterBuiltinHandlers(s)
	return s, home
}

func TestCreateMessageThread(t *testing.T) {
	s, home := newMessageTestServer(t)
	cwd := t.TempDir()

	id, err := s.createMessageThread(MessageThreadSpec{
		Message: "Thanks for using vix!",
		CWD:     cwd,
		Title:   "Feedback",
	})
	if err != nil {
		t.Fatalf("createMessageThread: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty thread id")
	}

	paths := config.NewVixPaths("", home, cwd)
	rec, found, err := loadOpenThreadRecord(paths, id)
	if err != nil || !found {
		t.Fatalf("record not persisted to open/ (found=%v err=%v)", found, err)
	}
	if rec.Origin != "vix" {
		t.Errorf("origin = %q, want vix", rec.Origin)
	}
	if !rec.Unread {
		t.Error("expected unread = true by default")
	}
	if rec.Title != "Feedback" {
		t.Errorf("title = %q, want Feedback", rec.Title)
	}
	if rec.CWD != cwd {
		t.Errorf("cwd = %q, want %q", rec.CWD, cwd)
	}
	if len(rec.Messages) != 1 || len(rec.Messages[0].Content) != 1 ||
		rec.Messages[0].Content[0].Text != "Thanks for using vix!" {
		t.Errorf("message not seeded as a single assistant text block: %+v", rec.Messages)
	}
}

func TestCreateMessageThreadValidation(t *testing.T) {
	s, _ := newMessageTestServer(t)
	cwd := t.TempDir()

	cases := []struct {
		name string
		spec MessageThreadSpec
	}{
		{"missing message", MessageThreadSpec{CWD: cwd}},
		{"blank message", MessageThreadSpec{Message: "   ", CWD: cwd}},
		{"missing cwd", MessageThreadSpec{Message: "hi"}},
		{"nonexistent cwd", MessageThreadSpec{Message: "hi", CWD: "/no/such/dir/xyz"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.createMessageThread(tc.spec); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestMessageCreateHandler(t *testing.T) {
	s, home := newMessageTestServer(t)
	cwd := t.TempDir()

	resp := callHandler(t, s, "message.create", map[string]any{
		"thread": map[string]any{
			"message": "hello from a hook",
			"cwd":     cwd,
			"title":   "Hook says hi",
		},
	})
	if resp["status"] != "ok" {
		t.Fatalf("status = %v, want ok (resp=%v)", resp["status"], resp)
	}
	id, _ := resp["thread_id"].(string)
	if id == "" {
		t.Fatal("missing thread_id in response")
	}

	paths := config.NewVixPaths("", home, cwd)
	if _, found, _ := loadOpenThreadRecord(paths, id); !found {
		t.Fatal("handler did not persist the thread record")
	}

	// Missing payload → error status, not a transport error.
	bad := callHandler(t, s, "message.create", map[string]any{})
	if bad["status"] != "error" {
		t.Errorf("missing 'thread' should yield error status, got %v", bad["status"])
	}
}
