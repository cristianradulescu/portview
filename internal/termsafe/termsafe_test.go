package termsafe

import "testing"

func TestString(t *testing.T) {
	tests := map[string]string{
		"nginx":                     "nginx",
		"python3.12":                "python3.12",
		"café ✓ 日本":                 "café ✓ 日本",
		"evil\x1b[2J\x1b]0;pwn\x07": `evil\x1b[2J\x1b]0;pwn\x07`,
		"tab\there\nnl":             `tab\x09here\x0anl`,
		"del\x7f":                   `del\x7f`,
		"c1\u009bX":                 `c1\u{009b}X`,
		"bidi\u202eexe.txt":         `bidi\u{202e}exe.txt`,
		"bad\xffutf8":               `bad\xffutf8`,
		"":                          "",
	}
	for in, want := range tests {
		if got := String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}
