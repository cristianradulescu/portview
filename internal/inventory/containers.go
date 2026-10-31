package inventory

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"

	"github.com/cristianradulescu/portview/internal/docker"
	"github.com/cristianradulescu/portview/internal/procs"
)

// Annotator enriches a snapshot after sockets and processes are joined
// (container names today; later e.g. processes started by portview itself).
type Annotator interface {
	Annotate(ctx context.Context, snap *Snapshot)
}

// ContainerRef links a socket to a container.
type ContainerRef struct {
	ID      string
	Name    string // empty when the Docker API is not available
	Image   string
	Runtime string // "docker" or "podman"
	Project string // docker compose project, if any
	Service string // docker compose service, if any

	// Forward is set when the socket is not inside the container but on the
	// host, forwarding a published port to it (docker-proxy).
	Forward *Forward
}

// Forward is a published port: host socket → container port.
type Forward struct {
	ContainerPort uint16
	Proto         string
}

// ShortID is the 12-character container ID.
func (c *ContainerRef) ShortID() string {
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

// Label is the name, or the short ID when the name is unknown.
func (c *ContainerRef) Label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.ShortID()
}

// ContainerLister lists running containers; *docker.Client implements it.
type ContainerLister interface {
	Containers(ctx context.Context) ([]docker.Container, error)
}

// ContainerAnnotator attaches containers to entries:
//
//  1. a socket owned by a process in a container (from its cgroup);
//  2. a socket in another network namespace whose owner is not visible:
//     the namespace's first process tells the container, which works
//     without privileges;
//  3. a host socket listening on a port that a container publishes: the
//     docker-proxy forwarding to it, even when its PID is hidden.
//
// Names come from the Docker API when Docker is set; without it (or when
// the daemon is unreachable) containers are still identified by ID.
type ContainerAnnotator struct {
	Docker ContainerLister
	// RetryAfter is how long to wait after a failed API call; default 30s.
	RetryAfter time.Duration

	mu        sync.Mutex
	nextTry   time.Time
	lastErr   error
	now       func() time.Time
	lastFetch []docker.Container
}

// Annotate implements Annotator.
func (a *ContainerAnnotator) Annotate(ctx context.Context, snap *Snapshot) {
	list, err := a.containers(ctx)
	if err != nil && !errors.Is(err, docker.ErrUnavailable) {
		snap.DockerErr = err
	}
	byID := make(map[string]*docker.Container, len(list))
	for i := range list {
		byID[list[i].ID] = &list[i]
	}

	nsContainer := namespaceContainers(snap.Procs)
	for i := range snap.Entries {
		e := &snap.Entries[i]
		var c procs.Container
		if e.Primary != nil {
			c = e.Primary.Container()
		}
		if c.ID == "" && !e.HostNetNS {
			c = nsContainer[e.NetNS]
		}
		if c.ID != "" {
			e.Container = newRef(c.ID, c.Runtime, byID[c.ID])
			continue
		}
		if e.HostNetNS && e.Listening() {
			if ct, port := publishedBy(list, e); ct != nil {
				ref := newRef(ct.ID, "docker", ct)
				ref.Forward = &Forward{ContainerPort: port.PrivatePort, Proto: port.Proto}
				e.Container = ref
			}
		}
	}
}

func (a *ContainerAnnotator) containers(ctx context.Context) ([]docker.Container, error) {
	if a.Docker == nil {
		return nil, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now
	if a.now != nil {
		now = a.now
	}
	if now().Before(a.nextTry) {
		return a.lastFetch, a.lastErr
	}
	list, err := a.Docker.Containers(ctx)
	if err != nil {
		retry := a.RetryAfter
		if retry <= 0 {
			retry = 30 * time.Second
		}
		a.nextTry, a.lastErr, a.lastFetch = now().Add(retry), err, nil
		return nil, err
	}
	a.nextTry, a.lastErr, a.lastFetch = time.Time{}, nil, list
	return list, nil
}

func newRef(id, runtime string, ct *docker.Container) *ContainerRef {
	ref := &ContainerRef{ID: id, Runtime: runtime}
	if ct != nil {
		ref.Name, ref.Image, ref.Project, ref.Service = ct.Name, ct.Image, ct.Project, ct.Service
	}
	return ref
}

// namespaceContainers maps each non-host namespace to the container of its
// first loaded member that is in one.
func namespaceContainers(t *procs.Table) map[uint64]procs.Container {
	out := make(map[uint64]procs.Container)
	if t == nil {
		return out
	}
	for _, ns := range t.Namespaces {
		if ns.Key == t.HostNetNS {
			continue
		}
		for _, pid := range ns.PIDs {
			if p := t.Procs[pid]; p != nil && p.Loaded {
				if c := p.Container(); c.ID != "" {
					out[ns.Key] = c
					break
				}
			}
		}
	}
	return out
}

// publishedBy finds the container publishing the host port e listens on.
// A visible owner other than docker-proxy rules the match out.
func publishedBy(list []docker.Container, e *Entry) (*docker.Container, *docker.Port) {
	if e.Primary != nil && e.Primary.Comm != "docker-proxy" {
		return nil, nil
	}
	local := e.Local.Addr().Unmap()
	for i := range list {
		for j := range list[i].Ports {
			p := &list[i].Ports[j]
			if p.PublicPort == 0 || p.PublicPort != e.Local.Port() || p.Proto != e.Proto.String() {
				continue
			}
			if !p.IP.IsValid() || addrMatches(p.IP.Unmap(), local) {
				return &list[i], p
			}
		}
	}
	return nil, nil
}

func addrMatches(published, local netip.Addr) bool {
	if published.IsUnspecified() && local.IsUnspecified() {
		return true
	}
	return published == local
}

// containerName is the container label used in lists and filters.
func containerName(e *Entry) string {
	if e.Container == nil {
		return ""
	}
	return e.Container.Label()
}
