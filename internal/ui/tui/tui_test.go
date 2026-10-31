package tui

import (
	"strings"
	"testing"
)

func TestListStateClamp(t *testing.T) {
	s := NewListState()
	s.Clamp(0, 5)
	if s.Selected != -1 || s.Origin != 0 {
		t.Errorf("empty: %+v", s)
	}
	s.Clamp(3, 5)
	if s.Selected != 0 {
		t.Errorf("first row should be selected: %+v", s)
	}
	s = ListState{Selected: 9, Origin: 9}
	s.Clamp(10, 4)
	if s.Origin != 6 {
		t.Errorf("should not leave empty space at the bottom: %+v", s)
	}
}

func TestListStateMoveScrollsMinimally(t *testing.T) {
	s := ListState{}
	for range 5 {
		s.Move(1, 10, 4)
	}
	if s.Selected != 5 || s.Origin != 2 {
		t.Errorf("down: %+v", s)
	}
	s.Move(-4, 10, 4)
	if s.Selected != 1 || s.Origin != 1 {
		t.Errorf("up: %+v", s)
	}
	s.Move(100, 10, 4)
	if s.Selected != 9 || s.Origin != 6 {
		t.Errorf("clamped: %+v", s)
	}
}

func TestListStateReanchor(t *testing.T) {
	s := ListState{Selected: 5, Origin: 3} // selected on screen line 2
	s.Reanchor(7, 20, 5)
	if s.Selected != 7 || s.Origin != 5 {
		t.Errorf("reanchor: %+v", s)
	}
	s.Reanchor(1, 20, 5) // can't keep line 2: origin would be negative
	if s.Selected != 1 || s.Origin != 0 {
		t.Errorf("reanchor near top: %+v", s)
	}
	s.Reanchor(-1, 0, 5)
	if s.Selected != -1 {
		t.Errorf("reanchor empty: %+v", s)
	}
}

func TestTextHelpers(t *testing.T) {
	if got := StringWidth(Color("abc", FgRed)); got != 3 {
		t.Errorf("StringWidth ignores SGR: %d", got)
	}
	if got := Truncate("postgresql-main", 8); got != "postgre…" {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("日本語テキスト", 5); StringWidth(got) > 5 {
		t.Errorf("Truncate wide = %q (%d cells)", got, StringWidth(got))
	}
	if got := TruncateMiddle("[2a02:2f0a:120b:c900:e28a:2d38:ec79:e11a]:32818", 25); got != "[2a02:2f0a:1…:e11a]:32818" {
		t.Errorf("TruncateMiddle = %q", got)
	}
	if got := TruncateMiddle("short", 25); got != "short" {
		t.Errorf("TruncateMiddle short = %q", got)
	}
	if got := Fit("ab", 4); got != "ab  " {
		t.Errorf("Fit = %q", got)
	}
	if got := Truncate("x", 0); got != "" {
		t.Errorf("Truncate to 0 = %q", got)
	}
}

func TestJoinStatus(t *testing.T) {
	got := JoinStatus("left", []string{"a 1", "b 2"}, 20)
	if StringWidth(got) != 20 || !strings.HasPrefix(got, "left") || !strings.HasSuffix(got, "a 1  b 2") {
		t.Errorf("JoinStatus = %q", got)
	}
	// Not enough room: hints are dropped from the end.
	got = JoinStatus("left", []string{"a 1", "b 2"}, 12)
	if !strings.HasSuffix(got, "a 1") || strings.Contains(got, "b 2") {
		t.Errorf("narrow JoinStatus = %q", got)
	}
	if got := JoinStatus("a very long left part", []string{"x"}, 5); got != "a very long left part" {
		t.Errorf("overflow JoinStatus = %q", got)
	}
}

func TestKeymapHelpAndHints(t *testing.T) {
	var k Keymap
	k.Add(
		Binding{View: "list", Keys: []any{'j'}, Label: "j", Description: "down", Section: "Nav"},
		Binding{View: "list", Keys: []any{'q'}, Label: "q", Description: "quit", Section: "General", Hint: "q quit"},
		Binding{View: "", Keys: []any{'x'}, Label: "^c", Description: "quit too", Section: "General"},
		Binding{View: "popup", Keys: []any{'z'}, Label: "z", Description: "hidden from list help"},
		Binding{View: "list", Keys: []any{'n'}}, // no description: not listed
	)
	help := StripANSI(k.Help("list", ""))
	for _, want := range []string{"Nav", "j   down", "General", "q   quit", "^c  quit too"} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, "hidden") {
		t.Errorf("help contains other view's binding:\n%s", help)
	}
	if h := k.Hints("list"); len(h) != 1 || h[0] != "q quit" {
		t.Errorf("hints = %v", h)
	}
}

func TestCentered(t *testing.T) {
	r := Centered(100, 40, 50, 10)
	if r != (Rect{25, 15, 75, 25}) {
		t.Errorf("Centered = %+v", r)
	}
	r = Centered(30, 10, 50, 20)
	if r.X1-r.X0 != 28 || r.Y1-r.Y0 != 8 {
		t.Errorf("Centered shrink = %+v", r)
	}
}

func TestWrapHanging(t *testing.T) {
	got := WrapHanging("nginx -g daemon on; master_process on;", 16, "    ")
	want := []string{"nginx -g daemon", "    on;", "    master_proce", "    ss on;"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("WrapHanging = %q", got)
	}
	for _, l := range got {
		if StringWidth(l) > 16 {
			t.Errorf("line too wide: %q", l)
		}
	}
	if got := WrapHanging("short", 16, "  "); len(got) != 1 || got[0] != "short" {
		t.Errorf("short = %q", got)
	}
	long := WrapHanging("/a/very/long/path/without/any/spaces/at/all", 12, "  ")
	for _, l := range long {
		if StringWidth(l) > 12 {
			t.Errorf("hard wrap too wide: %q", l)
		}
	}
	if strings.ReplaceAll(strings.Join(long, ""), "  ", "") != "/a/very/long/path/without/any/spaces/at/all" {
		t.Errorf("hard wrap lost text: %q", long)
	}
}
