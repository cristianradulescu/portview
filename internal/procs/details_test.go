package procs

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

const (
	dockerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	otherID  = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

func TestParseCgroupPath(t *testing.T) {
	tests := []struct {
		path      string
		unit      Unit
		container Container
	}{
		{"/system.slice/nginx.service", Unit{Name: "nginx.service", User: -1}, Container{}},
		{"/system.slice/system-getty.slice/getty@tty1.service", Unit{Name: "getty@tty1.service", User: -1}, Container{}},
		{"/system.slice/foo.service/payload", Unit{Name: "foo.service", User: -1}, Container{}},
		{"/init.scope", Unit{Name: "init.scope", User: -1}, Container{}},
		{"/user.slice/user-1000.slice/session-2.scope", Unit{Name: "session-2.scope", User: -1}, Container{}},
		{"/user.slice/user-1000.slice/user@1000.service/app.slice/pipewire.service",
			Unit{Name: "pipewire.service", UserManager: true, User: 1000}, Container{}},
		{"/user.slice/user-1000.slice/user@1000.service/app.slice/app-gnome-firefox-1234.scope",
			Unit{Name: "app-gnome-firefox-1234.scope", UserManager: true, User: 1000}, Container{}},
		{"/user.slice/user-1000.slice/user@1000.service", Unit{Name: "user@1000.service", User: 1000}, Container{}},
		{"/system.slice/docker-" + dockerID + ".scope",
			Unit{Name: "docker-" + dockerID + ".scope", User: -1}, Container{ID: dockerID, Runtime: "docker"}},
		{"/docker/" + dockerID, Unit{User: -1}, Container{ID: dockerID, Runtime: "docker"}},
		{"/machine.slice/libpod-" + dockerID + ".scope",
			Unit{Name: "libpod-" + dockerID + ".scope", User: -1}, Container{ID: dockerID, Runtime: "podman"}},
		{"/system.slice/docker-notanid.scope", Unit{Name: "docker-notanid.scope", User: -1}, Container{}},
		// Inside a user's delegated subtree, names are chosen by the user: a
		// nested user@0.service is just a name, not root's manager...
		{"/user.slice/user-1000.slice/user@1000.service/app.slice/user@0.service/foo.service",
			Unit{Name: "user@0.service", UserManager: true, User: 1000}, Container{}},
		{"/user.slice/user-1000.slice/user@1000.service/user@0.service",
			Unit{Name: "user@0.service", UserManager: true, User: 1000}, Container{}},
		// Below a unit or a container, names are chosen by its processes:
		// only the outermost unit and container count.
		{"/system.slice/docker-" + dockerID + ".scope/docker-" + otherID + ".scope",
			Unit{Name: "docker-" + dockerID + ".scope", User: -1}, Container{ID: dockerID, Runtime: "docker"}},
		{"/system.slice/docker-" + dockerID + ".scope/nginx.service",
			Unit{Name: "docker-" + dockerID + ".scope", User: -1}, Container{ID: dockerID, Runtime: "docker"}},
		{"/system.slice/docker-" + dockerID + ".scope/init.scope/sshd.service",
			Unit{Name: "docker-" + dockerID + ".scope", User: -1}, Container{ID: dockerID, Runtime: "docker"}},
		{"/docker/" + dockerID + "/docker/" + otherID, Unit{User: -1}, Container{ID: dockerID, Runtime: "docker"}},
		{"/docker/" + dockerID + "/system.slice/nginx.service", Unit{User: -1}, Container{ID: dockerID, Runtime: "docker"}},
		{"/system.slice/ci-runner.service/docker-" + dockerID + ".scope", Unit{Name: "ci-runner.service", User: -1}, Container{}},
		{"/system.slice/ci-runner.service/payload/postgresql.service", Unit{Name: "ci-runner.service", User: -1}, Container{}},
		{"/machine.slice/libpod-" + dockerID + ".scope/container",
			Unit{Name: "libpod-" + dockerID + ".scope", User: -1}, Container{ID: dockerID, Runtime: "podman"}},
		// Container IDs in a user's subtree are marked as delegated.
		{"/user.slice/user-1000.slice/user@1000.service/app.slice/docker-" + dockerID + ".scope",
			Unit{Name: "docker-" + dockerID + ".scope", UserManager: true, User: 1000},
			Container{ID: dockerID, Runtime: "docker", Delegated: true}},
		{"/user.slice/user-1000.slice/user@1000.service/user.slice/libpod-" + dockerID + ".scope",
			Unit{Name: "libpod-" + dockerID + ".scope", UserManager: true, User: 1000},
			Container{ID: dockerID, Runtime: "podman", Delegated: true}},
		{"/", Unit{User: -1}, Container{}},
		{"", Unit{User: -1}, Container{}},
	}
	for _, tt := range tests {
		u, c := ParseCgroupPath(tt.path)
		if u != tt.unit || c != tt.container {
			t.Errorf("ParseCgroupPath(%q)\n got  %+v %+v\n want %+v %+v", tt.path, u, c, tt.unit, tt.container)
		}
	}
	if !(Unit{Name: "x.service"}).IsService() || (Unit{Name: "x.scope"}).IsService() {
		t.Error("IsService")
	}
	if got := (Container{ID: dockerID}).ShortID(); got != "0123456789ab" {
		t.Errorf("ShortID = %q", got)
	}
}

func auxv(pairs ...uint64) []byte {
	var b []byte
	for _, v := range pairs {
		b = binary.NativeEndian.AppendUint64(b, v)
	}
	return b
}

func TestDetails(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("auxv fixture is 64-bit")
	}
	root := buildProc(t, []fakeProcess{
		{pid: 1, status: status(0, 0), comm: "systemd"},
		{pid: 812, status: "Name:\tnginx\nState:\tS (sleeping)\nPPid:\t1\nUid:\t0\t0\t0\t0\nThreads:\t3\n",
			comm: "nginx", cgroup: "0::/system.slice/nginx.service\n"},
	})
	pdir := filepath.Join(root, "812")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(pdir, "cmdline"), []byte("nginx: master process /usr/sbin/nginx\x00-g\x00daemon on;\x00"), 0o644))
	must(os.Symlink("/usr/sbin/nginx", filepath.Join(pdir, "exe")))
	// no cwd link: simulates an unreadable/missing cwd
	// comm with spaces and parentheses must not confuse the stat parser; starttime (field 22) = 250 ticks
	stat := "812 (evil) (name) S 1 812 812 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 3 0 250 1000 100\n"
	must(os.WriteFile(filepath.Join(pdir, "stat"), []byte(stat), 0o644))
	must(os.WriteFile(filepath.Join(root, "stat"), []byte("cpu 1 2 3\nbtime 1767225600\nprocesses 10\n"), 0o644))
	must(os.MkdirAll(filepath.Join(root, "self"), 0o755))
	must(os.WriteFile(filepath.Join(root, "self", "auxv"), auxv(6, 4096, 17, 100, 0, 0), 0o644))

	d, err := Scanner{Root: root}.Details(812)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"nginx: master process /usr/sbin/nginx", "-g", "daemon on;"}
	if !reflect.DeepEqual(d.Cmdline, wantArgs) {
		t.Errorf("Cmdline = %q", d.Cmdline)
	}
	if got := QuoteArgs(d.Cmdline); got != "'nginx: master process /usr/sbin/nginx' -g 'daemon on;'" {
		t.Errorf("QuoteArgs = %s", got)
	}
	if d.Exe != "/usr/sbin/nginx" || d.ExeErr != nil {
		t.Errorf("Exe = %q, %v", d.Exe, d.ExeErr)
	}
	if d.Cwd != "" || !errors.Is(d.CwdErr, fs.ErrNotExist) {
		t.Errorf("Cwd = %q, %v", d.Cwd, d.CwdErr)
	}
	if d.PPID != 1 || d.ParentComm != "systemd" || d.UID != 0 || d.Threads != 3 || d.State != "S (sleeping)" {
		t.Errorf("details = %+v", d)
	}
	if want := time.Unix(1767225600+2, 500_000_000); !d.StartTime.Equal(want) {
		t.Errorf("StartTime = %v, want %v", d.StartTime, want)
	}
	if d.Unit.Name != "nginx.service" || d.Container.ID != "" {
		t.Errorf("unit/container = %+v %+v", d.Unit, d.Container)
	}
	if d.StartTicks != 250 {
		t.Errorf("StartTicks = %d, want 250", d.StartTicks)
	}
	if ticks, err := (Scanner{Root: root}).StartTicks(812); ticks != 250 || err != nil {
		t.Errorf("Scanner.StartTicks = %d, %v", ticks, err)
	}
	if st, err := (Scanner{Root: root}).StateCode(812); st != 'S' || err != nil {
		t.Errorf("StateCode = %q, %v", st, err)
	}
	if _, err := (Scanner{Root: root}).StartTicks(999); !errors.Is(err, ErrExited) {
		t.Errorf("StartTicks of missing process: err = %v, want ErrExited", err)
	}
	if _, err := (Scanner{Root: root}).StateCode(999); !errors.Is(err, ErrExited) {
		t.Errorf("StateCode of missing process: err = %v, want ErrExited", err)
	}

	if _, err := (Scanner{Root: root}).Details(999); !errors.Is(err, ErrExited) {
		t.Errorf("missing process: err = %v, want ErrExited", err)
	}
}

func TestParseCmdline(t *testing.T) {
	if got := ParseCmdline(nil); got != nil {
		t.Errorf("kernel thread cmdline = %q", got)
	}
	if got := ParseCmdline([]byte("a\x00\x1b[31mb\x00")); !reflect.DeepEqual(got, []string{"a", `\x1b[31mb`}) {
		t.Errorf("sanitized cmdline = %q", got)
	}
	if got := QuoteArgs([]string{"python3", "-m", "http.server", ""}); got != "python3 -m http.server ''" {
		t.Errorf("QuoteArgs = %s", got)
	}
	if got := QuoteArgs([]string{"it's"}); got != `'it'\''s'` {
		t.Errorf("QuoteArgs quote = %s", got)
	}
}

func TestParseStartTicks(t *testing.T) {
	if _, ok := parseStartTicks([]byte("1 (x) S 0")); ok {
		t.Error("short stat should fail")
	}
	if _, ok := parseStartTicks([]byte("garbage")); ok {
		t.Error("no ')' should fail")
	}
}

func TestParseAuxv(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("64-bit fixture")
	}
	if v, ok := parseAuxv(auxv(6, 4096, 17, 250, 0, 0), 17); !ok || v != 250 {
		t.Errorf("AT_CLKTCK = %d, %v", v, ok)
	}
	if _, ok := parseAuxv(auxv(6, 4096, 0, 0, 17, 100), 17); ok {
		t.Error("entries after AT_NULL must be ignored")
	}
}
