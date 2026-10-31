// Package commands turns what portview shows and does into the shell
// commands that show or do the same thing, so portview doubles as a way
// to learn ss, lsof, /proc, systemctl and the Docker API.
//
// Every value that ends up in a command is shell-quoted: unit names,
// container names and the like come from untrusted sources, and these
// commands are meant to be copied into a shell.
package commands

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"

	"github.com/cristianradulescu/portview/internal/actions"
	"github.com/cristianradulescu/portview/internal/hostinfo"
	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

// Command is a shell command and what it shows.
type Command struct {
	Comment string // shown as "# Comment"
	Cmd     string
}

// Op is something portview did, as the command that does the same.
type Op struct {
	Desc string
	Cmd  string
	Err  error
}

// Env describes the session the commands are for.
type Env struct {
	// Privileged is true when portview runs as root: the equivalent
	// commands then need sudo to show the same.
	Privileged bool
	// EUID is portview's effective UID.
	EUID int
	// Users resolves UIDs for systemctl --machine=<user>@.host.
	Users inventory.UserResolver
	// DockerSocket is the Docker API socket; "" when Docker is disabled.
	DockerSocket string
	// Host knows the registered service names; nil leaves them out.
	Host *hostinfo.Info
}

func (env Env) sudo(cmd string) string {
	if env.Privileged {
		return "sudo " + cmd
	}
	return cmd
}

// --- the list -----------------------------------------------------------

// View is the ss command listing what the socket list shows with filter f
// (for the host network namespace; ss sees only its own namespace).
func View(f inventory.Filter, env Env) Command {
	flags := "-"
	switch f.OnlyProto {
	case sockets.TCP:
		flags += "t"
	case sockets.UDP:
		flags += "u"
	default:
		flags += "tu"
	}
	var state string
	switch f.State {
	case inventory.StateListening:
		flags += "l"
	case inventory.StateEstablished:
		state = " state established"
	default:
		flags += "a"
	}
	flags += "np"
	switch f.OnlyFamily {
	case sockets.IPv4:
		flags += "4"
	case sockets.IPv6:
		flags += "6"
	}
	cmd := "ss " + flags + state
	comment := "this list (host network namespace)"

	var ports, words []string
	for _, term := range strings.Fields(f.Text) {
		if n, err := strconv.ParseUint(term, 10, 16); err == nil {
			ports = append(ports, strconv.FormatUint(n, 10))
		} else {
			words = append(words, term)
		}
	}
	if len(ports) > 0 {
		var exprs []string
		for _, p := range ports {
			exprs = append(exprs, fmt.Sprintf("( sport = :%s or dport = :%s )", p, p))
		}
		cmd += " " + Quote(strings.Join(exprs, " and "))
		comment += "; ss matches numbers as ports only, not PIDs"
	}
	cmd = env.sudo(cmd)
	for _, w := range words {
		cmd += " | grep -iF -- " + Quote(w)
	}
	if len(words) > 0 {
		comment += "; grep also drops the header line"
	}
	if !env.Privileged {
		comment += "; add sudo to see every process"
	}
	return Command{Comment: comment, Cmd: cmd}
}

// --- the selection ------------------------------------------------------

// Selection is every equivalent command for the selected socket: the
// socket itself, its process, unit and container.
func Selection(e *inventory.Entry, snap *inventory.Snapshot, d *procs.Details, env Env) []Command {
	out := Socket(e, snap, env)
	if e.Primary != nil && d != nil {
		out = append(out, Process(e, d, env)...)
		if d.Unit.Name != "" {
			out = append(out, Unit(d.Unit, env)...)
		}
	}
	if e.Container != nil {
		out = append(out, Container(e.Container, env)...)
	}
	return out
}

// Socket is how to look at one socket with ss, lsof and /proc.
func Socket(e *inventory.Entry, snap *inventory.Snapshot, env Env) []Command {
	var out []Command
	add := func(comment, cmd string) { out = append(out, Command{comment, cmd}) }

	// Sockets in another network namespace are only visible to ss and
	// lsof run inside it; /proc/<pid>/net/* shows them from anywhere.
	leader := 0
	if !e.HostNetNS {
		leader = snap.Leader(e.NetNS)
	}
	inNS := func(cmd string) string {
		if leader > 0 {
			return fmt.Sprintf("sudo nsenter -t %d -n %s", leader, cmd)
		}
		return env.sudo(cmd)
	}
	nsNote := ""
	if leader > 0 {
		nsNote = fmt.Sprintf(", inside the network namespace of PID %d", leader)
	}

	// ss
	proto := "t"
	if e.Proto == sockets.UDP {
		proto = "u"
	}
	flags := "-" + proto + "a"
	if e.Listening() {
		flags = "-" + proto + "l"
	}
	add("this socket, with its owner (-p), uid and inode (-e)"+nsNote,
		inNS("ss "+flags+"npe "+Quote(ssFilter(e))))

	// lsof
	if e.Inode != 0 {
		lproto := "TCP"
		if e.Proto == sockets.UDP {
			lproto = "UDP"
		}
		lsof := fmt.Sprintf("lsof -nP -i%s:%d", lproto, e.Local.Port())
		if e.Proto == sockets.TCP && e.Listening() {
			lsof += " -sTCP:LISTEN"
		}
		add("the same with lsof"+nsNote, inNS(lsof))
	}

	if e.Listening() {
		if env.Host.Service(e.Local.Port(), e.Proto.String()) != "" {
			add("the service registered for this port in /etc/services", fmt.Sprintf("getent services %d/%s", e.Local.Port(), e.Proto))
		}
		add("ports below the first need CAP_NET_BIND_SERVICE; the range is where the kernel picks ports for outgoing connections",
			"sysctl net.ipv4.ip_unprivileged_port_start net.ipv4.ip_local_port_range")
	}

	// the raw kernel table
	add(fmt.Sprintf("the kernel's table, which ss and portview read: %s is %s in hex, state %02X is %s (i explains every field)",
		sockets.FormatAddrPort(e.Local), sockets.FormatProcNetAddr(e.Local, e.Family), uint8(e.State), e.State),
		RawLine(e, leader))

	// the owner, the way portview finds it
	if e.Inode != 0 {
		add(fmt.Sprintf("which process holds socket inode %d (how portview finds owners)", e.Inode),
			env.sudo(fmt.Sprintf(`find /proc/[0-9]*/fd -lname 'socket:\[%d\]' 2>/dev/null`, e.Inode)))
	}
	return out
}

// TablePath is the /proc file listing e: /proc/net/<table> for the host
// namespace, /proc/<leader>/net/<table> for another one (readable without
// root).
func TablePath(e *inventory.Entry, leader int) string {
	table := sockets.ProcNetTable(e.Proto, e.Family)
	if leader > 0 {
		return fmt.Sprintf("/proc/%d/net/%s", leader, table)
	}
	return "/proc/net/" + table
}

// RawLine is the awk command printing e's line of the kernel table, with
// the header.
func RawLine(e *inventory.Entry, leader int) string {
	match := fmt.Sprintf(`$2 == "%s"`, sockets.FormatProcNetAddr(e.Local, e.Family))
	if !e.Listening() {
		match += fmt.Sprintf(` && $3 == "%s"`, sockets.FormatProcNetAddr(e.Remote, e.Family))
	}
	return fmt.Sprintf("awk %s %s", Quote("NR == 1 || "+match), TablePath(e, leader))
}

// ssFilter selects exactly e in an ss filter expression: "sport = :80"
// for a wildcard address, "src 127.0.0.1:8080" otherwise, plus the remote
// end for connected sockets.
func ssFilter(e *inventory.Entry) string {
	side := func(dir, port string, ap netip.AddrPort) string {
		if ap.Addr().IsUnspecified() {
			return fmt.Sprintf("%s = :%d", port, ap.Port())
		}
		return dir + " " + ap.String()
	}
	f := side("src", "sport", e.Local)
	if !e.Listening() && e.Remote.IsValid() && e.Remote.Port() != 0 {
		f += " and " + side("dst", "dport", e.Remote)
	}
	return f
}

// Process is how to look at a process with ps and /proc.
func Process(e *inventory.Entry, d *procs.Details, env Env) []Command {
	pid := d.PID
	var out []Command
	add := func(comment, cmd string) { out = append(out, Command{comment, cmd}) }
	add("the process", fmt.Sprintf("ps -o pid,ppid,user,lstart,etime,args -p %d", pid))
	add("its full command line (arguments are NUL-separated in /proc)",
		fmt.Sprintf(`tr '\0' ' ' < /proc/%d/cmdline; echo`, pid))
	readlink := fmt.Sprintf("readlink /proc/%d/exe /proc/%d/cwd", pid, pid)
	if d.UID != env.EUID {
		readlink = "sudo " + readlink
	}
	add("its executable and working directory (root-only for other users' processes)", readlink)
	add("its cgroup, which names the systemd unit and container", fmt.Sprintf("cat /proc/%d/cgroup", pid))
	add("its ancestry and children", fmt.Sprintf("pstree -sp %d", pid))
	add("its effective capabilities (CapEff in /proc/<pid>/status, decoded)", fmt.Sprintf("getpcaps %d", pid))
	others := func(cmd string) string {
		if d.UID != env.EUID {
			return "sudo " + cmd
		}
		return cmd
	}
	add("how many files it has open, and its limit", others(fmt.Sprintf("ls /proc/%d/fd | wc -l", pid))+fmt.Sprintf("; prlimit --pid %d --nofile", pid))
	add("its namespaces; compare with ls -l /proc/self/ns", others(fmt.Sprintf("ls -l /proc/%d/ns", pid)))
	add("all its sockets", env.sudo("ss -tuanp")+" | grep "+Quote(fmt.Sprintf("pid=%d,", pid)))
	if len(e.Owners) > 1 {
		pids := make([]string, len(e.Owners))
		for i, o := range e.Owners {
			pids[i] = strconv.Itoa(o.PID)
		}
		add("every process sharing the socket", "ps -o pid,ppid,user,args -p "+strings.Join(pids, ","))
	}
	return out
}

// Unit is how to look at a systemd unit.
func Unit(u procs.Unit, env Env) []Command {
	mgr := strings.Join(actions.ManagerArgs(u, env.EUID, env.Users), " ")
	sc := "systemctl "
	if mgr != "" {
		sc += mgr + " "
	}
	name := Quote(u.Name)
	journal := "journalctl -u " + name + " -n 50 --no-pager"
	if u.UserManager {
		if u.User == env.EUID {
			journal = "journalctl --user -u " + name + " -n 50 --no-pager"
		} else {
			journal = fmt.Sprintf("journalctl _UID=%d _SYSTEMD_USER_UNIT=%s -n 50 --no-pager", u.User, name)
		}
	}
	// "--" ends the options: a unit named "--host=…" (users name units
	// freely in their own subtree) must stay a unit name. journalctl -u
	// always takes the next word as its value.
	return []Command{
		{"the unit's state and its processes", sc + "status -- " + name},
		{"its recent log", journal},
		{"whether systemd restarts it, and its main PID", sc + "show -p Restart,MainPID,ActiveState -- " + name},
	}
}

// Container is how to look at a container.
func Container(c *inventory.ContainerRef, env Env) []Command {
	bin := "docker"
	if c.Runtime == "podman" {
		bin = "podman"
	}
	ref := "-- " + Quote(c.Label()) // "--": a name is never an option
	out := []Command{
		{"the container", bin + " ps --filter " + Quote("id="+c.ID)},
		{"its configuration, state and networks", bin + " inspect " + ref},
		{"its processes, as seen from the host", bin + " top " + ref},
	}
	if c.Forward != nil {
		out = append(out, Command{"the host ports it publishes (docker-proxy forwards them)", bin + " port " + ref})
	}
	if bin == "docker" && env.DockerSocket != "" {
		out = append(out, Command{"the Docker API call portview uses to list containers",
			dockerAPI(env.DockerSocket, "GET", "/containers/json")})
	}
	return out
}

// --- actions --------------------------------------------------------------

// Option is the exact command an option of the kill dialog amounts to.
func Option(o *actions.Option, sig syscall.Signal, env Env) []string {
	switch o.Kind {
	case actions.Signal:
		pids := make([]int, len(o.Owners))
		for i, ow := range o.Owners {
			pids[i] = ow.PID
		}
		return []string{Kill(sig, pids...)}
	case actions.StopUnit:
		return []string{Systemctl(o.Unit, env, "stop")}
	case actions.StopContainer:
		cmds := []string{"docker stop -- " + Quote(o.Container.Label())}
		if env.DockerSocket != "" {
			cmds = append(cmds, ContainerStop(env, o.Container.ID)+"  # what portview sends")
		}
		return cmds
	}
	return nil
}

// Systemctl is the exact systemctl command portview runs for verb on u.
func Systemctl(u procs.Unit, env Env, verb ...string) string {
	return "systemctl " + Join(actions.SystemctlArgs(u, env.EUID, env.Users, verb...)...)
}

func dockerAPI(socket, method, path string) string {
	cmd := "curl -s --unix-socket " + Quote(socket)
	if method != "GET" {
		cmd += " -X " + method
	}
	return cmd + " " + Quote("http://localhost"+path)
}

// --- the command log ------------------------------------------------------

// NamespaceScan is what one collection amounts to: ss in every network
// namespace portview reads.
func NamespaceScan(snap *inventory.Snapshot, env Env) []Op {
	if snap == nil || snap.Procs == nil {
		return nil
	}
	var ops []Op
	for _, ns := range snap.Procs.Namespaces {
		if ns.Key == snap.Procs.HostNetNS {
			ops = append(ops, Op{Desc: "sockets of the host network namespace", Cmd: env.sudo("ss -tuanp")})
			continue
		}
		if len(ns.PIDs) == 0 {
			continue
		}
		ops = append(ops, Op{
			Desc: "sockets of network namespace " + snap.NetNSLabel(ns.Key),
			Cmd:  fmt.Sprintf("sudo nsenter -t %d -n ss -tuanp", ns.PIDs[0]),
		})
	}
	return ops
}

// ContainerList is the Docker API call that names containers.
func ContainerList(env Env, err error) Op {
	return Op{Desc: "running containers (docker ps)", Cmd: dockerAPI(env.DockerSocket, "GET", "/containers/json"), Err: err}
}

// DetailsRead is reading one process's details.
func DetailsRead(pid int, err error) Op {
	return Op{Desc: fmt.Sprintf("details of PID %d", pid),
		Cmd: fmt.Sprintf("ps -o pid,ppid,user,lstart,args -p %d; cat /proc/%d/cgroup", pid, pid), Err: err}
}

// RestartPolicy is reading a unit's Restart= setting.
func RestartPolicy(u procs.Unit, env Env, err error) Op {
	return Op{Desc: "whether systemd restarts " + u.Name, Cmd: Systemctl(u, env, "show", "--property=Restart", "--value"), Err: err}
}

// --- quoting --------------------------------------------------------------

// Quote quotes s for a POSIX shell. Plain words are left as they are.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Join quotes and joins arguments.
func Join(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = Quote(a)
	}
	return strings.Join(q, " ")
}

// ContainerStop is the Docker API call portview makes to stop a container.
func ContainerStop(env Env, id string) string {
	return dockerAPI(env.DockerSocket, "POST", "/containers/"+url.PathEscape(id)+"/stop?t=10")
}

// Kill is the kill(1) command sending sig to pids.
func Kill(sig syscall.Signal, pids ...int) string {
	s := make([]string, len(pids))
	for i, p := range pids {
		s[i] = strconv.Itoa(p)
	}
	return fmt.Sprintf("kill -%s %s", strings.TrimPrefix(actions.SignalName(sig), "SIG"), strings.Join(s, " "))
}
