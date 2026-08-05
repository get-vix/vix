package report

import "testing"

func TestAnsiToHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello", "hello"},
		{"escapes html", "a<b>&c", "a&lt;b&gt;&amp;c"},
		{"basic blue then reset", "\x1b[34mblue\x1b[0m end",
			`<span style="color:#2472c8">blue</span> end`},
		{"bold", "\x1b[1mhi\x1b[0m", `<span style="font-weight:600">hi</span>`},
		{"truecolor fg", "\x1b[38;2;255;0;0mr\x1b[0m",
			`<span style="color:#ff0000">r</span>`},
		{"256 color", "\x1b[38;5;33mx\x1b[0m",
			`<span style="color:#0087ff">x</span>`},
		{"non-sgr csi stripped", "a\x1b[2Kb", "ab"},
		{"reset only at end yields no trailing span", "\x1b[0mhi", "hi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(ansiToHTML(c.in))
			if got != c.want {
				t.Errorf("ansiToHTML(%q)\n  got  %q\n  want %q", c.in, got, c.want)
			}
		})
	}
}
