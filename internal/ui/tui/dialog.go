package tui

import (
	"strconv"
	"strings"

	"github.com/jesseduffield/gocui"
)

// Choice is one selectable line of a Dialog.
type Choice struct {
	Label   string
	Handler func() error
	// Danger shows the choice in red (e.g. SIGKILL).
	Danger bool
	// Commands are shown below the choices while this one is selected,
	// e.g. the exact command it runs.
	Commands []string
}

// Dialog is a modal box with a text body and a list of choices, picked
// with j/k and Enter or by number. Without choices it is a message box
// that Enter and Esc close.
type Dialog struct {
	Name string
	// MaxWidth caps the box width (default 100 cells).
	MaxWidth int
	// OnCancel runs after Esc/q closed the dialog, and after Enter closed a
	// message box. Open resets it; set it after each Open.
	OnCancel func() error
	// Busy makes Enter do nothing (a progress message). Open resets it.
	Busy bool
	// Footer lines are shown below the choices (key hints). Open resets
	// it.
	Footer []string
	// Relayout, if set, runs after a handler reopened the dialog. gocui
	// handles all queued keys before laying out, so without it keys typed
	// right after a choice would reach the view below.
	Relayout func() error

	active   bool
	title    string
	body     []string
	choices  []Choice
	selected int
	origin   int
}

// Active reports whether the dialog is shown.
func (d *Dialog) Active() bool { return d.active }

// Open shows the dialog, replacing what it showed before. body lines may
// contain SGR sequences; long lines are cut to the box width. sel is the
// preselected choice.
func (d *Dialog) Open(title string, body []string, choices []Choice, sel int) {
	d.active, d.title, d.body, d.choices = true, title, body, choices
	d.selected = max(0, min(sel, len(choices)-1))
	d.origin = 0
	d.OnCancel, d.Busy, d.Footer = nil, false, nil
}

// Selected returns the index of the selected choice.
func (d *Dialog) Selected() int { return d.selected }

// Close hides the dialog and returns focus to focusView.
func (d *Dialog) Close(g *gocui.Gui, focusView string) error {
	d.active = false
	if err := DeleteView(g, d.Name); err != nil {
		return err
	}
	_, err := g.SetCurrentView(focusView)
	if IsUnknownView(err) {
		return nil
	}
	return err
}

// Move changes the selection by delta, clamped.
func (d *Dialog) Move(delta int) {
	d.selected = max(0, min(d.selected+delta, len(d.choices)-1))
}

// Width is the inner width the dialog would use on a screen maxX wide.
func (d *Dialog) Width(maxX int) int {
	limit := d.MaxWidth
	if limit <= 0 {
		limit = 100
	}
	w := StringWidth(d.title) + 2
	for _, l := range d.body {
		w = max(w, StringWidth(l)+2)
	}
	for i, c := range d.choices {
		w = max(w, StringWidth(choicePrefix(i))+StringWidth(c.Label)+1)
	}
	for _, l := range d.Footer {
		w = max(w, StringWidth(l)+2)
	}
	for _, c := range d.choices {
		for _, l := range c.Commands {
			w = max(w, StringWidth(l)+6)
		}
	}
	return max(20, min(w, limit, maxX-4))
}

func choicePrefix(i int) string {
	if i < 9 {
		return " " + strconv.Itoa(i+1) + ". "
	}
	return "    "
}

// Lines renders the content for an inner width w.
func (d *Dialog) Lines(w int) []string {
	lines, _ := d.render(w)
	return lines
}

// render returns the lines and the index of the selected choice's line.
func (d *Dialog) render(w int) ([]string, int) {
	selLine := -1
	lines := make([]string, 0, len(d.body)+len(d.choices)+len(d.Footer)+2)
	// Text lines get a one-cell margin on each side.
	fit := func(l string) string {
		if l == "" {
			return ""
		}
		if StringWidth(l) > w-2 {
			l = Truncate(StripANSI(l), w-2)
		}
		return " " + l
	}
	for _, l := range d.body {
		lines = append(lines, fit(l))
	}
	if len(d.choices) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		for i, c := range d.choices {
			label := Truncate(c.Label, w-StringWidth(choicePrefix(i)))
			line := PadRight(choicePrefix(i)+label, w)
			var codes []string
			if c.Danger {
				codes = append(codes, FgRed)
			}
			if i == d.selected {
				codes = append(codes, "7") // reverse video
				selLine = len(lines)
			}
			lines = append(lines, Color(line, codes...))
		}
		if cmds := d.choices[d.selected].Commands; len(cmds) > 0 {
			lines = append(lines, "")
			for _, c := range cmds {
				lines = append(lines, Color(fit("$ "+c), FgCyan))
			}
		}
	}
	if len(d.Footer) > 0 {
		lines = append(lines, "")
		for _, l := range d.Footer {
			lines = append(lines, fit(l))
		}
	}
	return lines, selLine
}

// Layout draws the dialog when active.
func (d *Dialog) Layout(g *gocui.Gui) error {
	if !d.active {
		return nil
	}
	maxX, maxY := g.Size()
	w := d.Width(maxX)
	lines, selLine := d.render(w)
	v, created, err := SetView(g, d.Name, Centered(maxX, maxY, w+1, len(lines)+1))
	if err != nil {
		return err
	}
	if created {
		v.Wrap = false
		if _, err := g.SetCurrentView(d.Name); err != nil {
			return err
		}
	}
	v.Title = d.title
	_, h := v.InnerSize()
	// Keep the selected choice in view; the body scrolls away if needed.
	if selLine >= 0 {
		d.origin = max(0, selLine-h+1)
	} else {
		d.origin = min(d.origin, max(0, len(lines)-h))
	}
	v.Clear()
	v.WriteString(strings.Join(lines[d.origin:], "\n"))
	return nil
}

// Bindings returns the keys of the dialog view. Choosing or cancelling
// closes the dialog and focuses focusView before the handler runs, so a
// handler may open the dialog again with new content.
func (d *Dialog) Bindings(g *gocui.Gui, focusView string) []Binding {
	after := func(err error) error {
		if err == nil && d.active && d.Relayout != nil {
			return d.Relayout()
		}
		return err
	}
	cancel := func() error {
		onCancel := d.OnCancel
		if err := d.Close(g, focusView); err != nil {
			return err
		}
		if onCancel != nil {
			return after(onCancel())
		}
		return nil
	}
	// choose(n) picks choice n; choose(-1) picks the selected one (Enter).
	choose := func(n int) func() error {
		return func() error {
			if len(d.choices) == 0 {
				if d.Busy || n >= 0 {
					return nil
				}
				return cancel() // message box: Enter closes it
			}
			i := n // never assign to n: the closure is reused for every press
			if i < 0 {
				i = d.selected
			}
			if i >= len(d.choices) {
				return nil
			}
			c := d.choices[i]
			if err := d.Close(g, focusView); err != nil {
				return err
			}
			if c.Handler != nil {
				return after(c.Handler())
			}
			return nil
		}
	}
	bs := []Binding{
		{View: d.Name, Keys: []any{'j', gocui.KeyArrowDown}, Handler: func() error { d.Move(1); return nil }},
		{View: d.Name, Keys: []any{'k', gocui.KeyArrowUp}, Handler: func() error { d.Move(-1); return nil }},
		{View: d.Name, Keys: []any{gocui.KeyEnter}, Handler: choose(-1)},
		{View: d.Name, Keys: []any{gocui.KeyEsc, 'q'}, Handler: cancel},
	}
	for i := range 9 {
		bs = append(bs, Binding{View: d.Name, Keys: []any{rune('1' + i)}, Handler: choose(i)})
	}
	return bs
}
