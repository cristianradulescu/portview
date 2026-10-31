package portsview

import (
	"strings"
	"testing"

	"github.com/cristianradulescu/portview/internal/explain"
	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

func unixSnapshot() *inventory.Snapshot {
	snap := testSnapshot()
	nginx := snap.Entries[0].Primary // PID 100, details from fakeDetails
	dockerd := &procs.Process{PID: 900, Comm: "dockerd", Loaded: true}
	snap.Unix = []inventory.UnixEntry{
		{UnixSocket: sockets.UnixSocket{Type: sockets.UnixStream, State: sockets.UnixUnconnected, Flags: 0x10000,
			Inode: 11, Path: "/run/docker.sock", NetNS: 1}, Owners: []*procs.Process{dockerd}, Primary: dockerd, User: "root", HostNetNS: true},
		{UnixSocket: sockets.UnixSocket{Type: sockets.UnixStream, State: sockets.UnixUnconnected, Flags: 0x10000,
			Inode: 12, Path: "@/tmp/.X11-unix/X0", NetNS: 1}, User: "?", HostNetNS: true},
		{UnixSocket: sockets.UnixSocket{Type: sockets.UnixStream, State: sockets.UnixUnconnected, Flags: 0x10000,
			Inode: 13, Path: "/run/nginx.sock", NetNS: 1}, Owners: []*procs.Process{nginx}, Primary: nginx, User: "root", HostNetNS: true},
		{UnixSocket: sockets.UnixSocket{Type: sockets.UnixStream, State: sockets.UnixConnected, Inode: 14, NetNS: 1},
			Owners: []*procs.Process{nginx}, Primary: nginx, User: "root", HostNetNS: true},
	}
	return snap
}

func TestUnixTab(t *testing.T) {
	a, g := newHeadless(t, 170, 60)
	a.opts.UnixAccess = func(path string, euid int, groups []int, u, gr func(int) string) explain.Access {
		return explain.Access{Mode: "srw-rw----", Owner: "root:docker", CanConnect: true,
			Text: "You are in its group docker, which has write permission. You can connect."}
	}
	var rawInode uint64
	a.opts.ReadUnixRaw = func(path string, inode uint64) (*explain.Report, error) {
		rawInode = inode
		line := "0000000000000000: 00000002 00000000 00010000 0001 01 13 /run/nginx.sock"
		fs, err := explain.UnixFields(line)
		return &explain.Report{Path: path, LineNo: 4, Header: "Num RefCount Protocol Flags Type St Inode Path", Line: line, Fields: fs}, err
	}
	a.apply(unixSnapshot(), nil)
	draw(t, g)
	if err := press(t, a, viewList, ']'); err != nil {
		t.Fatal(err)
	}
	out := draw(t, g)
	if currentView(g) != viewUnix {
		t.Fatalf("focus = %q, want the Unix tab", currentView(g))
	}
	for _, want := range []string{"Sockets 4/4 - Unix 3/4 - Events 0", "TYPE", "PATH", "/run/docker.sock", "dockerd",
		"@/tmp/.X11-unix/X0", "/run/nginx.sock"} {
		if !strings.Contains(out, want) {
			t.Errorf("Unix tab missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(unnamed)") {
		t.Error("the listening filter should hide the unnamed connection")
	}

	// docker.sock is selected first.
	details := strings.Join(strings.Fields(viewText(t, g, viewDetails)), " ")
	for _, want := range []string{"Type stream (u_str in ss)", "like TCP", "State LISTEN", "listen() was called",
		"Path /run/docker.sock", "a file in the filesystem", "File srw-rw---- root:docker", "You are in its group docker",
		"Inode 11", "$ ss -xlp", "$ ls -l /run/docker.sock", "awk 'NR == 1 || $7 == 11' /proc/net/unix"} {
		if !strings.Contains(details, want) {
			t.Errorf("details missing %q:\n%s", want, details)
		}
	}

	// The abstract socket: no file, anyone can connect; owner hidden.
	_ = press(t, a, viewUnix, 'j')
	draw(t, g)
	details = strings.Join(strings.Fields(viewText(t, g, viewDetails)), " ")
	if !strings.Contains(details, "abstract") || !strings.Contains(details, "SO_PEERCRED") || strings.Contains(details, "File ") ||
		!strings.Contains(details, "cannot inspect") {
		t.Errorf("abstract details:\n%s", details)
	}

	// nginx's socket: the process section is reused; i explains the raw line.
	_ = press(t, a, viewUnix, 'j')
	draw(t, g)
	details = strings.Join(strings.Fields(viewText(t, g, viewDetails)), " ")
	if !strings.Contains(details, "Process nginx · PID 100") || !strings.Contains(details, "Caps all (it runs as root)") {
		t.Errorf("process section:\n%s", details)
	}
	_ = press(t, a, viewUnix, 'i')
	out = draw(t, g)
	if rawInode != 13 || !strings.Contains(out, "__SO_ACCEPTCON") || !strings.Contains(out, "af_unix.c") {
		t.Errorf("inspect (inode %d):\n%s", rawInode, out)
	}
	_ = press(t, a, viewInspect, 'i')
	draw(t, g)

	// a shows connections too; the text filter matches paths.
	_ = press(t, a, viewUnix, 'a')
	if out := draw(t, g); !strings.Contains(out, "(unnamed)") || !strings.Contains(out, "Unix 4/4") {
		t.Errorf("all:\n%s", out)
	}
	a.setFilterText("docker")
	if out := draw(t, g); !strings.Contains(out, "Unix 1/4") {
		t.Errorf("text filter:\n%s", out)
	}
	// The IP-only keys do nothing here.
	for _, b := range a.keys.Bindings() {
		if b.View == viewUnix && len(b.Keys) > 0 && (b.Keys[0] == 't' || b.Keys[0] == '4') {
			t.Errorf("IP-only key %v bound on the Unix tab", b.Keys[0])
		}
	}
	// ] goes on to Events, [ back to Unix.
	_ = press(t, a, viewUnix, ']')
	draw(t, g)
	if currentView(g) != viewEvents {
		t.Errorf("] from Unix: focus %q", currentView(g))
	}
	_ = press(t, a, viewEvents, '[')
	draw(t, g)
	if currentView(g) != viewUnix {
		t.Errorf("[ from Events: focus %q", currentView(g))
	}
}
