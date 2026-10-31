package inventory

import (
	"slices"
	"time"
)

// EventKind says whether a socket appeared or disappeared.
type EventKind int

const (
	// Opened: the socket appeared since the previous refresh.
	Opened EventKind = iota
	// Closed: the socket disappeared since the previous refresh.
	Closed
)

func (k EventKind) String() string {
	if k == Closed {
		return "closed"
	}
	return "opened"
}

// Event is a socket appearing or disappearing between two snapshots.
// Sockets that open and close within one refresh interval are not seen.
type Event struct {
	// Seq numbers events in the order they were recorded.
	Seq  uint64
	Time time.Time
	Kind EventKind
	// Entry is the socket as last seen: in the snapshot where it appeared,
	// or the last one it was in before it closed.
	Entry Entry
	// Cmdline is the primary owner's command line, read when the event was
	// recorded (listening sockets only; "" when not read or unreadable).
	Cmdline string
}

// Diff returns the sockets in next but not in prev (opened) and those in
// prev but not in next (closed), each in display order. Nothing is
// reported for the first snapshot (prev nil).
func Diff(prev, next *Snapshot) (opened, closed []Entry) {
	if prev == nil || next == nil {
		return nil, nil
	}
	have := func(es []Entry) map[Key]struct{} {
		m := make(map[Key]struct{}, len(es))
		for i := range es {
			m[es[i].Key()] = struct{}{}
		}
		return m
	}
	before, after := have(prev.Entries), have(next.Entries)
	for i := range next.Entries {
		if _, ok := before[next.Entries[i].Key()]; !ok {
			opened = append(opened, next.Entries[i])
		}
	}
	for i := range prev.Entries {
		if _, ok := after[prev.Entries[i].Key()]; !ok {
			closed = append(closed, prev.Entries[i])
		}
	}
	byOrder := func(a, b Entry) int {
		switch {
		case Less(&a, &b):
			return -1
		case Less(&b, &a):
			return 1
		}
		return 0
	}
	slices.SortStableFunc(opened, byOrder)
	slices.SortStableFunc(closed, byOrder)
	return opened, closed
}
