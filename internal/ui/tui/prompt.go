package tui

import (
	"github.com/jesseduffield/gocui"
)

// Prompt is a single-line text input in a framed box. OnChange fires on
// every edit, which allows live filtering.
type Prompt struct {
	Name     string
	Title    string
	OnChange func(text string)
	OnSubmit func(text string) error
	OnCancel func() error

	active bool
	text   string
}

// Active reports whether the prompt is shown.
func (p *Prompt) Active() bool { return p.active }

// Open shows the prompt pre-filled with text. Call Layout afterwards (from
// the manager) to create the view.
func (p *Prompt) Open(text string) {
	p.active = true
	p.text = text
}

// Close hides the prompt and returns focus to focusView.
func (p *Prompt) Close(g *gocui.Gui, focusView string) error {
	p.active = false
	g.Cursor = false
	if err := DeleteView(g, p.Name); err != nil {
		return err
	}
	if focusView != "" {
		if _, err := g.SetCurrentView(focusView); err != nil && !IsUnknownView(err) {
			return err // an unknown view: the manager picks the focus
		}
	}
	return nil
}

// Layout creates/positions the prompt view inside r when active.
func (p *Prompt) Layout(g *gocui.Gui, r Rect) error {
	if !p.active {
		return nil
	}
	v, created, err := SetView(g, p.Name, r)
	if err != nil {
		return err
	}
	if created {
		v.Title = p.Title
		v.Editable = true
		v.Editor = gocui.EditorFunc(func(v *gocui.View, key gocui.Key, ch rune, mod gocui.Modifier) bool {
			if key == gocui.KeyEnter || key == gocui.KeyArrowUp || key == gocui.KeyArrowDown {
				return false // keep it single-line
			}
			handled := gocui.DefaultEditor.Edit(v, key, ch, mod)
			p.text = v.TextArea.GetContent()
			if p.OnChange != nil {
				p.OnChange(p.text)
			}
			return handled
		})
		v.TextArea.TypeString(p.text)
		v.RenderTextArea()
		g.Cursor = true
		if _, err := g.SetCurrentView(p.Name); err != nil {
			return err
		}
	}
	return nil
}

// Bindings returns the Enter/Esc bindings for the prompt view.
func (p *Prompt) Bindings() []Binding {
	return []Binding{
		{View: p.Name, Keys: []any{gocui.KeyEnter}, Handler: func() error {
			if p.OnSubmit != nil {
				return p.OnSubmit(p.text)
			}
			return nil
		}},
		{View: p.Name, Keys: []any{gocui.KeyEsc}, Handler: func() error {
			if p.OnCancel != nil {
				return p.OnCancel()
			}
			return nil
		}},
	}
}
