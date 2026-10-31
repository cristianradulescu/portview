package inventory

import (
	"context"
	"testing"

	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

type fakeUnix []sockets.UnixSocket

func (f fakeUnix) CollectUnix(context.Context, []sockets.Target) ([]sockets.UnixSocket, error) {
	return f, nil
}

func TestUnixJoinAndFilter(t *testing.T) {
	dockerd := &procs.Process{PID: 900, Comm: "dockerd", UID: 0, Loaded: true}
	app := &procs.Process{PID: 2000, Comm: "app", UID: 1000, Loaded: true}
	table := &procs.Table{HostNetNS: 1,
		Procs:   map[int]*procs.Process{900: dockerd, 2000: app},
		ByInode: map[uint64][]int{11: {900}, 12: {2000}},
	}
	us := []sockets.UnixSocket{
		{Type: sockets.UnixStream, State: sockets.UnixConnected, Inode: 12, NetNS: 1},
		{Type: sockets.UnixStream, State: sockets.UnixUnconnected, Flags: 0x10000, Inode: 11, Path: "/run/docker.sock", NetNS: 1},
		{Type: sockets.UnixDgram, State: sockets.UnixUnconnected, Inode: 13, Path: "@hidden", NetNS: 1},
	}
	got := joinUnix(us, table, mapUsers{0: "root", 1000: "alice"}, false)
	if len(got) != 3 || got[0].Path != "/run/docker.sock" || got[1].Path != "@hidden" || got[2].Path != "" {
		t.Fatalf("order = %+v", got)
	}
	docker, abstract, conn := &got[0], &got[1], &got[2]
	if docker.Primary != dockerd || docker.User != "root" || docker.PIDLabel(false) != "900" || docker.ProcessLabel(false) != "dockerd" {
		t.Errorf("docker.sock = %+v", docker)
	}
	if abstract.Primary != nil || abstract.User != "?" || abstract.PIDLabel(false) != "?" || abstract.PIDLabel(true) != "-" {
		t.Errorf("hidden owner = %+v", abstract)
	}
	if conn.User != "alice" {
		t.Errorf("connection user = %q", conn.User)
	}

	match := func(f Filter) []string {
		var out []string
		for i := range got {
			if f.MatchUnix(&got[i]) {
				out = append(out, got[i].Path+"|"+got[i].StateLabel())
			}
		}
		return out
	}
	if m := match(Filter{State: StateListening}); len(m) != 2 {
		t.Errorf("listening = %v (LISTEN and UNCONN)", m)
	}
	if m := match(Filter{State: StateEstablished}); len(m) != 1 || m[0] != "|ESTAB" {
		t.Errorf("established = %v", m)
	}
	if m := match(Filter{State: StateAll, Text: "docker"}); len(m) != 1 {
		t.Errorf("text docker = %v", m)
	}
	if m := match(Filter{State: StateAll, Text: "2000"}); len(m) != 1 || m[0] != "|ESTAB" {
		t.Errorf("PID 2000 = %v", m)
	}
	if m := match(Filter{State: StateAll, Text: "13"}); len(m) != 1 || m[0] != "@hidden|UNCONN" {
		t.Errorf("inode 13 = %v", m)
	}
	if m := match(Filter{State: StateAll, OnlyProto: sockets.TCP, OnlyFamily: sockets.IPv6}); len(m) != 3 {
		t.Errorf("IP filters must not hide Unix sockets: %v", m)
	}

	c := &Collector{Unix: fakeUnix(us)}
	snap := &Snapshot{Procs: table}
	c.collectUnix(context.Background(), nil, snap)
	if len(snap.Unix) != 3 {
		t.Errorf("collected %d", len(snap.Unix))
	}
}
