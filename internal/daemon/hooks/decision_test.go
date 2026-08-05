package hooks

import "testing"

func TestParseCommandDecision(t *testing.T) {
	cases := []struct {
		name           string
		code           int
		stdout, stderr string
		want           Decision
	}{
		{"exit0 empty allow", 0, "", "", Decision{Behavior: BehaviorAllow}},
		{"exit2 deny stderr", 2, "", "nope", Decision{Behavior: BehaviorDeny, Reason: "nope"}},
		{"exit2 deny stdout fallback", 2, "blocked", "", Decision{Behavior: BehaviorDeny, Reason: "blocked"}},
		{"exit0 text is context", 0, "some note", "", Decision{Behavior: BehaviorContext, Context: "some note"}},
		{"exit0 json deny", 0, `{"behavior":"deny","reason":"r"}`, "", Decision{Behavior: BehaviorDeny, Reason: "r"}},
		{"exit0 json modify", 0, `{"behavior":"modify","input":{"path":"x"}}`, "", Decision{Behavior: BehaviorModify, Input: map[string]any{"path": "x"}}},
		{"exit0 json context", 0, `{"behavior":"context","context":"c"}`, "", Decision{Behavior: BehaviorContext, Context: "c"}},
		{"nonzero non2 fails open", 1, "x", "y", Decision{Behavior: BehaviorAllow}},
		{"exit0 non-decision json treated as context", 0, `{"foo":1}`, "", Decision{Behavior: BehaviorContext, Context: `{"foo":1}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseCommandDecision(tc.code, tc.stdout, tc.stderr)
			if got.Behavior != tc.want.Behavior || got.Reason != tc.want.Reason || got.Context != tc.want.Context {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
			if tc.want.Input != nil && got.Input["path"] != tc.want.Input["path"] {
				t.Fatalf("input got %+v want %+v", got.Input, tc.want.Input)
			}
		})
	}
}

func TestParseTextDecision(t *testing.T) {
	if d := ParseTextDecision(""); d.Behavior != BehaviorAllow {
		t.Errorf("empty: %+v", d)
	}
	if d := ParseTextDecision("BLOCK: too risky"); d.Behavior != BehaviorDeny || d.Reason != "too risky" {
		t.Errorf("sentinel: %+v", d)
	}
	if d := ParseTextDecision(`{"behavior":"deny","reason":"x"}`); d.Behavior != BehaviorDeny {
		t.Errorf("json: %+v", d)
	}
	if d := ParseTextDecision("all good"); d.Behavior != BehaviorContext || d.Context != "all good" {
		t.Errorf("text: %+v", d)
	}
}

func TestCombine(t *testing.T) {
	// deny beats modify beats context beats allow.
	got := Combine([]Decision{
		{Behavior: BehaviorContext, Context: "c1"},
		{Behavior: BehaviorModify, Input: map[string]any{"a": 1}},
		{Behavior: BehaviorDeny, Reason: "stop"},
		{Behavior: BehaviorAllow},
	})
	if got.Behavior != BehaviorDeny || got.Reason != "stop" {
		t.Fatalf("expected deny, got %+v", got)
	}
	if got.Context != "c1" {
		t.Errorf("context should be preserved across hooks: %q", got.Context)
	}

	// modify wins over context/allow.
	got = Combine([]Decision{{Behavior: BehaviorContext, Context: "c"}, {Behavior: BehaviorModify, Input: map[string]any{"a": 1}}})
	if got.Behavior != BehaviorModify {
		t.Fatalf("expected modify, got %+v", got)
	}

	// contexts concatenated.
	got = Combine([]Decision{{Behavior: BehaviorContext, Context: "a"}, {Behavior: BehaviorContext, Context: "b"}})
	if got.Behavior != BehaviorContext || got.Context != "a\nb" {
		t.Fatalf("expected joined context, got %+v", got)
	}

	// empty → allow.
	if got = Combine(nil); got.Behavior != BehaviorAllow {
		t.Fatalf("expected allow, got %+v", got)
	}
}
