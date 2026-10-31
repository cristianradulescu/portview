package portsview

import (
	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/ui/tui"
)

// Model is the UI state of the socket list, independent of gocui: the
// latest snapshot, the active filter, the filtered rows and the selection.
type Model struct {
	snap   *inventory.Snapshot
	filter inventory.Filter
	rows   []inventory.Entry
	list   tui.ListState
	height int
	fresh  map[inventory.Key]bool
	err    error
}

// NewModel returns an empty model with the given initial filter.
func NewModel(f inventory.Filter) *Model {
	return &Model{filter: f, list: tui.NewListState(), height: 1}
}

// SetSnapshot replaces the data, keeping the selected socket selected (or
// its nearest surviving neighbor) at the same screen position.
func (m *Model) SetSnapshot(s *inventory.Snapshot) {
	var prev []inventory.Entry
	if m.snap != nil {
		prev = m.snap.Entries
	}
	m.fresh = inventory.NewKeys(prev, s.Entries)
	m.snap = s
	m.err = nil
	m.refilter()
}

// SetError records a failed refresh; the previous data stays visible.
func (m *Model) SetError(err error) { m.err = err }

// Err returns the last refresh error, cleared by the next good snapshot.
func (m *Model) Err() error { return m.err }

// Snapshot returns the current snapshot (nil before the first refresh).
func (m *Model) Snapshot() *inventory.Snapshot { return m.snap }

// Filter returns the active filter.
func (m *Model) Filter() inventory.Filter { return m.filter }

// SetFilter changes the filter, preserving the selection when the selected
// socket is still visible.
func (m *Model) SetFilter(f inventory.Filter) {
	if f == m.filter {
		return
	}
	m.filter = f
	m.refilter()
}

// UpdateFilter applies fn to a copy of the filter and sets the result.
func (m *Model) UpdateFilter(fn func(*inventory.Filter)) {
	f := m.filter
	fn(&f)
	m.SetFilter(f)
}

func (m *Model) refilter() {
	if m.snap == nil {
		return
	}
	prev, sel := m.rows, m.list.Selected
	m.rows = m.filter.Apply(m.snap.Entries)
	m.list.Reanchor(inventory.Reconcile(prev, sel, m.rows), len(m.rows), m.height)
}

// Rows returns the filtered rows in display order.
func (m *Model) Rows() []inventory.Entry { return m.rows }

// Total returns the number of sockets before filtering.
func (m *Model) Total() int {
	if m.snap == nil {
		return 0
	}
	return len(m.snap.Entries)
}

// List returns the selection/scroll state.
func (m *Model) List() tui.ListState { return m.list }

// Selected returns the selected row, or nil.
func (m *Model) Selected() *inventory.Entry {
	if m.list.Selected < 0 || m.list.Selected >= len(m.rows) {
		return nil
	}
	return &m.rows[m.list.Selected]
}

// IsFresh reports whether the entry appeared in the latest refresh.
func (m *Model) IsFresh(e *inventory.Entry) bool { return m.fresh[e.Key()] }

// SetHeight sets the number of visible rows.
func (m *Model) SetHeight(h int) {
	m.height = max(1, h)
	m.list.Clamp(len(m.rows), m.height)
}

// Height returns the number of visible rows.
func (m *Model) Height() int { return m.height }

// Move moves the selection by delta rows.
func (m *Model) Move(delta int) { m.list.Move(delta, len(m.rows), m.height) }

// Page moves the selection by pages (negative is up).
func (m *Model) Page(pages int) { m.Move(pages * max(1, m.height-1)) }

// Top selects the first row.
func (m *Model) Top() { m.list.Select(0, len(m.rows), m.height) }

// Bottom selects the last row.
func (m *Model) Bottom() { m.list.Select(len(m.rows)-1, len(m.rows), m.height) }

// Has reports whether the current snapshot contains the socket k.
func (m *Model) Has(k inventory.Key) bool {
	if m.snap == nil {
		return false
	}
	for i := range m.snap.Entries {
		if m.snap.Entries[i].Key() == k {
			return true
		}
	}
	return false
}

// SelectKey selects the row of socket k; false if it is not shown.
func (m *Model) SelectKey(k inventory.Key) bool {
	for i := range m.rows {
		if m.rows[i].Key() == k {
			m.list.Select(i, len(m.rows), m.height)
			return true
		}
	}
	return false
}
