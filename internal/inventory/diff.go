package inventory

import (
	"net/netip"

	"github.com/cristianradulescu/portview/internal/sockets"
)

// Key is the stable identity of a socket across refreshes.
//
// A socket with a file is identified by its inode, which stays the same
// while the socket lives even if its state changes (SYN-SENT → ESTAB). A
// socket without a file (TIME-WAIT) has inode 0 and is identified by its
// full tuple.
type Key struct {
	NetNS  uint64
	Inode  uint64
	Proto  sockets.Proto
	Family sockets.Family
	Local  netip.AddrPort
	Remote netip.AddrPort
}

// Key returns the entry's identity.
func (e *Entry) Key() Key {
	if e.Inode != 0 {
		return Key{NetNS: e.NetNS, Inode: e.Inode}
	}
	return Key{NetNS: e.NetNS, Proto: e.Proto, Family: e.Family, Local: e.Local, Remote: e.Remote}
}

// NewKeys returns the keys present in next but not in prev. It returns nil
// when prev is nil (first snapshot: nothing is "new").
func NewKeys(prev, next []Entry) map[Key]bool {
	if prev == nil {
		return nil
	}
	old := make(map[Key]struct{}, len(prev))
	for i := range prev {
		old[prev[i].Key()] = struct{}{}
	}
	var added map[Key]bool
	for i := range next {
		k := next[i].Key()
		if _, ok := old[k]; !ok {
			if added == nil {
				added = make(map[Key]bool)
			}
			added[k] = true
		}
	}
	return added
}

// Reconcile finds where the selection should land in next, given that row
// sel was selected in prev:
//
//   - the same socket, if it is still there;
//   - otherwise the nearest row after it in prev that survived (the row that
//     "moves up" into its place), then the nearest one before it;
//   - otherwise the same index, clamped.
//
// It returns -1 when next is empty.
func Reconcile(prev []Entry, sel int, next []Entry) int {
	if len(next) == 0 {
		return -1
	}
	if sel < 0 || sel >= len(prev) {
		return clampIndex(sel, len(next))
	}
	pos := make(map[Key]int, len(next))
	for i := range next {
		pos[next[i].Key()] = i
	}
	if i, ok := pos[prev[sel].Key()]; ok {
		return i
	}
	for i := sel + 1; i < len(prev); i++ {
		if j, ok := pos[prev[i].Key()]; ok {
			return j
		}
	}
	for i := sel - 1; i >= 0; i-- {
		if j, ok := pos[prev[i].Key()]; ok {
			return j
		}
	}
	return clampIndex(sel, len(next))
}

func clampIndex(i, n int) int {
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}
