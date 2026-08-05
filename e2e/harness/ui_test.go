package harness

import "testing"

// TestFgColorOf checks the ANSI foreground extractor used by tab-title
// highlight/blink assertions. It mirrors what tmux's `capture-pane -e` emits:
// inline SGR runs, combined attributes, 256-color and truecolor foregrounds,
// and resets.
func TestFgColorOf(t *testing.T) {
	cases := []struct {
		name  string
		ansi  string
		label string
		want  string
		ok    bool
	}{
		{
			name:  "256-color fg",
			ansi:  "\x1b[38;5;245m Threads [F1] \x1b[0m",
			label: "Threads [F1]",
			want:  "38;5;245",
			ok:    true,
		},
		{
			name:  "combined bold + 256-color",
			ansi:  "\x1b[1;38;5;155mThreads [F1]\x1b[0m",
			label: "Threads [F1]",
			want:  "38;5;155",
			ok:    true,
		},
		{
			name:  "truecolor fg",
			ansi:  "\x1b[38;2;163;252;99mThreads [F1]\x1b[m",
			label: "Threads [F1]",
			want:  "38;2;163;252;99",
			ok:    true,
		},
		{
			name:  "basic fg then reset before label",
			ansi:  "\x1b[31mred\x1b[0mThreads [F1]",
			label: "Threads [F1]",
			want:  "",
			ok:    true,
		},
		{
			name:  "default fg via 39",
			ansi:  "\x1b[38;5;9m\x1b[39mThreads [F1]",
			label: "Threads [F1]",
			want:  "",
			ok:    true,
		},
		{
			name:  "label preceded by styled border run",
			ansi:  "\x1b[38;5;15m│\x1b[0m\x1b[38;5;42mThreads [F1]\x1b[0m\x1b[38;5;15m│\x1b[0m",
			label: "Threads [F1]",
			want:  "38;5;42",
			ok:    true,
		},
		{
			name:  "label absent",
			ansi:  "\x1b[38;5;245mWorkspace [F2]\x1b[0m",
			label: "Threads [F1]",
			want:  "",
			ok:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := fgColorOf(tc.ansi, tc.label)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("fgColorOf(%q, %q) = (%q, %v), want (%q, %v)",
					tc.ansi, tc.label, got, ok, tc.want, tc.ok)
			}
		})
	}
}
