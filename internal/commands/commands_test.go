package commands

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/cristianradulescu/portview/internal/actions"
	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

const ctrID = "4bf8254b1f0f0123456789abcdef0123456789abcdef0123456789abcdef0123"

type users map[int]string

func (u users) Name(uid int) string {
	if n, ok := u[uid]; ok {
		return n
	}
	return "?"
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"nginx.service":   "nginx.service",
		"web-1":           "web-1",
		"":                "''",
		"a b":             "'a b'",
		"it's":            `'it'\''s'`,
		"$(touch /tmp/x)": "'$(touch /tmp/x)'",
		"x; rm -rf ~":     "'x; rm -rf ~'",
		"`id`":            "'`id`'",
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestQuoteSurvivesShell runs quoted hostile strings through sh: each must
// come back as exactly one, unchanged argument, and nothing may execute.
func TestQuoteSurvivesShell(t *testing.T) {
	dir := t.TempDir()
	marker := dir + "/pwned"
	hostile := []string{
		"$(touch " + marker + ")", "`touch " + marker + "`", "a'b\"c", "x;touch " + marker,
		"-rf", "new\nline", "tab\there", "*", "~", "!!", "\\", "$HOME",
	}
	for _, h := range hostile {
		out, err := exec.Command("sh", "-c", "printf '%s\\0' "+Quote(h)).Output()
		if err != nil {
			t.Fatalf("%q: %v", h, err)
		}
		if got := strings.TrimSuffix(string(out), "\x00"); got != h {
			t.Errorf("%q came back as %q", h, got)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a quoted string was executed")
	}
}

func TestView(t *testing.T) {
	tests := []struct {
		f    inventory.Filter
		priv bool
		want string
	}{
		{inventory.Filter{State: inventory.StateListening}, false, "ss -tulnp"},
		{inventory.Filter{State: inventory.StateListening}, true, "sudo ss -tulnp"},
		{inventory.Filter{State: inventory.StateAll, OnlyProto: sockets.TCP, OnlyFamily: sockets.IPv6}, false, "ss -tanp6"},
		{inventory.Filter{State: inventory.StateEstablished, OnlyProto: sockets.UDP}, false, "ss -unp state established"},
		{inventory.Filter{State: inventory.StateListening, Text: "80"}, false, "ss -tulnp '( sport = :80 or dport = :80 )'"},
		{inventory.Filter{State: inventory.StateListening, Text: "nginx 443"}, true,
			"sudo ss -tulnp '( sport = :443 or dport = :443 )' | grep -iF -- nginx"},
		{inventory.Filter{State: inventory.StateListening, Text: "it's"}, false, `ss -tulnp | grep -iF -- 'it'\''s'`},
	}
	for _, tt := range tests {
		if got := View(tt.f, Env{Privileged: tt.priv}); got.Cmd != tt.want {
			t.Errorf("View(%+v) = %s\nwant %s", tt.f, got.Cmd, tt.want)
		}
	}
	if c := View(inventory.Filter{}, Env{}); !strings.Contains(c.Comment, "add sudo") {
		t.Errorf("unprivileged comment = %q", c.Comment)
	}
}

func entry(proto sockets.Proto, fam sockets.Family, state sockets.State, local, remote string) *inventory.Entry {
	e := &inventory.Entry{Socket: sockets.Socket{
		Proto: proto, Family: fam, State: state, Local: netip.MustParseAddrPort(local), Inode: 801, NetNS: 1,
	}, HostNetNS: true}
	if remote != "" {
		e.Remote = netip.MustParseAddrPort(remote)
	}
	return e
}

func cmds(cs []Command) string {
	var sb strings.Builder
	for _, c := range cs {
		sb.WriteString(c.Cmd + "\n")
	}
	return sb.String()
}

func TestSocketHost(t *testing.T) {
	e := entry(sockets.TCP, sockets.IPv4, sockets.StateListen, "0.0.0.0:80", "0.0.0.0:0")
	got := cmds(Socket(e, nil, Env{Privileged: true}))
	for _, want := range []string{
		"sudo ss -tlnpe 'sport = :80'\n",
		"sudo lsof -nP -iTCP:80 -sTCP:LISTEN\n",
		`awk 'NR == 1 || $2 == "00000000:0050"' /proc/net/tcp` + "\n",
		`sudo find /proc/[0-9]*/fd -lname 'socket:\[801\]' 2>/dev/null` + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}

	est := entry(sockets.TCP, sockets.IPv6, sockets.StateEstablished, "[2001:db8::5]:43210", "[2001:db8::1]:443")
	got = cmds(Socket(est, nil, Env{}))
	if !strings.Contains(got, "ss -tanpe 'src [2001:db8::5]:43210 and dst [2001:db8::1]:443'\n") ||
		!strings.Contains(got, "/proc/net/tcp6") || !strings.Contains(got, `&& $3 == "`) {
		t.Errorf("established:\n%s", got)
	}

	udp := entry(sockets.UDP, sockets.IPv4, sockets.StateClose, "127.0.0.53:53", "0.0.0.0:0")
	got = cmds(Socket(udp, nil, Env{}))
	if !strings.Contains(got, "ss -ulnpe 'src 127.0.0.53:53'\n") || !strings.Contains(got, "lsof -nP -iUDP:53\n") {
		t.Errorf("udp:\n%s", got)
	}
}

func TestSocketOtherNamespace(t *testing.T) {
	e := entry(sockets.TCP, sockets.IPv6, sockets.StateListen, "[::]:8080", "[::]:0")
	e.NetNS, e.HostNetNS = 2, false
	snap := &inventory.Snapshot{Procs: &procs.Table{HostNetNS: 1,
		Namespaces: []procs.Namespace{{Key: 1, PIDs: []int{1}}, {Key: 2, PIDs: []int{4242, 4300}}}}}
	got := cmds(Socket(e, snap, Env{}))
	for _, want := range []string{
		"sudo nsenter -t 4242 -n ss -tlnpe 'sport = :8080'\n",
		"sudo nsenter -t 4242 -n lsof -nP -iTCP:8080 -sTCP:LISTEN\n",
		"/proc/4242/net/tcp6\n", // readable without root
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestProcessUnitContainer(t *testing.T) {
	e := entry(sockets.TCP, sockets.IPv4, sockets.StateListen, "0.0.0.0:80", "")
	e.Owners = []*procs.Process{{PID: 812}, {PID: 813}}
	d := &procs.Details{PID: 812, UID: 0}
	got := cmds(Process(e, d, Env{EUID: 1000}))
	for _, want := range []string{
		"ps -o pid,ppid,user,lstart,etime,args -p 812\n",
		`tr '\0' ' ' < /proc/812/cmdline; echo` + "\n",
		"sudo readlink /proc/812/exe /proc/812/cwd\n", // another user's process
		"ss -tuanp | grep pid=812,\n",
		"ps -o pid,ppid,user,args -p 812,813\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}

	sys := procs.Unit{Name: "nginx.service", User: -1}
	if got := cmds(Unit(sys, Env{})); !strings.Contains(got, "systemctl status -- nginx.service\n") ||
		!strings.Contains(got, "journalctl -u nginx.service -n 50 --no-pager\n") {
		t.Errorf("system unit:\n%s", got)
	}
	user := procs.Unit{Name: "dev.service", UserManager: true, User: 1000}
	if got := cmds(Unit(user, Env{EUID: 1000})); !strings.Contains(got, "systemctl --user status -- dev.service\n") ||
		!strings.Contains(got, "journalctl --user -u dev.service") {
		t.Errorf("own user unit:\n%s", got)
	}
	if got := cmds(Unit(user, Env{EUID: 0, Users: users{1000: "alice"}})); !strings.Contains(got, "systemctl --user --machine=alice@.host status -- dev.service\n") ||
		!strings.Contains(got, "journalctl _UID=1000 _SYSTEMD_USER_UNIT=dev.service") {
		t.Errorf("other user's unit as root:\n%s", got)
	}
	hostile := procs.Unit{Name: "x$(id).service", User: -1}
	if got := cmds(Unit(hostile, Env{})); !strings.Contains(got, "systemctl status -- 'x$(id).service'") {
		t.Errorf("unit name not quoted:\n%s", got)
	}
	// A unit name that looks like an option must not become one.
	optLike := procs.Unit{Name: "--host=x@evil.service", UserManager: true, User: 1000}
	for _, c := range Unit(optLike, Env{EUID: 1000}) {
		if strings.Contains(c.Cmd, "systemctl") && !strings.Contains(c.Cmd, " -- --host=x@evil.service") {
			t.Errorf("option-like unit name not after --: %s", c.Cmd)
		}
		if strings.Contains(c.Cmd, "journalctl") && !strings.Contains(c.Cmd, "-u --host=x@evil.service") {
			t.Errorf("journalctl: %s", c.Cmd)
		}
	}

	c := &inventory.ContainerRef{ID: ctrID, Name: "web-1", Runtime: "docker", Forward: &inventory.Forward{ContainerPort: 80, Proto: "tcp"}}
	got = cmds(Container(c, Env{DockerSocket: "/var/run/docker.sock"}))
	for _, want := range []string{
		"docker ps --filter id=" + ctrID + "\n", // plain hex needs no quotes
		"docker inspect -- web-1\n", "docker top -- web-1\n", "docker port -- web-1\n",
		"curl -s --unix-socket /var/run/docker.sock http://localhost/containers/json\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	evil := &inventory.ContainerRef{ID: "x;id", Name: "web-1", Runtime: "docker"}
	if got := cmds(Container(evil, Env{})); !strings.Contains(got, "docker ps --filter 'id=x;id'\n") {
		t.Errorf("container ID not quoted:\n%s", got)
	}
	if got := ContainerStop(Env{DockerSocket: "/s"}, "../../x"); !strings.Contains(got, "/containers/..%2F..%2Fx/stop") {
		t.Errorf("ID not path-escaped: %s", got)
	}
	pod := &inventory.ContainerRef{ID: ctrID, Runtime: "podman"}
	if got := cmds(Container(pod, Env{DockerSocket: "/var/run/docker.sock"})); !strings.Contains(got, "podman inspect -- 4bf8254b1f0f\n") || strings.Contains(got, "curl") {
		t.Errorf("podman:\n%s", got)
	}
}

func TestOption(t *testing.T) {
	env := Env{EUID: 0, DockerSocket: "/var/run/docker.sock"}
	sig := &actions.Option{Kind: actions.Signal, Owners: []actions.Owner{{PID: 812}, {PID: 813}}}
	if got := Option(sig, syscall.SIGKILL, env); len(got) != 1 || got[0] != "kill -KILL 812 813" {
		t.Errorf("signal = %q", got)
	}
	unit := &actions.Option{Kind: actions.StopUnit, Unit: procs.Unit{Name: "nginx.service", User: -1}}
	if got := Option(unit, 0, env); len(got) != 1 || got[0] != "systemctl --no-ask-password stop -- nginx.service" {
		t.Errorf("unit = %q", got)
	}
	ctr := &actions.Option{Kind: actions.StopContainer, Container: &inventory.ContainerRef{ID: ctrID, Name: "web-1"}}
	got := Option(ctr, 0, env)
	if len(got) != 2 || got[0] != "docker stop -- web-1" ||
		got[1] != "curl -s --unix-socket /var/run/docker.sock -X POST 'http://localhost/containers/"+ctrID+"/stop?t=10'  # what portview sends" {
		t.Errorf("container = %q", got)
	}
}

func TestUnixSocketCommands(t *testing.T) {
	u := &inventory.UnixEntry{UnixSocket: sockets.UnixSocket{Type: sockets.UnixStream, Flags: 0x10000,
		State: sockets.UnixUnconnected, Inode: 42, Path: "-rf x"}, HostNetNS: true}
	got := cmds(UnixSocket(u, nil, Env{}))
	for _, want := range []string{"ss -xape -- src '-rf x'\n", "ls -l -- '-rf x'\n", "awk 'NR == 1 || $7 == 42' /proc/net/unix\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestNamespaceScan(t *testing.T) {
	snap := &inventory.Snapshot{Procs: &procs.Table{HostNetNS: 1,
		Procs:      map[int]*procs.Process{4242: {PID: 4242, Comm: "srv", Loaded: true}},
		Namespaces: []procs.Namespace{{Key: 1, PIDs: []int{1}}, {Key: 2, PIDs: []int{4242}}}}}
	ops := NamespaceScan(snap, Env{})
	if len(ops) != 2 || ops[0].Cmd != "ss -tuxanp" || ops[1].Cmd != "sudo nsenter -t 4242 -n ss -tuxanp" ||
		!strings.Contains(ops[1].Desc, "srv(4242)") {
		t.Errorf("ops = %+v", ops)
	}
}

// TestCommandsAgainstRealSocket runs the generated commands for a socket
// this test listens on, so they are checked against the real tools.
func TestCommandsAgainstRealSocket(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	local := netip.MustParseAddrPort(ln.Addr().String())

	// Find the socket's inode the way portview does.
	data, err := os.ReadFile("/proc/self/net/tcp")
	if err != nil {
		t.Skip("no /proc/self/net/tcp:", err)
	}
	var inode uint64
	for _, s := range sockets.ParseProcNetBytes(data, sockets.TCP, sockets.IPv4) {
		if s.Local == local && s.State == sockets.StateListen {
			inode = s.Inode
		}
	}
	if inode == 0 {
		t.Fatal("listener not found in /proc/self/net/tcp")
	}
	e := entry(sockets.TCP, sockets.IPv4, sockets.StateListen, local.String(), "0.0.0.0:0")
	e.Inode = inode
	port := strconv.Itoa(int(local.Port()))

	run := func(c Command) string {
		t.Helper()
		out, err := exec.Command("sh", "-c", c.Cmd).Output()
		// Unprivileged, find cannot read other users' fd directories and
		// exits 1 even though it prints the match (its errors are hidden).
		if err != nil && !strings.HasPrefix(c.Cmd, "find ") {
			t.Fatalf("%s: %v", c.Cmd, err)
		}
		return string(out)
	}
	for _, c := range Socket(e, nil, Env{}) {
		bin := strings.Fields(c.Cmd)[0]
		if _, err := exec.LookPath(bin); err != nil {
			t.Logf("skipping %s: not installed", bin)
			continue
		}
		out := run(c)
		switch bin {
		case "ss":
			if !strings.Contains(out, local.String()) || !strings.Contains(out, "ino:"+strconv.FormatUint(inode, 10)) {
				t.Errorf("%s:\n%s", c.Cmd, out)
			}
		case "lsof":
			if !strings.Contains(out, ":"+port+" (LISTEN)") {
				t.Errorf("%s:\n%s", c.Cmd, out)
			}
		case "awk":
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) != 2 || !strings.Contains(lines[1], sockets.FormatProcNetAddr(local, sockets.IPv4)) {
				t.Errorf("%s: want the header and one line:\n%s", c.Cmd, out)
			}
		case "find":
			if !strings.Contains(out, "/proc/"+strconv.Itoa(os.Getpid())+"/fd/") {
				t.Errorf("%s: want this process's fd:\n%s", c.Cmd, out)
			}
		}
	}
}
