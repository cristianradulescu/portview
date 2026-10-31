package sockets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Target is one network namespace to read sockets from.
type Target struct {
	// NetNS is the namespace key; it is copied into Socket.NetNS.
	NetNS uint64
	// PIDs are processes living in the namespace. The source reads
	// /proc/<pid>/net/* through the first one that is still alive.
	PIDs []int
}

// Source collects sockets from a set of network namespaces.
type Source interface {
	Collect(ctx context.Context, targets []Target) ([]Socket, error)
}

// ProcSource reads /proc/<pid>/net/{tcp,tcp6,udp,udp6}. These files are
// world-readable and show the namespace of the given process, so no setns(2)
// and no privileges are needed to see sockets in other namespaces.
type ProcSource struct {
	// Root is the procfs mount point; empty means "/proc".
	Root string
}

type table struct {
	name   string
	proto  Proto
	family Family
}

var tables = []table{
	{"tcp", TCP, IPv4},
	{"tcp6", TCP, IPv6},
	{"udp", UDP, IPv4},
	{"udp6", UDP, IPv6},
}

// ErrNoSockets is returned when no namespace could be read at all.
var ErrNoSockets = errors.New("no socket tables could be read")

// Collect implements Source. Namespaces whose processes all disappeared are
// skipped silently. A missing tcp6/udp6 table (IPv6 disabled) is not an error.
func (p ProcSource) Collect(ctx context.Context, targets []Target) ([]Socket, error) {
	root := p.Root
	if root == "" {
		root = "/proc"
	}
	var out []Socket
	read := 0
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		for _, pid := range t.PIDs {
			socks, ok := readNamespace(filepath.Join(root, strconv.Itoa(pid), "net"), t.NetNS)
			if ok {
				out = append(out, socks...)
				read++
				break
			}
		}
	}
	if read == 0 && len(targets) > 0 {
		return nil, fmt.Errorf("%w (procfs at %s)", ErrNoSockets, root)
	}
	return out, nil
}

// readNamespace reads all four tables from dir. It reports false when the
// process went away (the IPv4 tcp table, which always exists, is unreadable).
func readNamespace(dir string, netns uint64) ([]Socket, bool) {
	var out []Socket
	for i, tb := range tables {
		data, err := os.ReadFile(filepath.Join(dir, tb.name))
		if err != nil {
			if i == 0 {
				return nil, false
			}
			continue // e.g. tcp6/udp6 missing because IPv6 is disabled
		}
		socks := ParseProcNetBytes(data, tb.proto, tb.family)
		for j := range socks {
			socks[j].NetNS = netns
		}
		out = append(out, socks...)
	}
	return out, true
}
