package inventory

import (
	"net/netip"
	"testing"

	"github.com/cristianradulescu/portview/internal/sockets"
)

func listenEntry(port uint16, inode uint64) Entry {
	return Entry{Socket: sockets.Socket{
		Proto: sockets.TCP, Family: sockets.IPv4, State: sockets.StateListen,
		Local: netip.AddrPortFrom(netip.IPv4Unspecified(), port), Inode: inode, NetNS: 1,
	}}
}

func rows(ports ...uint16) []Entry {
	out := make([]Entry, len(ports))
	for i, p := range ports {
		out[i] = listenEntry(p, uint64(p)) // inode == port keeps tests readable
	}
	return out
}

func TestKey(t *testing.T) {
	a := listenEntry(80, 100)
	b := a
	b.State = sockets.StateClose // state change keeps identity
	if a.Key() != b.Key() {
		t.Error("state change must not change the key of a socket with an inode")
	}
	c := a
	c.NetNS = 2
	if a.Key() == c.Key() {
		t.Error("same inode in another namespace must differ")
	}

	tw1 := Entry{Socket: sockets.Socket{Proto: sockets.TCP, Family: sockets.IPv4, State: sockets.StateTimeWait,
		Local: netip.MustParseAddrPort("127.0.0.1:80"), Remote: netip.MustParseAddrPort("127.0.0.1:4000")}}
	tw2 := tw1
	tw2.Remote = netip.MustParseAddrPort("127.0.0.1:4001")
	if tw1.Key() == tw2.Key() {
		t.Error("inode-less sockets must be keyed by their tuple")
	}
}

func TestReconcile(t *testing.T) {
	tests := []struct {
		name string
		prev []Entry
		sel  int
		next []Entry
		want int
	}{
		{"same row stays selected", rows(22, 80, 443), 1, rows(22, 80, 443), 1},
		{"row inserted above: follow the socket", rows(22, 80, 443), 1, rows(21, 22, 80, 443), 2},
		{"row removed above: follow the socket", rows(22, 80, 443), 2, rows(80, 443), 1},
		{"selected removed: next survivor moves up", rows(22, 80, 443, 8080), 1, rows(22, 443, 8080), 1},
		{"selected and next removed", rows(22, 80, 443, 8080), 1, rows(22, 8080), 1},
		{"selected last removed: previous survivor", rows(22, 80, 443), 2, rows(22, 80), 1},
		{"everything replaced: clamp index", rows(22, 80, 443), 2, rows(1, 2), 1},
		{"filter narrowed, selection kept", rows(22, 80, 443, 8080), 3, rows(443, 8080), 1},
		{"empty next", rows(22), 0, nil, -1},
		{"empty prev", nil, -1, rows(22, 80), 0},
		{"out of range sel", rows(22), 7, rows(22, 80), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Reconcile(tt.prev, tt.sel, tt.next); got != tt.want {
				t.Errorf("Reconcile = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNewKeys(t *testing.T) {
	if NewKeys(nil, rows(22, 80)) != nil {
		t.Error("first snapshot must not mark anything as new")
	}
	added := NewKeys(rows(22, 80), rows(22, 80, 443))
	if len(added) != 1 || !added[func() Key { e := listenEntry(443, 443); return e.Key() }()] {
		t.Errorf("added = %v", added)
	}
	if NewKeys(rows(22, 80), rows(80)) != nil {
		t.Error("removals only: expected nil")
	}
}
