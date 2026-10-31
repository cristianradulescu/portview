package procs

import (
	"strings"

	"github.com/cristianradulescu/portview/internal/termsafe"
)

// Unit is the systemd unit a process belongs to, derived from its cgroup.
type Unit struct {
	// Name is the unit name, e.g. "nginx.service" or "session-2.scope";
	// empty when the process is not in a unit.
	Name string
	// UserManager is true for units of a per-user systemd instance
	// (managed with `systemctl --user`).
	UserManager bool
	// User is the UID owning the user manager ("user@1000.service"), -1 for
	// system units.
	User int
}

// IsService reports whether the unit is a .service. Only services are
// (re)started by systemd; scopes just group externally started processes.
func (u Unit) IsService() bool { return strings.HasSuffix(u.Name, ".service") }

// String renders the unit with its manager, e.g. "nginx.service" or
// "pipewire.service (user 1000)".
func (u Unit) String() string {
	if u.Name == "" {
		return ""
	}
	if u.UserManager {
		return u.Name + " (user " + itoa(u.User) + ")"
	}
	return u.Name
}

// Container identifies a container a process runs in.
type Container struct {
	// ID is the full container ID (64 hex digits); empty when not in a
	// container.
	ID string
	// Runtime is "docker" or "podman".
	Runtime string
	// Delegated is set when the cgroup lies inside a user manager's
	// subtree (user@UID.service). systemd delegates that subtree to the
	// user, who can create cgroups with any name there, so a "docker-<id>"
	// segment in it proves nothing about the system's Docker containers.
	// Rootless containers (podman, rootless Docker) live there too.
	Delegated bool
}

// ShortID is the 12-character ID shown by `docker ps`.
func (c Container) ShortID() string {
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

// ParseCgroupPath extracts the systemd unit and container from a cgroup v2
// path (as returned by ParseCgroup), for example:
//
//	/system.slice/nginx.service                          → nginx.service
//	/user.slice/user-1000.slice/user@1000.service/app.slice/foo.service
//	                                                     → foo.service (user manager of 1000)
//	/system.slice/docker-<id>.scope                      → container <id> (docker, systemd driver)
//	/docker/<id>                                         → container <id> (docker, cgroupfs driver)
//	/machine.slice/libpod-<id>.scope                     → container <id> (podman)
func ParseCgroupPath(path string) (Unit, Container) {
	u := Unit{User: -1}
	var c Container
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range segs {
		switch {
		// Only the first user@UID.service is the manager: below it the
		// user names cgroups freely, and a nested "user@0.service" must
		// not redirect systemctl to another user's manager.
		case !u.UserManager && strings.HasPrefix(seg, "user@") && strings.HasSuffix(seg, ".service"):
			u.UserManager = true
			u.User = atoiOr(strings.TrimSuffix(strings.TrimPrefix(seg, "user@"), ".service"), -1)
			u.Name = "" // units below belong to the user manager
		case strings.HasSuffix(seg, ".service") || strings.HasSuffix(seg, ".scope"):
			u.Name = termsafe.String(seg)
		}
		if id, rt, ok := containerFromSegment(seg, segs, i); ok {
			c = Container{ID: id, Runtime: rt, Delegated: u.UserManager}
		}
	}
	if u.UserManager && u.Name == "" {
		// A process directly in user@UID.service is the manager itself.
		u.Name, u.UserManager = "user@"+itoa(u.User)+".service", false
	}
	return u, c
}

func containerFromSegment(seg string, segs []string, i int) (id, runtime string, ok bool) {
	switch {
	case strings.HasPrefix(seg, "docker-") && strings.HasSuffix(seg, ".scope"):
		id, runtime = seg[len("docker-"):len(seg)-len(".scope")], "docker"
	case strings.HasPrefix(seg, "libpod-") && strings.HasSuffix(seg, ".scope"):
		id, runtime = seg[len("libpod-"):len(seg)-len(".scope")], "podman"
	case i > 0 && segs[i-1] == "docker":
		id, runtime = seg, "docker"
	default:
		return "", "", false
	}
	return id, runtime, isContainerID(id)
}

func isContainerID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Unit returns the systemd unit of a loaded process.
func (p *Process) Unit() Unit {
	u, _ := ParseCgroupPath(p.Cgroup)
	return u
}

// Container returns the container of a loaded process.
func (p *Process) Container() Container {
	_, c := ParseCgroupPath(p.Cgroup)
	return c
}
