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
