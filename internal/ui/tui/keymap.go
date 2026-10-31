package tui

import (
	"fmt"
	"strings"

	"github.com/jesseduffield/gocui"
)

// Binding is one keybinding. Keys are gocui.Key values or runes; all of them
// trigger Handler when View is focused ("" means any view).
type Binding struct {
	View    string
	Keys    []any
	Mod     gocui.Modifier
	Handler func() error

	// Label is how the keys are shown in help, e.g. "j/↓".
	Label string
	// Description is a short help text. Bindings without one are not listed.
	Description string
	// Section groups bindings in the help screen.
	Section string
	// Hint, when set, is shown in the status bar (e.g. "l/e/a state"); one
	// binding of a group can carry the hint for all of them.
	Hint string
}

// Keymap is a registry of bindings. It is the single source of truth for
// both gocui keybindings and the generated help, so they cannot drift apart.
type Keymap struct {
	bindings []Binding
}

// Add registers bindings.
func (k *Keymap) Add(bs ...Binding) {
	k.bindings = append(k.bindings, bs...)
}

// Bindings returns the registered bindings in registration order.
func (k *Keymap) Bindings() []Binding { return k.bindings }

// Apply installs all bindings on g.
func (k *Keymap) Apply(g *gocui.Gui) error {
	for _, b := range k.bindings {
		h := b.Handler
		for _, key := range b.Keys {
			if err := g.SetKeybinding(b.View, key, b.Mod, func(*gocui.Gui, *gocui.View) error { return h() }); err != nil {
				return fmt.Errorf("binding %v on view %q: %w", key, b.View, err)
			}
		}
	}
	return nil
}

// Hints returns the status bar hints of the bindings for view.
func (k *Keymap) Hints(view string) []string {
	var out []string
	for _, b := range k.bindings {
		if b.Hint != "" && (b.View == view || b.View == "") {
			out = append(out, b.Hint)
		}
	}
	return out
}

// Help renders the bindings of the given views as text grouped by section,
// in registration order.
func (k *Keymap) Help(views ...string) string {
	want := make(map[string]bool, len(views))
	for _, v := range views {
		want[v] = true
	}
	var (
		sections []string
		bySec    = map[string][]Binding{}
		width    int
	)
	for _, b := range k.bindings {
		if b.Description == "" || !want[b.View] {
			continue
		}
		if _, ok := bySec[b.Section]; !ok {
			sections = append(sections, b.Section)
		}
		bySec[b.Section] = append(bySec[b.Section], b)
		width = max(width, StringWidth(b.Label))
	}
	var sb strings.Builder
	for i, sec := range sections {
		if i > 0 {
			sb.WriteString("\n")
		}
		if sec != "" {
			sb.WriteString(Bold(sec) + "\n")
		}
		for _, b := range bySec[sec] {
			fmt.Fprintf(&sb, "  %s  %s\n", PadRight(b.Label, width), b.Description)
		}
	}
	return sb.String()
}
