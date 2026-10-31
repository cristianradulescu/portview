package portsview

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

func listen(proto sockets.Proto, port uint16) inventory.Entry {
	state := sockets.StateListen
	if proto == sockets.UDP {
		state = sockets.StateClose
	}
	s := sockets.Socket{Proto: proto, Family: sockets.IPv4, State: state,
		Local: netip.AddrPortFrom(netip.IPv4Unspecified(), port), Inode: uint64(port)*10 + uint64(proto), NetNS: 1}
	return inventory.Entry{Socket: s, Exposure: inventory.ExposureAll, HostNetNS: true, User: "root"}
}

func snapshot(es ...inventory.Entry) *inventory.Snapshot {
	return &inventory.Snapshot{Entries: es, Procs: &procs.Table{HostNetNS: 1}}
}

func tcp(ports ...uint16) *inventory.Snapshot {
	es := make([]inventory.Entry, len(ports))
	for i, p := range ports {
		es[i] = listen(sockets.TCP, p)
	}
	return snapshot(es...)
}

func selectedPort(t *testing.T, m *Model) uint16 {
	t.Helper()
	e := m.Selected()
	if e == nil {
		t.Fatal("nothing selected")
	}
	return e.Local.Port()
}

func TestModelKeepsSelectionAcrossRefresh(t *testing.T) {
	m := NewModel(inventory.Filter{})
	m.SetHeight(3)
	m.SetSnapshot(tcp(22, 80, 443, 5432, 8080))
	if selectedPort(t, m) != 22 {
		t.Fatal("first row should be selected initially")
	}
	m.Move(3) // 5432, window scrolled to [80 443 5432]
	if l := m.List(); l.Selected != 3 || l.Origin != 1 {
		t.Fatalf("after move: %+v", l)
	}

	// A socket appears above: the selection follows 5432 and stays on the
	// same screen line (origin shifts with it).
	m.SetSnapshot(tcp(21, 22, 80, 443, 5432, 8080))
	if selectedPort(t, m) != 5432 {
		t.Fatalf("selected %d, want 5432", selectedPort(t, m))
	}
	if l := m.List(); l.Selected-l.Origin != 2 {
		t.Errorf("selected row moved on screen: %+v", l)
	}
	if !m.IsFresh(&m.Rows()[0]) || m.IsFresh(&m.Rows()[1]) {
		t.Error("only port 21 should be fresh")
	}

	// The selected socket goes away: the next one moves into its place.
	m.SetSnapshot(tcp(21, 22, 80, 443, 8080))
	if selectedPort(t, m) != 8080 {
		t.Fatalf("selected %d, want 8080", selectedPort(t, m))
	}

	// Everything goes away, then comes back.
	m.SetSnapshot(tcp())
	if m.Selected() != nil || m.List().Selected != -1 {
		t.Fatalf("empty list should have no selection: %+v", m.List())
	}
	m.SetSnapshot(tcp(22, 80))
	if m.Selected() == nil {
		t.Fatal("selection should come back")
	}
}

func TestModelFilterPreservesSelection(t *testing.T) {
	m := NewModel(inventory.Filter{State: inventory.StateAll})
	m.SetHeight(10)
	m.SetSnapshot(snapshot(listen(sockets.TCP, 22), listen(sockets.TCP, 80), listen(sockets.UDP, 53), listen(sockets.UDP, 5353)))
	m.Move(1) // tcp 80
	m.UpdateFilter(func(f *inventory.Filter) { f.OnlyProto = sockets.TCP })
	if len(m.Rows()) != 2 || selectedPort(t, m) != 80 {
		t.Fatalf("rows=%d selected=%d", len(m.Rows()), selectedPort(t, m))
	}
	m.UpdateFilter(func(f *inventory.Filter) { f.OnlyProto = sockets.UDP })
	if len(m.Rows()) != 2 {
		t.Fatalf("rows=%d", len(m.Rows()))
	}
	m.UpdateFilter(func(f *inventory.Filter) { f.OnlyProto = 0; f.Text = "5353" })
	if len(m.Rows()) != 1 || selectedPort(t, m) != 5353 {
		t.Fatalf("text filter: rows=%d", len(m.Rows()))
	}
}

func TestModelNavigation(t *testing.T) {
	m := NewModel(inventory.Filter{})
	m.SetHeight(4)
	m.SetSnapshot(tcp(1, 2, 3, 4, 5, 6, 7, 8, 9, 10))
	m.Bottom()
	if l := m.List(); l.Selected != 9 || l.Origin != 6 {
		t.Errorf("bottom: %+v", l)
	}
	m.Page(-1)
	if l := m.List(); l.Selected != 6 {
		t.Errorf("page up: %+v", l)
	}
	m.Top()
	if l := m.List(); l.Selected != 0 || l.Origin != 0 {
		t.Errorf("top: %+v", l)
	}
	m.Move(-5)
	if l := m.List(); l.Selected != 0 {
		t.Errorf("move past top: %+v", l)
	}
	// Shrinking the window keeps the selection visible.
	m.Move(3)
	m.SetHeight(2)
	if l := m.List(); l.Selected != 3 || l.Origin != 2 {
		t.Errorf("after resize: %+v", l)
	}
}

func TestWidthsFitPanel(t *testing.T) {
	long := listen(sockets.TCP, 54006)
	long.Family = sockets.IPv6
	long.State = sockets.StateEstablished
	long.Local = netip.MustParseAddrPort("[2a02:2f0a:120b:c900:e28a:2d38:ec79:e11a]:54006")
	long.Remote = netip.MustParseAddrPort("[2600:1901:0:38d7::]:443")
	rows := []inventory.Entry{long, listen(sockets.UDP, 53)}

	natural := widths(rows, false, 1000)
	if natural[colLocal] != 47 {
		t.Fatalf("natural local width = %d", natural[colLocal])
	}
	w := widths(rows, false, 100)
	total := -1
	for _, x := range w {
		if x > 0 { // hidden columns take no space and no separator
			total += x + 1
		}
	}
	if w[colContainer] != 0 {
		t.Errorf("CONTAINER column should be hidden without containers")
	}
	if total > 100 {
		t.Errorf("total width %d exceeds 100: %v", total, w)
	}
	// Only the wider address column gives up space until both are equal.
	if w[colRemote] != natural[colRemote] || w[colLocal] < w[colRemote] {
		t.Errorf("widest address column should shrink first: local=%d remote=%d", w[colLocal], w[colRemote])
	}
	if w2 := widths(rows, false, 85); w2[colLocal]-w2[colRemote] > 1 || w2[colRemote]-w2[colLocal] > 1 {
		t.Errorf("when both shrink they stay balanced: local=%d remote=%d", w2[colLocal], w2[colRemote])
	}
	if w[colPID] != natural[colPID] || w[colExposure] != natural[colExposure] {
		t.Errorf("PID/EXPOSURE must not shrink: %v", w)
	}
	// The port survives truncation.
	line := rowLine(&rows[0], w, false, false)
	if !strings.Contains(line, ":54006") || !strings.Contains(line, ":443") {
		t.Errorf("ports lost: %q", line)
	}
}
