package inventory

import (
	"net/netip"
	"testing"

	"github.com/cristianradulescu/portview/internal/sockets"
)

func TestDiff(t *testing.T) {
	mk := func(port uint16, inode uint64) Entry {
		return Entry{Socket: sockets.Socket{Proto: sockets.TCP, Family: sockets.IPv4, State: sockets.StateListen,
			Local: netip.AddrPortFrom(netip.IPv4Unspecified(), port), Inode: inode, NetNS: 1}}
	}
	a, b, c := mk(22, 1), mk(80, 2), mk(443, 3)
	prev := &Snapshot{Entries: []Entry{a, b}}
	next := &Snapshot{Entries: []Entry{c, a}}
	// Same socket, new state: not an event (identity is the inode).
	moved := b
	moved.State = sockets.StateClose
	opened, closed := Diff(prev, next)
	if len(opened) != 1 || opened[0].Local.Port() != 443 || len(closed) != 1 || closed[0].Local.Port() != 80 {
		t.Errorf("opened %v closed %v", opened, closed)
	}
	if o, c := Diff(nil, next); o != nil || c != nil {
		t.Error("first snapshot must not produce events")
	}
	if o, c := Diff(&Snapshot{Entries: []Entry{b}}, &Snapshot{Entries: []Entry{moved}}); len(o)+len(c) != 0 {
		t.Errorf("state change reported as events: %v %v", o, c)
	}
}
