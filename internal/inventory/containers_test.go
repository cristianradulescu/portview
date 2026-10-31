package inventory

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/cristianradulescu/portview/internal/docker"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

const (
	ctrID   = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	otherID = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

type fakeDocker struct {
	list  []docker.Container
	err   error
	calls int
}

func (f *fakeDocker) Containers(context.Context) ([]docker.Container, error) {
	f.calls++
	return f.list, f.err
}

func webContainer() docker.Container {
	return docker.Container{
		ID: ctrID, Name: "web-1", Image: "nginx:1.29", Project: "shop", Service: "web",
		Ports: []docker.Port{
			{IP: netip.MustParseAddr("0.0.0.0"), PrivatePort: 80, PublicPort: 8080, Proto: "tcp"},
			{IP: netip.MustParseAddr("::"), PrivatePort: 80, PublicPort: 8080, Proto: "tcp"},
			{IP: netip.MustParseAddr("127.0.0.1"), PrivatePort: 5353, PublicPort: 5353, Proto: "udp"},
			{PrivatePort: 443, Proto: "tcp"}, // exposed, not published
		},
	}
}

func containerSnapshot() *Snapshot {
	const host, ctrNS = 1, 2
	inCtr := &procs.Process{PID: 500, Comm: "nginx", Loaded: true, NetNS: ctrNS,
		Cgroup: "/system.slice/docker-" + ctrID + ".scope"}
	proxy := &procs.Process{PID: 900, Comm: "docker-proxy", Loaded: true, NetNS: host,
		Cgroup: "/system.slice/docker.service"}
	hostNginx := &procs.Process{PID: 300, Comm: "nginx", Loaded: true, NetNS: host,
		Cgroup: "/system.slice/nginx.service"}
	table := &procs.Table{
		HostNetNS: host,
		Procs:     map[int]*procs.Process{500: inCtr, 900: proxy, 300: hostNginx},
		Namespaces: []procs.Namespace{
			{Key: host, PIDs: []int{300, 900}},
			{Key: ctrNS, PIDs: []int{500}},
		},
	}
	mk := func(proto sockets.Proto, local string, ns uint64, owners ...*procs.Process) Entry {
		state := sockets.StateListen
		if proto == sockets.UDP {
			state = sockets.StateClose
		}
		ap := netip.MustParseAddrPort(local)
		fam := sockets.IPv4
		if ap.Addr().Is6() {
			fam = sockets.IPv6
		}
		e := Entry{Socket: sockets.Socket{Proto: proto, Family: fam, State: state, Local: ap, NetNS: ns, Inode: 1},
			Owners: owners, Primary: PrimaryOwner(owners), HostNetNS: ns == host}
		return e
	}
	return &Snapshot{Procs: table, Entries: []Entry{
		mk(sockets.TCP, "0.0.0.0:80", ctrNS, inCtr),      // 0: owned by a container process
		mk(sockets.TCP, "0.0.0.0:443", ctrNS),            // 1: container netns, owner hidden
		mk(sockets.TCP, "0.0.0.0:8080", host, proxy),     // 2: docker-proxy, visible
		mk(sockets.TCP, "[::]:8080", host),               // 3: docker-proxy, PID hidden
		mk(sockets.UDP, "127.0.0.1:5353", host),          // 4: published on loopback only
		mk(sockets.UDP, "0.0.0.0:5353", host),            // 5: different address: not the mapping
		mk(sockets.TCP, "0.0.0.0:9090", host, hostNginx), // 6: plain host service
	}}
}

func TestContainerAnnotator(t *testing.T) {
	d := &fakeDocker{list: []docker.Container{webContainer()}}
	snap := containerSnapshot()
	(&ContainerAnnotator{Docker: d}).Annotate(context.Background(), snap)

	want := []struct {
		name    string
		forward uint16
	}{
		{"web-1", 0}, {"web-1", 0}, {"web-1", 80}, {"web-1", 80}, {"web-1", 5353}, {"", 0}, {"", 0},
	}
	for i, w := range want {
		c := snap.Entries[i].Container
		switch {
		case w.name == "" && c != nil:
			t.Errorf("entry %d: unexpected container %+v", i, c)
		case w.name != "" && c == nil:
			t.Errorf("entry %d: no container, want %s", i, w.name)
		case c != nil && (c.Name != w.name || c.Image != "nginx:1.29" || c.Project != "shop"):
			t.Errorf("entry %d: container %+v", i, c)
		case c != nil && w.forward == 0 && c.Forward != nil:
			t.Errorf("entry %d: unexpected forward %+v", i, c.Forward)
		case c != nil && w.forward != 0 && (c.Forward == nil || c.Forward.ContainerPort != w.forward):
			t.Errorf("entry %d: forward = %+v, want port %d", i, c.Forward, w.forward)
		}
	}
	if snap.DockerErr != nil {
		t.Errorf("DockerErr = %v", snap.DockerErr)
	}
}

func TestContainerAnnotatorHostProcessOnPublishedPort(t *testing.T) {
	// A visible non-proxy owner on a published port is not the mapping.
	snap := containerSnapshot()
	snap.Entries[6].Local = netip.MustParseAddrPort("0.0.0.0:8080")
	(&ContainerAnnotator{Docker: &fakeDocker{list: []docker.Container{webContainer()}}}).Annotate(context.Background(), snap)
	if snap.Entries[6].Container != nil {
		t.Errorf("host nginx labelled as container: %+v", snap.Entries[6].Container)
	}
}

func TestContainerAnnotatorWithoutAPI(t *testing.T) {
	// --no-docker: IDs from cgroups only, no names, no published-port mapping.
	snap := containerSnapshot()
	(&ContainerAnnotator{}).Annotate(context.Background(), snap)
	if c := snap.Entries[0].Container; c == nil || c.Name != "" || c.Label() != "0123456789ab" || c.Runtime != "docker" {
		t.Errorf("entry 0 = %+v", c)
	}
	if c := snap.Entries[1].Container; c == nil || c.ID != ctrID {
		t.Errorf("namespace fallback = %+v", c)
	}
	if snap.Entries[2].Container != nil {
		t.Error("published-port mapping needs the API")
	}
}

func TestContainerAnnotatorErrorsAndBackoff(t *testing.T) {
	now := time.Unix(1000, 0)
	d := &fakeDocker{err: docker.ErrPermission}
	a := &ContainerAnnotator{Docker: d, RetryAfter: 30 * time.Second, now: func() time.Time { return now }}

	snap := containerSnapshot()
	a.Annotate(context.Background(), snap)
	if !errors.Is(snap.DockerErr, docker.ErrPermission) {
		t.Errorf("DockerErr = %v", snap.DockerErr)
	}
	if c := snap.Entries[0].Container; c == nil || c.Name != "" {
		t.Errorf("cgroup ID should still be shown: %+v", c)
	}

	a.Annotate(context.Background(), containerSnapshot())
	if d.calls != 1 {
		t.Errorf("retried too early: %d calls", d.calls)
	}
	now = now.Add(31 * time.Second)
	d.err, d.list = nil, []docker.Container{webContainer()}
	snap = containerSnapshot()
	a.Annotate(context.Background(), snap)
	if d.calls != 2 || snap.Entries[0].Container.Name != "web-1" || snap.DockerErr != nil {
		t.Errorf("after backoff: calls=%d %+v err=%v", d.calls, snap.Entries[0].Container, snap.DockerErr)
	}

	// Docker not installed: silent.
	snap = containerSnapshot()
	(&ContainerAnnotator{Docker: &fakeDocker{err: docker.ErrUnavailable}}).Annotate(context.Background(), snap)
	if snap.DockerErr != nil {
		t.Errorf("ErrUnavailable must not be reported: %v", snap.DockerErr)
	}
}

func TestFilterMatchesContainerName(t *testing.T) {
	snap := containerSnapshot()
	(&ContainerAnnotator{Docker: &fakeDocker{list: []docker.Container{webContainer()}}}).Annotate(context.Background(), snap)
	got := Filter{State: StateAll, Text: "web-1"}.Apply(snap.Entries)
	if len(got) != 5 {
		t.Errorf("filter by container name matched %d entries, want 5", len(got))
	}
}
