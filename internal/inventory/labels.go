package inventory

import (
	"fmt"
	"strconv"
)

// PIDLabel is the PID column text: "812" for a single owner, "812+4" when
// four more processes share the socket, "?" when the owner cannot be
// inspected, "-" when there is no owning process.
func (e *Entry) PIDLabel(privileged bool) string {
	switch {
	case e.Primary != nil && len(e.Owners) > 1:
		return fmt.Sprintf("%d+%d", e.Primary.PID, len(e.Owners)-1)
	case e.Primary != nil:
		return strconv.Itoa(e.Primary.PID)
	case e.Inode == 0 || privileged:
		return "-" // no file (TIME-WAIT) or kernel-owned
	default:
		return "?"
	}
}

// ProcessLabel is the process-name column text, with the same conventions
// as PIDLabel.
func (e *Entry) ProcessLabel(privileged bool) string {
	switch {
	case e.Primary != nil && e.Primary.Comm != "":
		return e.Primary.Comm
	case e.Primary != nil:
		return "?" // exited before its details were read
	case e.Inode == 0 || privileged:
		return "-"
	default:
		return "?"
	}
}

// NetNSLabel names a network namespace: "host", or its first process, e.g.
// "bwrap(4242)", which is meaningful even without privileges.
func (s *Snapshot) NetNSLabel(key uint64) string {
	if s.Procs == nil {
		return "?"
	}
	if key == s.Procs.HostNetNS {
		return "host"
	}
	ns, ok := s.Procs.Namespace(key)
	if !ok || len(ns.PIDs) == 0 {
		return "?"
	}
	leader := ns.PIDs[0]
	name := "?"
	if p := s.Procs.Procs[leader]; p != nil && p.Comm != "" {
		name = p.Comm
	}
	return fmt.Sprintf("%s(%d)", name, leader)
}

// Leader is the first process of a non-host network namespace, through
// which its /proc/<pid>/net tables are read; 0 for the host namespace or
// an unknown one.
func (s *Snapshot) Leader(netns uint64) int {
	if s == nil || s.Procs == nil || netns == s.Procs.HostNetNS {
		return 0
	}
	if ns, ok := s.Procs.Namespace(netns); ok && len(ns.PIDs) > 0 {
		return ns.PIDs[0]
	}
	return 0
}

// ContainerLabel is the container column text: the container name (or short
// ID), prefixed with "→" for a host socket forwarding a published port to it,
// e.g. "→web-1:80". Empty when the socket is not related to a container.
func (e *Entry) ContainerLabel() string {
	c := e.Container
	if c == nil {
		return ""
	}
	if c.Forward != nil {
		return fmt.Sprintf("→%s:%d", c.Label(), c.Forward.ContainerPort)
	}
	return c.Label()
}
