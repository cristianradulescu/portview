// Package termsafe makes untrusted strings safe to print to a terminal.
//
// Process names, command lines and container names are controlled by
// whoever started the process; any user can, for example, set its comm to
// contain escape sequences. Printed raw — especially by a root-run portview —
// those could rewrite the screen or drive the terminal. Every such string
// must pass through String before display.
package termsafe

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// String returns s with control characters (C0, DEL, C1), invalid UTF-8 and
// Unicode format characters (e.g. bidi overrides) replaced by visible \xNN or
// \u{NNNN} escapes. Printable text, including non-ASCII, is kept as is.
func String(s string) string {
	if isClean(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r < 0x80 && unsafeRune(r):
			fmt.Fprintf(&b, `\x%02x`, r)
		case unsafeRune(r):
			fmt.Fprintf(&b, `\u{%04x}`, r)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

func isClean(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f || c >= 0x80 {
			if c >= 0x80 {
				return isCleanSlow(s[i:])
			}
			return false
		}
	}
	return true
}

func isCleanSlow(s string) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size <= 1) || unsafeRune(r) {
			return false
		}
		i += size
	}
	return true
}

func unsafeRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || unicode.Is(unicode.Cf, r)
}
