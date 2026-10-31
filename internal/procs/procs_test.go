package procs

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// fakeProcess describes one /proc/<pid> directory of the fake tree.
type fakeProcess struct {
	pid     int
	netnsOf int               // share net/tcp (hardlink) with this pid; 0 = own namespace
	noNet   bool              // no net/ directory (zombie)
	fds     map[string]string // fd name → link target; nil = fd dir unreadable/missing
	nsLink  string            // ns/net target; "" = not readable
	status  string
	comm    string
	cgroup  string
}

func status(ppid, uid int) string {
	u := strconv.Itoa(uid)
	return "Name:\tx\nState:\tS (sleeping)\nPPid:\t" + strconv.Itoa(ppid) +
		"\nUid:\t" + u + "\t" + u + "\t" + u + "\t" + u + "\nGid:\t0\t0\t0\t0\n"
}

func buildProc(t *testing.T, procs []fakeProcess) string {
	t.Helper()
	root := t.TempDir()
	write := func(path, data string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(p.pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		switch {
		case p.noNet:
		case p.netnsOf != 0:
			if err := os.MkdirAll(filepath.Join(dir, "net"), 0o755); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(root, strconv.Itoa(p.netnsOf), "net", "tcp")
			if err := os.Link(src, filepath.Join(dir, "net", "tcp")); err != nil {
				t.Fatal(err)
			}
		default:
			write(filepath.Join(dir, "net", "tcp"), "header\n")
		}
		if p.fds != nil {
			if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
				t.Fatal(err)
			}
			for fd, target := range p.fds {
				if err := os.Symlink(target, filepath.Join(dir, "fd", fd)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if p.nsLink != "" {
			if err := os.MkdirAll(filepath.Join(dir, "ns"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(p.nsLink, filepath.Join(dir, "ns", "net")); err != nil {
				t.Fatal(err)
			}
		}
		if p.status != "" {
			write(filepath.Join(dir, "status"), p.status)
		}
		if p.comm != "" {
			write(filepath.Join(dir, "comm"), p.comm+"\n")
		}
		if p.cgroup != "" {
			write(filepath.Join(dir, "cgroup"), p.cgroup)
		}
	}
	// Non-PID entries must be ignored.
	write(filepath.Join(root, "meminfo"), "MemTotal: 1 kB\n")
	if err := os.MkdirAll(filepath.Join(root, "sys"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestScan(t *testing.T) {
	root := buildProc(t, []fakeProcess{
		{pid: 1, fds: map[string]string{"0": "/dev/null", "40": "socket:[100]"},
			nsLink: "net:[4026531840]", status: status(0, 0), comm: "systemd", cgroup: "0::/init.scope\n"},
		// nginx master + worker sharing socket 200; the master holds it twice (dup).
		{pid: 100, netnsOf: 1, fds: map[string]string{"3": "socket:[200]", "4": "socket:[200]", "5": "pipe:[7]"},
			status: status(1, 0), comm: "nginx", cgroup: "0::/system.slice/nginx.service\n"},
		{pid: 101, netnsOf: 1, fds: map[string]string{"3": "socket:[200]", "6": "socket:[300]"},
			status: status(100, 33), comm: "nginx", cgroup: "0::/system.slice/nginx.service\n"},
		// a process in the host namespace we may not inspect
		{pid: 150, netnsOf: 1, status: status(1, 0), comm: "secret"},
		// container namespace, no fd access, ns link unreadable
		{pid: 200, status: status(190, 0), comm: "postgres",
			cgroup: "0::/system.slice/docker-0123456789abcdef.scope\n"},
		{pid: 201, netnsOf: 200, status: status(200, 999), comm: "postgres"},
		// zombie: no net directory, no fds
		{pid: 300, noNet: true, status: status(1, 1000), comm: "defunct"},
	})

	tbl, err := Scanner{Root: root}.Scan()
	if err != nil {
		t.Fatal(err)
	}

	wantInodes := map[uint64][]int{100: {1}, 200: {100, 101}, 300: {101}}
	if !reflect.DeepEqual(tbl.ByInode, wantInodes) {
		t.Errorf("ByInode = %v, want %v", tbl.ByInode, wantInodes)
	}
	if len(tbl.Procs) != 7 {
		t.Errorf("got %d processes, want 7", len(tbl.Procs))
	}
	if tbl.Uninspectable != 4 { // 150, 200, 201, 300
		t.Errorf("Uninspectable = %d, want 4", tbl.Uninspectable)
	}

	if len(tbl.Namespaces) != 2 {
		t.Fatalf("got %d namespaces, want 2: %+v", len(tbl.Namespaces), tbl.Namespaces)
	}
	host, ctr := tbl.Namespaces[0], tbl.Namespaces[1]
	if host.Key != tbl.HostNetNS || host.Key == 0 {
		t.Errorf("first namespace %+v is not the host (%d)", host, tbl.HostNetNS)
	}
	if !reflect.DeepEqual(host.PIDs, []int{1, 100, 101, 150}) || host.Inode != 4026531840 {
		t.Errorf("host namespace = %+v", host)
	}
	if !reflect.DeepEqual(ctr.PIDs, []int{200, 201}) || ctr.Inode != 0 {
		t.Errorf("container namespace = %+v", ctr)
	}
	if got, ok := tbl.Namespace(ctr.Key); !ok || got.Key != ctr.Key {
		t.Errorf("Namespace(%d) lookup failed", ctr.Key)
	}

	master := tbl.Procs[100]
	if !master.Loaded || master.Comm != "nginx" || master.PPID != 1 || master.UID != 0 ||
		master.Cgroup != "/system.slice/nginx.service" || !master.Inspectable || master.NetNS != host.Key {
		t.Errorf("master = %+v", master)
	}
	if w := tbl.Procs[101]; w.UID != 33 || w.PPID != 100 {
		t.Errorf("worker = %+v", w)
	}
	// Namespace leaders are loaded even without visible sockets ...
	if l := tbl.Procs[200]; !l.Loaded || l.Comm != "postgres" || l.Inspectable {
		t.Errorf("container leader = %+v", l)
	}
	// ... other non-owners are not.
	if p := tbl.Procs[150]; p.Loaded {
		t.Errorf("non-owner 150 should not be loaded: %+v", p)
	}
	if z := tbl.Procs[300]; z.NetNS != 0 {
		t.Errorf("zombie NetNS = %d, want 0", z.NetNS)
	}
}

func TestScanMissingRoot(t *testing.T) {
	if _, err := (Scanner{Root: filepath.Join(t.TempDir(), "nope")}).Scan(); err == nil {
		t.Fatal("expected an error for a missing procfs root")
	}
}

func TestLoadVanishedProcess(t *testing.T) {
	// Owner whose status file is gone (exited between fd walk and load).
	root := buildProc(t, []fakeProcess{
		{pid: 5, fds: map[string]string{"3": "socket:[1]"}},
	})
	tbl, err := Scanner{Root: root}.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if p := tbl.Procs[5]; p.Loaded || p.UID != -1 || p.PPID != -1 {
		t.Errorf("vanished process = %+v", p)
	}
}

func TestParseCgroup(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"v2", "0::/system.slice/nginx.service\n", "/system.slice/nginx.service"},
		{"hybrid prefers unified", "12:cpu:/x\n1:name=systemd:/system.slice/a.service\n0::/system.slice/a.service\n", "/system.slice/a.service"},
		{"v1 only", "12:cpu,cpuacct:/docker/abc\n1:name=systemd:/docker/abc\n", "/docker/abc"},
		{"empty", "", ""},
		{"garbage", "nonsense\n", ""},
	}
	for _, tt := range tests {
		if got := ParseCgroup([]byte(tt.in)); got != tt.want {
			t.Errorf("%s: ParseCgroup = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestParseStatus(t *testing.T) {
	ppid, uid := parseStatus([]byte(status(42, 1000)))
	if ppid != 42 || uid != 1000 {
		t.Errorf("got ppid=%d uid=%d", ppid, uid)
	}
	// real != effective: we want the effective UID.
	ppid, uid = parseStatus([]byte("PPid:\t7\nUid:\t1000\t0\t0\t0\n"))
	if ppid != 7 || uid != 0 {
		t.Errorf("setuid case: got ppid=%d uid=%d", ppid, uid)
	}
	ppid, uid = parseStatus([]byte("garbage"))
	if ppid != -1 || uid != -1 {
		t.Errorf("garbage: got ppid=%d uid=%d", ppid, uid)
	}
}

func TestParseBracketInode(t *testing.T) {
	tests := []struct {
		link, prefix string
		want         uint64
		ok           bool
	}{
		{"socket:[12345]", "socket:[", 12345, true},
		{"net:[4026531840]", "net:[", 4026531840, true},
		{"pipe:[7]", "socket:[", 0, false},
		{"socket:[abc]", "socket:[", 0, false},
		{"socket:[12", "socket:[", 0, false},
		{"/dev/null", "socket:[", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseBracketInode(tt.link, tt.prefix)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseBracketInode(%q) = %d, %v", tt.link, got, ok)
		}
	}
}
