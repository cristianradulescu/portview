package tui

import (
	"strings"

	"github.com/jesseduffield/gocui"
)

// Popup is a framed, scrollable, read-only text box centered on screen,
// used for help and messages.
type Popup struct {
	Name  string
	Title string

	active bool
	text   string
	origin int
}

// Active reports whether the popup is shown.
func (p *Popup) Active() bool { return p.active }

// Open shows text in the popup.
func (p *Popup) Open(text string) {
	p.active, p.text, p.origin = true, text, 0
}

// Close hides the popup and returns focus to focusView.
func (p *Popup) Close(g *gocui.Gui, focusView string) error {
	p.active = false
	if err := DeleteView(g, p.Name); err != nil {
		return err
	}
	if _, err := g.SetCurrentView(focusView); err != nil && !IsUnknownView(err) {
		return err // an unknown view: the manager picks the focus
	}
	return nil
}

// Scroll moves the popup content by delta lines.
func (p *Popup) Scroll(delta int) { p.origin = max(0, p.origin+delta) }

// Layout draws the popup when active.
func (p *Popup) Layout(g *gocui.Gui) error {
	if !p.active {
		return nil
	}
	maxX, maxY := g.Size()
	lines := strings.Split(strings.TrimRight(p.text, "\n"), "\n")
	w := 0
	for _, l := range lines {
		w = max(w, StringWidth(l))
	}
	r := Centered(maxX, maxY, w+3, len(lines)+1)
	v, created, err := SetView(g, p.Name, r)
	if err != nil {
		return err
	}
	if created {
		v.Title = p.Title
		v.Wrap = false
		if _, err := g.SetCurrentView(p.Name); err != nil {
			return err
		}
	}
	_, h := v.InnerSize()
	p.origin = min(p.origin, max(0, len(lines)-h))
	v.Clear()
	v.WriteString(strings.Join(lines[p.origin:], "\n"))
	return nil
}

// Bindings returns close/scroll bindings for the popup view. closeKeys
// close it and return focus to focusView.
func (p *Popup) Bindings(g *gocui.Gui, focusView string, closeKeys ...any) []Binding {
	return []Binding{
		{View: p.Name, Keys: closeKeys, Handler: func() error { return p.Close(g, focusView) }},
		{View: p.Name, Keys: []any{'j', gocui.KeyArrowDown}, Handler: func() error { p.Scroll(1); return nil }},
		{View: p.Name, Keys: []any{'k', gocui.KeyArrowUp}, Handler: func() error { p.Scroll(-1); return nil }},
	}
}
