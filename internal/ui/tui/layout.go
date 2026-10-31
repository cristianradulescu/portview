package tui

import (
	"errors"

	"github.com/jesseduffield/gocui"
)

// Rect is a view rectangle in gocui coordinates (frame corners inclusive).
type Rect struct{ X0, Y0, X1, Y1 int }

// Centered returns a w×h rectangle centered in a maxX×maxY screen, shrunk to
// fit.
func Centered(maxX, maxY, w, h int) Rect {
	w = min(w, maxX-2)
	h = min(h, maxY-2)
	x0 := (maxX - w) / 2
	y0 := (maxY - h) / 2
	return Rect{x0, y0, x0 + w, y0 + h}
}

// SetView creates or updates a view; created reports whether it is new, so
// callers can do one-time initialization.
func SetView(g *gocui.Gui, name string, r Rect) (v *gocui.View, created bool, err error) {
	v, err = g.SetView(name, r.X0, r.Y0, r.X1, r.Y1, 0)
	if err != nil {
		if !IsUnknownView(err) {
			return nil, false, err
		}
		return v, true, nil
	}
	return v, false, nil
}

// DeleteView removes a view if it exists.
func DeleteView(g *gocui.Gui, name string) error {
	if err := g.DeleteView(name); err != nil && !IsUnknownView(err) {
		return err
	}
	return nil
}

// IsUnknownView reports whether err is gocui.ErrUnknownView. gocui wraps it
// with go-errors v1.0.2, which does not implement Unwrap, so errors.Is alone
// does not see through the wrapper.
func IsUnknownView(err error) bool {
	return err != nil && (errors.Is(err, gocui.ErrUnknownView) || err.Error() == gocui.ErrUnknownView.Error())
}
