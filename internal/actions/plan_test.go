package actions

import (
	"fmt"
	"net/netip"
	"strings"
	"syscall"
	"testing"

	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

const (
	nginxCgroup = "/system.slice/nginx.service"
	ctrID       = "4bf8254b1f0f0123456789abcdef0123456789abcdef0123456789abcdef0123"
)

// fakeProcs is a process table for Details.
type fakeProcs map[int]*procs.Details

func (f fakeProcs) Details(pid int) (*procs.Details, error) {
	if d, ok := f[pid]; ok {
		return d, nil
	}
	return nil, procs.ErrExited
}

func proc(pid, ppid int, comm, cgroup string) *procs.Process {
	return &procs.Process{PID: pid, PPID: ppid, Comm: comm, Cgroup: cgroup, Loaded: true, Inspectable: true}
}

func details(p *procs.Process, uid int, cmd ...string) *procs.Details {
	u, c := procs.ParseCgroupPath(p.Cgroup)
	return &procs.Details{PID: p.PID, Comm: p.Comm, UID: uid, Cmdline: cmd, StartTicks: uint64(p.PID) * 10, Unit: u, Container: c}
}

func entry(owners ...*procs.Process) *inventory.Entry {
	e := &inventory.Entry{Socket: sockets.Socket{
		Proto: sockets.TCP, Family: sockets.IPv4, State: sockets.StateListen,
		Local: netip.MustParseAddrPort("0.0.0.0:80"), Inode: 1234,
	}, Owners: owners, HostNetNS: true}
	e.Primary = inventory.PrimaryOwner(owners)
	return e
}

func labels(p *Plan) []string {
	var out []string
	for i, o := range p.Options {
		l := o.Text(syscall.SIGTERM)
		if i == p.Default {
			l = "*" + l
		}
		out = append(out, l)
	}
	return out
}

func notes(p *Plan) string { return strings.Join(p.Notes, "\n") }

func TestPlanSharedService(t *testing.T) {
	master := proc(812, 1, "nginx", nginxCgroup)
	w1 := proc(813, 812, "nginx", nginxCgroup)
	w2 := proc(814, 812, "nginx", nginxCgroup)
	fp := fakeProcs{
		812: details(master, 0, "nginx: master process /usr/sbin/nginx"),
		813: details(w1, 33, "nginx: worker process"),
		814: details(w2, 33, "nginx: worker process"),
	}
	// Owners in a non-PID order: the primary must still come first.
	p := NewPlan(PlanInput{Entry: entry(w2, master, w1), Details: fp.Details, Users: users{0: "root", 33: "www-data"}, Privileged: true})

	want := []string{
		"*SIGTERM PID 812 nginx (primary)", "SIGTERM PID 813 nginx", "SIGTERM PID 814 nginx",
		"SIGTERM all 3 processes", "systemctl stop nginx.service",
	}
	if got := labels(p); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("options = %q\nwant      %q", got, want)
	}
	o := p.Owners[0]
	if o.PID != 812 || !o.Primary || o.User != "root" || o.StartTicks != 8120 || o.Command != "'nginx: master process /usr/sbin/nginx'" {
		t.Errorf("primary owner = %+v", o)
	}
	if p.Unit.Name != "nginx.service" {
		t.Errorf("unit = %+v", p.Unit)
	}
	if all := p.Options[3]; all.Kind != Signal || len(all.Owners) != 3 {
		t.Errorf("all option = %+v", all)
	}
	if len(p.Notes) != 0 {
		t.Errorf("notes = %q", p.Notes)
	}
}

func TestPlanManyOwnersCapped(t *testing.T) {
	var owners []*procs.Process
	fp := fakeProcs{}
	for pid := 100; pid < 120; pid++ {
		p := proc(pid, 99, "worker", "/user.slice/user-1000.slice/session-2.scope")
		owners = append(owners, p)
		fp[pid] = details(p, 1000, "worker")
	}
	p := NewPlan(PlanInput{Entry: entry(owners...), Details: fp.Details})
	if len(p.Options) != MaxOwnerOptions+1 {
		t.Errorf("options = %q", labels(p))
	}
	if last := p.Options[len(p.Options)-1]; last.Text(syscall.SIGKILL) != "SIGKILL all 20 processes" || len(last.Owners) != 20 {
		t.Errorf("last option = %+v", last.Label)
	}
	if p.Unit.Name != "" {
		t.Errorf("a scope must not be offered: %+v", p.Unit)
	}
}

func TestPlanExitedAndReusedOwners(t *testing.T) {
	a, b, c := proc(10, 1, "srv", ""), proc(11, 10, "srv", ""), proc(12, 10, "srv", "")
	fp := fakeProcs{
		10: details(a, 1000, "srv"),
		12: details(proc(12, 1, "other", ""), 1000, "other"), // PID reused
	}
	p := NewPlan(PlanInput{Entry: entry(a, b, c), Details: fp.Details})
	if len(p.Owners) != 1 || p.Owners[0].PID != 10 {
		t.Errorf("owners = %+v", p.Owners)
	}
	if !strings.Contains(notes(p), "2 of the processes holding this socket exited") {
		t.Errorf("notes = %q", p.Notes)
	}
	if got := labels(p); fmt.Sprint(got) != "[*SIGTERM PID 10 srv]" {
		t.Errorf("options = %q", got)
	}
}

func TestPlanNoOwner(t *testing.T) {
	tests := []struct {
		name       string
		inode      uint64
		privileged bool
		note       string
	}{
		{"hidden", 1234, false, "Rerun portview with sudo"},
		{"kernel", 1234, true, "kernel socket"},
		{"time-wait", 0, false, "TIME-WAIT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := entry()
			e.Inode = tt.inode
			p := NewPlan(PlanInput{Entry: e, Details: fakeProcs{}.Details, Privileged: tt.privileged})
			if len(p.Options) != 0 || p.Default != -1 {
				t.Errorf("options = %q, default %d", labels(p), p.Default)
			}
			if !strings.Contains(notes(p), tt.note) {
				t.Errorf("notes = %q, want %q", p.Notes, tt.note)
			}
		})
	}
}

func TestPlanSocketActivation(t *testing.T) {
	// Only PID 1 holds the socket: nothing to signal.
	p := NewPlan(PlanInput{Entry: entry(proc(1, 0, "systemd", "/init.scope")), Details: fakeProcs{}.Details, Privileged: true})
	if len(p.Options) != 0 || !strings.Contains(notes(p), ".socket unit") {
		t.Errorf("options = %q, notes = %q", labels(p), p.Notes)
	}

	// The user manager holding a socket is not offered either.
	mgr := proc(1500, 1, "systemd", "/user.slice/user-1000.slice/user@1000.service/init.scope")
	p = NewPlan(PlanInput{Entry: entry(mgr), Details: fakeProcs{1500: details(mgr, 1000, "/usr/lib/systemd/systemd", "--user")}.Details})
	if len(p.Options) != 0 || !strings.Contains(notes(p), "systemctl --user") {
		t.Errorf("user manager: options = %q, notes = %q", labels(p), p.Notes)
	}
}

func TestPlanUserService(t *testing.T) {
	app := proc(2000, 1500, "node", "/user.slice/user-1000.slice/user@1000.service/app.slice/dev.service")
	p := NewPlan(PlanInput{Entry: entry(app), Details: fakeProcs{2000: details(app, 1000, "node", "server.js")}.Details})
	want := "[*SIGTERM PID 2000 node systemctl --user stop dev.service]"
	if got := labels(p); fmt.Sprint(got) != want {
		t.Errorf("options = %q, want %s", got, want)
	}
	if !p.Unit.UserManager || p.Unit.User != 1000 {
		t.Errorf("unit = %+v", p.Unit)
	}
}

func TestPlanContainer(t *testing.T) {
	srv := proc(3000, 2990, "srv", "/system.slice/docker-"+ctrID+".scope")
	fp := fakeProcs{3000: details(srv, 0, "/srv")}
	e := entry(srv)
	e.Container = &inventory.ContainerRef{ID: ctrID, Name: "web-1", Runtime: "docker"}

	p := NewPlan(PlanInput{Entry: e, Details: fp.Details, Privileged: true, CanStopContainers: true})
	want := "[SIGTERM PID 3000 srv *docker stop web-1]"
	if got := labels(p); fmt.Sprint(got) != want {
		t.Errorf("options = %q, want %s", got, want)
	}

	// Without the API the process can still be signalled.
	p = NewPlan(PlanInput{Entry: e, Details: fp.Details, Privileged: true})
	if got := labels(p); fmt.Sprint(got) != "[*SIGTERM PID 3000 srv]" || !strings.Contains(notes(p), "Docker API is not available") {
		t.Errorf("no API: options = %q, notes = %q", got, p.Notes)
	}

	// Hidden owner: the container is found through the namespace.
	hidden := entry()
	hidden.Container = e.Container
	p = NewPlan(PlanInput{Entry: hidden, Details: fp.Details, CanStopContainers: true})
	if got := labels(p); fmt.Sprint(got) != "[*docker stop web-1]" {
		t.Errorf("hidden owner: options = %q", got)
	}
	if strings.Contains(notes(p), "sudo") {
		t.Errorf("docker stop works without sudo; notes = %q", p.Notes)
	}

	// Podman: identified, but not stoppable through the Docker API.
	pod := entry()
	pod.Container = &inventory.ContainerRef{ID: ctrID, Runtime: "podman"}
	p = NewPlan(PlanInput{Entry: pod, Details: fp.Details, CanStopContainers: true})
	if len(p.Options) != 0 || !strings.Contains(notes(p), "podman stop 4bf8254b1f0f") {
		t.Errorf("podman: options = %q, notes = %q", labels(p), p.Notes)
	}
}

func TestPlanDockerProxy(t *testing.T) {
	proxy := proc(4000, 900, "docker-proxy", "/system.slice/docker.service")
	fp := fakeProcs{4000: details(proxy, 0, "/usr/bin/docker-proxy", "-proto", "tcp")}
	e := entry(proxy)
	e.Container = &inventory.ContainerRef{ID: ctrID, Name: "web-1", Runtime: "docker",
		Forward: &inventory.Forward{ContainerPort: 8080, Proto: "tcp"}}
	p := NewPlan(PlanInput{Entry: e, Details: fp.Details, Privileged: true, CanStopContainers: true})
	// docker.service must never be offered: it would stop every container.
	want := "[SIGTERM PID 4000 docker-proxy *docker stop web-1]"
	if got := labels(p); fmt.Sprint(got) != want {
		t.Errorf("options = %q, want %s", got, want)
	}
	if p.Unit.Name != "" || !strings.Contains(notes(p), "only breaks the port mapping") {
		t.Errorf("unit = %+v, notes = %q", p.Unit, p.Notes)
	}
}

func TestPlanUserUnitMustMatchOwner(t *testing.T) {
	// A root process can't be in a unit of user 1000's manager: the cgroup
	// path is lying (or stale), so systemctl is not offered.
	app := proc(2100, 1500, "srv", "/user.slice/user-1000.slice/user@1000.service/app.slice/dev.service")
	p := NewPlan(PlanInput{Entry: entry(app), Details: fakeProcs{2100: details(app, 0, "srv")}.Details, Privileged: true})
	if p.Unit.Name != "" || len(p.Options) != 1 {
		t.Errorf("unit = %+v, options = %q", p.Unit, labels(p))
	}
}
