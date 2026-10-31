package tui

// ListState is the selection and scroll position of a list rendered in a
// window of Height rows. It is pure state, independent of gocui, so the
// scrolling rules can be tested on their own.
type ListState struct {
	// Selected is the index of the selected row, -1 when the list is empty.
	Selected int
	// Origin is the index of the first visible row.
	Origin int
}

// NewListState returns the state of an empty list.
func NewListState() ListState { return ListState{Selected: -1} }

// Clamp brings the state back into range for n rows shown in a window of
// height rows, scrolling only as far as needed to keep the selection
// visible.
func (s *ListState) Clamp(n, height int) {
	if n <= 0 {
		s.Selected, s.Origin = -1, 0
		return
	}
	if height < 1 {
		height = 1
	}
	s.Selected = clamp(s.Selected, 0, n-1)
	if s.Selected < s.Origin {
		s.Origin = s.Selected
	}
	if s.Selected >= s.Origin+height {
		s.Origin = s.Selected - height + 1
	}
	// Do not leave empty space at the bottom when rows could fill it.
	s.Origin = clamp(s.Origin, 0, max(0, n-height))
}

// Select moves the selection to i.
func (s *ListState) Select(i, n, height int) {
	s.Selected = i
	s.Clamp(n, height)
}

// Move moves the selection by delta rows (negative is up).
func (s *ListState) Move(delta, n, height int) {
	if n <= 0 {
		s.Clamp(n, height)
		return
	}
	s.Select(max(s.Selected, 0)+delta, n, height)
}

// Reanchor selects newSel after the underlying rows changed, keeping the
// selected row at the same screen line where possible, so the list does not
// visually jump when rows appear or disappear above it.
func (s *ListState) Reanchor(newSel, n, height int) {
	offset := s.Selected - s.Origin
	if s.Selected < 0 || offset < 0 {
		offset = 0
	}
	s.Selected = newSel
	s.Origin = newSel - offset
	s.Clamp(n, height)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
