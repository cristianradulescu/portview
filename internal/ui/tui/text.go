package tui

import (
	"regexp"
	"strings"

	"github.com/rivo/uniseg"
)

// ANSI SGR helpers. gocui interprets these escape sequences when rendering
// view content.
const (
	reset = "\x1b[0m"

	FgRed     = "31"
	FgGreen   = "32"
	FgYellow  = "33"
	FgBlue    = "34"
	FgMagenta = "35"
	FgCyan    = "36"
	FgGray    = "90"
)

// Color wraps s in the given SGR code(s), e.g. Color("x", FgRed).
func Color(s string, codes ...string) string {
	if s == "" || len(codes) == 0 {
		return s
	}
	return "\x1b[" + strings.Join(codes, ";") + "m" + s + reset
}

// Bold renders s in bold.
func Bold(s string) string { return Color(s, "1") }

// Dim renders s in a dimmed gray.
func Dim(s string) string { return Color(s, FgGray) }

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// StripANSI removes SGR sequences.
func StripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// StringWidth is the display width of s in terminal cells, ignoring SGR
// sequences.
func StringWidth(s string) int { return uniseg.StringWidth(StripANSI(s)) }

// Truncate shortens plain (uncolored) s to at most width cells, ending with
// "…" when cut.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if uniseg.StringWidth(s) <= width {
		return s
	}
	var (
		sb strings.Builder
		w  int
	)
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		cw := g.Width()
		if w+cw > width-1 {
			break
		}
		sb.WriteString(g.Str())
		w += cw
	}
	return sb.String() + "…"
}

// TruncateMiddle shortens plain s to at most width cells by cutting out
// its middle, which keeps both ends visible (e.g. the port of an address).
func TruncateMiddle(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if uniseg.StringWidth(s) <= width {
		return s
	}
	if width < 3 {
		return Truncate(s, width)
	}
	var cl []string // grapheme clusters
	for g := uniseg.NewGraphemes(s); g.Next(); {
		cl = append(cl, g.Str())
	}
	keep := width - 1
	tailW := keep / 2
	headW := keep - tailW
	var head, tail strings.Builder
	w := 0
	for _, c := range cl {
		cw := uniseg.StringWidth(c)
		if w+cw > headW {
			break
		}
		head.WriteString(c)
		w += cw
	}
	var tailParts []string
	w = 0
	for i := len(cl) - 1; i >= 0; i-- {
		cw := uniseg.StringWidth(cl[i])
		if w+cw > tailW {
			break
		}
		tailParts = append(tailParts, cl[i])
		w += cw
	}
	for i := len(tailParts) - 1; i >= 0; i-- {
		tail.WriteString(tailParts[i])
	}
	return head.String() + "…" + tail.String()
}

// PadRight pads s (which may contain SGR sequences) with spaces to width
// cells.
func PadRight(s string, width int) string {
	if pad := width - StringWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// Fit truncates plain s to width and pads it to exactly width cells.
func Fit(s string, width int) string { return PadRight(Truncate(s, width), width) }

// JoinStatus lays out left and right parts on one line of the given width.
// The right part is dropped piece by piece (from the end) if both do not fit.
func JoinStatus(left string, right []string, width int) string {
	sep := "  "
	for n := len(right); n >= 0; n-- {
		r := strings.Join(right[:n], sep)
		gap := width - StringWidth(left) - StringWidth(r)
		if n == 0 || gap >= 2 {
			if gap < 1 {
				return left
			}
			return left + strings.Repeat(" ", gap) + r
		}
	}
	return left
}

// WrapHanging wraps plain text to width cells. Continuation lines are
// prefixed with indent (whose width is subtracted from them). It breaks at
// spaces where possible and inside words otherwise.
func WrapHanging(text string, width int, indent string) []string {
	if width <= 0 || uniseg.StringWidth(text) <= width {
		return []string{text}
	}
	var (
		lines []string
		cur   strings.Builder
		curW  int
		limit = width
	)
	flush := func() {
		lines = append(lines, strings.TrimRight(cur.String(), " "))
		cur.Reset()
		curW = 0
		limit = max(1, width-uniseg.StringWidth(indent))
	}
	for _, word := range strings.SplitAfter(text, " ") {
		ww := uniseg.StringWidth(word)
		if curW > 0 && curW+uniseg.StringWidth(strings.TrimRight(word, " ")) > limit {
			flush()
		}
		// Only the word itself must fit: its trailing space may hang past
		// the end (it is trimmed), or it would start the next line.
		for uniseg.StringWidth(strings.TrimRight(word, " ")) > limit-curW { // word longer than the remaining line
			g := uniseg.NewGraphemes(word)
			var head strings.Builder
			hw := 0
			for g.Next() && hw+g.Width() <= limit-curW {
				head.WriteString(g.Str())
				hw += g.Width()
			}
			if hw == 0 {
				break
			}
			cur.WriteString(head.String())
			curW += hw
			word = word[head.Len():]
			ww -= hw
			flush()
		}
		cur.WriteString(word)
		curW += ww
	}
	if cur.Len() > 0 {
		lines = append(lines, strings.TrimRight(cur.String(), " "))
	}
	for i := 1; i < len(lines); i++ {
		lines[i] = indent + lines[i]
	}
	return lines
}

// Chunk splits plain s into lines of at most width cells, breaking
// anywhere (between grapheme clusters), like a terminal does.
func Chunk(s string, width int) []string {
	if width <= 0 || uniseg.StringWidth(s) <= width {
		return []string{s}
	}
	var (
		out []string
		sb  strings.Builder
		w   int
	)
	for g := uniseg.NewGraphemes(s); g.Next(); {
		cw := g.Width()
		if w+cw > width && sb.Len() > 0 {
			out = append(out, sb.String())
			sb.Reset()
			w = 0
		}
		sb.WriteString(g.Str())
		w += cw
	}
	if sb.Len() > 0 {
		out = append(out, sb.String())
	}
	return out
}
