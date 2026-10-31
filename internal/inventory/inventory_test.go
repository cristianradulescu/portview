package inventory

import (
	"context"
	"net/netip"
	"sort"
	"strconv"
	"testing"

	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

func proc(pid, ppid int, comm string, uid int) *procs.Process {
	return &procs.Process{PID: pid, PPID: ppid, Comm: comm, UID: uid, Loaded: true, Inspectable: true}
}

func TestPrimaryOwner(t *testing.T) {
	tests := []struct {
		name   string
		owners []*procs.Process
		want   int // 0 = nil
	}{
		{"none", nil, 0},
		{"single", []*procs.Process{proc(10, 1, "sshd", 0)}, 10},
		{"master and workers", []*procs.Process{proc(100, 1, "nginx", 0), proc(101, 100, "nginx", 33), proc(102, 100, "nginx", 33)}, 100},
		{"workers listed first", []*procs.Process{proc(50, 60, "php-fpm", 33), proc(60, 1, "php-fpm", 0)}, 60},
		{"socket activation skips pid 1", []*procs.Process{proc(1, 0, "systemd", 0), proc(900, 1, "cupsd", 0)}, 900},
		{"pid 1 alone", []*procs.Process{proc(1, 0, "systemd", 0)}, 1},
		{"unrelated owners: lowest pid", []*procs.Process{proc(300, 1, "a", 0), proc(200, 1, "b", 0)}, 200},
		{"unloaded parents (-1)", []*procs.Process{{PID: 8, PPID: -1}, {PID: 9, PPID: -1}}, 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PrimaryOwner(tt.owners)
			switch {
			case tt.want == 0 && got != nil:
				t.Fatalf("got %d, want nil", got.PID)
			case tt.want != 0 && (got == nil || got.PID != tt.want):
				t.Fatalf("got %+v, want pid %d", got, tt.want)
			}
		})
	}
}

func sock(proto sockets.Proto, state sockets.State, local, remote string) sockets.Socket {
	l := netip.MustParseAddrPort(local)
	fam := sockets.IPv4
	if l.Addr().Is6() {
		fam = sockets.IPv6
	}
	return sockets.Socket{Proto: proto, Family: fam, State: state, Local: l, Remote: netip.MustParseAddrPort(remote)}
}

func TestExposureOf(t *testing.T) {
	L, U, E := sockets.StateListen, sockets.StateClose, sockets.StateEstablished
	tests := []struct {
		s    sockets.Socket
		host bool
		want Exposure
	}{
		{sock(sockets.TCP, L, "0.0.0.0:22", "0.0.0.0:0"), true, ExposureAll},
		{sock(sockets.TCP, L, "[::]:22", "[::]:0"), true, ExposureAll},
		{sock(sockets.TCP, L, "127.0.0.1:631", "0.0.0.0:0"), true, ExposureLoopback},
		{sock(sockets.TCP, L, "127.0.0.53:53", "0.0.0.0:0"), true, ExposureLoopback},
		{sock(sockets.TCP, L, "[::1]:631", "[::]:0"), true, ExposureLoopback},
		{sock(sockets.TCP, L, "[::ffff:127.0.0.1]:80", "[::]:0"), true, ExposureLoopback},
		{sock(sockets.TCP, L, "192.168.1.5:8080", "0.0.0.0:0"), true, ExposureInterface},
		{sock(sockets.UDP, U, "0.0.0.0:5353", "0.0.0.0:0"), true, ExposureAll},
		{sock(sockets.TCP, L, "0.0.0.0:5432", "0.0.0.0:0"), false, ExposureNamespace},
		{sock(sockets.TCP, E, "192.168.1.5:5000", "1.1.1.1:443"), true, ExposureNone},
		{sock(sockets.UDP, E, "192.168.1.5:5000", "8.8.8.8:53"), true, ExposureNone},
	}
	for _, tt := range tests {
		if got := ExposureOf(tt.s, tt.host); got != tt.want {
			t.Errorf("%s %s host=%v: got %v, want %v", tt.s.Netid(), tt.s.Local, tt.host, got, tt.want)
		}
	}
}

func entry(s sockets.Socket, user string, owners ...*procs.Process) Entry {
	return Entry{Socket: s, User: user, Owners: owners, Primary: PrimaryOwner(owners)}
}

func TestFilterMatch(t *testing.T) {
	L, E := sockets.StateListen, sockets.StateEstablished
	nginx := entry(sock(sockets.TCP, L, "0.0.0.0:80", "0.0.0.0:0"), "root", proc(100, 1, "nginx", 0))
	alt := entry(sock(sockets.TCP, L, "[::1]:8080", "[::]:0"), "dev", proc(4242, 1, "node", 1000))
	dns := entry(sock(sockets.UDP, sockets.StateClose, "127.0.0.53:53", "0.0.0.0:0"), "systemd-resolve")
	conn := entry(sock(sockets.TCP, E, "192.168.1.5:50000", "140.82.112.3:443"), "dev", proc(777, 1, "firefox", 1000))

	all := []Entry{nginx, alt, dns, conn}
	tests := []struct {
		name string
		f    Filter
		want []string // netid+local of matches
	}{
		{"default is listening", Filter{}, []string{"tcp 0.0.0.0:80", "tcp6 [::1]:8080", "udp 127.0.0.53:53"}},
		{"established", Filter{State: StateEstablished}, []string{"tcp 192.168.1.5:50000"}},
		{"all", Filter{State: StateAll}, []string{"tcp 0.0.0.0:80", "tcp6 [::1]:8080", "udp 127.0.0.53:53", "tcp 192.168.1.5:50000"}},
		{"udp only", Filter{State: StateAll, OnlyProto: sockets.UDP}, []string{"udp 127.0.0.53:53"}},
		{"ipv6 only", Filter{State: StateAll, OnlyFamily: sockets.IPv6}, []string{"tcp6 [::1]:8080"}},
		{"port is exact", Filter{State: StateAll, Text: "80"}, []string{"tcp 0.0.0.0:80"}},
		{"colon port", Filter{State: StateAll, Text: ":8080"}, []string{"tcp6 [::1]:8080"}},
		{"remote port", Filter{State: StateAll, Text: "443"}, []string{"tcp 192.168.1.5:50000"}},
		{"pid", Filter{State: StateAll, Text: "4242"}, []string{"tcp6 [::1]:8080"}},
		{"process name, case-insensitive", Filter{State: StateAll, Text: "NGINX"}, []string{"tcp 0.0.0.0:80"}},
		{"address substring", Filter{State: StateAll, Text: "127.0.0"}, []string{"udp 127.0.0.53:53"}},
		{"remote address", Filter{State: StateAll, Text: "140.82"}, []string{"tcp 192.168.1.5:50000"}},
		{"user", Filter{State: StateAll, Text: "resolve"}, []string{"udp 127.0.0.53:53"}},
		{"terms are ANDed", Filter{State: StateAll, Text: "dev node"}, []string{"tcp6 [::1]:8080"}},
		{"no match", Filter{State: StateAll, Text: "postgres"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, e := range tt.f.Apply(all) {
				got = append(got, e.Netid()+" "+sockets.FormatAddrPort(e.Local))
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestParseStateFilter(t *testing.T) {
	for in, want := range map[string]StateFilter{"listen": StateListening, "established": StateEstablished, "all": StateAll} {
		if got, ok := ParseStateFilter(in); !ok || got != want {
			t.Errorf("ParseStateFilter(%q) = %v, %v", in, got, ok)
		}
	}
	if _, ok := ParseStateFilter("closed"); ok {
		t.Error("closed should be rejected")
	}
}

func TestLessIsStableIdentityOrder(t *testing.T) {
	L := sockets.StateListen
	es := []Entry{
		{Socket: sock(sockets.UDP, sockets.StateClose, "0.0.0.0:53", "0.0.0.0:0")},
		{Socket: sock(sockets.TCP, L, "[::]:80", "[::]:0")},
		{Socket: sock(sockets.TCP, L, "0.0.0.0:80", "0.0.0.0:0")},
		{Socket: sock(sockets.TCP, L, "127.0.0.1:22", "0.0.0.0:0")},
		{Socket: sock(sockets.TCP, L, "0.0.0.0:22", "0.0.0.0:0")},
	}
	sort.Slice(es, func(i, j int) bool { return Less(&es[i], &es[j]) })
	want := []string{"tcp 0.0.0.0:22", "tcp 127.0.0.1:22", "tcp 0.0.0.0:80", "tcp6 [::]:80", "udp 0.0.0.0:53"}
	for i, e := range es {
		if got := e.Netid() + " " + sockets.FormatAddrPort(e.Local); got != want[i] {
			t.Errorf("position %d: got %s, want %s", i, got, want[i])
		}
	}
}

// --- Collector with fakes ---

type fakeScanner struct{ t *procs.Table }

func (f fakeScanner) Scan() (*procs.Table, error) { return f.t, nil }

type fakeSource struct {
	socks   []sockets.Socket
	targets []sockets.Target
}

func (f *fakeSource) Collect(_ context.Context, ts []sockets.Target) ([]sockets.Socket, error) {
	f.targets = ts
	return f.socks, nil
}

type mapUsers map[int]string

func (m mapUsers) Name(uid int) string {
	if n, ok := m[uid]; ok {
		return n
	}
	return strconv.Itoa(uid)
}

func TestCollectorCollect(t *testing.T) {
	const host, ctr = 11, 22
	master, worker := proc(100, 1, "nginx", 0), proc(101, 100, "nginx", 33)
	master.NetNS, worker.NetNS = host, host
	table := &procs.Table{
		Procs:     map[int]*procs.Process{100: master, 101: worker, 500: {PID: 500, NetNS: ctr, UID: -1, PPID: -1}},
		ByInode:   map[uint64][]int{1: {100, 101}},
		HostNetNS: host,
		Namespaces: []procs.Namespace{
			{Key: host, PIDs: []int{100, 101}},
			{Key: ctr, PIDs: []int{500}},
		},
	}
	withNS := func(s sockets.Socket, ns, inode uint64, uid uint32) sockets.Socket {
		s.NetNS, s.Inode, s.UID = ns, inode, uid
		return s
	}
	src := &fakeSource{socks: []sockets.Socket{
		withNS(sock(sockets.TCP, sockets.StateListen, "0.0.0.0:80", "0.0.0.0:0"), host, 1, 0),
		withNS(sock(sockets.TCP, sockets.StateListen, "0.0.0.0:5432", "0.0.0.0:0"), ctr, 2, 999),
		withNS(sock(sockets.TCP, sockets.StateTimeWait, "127.0.0.1:80", "127.0.0.1:4000"), host, 0, 0),
		withNS(sock(sockets.TCP, sockets.StateListen, "127.0.0.1:22", "0.0.0.0:0"), host, 3, 0),
	}}
	priv := false
	c := &Collector{Procs: fakeScanner{table}, Sockets: src, Users: mapUsers{0: "root", 33: "www-data", 999: "postgres"}, Privileged: &priv}

	snap, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(src.targets) != 2 || src.targets[1].NetNS != ctr || src.targets[1].PIDs[0] != 500 {
		t.Errorf("targets = %+v", src.targets)
	}
	if snap.Hidden != 2 { // inode 2 (container) and 3; TIME-WAIT has no inode
		t.Errorf("Hidden = %d, want 2", snap.Hidden)
	}
	if snap.Privileged {
		t.Error("Privileged override ignored")
	}

	byPort := map[uint16]Entry{}
	for _, e := range snap.Entries {
		if e.Listening() {
			byPort[e.Local.Port()] = e
		}
	}
	web := byPort[80]
	if len(web.Owners) != 2 || web.Primary != master || web.User != "root" || !web.HostNetNS || web.Exposure != ExposureAll {
		t.Errorf("web entry = %+v", web)
	}
	db := byPort[5432]
	if db.Primary != nil || db.User != "postgres" || db.HostNetNS || db.Exposure != ExposureNamespace {
		t.Errorf("container entry = %+v (user from socket UID expected)", db)
	}
	// Entries come out in display order.
	for i := 1; i < len(snap.Entries); i++ {
		if Less(&snap.Entries[i], &snap.Entries[i-1]) {
			t.Fatalf("entries not sorted at %d", i)
		}
	}
}

func TestUserCache(t *testing.T) {
	c := NewUserCache()
	if got := c.Name(-1); got != "?" {
		t.Errorf("Name(-1) = %q", got)
	}
	if got := c.Name(0); got != "root" {
		t.Errorf("Name(0) = %q, want root", got)
	}
	// An unassigned UID falls back to the number and is cached.
	if got := c.Name(4000000000 - 7); got != "3999999993" {
		t.Errorf("unknown uid = %q", got)
	}
}
