package inventory

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

// UnixEntry is a Unix socket joined with the processes holding it.
type UnixEntry struct {
	sockets.UnixSocket
	Owners  []*procs.Process
	Primary *procs.Process
	// User is the primary owner's user name; /proc/net/unix has no owner
	// of its own, so it is "?" when the owner is not visible.
	User      string
	HostNetNS bool
}

// PIDLabel follows the conventions of Entry.PIDLabel.
func (u *UnixEntry) PIDLabel(privileged bool) string {
	switch {
	case u.Primary != nil && len(u.Owners) > 1:
		return fmt.Sprintf("%d+%d", u.Primary.PID, len(u.Owners)-1)
	case u.Primary != nil:
		return strconv.Itoa(u.Primary.PID)
	case privileged:
		return "-"
	}
	return "?"
}

// ProcessLabel follows the conventions of Entry.ProcessLabel.
func (u *UnixEntry) ProcessLabel(privileged bool) string {
	switch {
	case u.Primary != nil && u.Primary.Comm != "":
		return u.Primary.Comm
	case u.Primary == nil && privileged:
		return "-"
	}
	return "?"
}

// joinUnix attaches owners to Unix sockets and sorts them: named sockets
// by path, then unnamed ones by inode.
func joinUnix(us []sockets.UnixSocket, table *procs.Table, users UserResolver, privileged bool) []UnixEntry {
	out := make([]UnixEntry, 0, len(us))
	for _, s := range us {
		u := UnixEntry{UnixSocket: s, HostNetNS: s.NetNS == table.HostNetNS}
		for _, pid := range table.ByInode[s.Inode] {
			if p := table.Procs[pid]; p != nil {
				u.Owners = append(u.Owners, p)
			}
		}
		u.Primary = PrimaryOwner(u.Owners)
		switch {
		case u.Primary != nil && u.Primary.UID >= 0 && users != nil:
			u.User = users.Name(u.Primary.UID)
		case u.Primary != nil && u.Primary.UID >= 0:
			u.User = strconv.Itoa(u.Primary.UID)
		case privileged:
			u.User = "-"
		default:
			u.User = "?"
		}
		out = append(out, u)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := &out[i], &out[j]
		if (a.Path == "") != (b.Path == "") {
			return a.Path != ""
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.HostNetNS != b.HostNetNS {
			return a.HostNetNS
		}
		return a.Inode < b.Inode
	})
	return out
}

func (c *Collector) collectUnix(ctx context.Context, targets []sockets.Target, snap *Snapshot) {
	if c.Unix == nil {
		return
	}
	us, err := c.Unix.CollectUnix(ctx, targets)
	if err != nil {
		return // Unix sockets are best effort; IP sockets are the point
	}
	snap.Unix = joinUnix(us, snap.Procs, c.Users, snap.Privileged)
}

// MatchUnix reports whether u passes the filter. The state filter applies
// as for ss -x (listening: LISTEN or UNCONN); protocol and IP family
// filters do not apply to Unix sockets.
func (f Filter) MatchUnix(u *UnixEntry) bool {
	switch f.State {
	case StateListening:
		if !u.Listening() {
			return false
		}
	case StateEstablished:
		if u.State != sockets.UnixConnected || u.Listen() {
			return false
		}
	}
	for term := range strings.FieldsSeq(strings.ToLower(f.Text)) {
		if !matchUnixTerm(u, term) {
			return false
		}
	}
	return true
}

func matchUnixTerm(u *UnixEntry, term string) bool {
	// A bare number matches a PID or the inode exactly.
	if n, err := strconv.ParseUint(term, 10, 64); err == nil {
		if u.Inode == n {
			return true
		}
		for _, p := range u.Owners {
			if uint64(p.PID) == n {
				return true
			}
		}
		return false
	}
	fields := []string{u.Path, u.User, u.Type.String(), u.Type.Netid(), u.StateLabel()}
	if u.Primary != nil {
		fields = append(fields, u.Primary.Comm)
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), term) {
			return true
		}
	}
	return false
}
