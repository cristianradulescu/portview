package inventory

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

// ProcScanner produces a process table; procs.Scanner implements it.
type ProcScanner interface {
	Scan() (*procs.Table, error)
}

// UserResolver maps UIDs to user names.
type UserResolver interface {
	Name(uid int) string
}

// Entry is one socket joined with the processes that hold it.
type Entry struct {
	sockets.Socket

	// Owners are the processes holding the socket, ascending by PID. Empty
	// when the socket's processes could not be inspected (unprivileged) or
	// the socket has no file (kernel-owned, TIME-WAIT).
	Owners []*procs.Process
	// Primary is the owner actions should target: the master of a forking
	// server. Nil when Owners is empty.
	Primary *procs.Process

	Exposure Exposure
	// User is the user name of the primary owner, or of the socket's UID
	// when the owner is unknown.
	User string
	// HostNetNS reports whether the socket lives in the host namespace.
	HostNetNS bool
	// Container is set when the socket belongs to (or forwards to) a
	// container.
	Container *ContainerRef
}

// Snapshot is the result of one collection cycle.
type Snapshot struct {
	// Entries holds every socket in stable display order (see Less).
	Entries []Entry
	Procs   *procs.Table
	// Privileged reports whether portview runs as root.
	Privileged bool
	// Hidden counts sockets that have a file (inode != 0) but no visible
	// owner. When not privileged these most likely belong to other users.
	Hidden int
	// DockerErr is set when the Docker API was reachable in principle but
	// failed, e.g. permission denied on the socket.
	DockerErr error
	Taken     time.Time
}

// Collector assembles snapshots. It does not depend on any UI.
type Collector struct {
	Procs   ProcScanner
	Sockets sockets.Source
	Users   UserResolver
	// Annotators run after sockets and processes are joined.
	Annotators []Annotator
	// Privileged overrides the root detection; nil means os.Geteuid() == 0.
	Privileged *bool
}

// NewCollector returns a collector reading the live /proc.
func NewCollector() *Collector {
	return &Collector{
		Procs:   procs.Scanner{},
		Sockets: sockets.ProcSource{},
		Users:   NewUserCache(),
	}
}

// Collect runs one full scan.
func (c *Collector) Collect(ctx context.Context) (*Snapshot, error) {
	table, err := c.Procs.Scan()
	if err != nil {
		return nil, fmt.Errorf("scanning processes: %w", err)
	}
	targets := make([]sockets.Target, 0, len(table.Namespaces))
	for _, ns := range table.Namespaces {
		targets = append(targets, sockets.Target{NetNS: ns.Key, PIDs: ns.PIDs})
	}
	socks, err := c.Sockets.Collect(ctx, targets)
	if err != nil {
		return nil, fmt.Errorf("collecting sockets: %w", err)
	}

	snap := &Snapshot{
		Procs:      table,
		Privileged: os.Geteuid() == 0,
		Taken:      time.Now(),
		Entries:    make([]Entry, 0, len(socks)),
	}
	if c.Privileged != nil {
		snap.Privileged = *c.Privileged
	}
	for _, s := range socks {
		e := Entry{Socket: s, HostNetNS: s.NetNS == table.HostNetNS}
		if s.Inode != 0 {
			for _, pid := range table.ByInode[s.Inode] {
				if p := table.Procs[pid]; p != nil {
					e.Owners = append(e.Owners, p)
				}
			}
			if len(e.Owners) == 0 {
				snap.Hidden++
			}
		}
		e.Primary = PrimaryOwner(e.Owners)
		e.Exposure = ExposureOf(e.Socket, e.HostNetNS)
		uid := int(s.UID)
		if e.Primary != nil && e.Primary.UID >= 0 {
			uid = e.Primary.UID
		}
		if c.Users != nil {
			e.User = c.Users.Name(uid)
		} else {
			e.User = fmt.Sprint(uid)
		}
		snap.Entries = append(snap.Entries, e)
	}
	for _, a := range c.Annotators {
		a.Annotate(ctx, snap)
	}
	sort.SliceStable(snap.Entries, func(i, j int) bool { return Less(&snap.Entries[i], &snap.Entries[j]) })
	return snap, nil
}

// PrimaryOwner picks the process that "owns" a shared socket: among the
// owners, the one whose parent is not itself an owner (the master of a
// pre-forking server such as nginx or php-fpm), lowest PID on ties.
//
// PID 1 is only chosen when it is the sole owner: with socket activation
// systemd keeps holding the listening socket next to the service, and the
// service is what the user means.
func PrimaryOwner(owners []*procs.Process) *procs.Process {
	cands := owners
	if len(owners) > 1 {
		cands = make([]*procs.Process, 0, len(owners))
		for _, p := range owners {
			if p.PID != 1 {
				cands = append(cands, p)
			}
		}
	}
	if len(cands) == 0 {
		return nil
	}
	in := make(map[int]bool, len(cands))
	for _, p := range cands {
		in[p.PID] = true
	}
	var best *procs.Process
	for _, p := range cands {
		if in[p.PPID] {
			continue
		}
		if best == nil || p.PID < best.PID {
			best = p
		}
	}
	if best == nil { // parent cycle can't happen, but stay safe
		best = cands[0]
	}
	return best
}
