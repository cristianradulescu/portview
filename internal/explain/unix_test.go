package explain

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cristianradulescu/portview/internal/sockets"
)

func TestUnixFields(t *testing.T) {
	fs, err := UnixFields("0000000000000000: 00000002 00000000 00010000 0001 01 10662 /run/systemd/io.systemd.Manager")
	if err != nil {
		t.Fatal(err)
	}
	m := byName(t, fs)
	check(t, m, "Num", "kptr_restrict")
	check(t, m, "Flags", "__SO_ACCEPTCON", "listen()")
	check(t, m, "Type", "1 = stream", "like TCP")
	check(t, m, "St", "SS_UNCONNECTED", "LISTEN from the flag")
	check(t, m, "Inode", "socket:[10662]")
	check(t, m, "Path", "write permission")

	m = byName(t, must(UnixFields("0000000000000000: 00000003 00000000 00000000 0001 03 430107")))
	check(t, m, "Path", "unnamed", "socketpair")
	check(t, m, "St", "SS_CONNECTED")
	m = byName(t, must(UnixFields("0000000000000000: 00000002 00000000 00010000 0001 01 33913 @/tmp/.X11-unix/X0")))
	check(t, m, "Path", "abstract", "SO_PEERCRED")
}

func TestUnixAccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	names := func(id int) string { return map[int]string{0: "root", 1000: "alice", 999: "docker"}[id] }
	uid, gid := os.Getuid(), os.Getgid()

	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	if a := UnixAccess(path, uid, nil, names, names); !a.CanConnect || a.Mode != "srw-rw----" || !strings.Contains(a.Text, "You own the file") {
		t.Errorf("owner: %+v", a)
	}
	// Another user in the file's group.
	if a := UnixAccess(path, uid+12345, []int{gid}, names, names); !a.CanConnect || !strings.Contains(a.Text, "You are in its group") {
		t.Errorf("group member: %+v", a)
	}
	// Another user, not in the group: 0660 keeps them out.
	if a := UnixAccess(path, uid+12345, nil, names, names); a.CanConnect || !strings.Contains(a.Text, "cannot connect") {
		t.Errorf("other: %+v", a)
	}
	if a := UnixAccess(path, 0, nil, names, names); !a.CanConnect || !strings.Contains(a.Text, "root") {
		t.Errorf("root: %+v", a)
	}
	os.Remove(path)
	if a := UnixAccess(path, uid, nil, names, names); !a.Missing || !strings.Contains(a.Text, "no longer exists") {
		t.Errorf("deleted: %+v", a)
	}
}

func TestReadUnixLive(t *testing.T) {
	dir := t.TempDir()
	l, err := net.Listen("unix", filepath.Join(dir, "live.sock"))
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	data, err := os.ReadFile("/proc/self/net/unix")
	if err != nil {
		t.Skip(err)
	}
	var inode uint64
	for _, u := range sockets.ParseProcNetUnix(data) {
		if strings.HasSuffix(u.Path, "/live.sock") {
			inode = u.Inode
		}
	}
	if inode == 0 {
		t.Fatal("listener not in /proc/self/net/unix")
	}
	r, err := ReadUnix("/proc/self/net/unix", inode)
	if err != nil || !strings.Contains(r.Line, "live.sock") || r.LineNo < 2 {
		t.Fatalf("report = %+v, %v", r, err)
	}
	if _, err := ReadUnix("/proc/self/net/unix", 1); err != ErrNotFound {
		t.Errorf("unknown inode: %v", err)
	}
}

func TestReadUnixSanitizesRawLine(t *testing.T) {
	table := filepath.Join(t.TempDir(), "unix")
	data := "Num       RefCount Protocol Flags    Type St Inode Path\n" +
		"0000000000000000: 00000002 00000000 00010000 0001 01 4242 /tmp/\x1b]0;pwned\x07\x1b[2J.sock\n"
	if err := os.WriteFile(table, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := ReadUnix(table, 4242)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{r.Line, r.Header} {
		if strings.ContainsAny(s, "\x1b\x07") {
			t.Errorf("raw control characters reach the terminal: %q", s)
		}
	}
}
