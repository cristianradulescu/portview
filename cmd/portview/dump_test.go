package main

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"

	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

func TestWriteDump(t *testing.T) {
	master := &procs.Process{PID: 100, Comm: "nginx", Loaded: true}
	worker := &procs.Process{PID: 101, Comm: "nginx", Loaded: true}
	leader := &procs.Process{PID: 500, Comm: "bwrap", Loaded: true}
	snap := &inventory.Snapshot{Procs: &procs.Table{
		Procs:      map[int]*procs.Process{100: master, 101: worker, 500: leader},
		Namespaces: []procs.Namespace{{Key: 1, PIDs: []int{100, 101}}, {Key: 2, PIDs: []int{500}}},
		HostNetNS:  1,
	}}
	listen := func(addr string, ns, inode uint64) sockets.Socket {
		return sockets.Socket{Proto: sockets.TCP, Family: sockets.IPv4, State: sockets.StateListen,
			Local: netip.MustParseAddrPort(addr), Remote: netip.MustParseAddrPort("0.0.0.0:0"), NetNS: ns, Inode: inode}
	}
	rows := []inventory.Entry{
		{Socket: listen("0.0.0.0:80", 1, 10), Owners: []*procs.Process{master, worker}, Primary: master,
			Exposure: inventory.ExposureAll, User: "root", HostNetNS: true},
		{Socket: listen("127.0.0.1:631", 1, 11), Exposure: inventory.ExposureLoopback, User: "root", HostNetNS: true},
		{Socket: listen("0.0.0.0:5432", 2, 12), Exposure: inventory.ExposureNamespace, User: "999"},
	}

	var buf bytes.Buffer
	writeDump(&buf, snap, rows)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines:\n%s", len(lines), buf.String())
	}
	want := [][]string{
		{"tcp", "LISTEN", "0.0.0.0:80", "0.0.0.0:*", "all", "100+1", "nginx", "root", "host"},
		{"tcp", "LISTEN", "127.0.0.1:631", "0.0.0.0:*", "loopback", "?", "?", "root", "host"},
		{"tcp", "LISTEN", "0.0.0.0:5432", "0.0.0.0:*", "netns", "?", "?", "999", "bwrap(500)"},
	}
	for i, w := range want {
		if got := strings.Fields(lines[i+1]); strings.Join(got, " ") != strings.Join(w, " ") {
			t.Errorf("row %d:\n got  %v\n want %v", i, got, w)
		}
	}

	// As root, unowned sockets are kernel-owned: "-" rather than "?".
	snap.Privileged = true
	buf.Reset()
	writeDump(&buf, snap, rows[1:2])
	if f := strings.Fields(strings.Split(buf.String(), "\n")[1]); f[5] != "-" || f[6] != "-" {
		t.Errorf("privileged unowned row = %v", f)
	}
}
