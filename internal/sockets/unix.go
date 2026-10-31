package sockets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cristianradulescu/portview/internal/termsafe"
)

// UnixType is the socket type of a Unix socket.
type UnixType uint16

// Unix socket types (SOCK_* in the kernel).
const (
	UnixStream    UnixType = 1
	UnixDgram     UnixType = 2
	UnixSeqPacket UnixType = 5
)

func (t UnixType) String() string {
	switch t {
	case UnixStream:
		return "stream"
	case UnixDgram:
		return "dgram"
	case UnixSeqPacket:
		return "seqpacket"
	}
	return "type" + strconv.Itoa(int(t))
}

// Netid is the ss name: u_str, u_dgr, u_seq.
func (t UnixType) Netid() string {
	switch t {
	case UnixStream:
		return "u_str"
	case UnixDgram:
		return "u_dgr"
	case UnixSeqPacket:
		return "u_seq"
	}
	return "u_?"
}

// UnixState is the socket state (SS_* in the kernel).
type UnixState uint8

// Unix socket states.
const (
	UnixUnconnected   UnixState = 1
	UnixConnecting    UnixState = 2
	UnixConnected     UnixState = 3
	UnixDisconnecting UnixState = 4
)

// unixAcceptCon is __SO_ACCEPTCON: the socket listens (listen() was called).
const unixAcceptCon = 0x10000

// UnixSocket is one line of /proc/net/unix.
type UnixSocket struct {
	RefCount uint32
	Flags    uint32
	Type     UnixType
	State    UnixState
	Inode    uint64
	// Path is the socket file, "@name" for an abstract socket (a name in
	// the kernel only, scoped to the network namespace), or "" for an
	// unnamed socket (one end of a connection or socketpair).
	Path  string
	NetNS uint64
	// Suspicious is set when another record of the same table has the
	// same inode. Paths may contain newlines, which the kernel prints as
	// they are, so a path can forge whole lines of /proc/net/unix; a
	// forged line pointing at a real socket must repeat its inode.
	Suspicious bool
}

// Listen reports whether listen() was called on the socket.
func (u *UnixSocket) Listen() bool { return u.Flags&unixAcceptCon != 0 }

// Listening reports whether ss -l would show the socket: listening, or
// not connected (UNCONN), as with UDP.
func (u *UnixSocket) Listening() bool { return u.Listen() || u.State == UnixUnconnected }

// Abstract reports whether the socket has an abstract name.
func (u *UnixSocket) Abstract() bool { return strings.HasPrefix(u.Path, "@") }

// StateLabel is the state as ss shows it.
func (u *UnixSocket) StateLabel() string {
	if u.Listen() {
		return "LISTEN"
	}
	switch u.State {
	case UnixUnconnected:
		return "UNCONN"
	case UnixConnecting:
		return "SYN-SENT"
	case UnixConnected:
		return "ESTAB"
	case UnixDisconnecting:
		return "CLOSING"
	}
	return "?"
}

// ParseProcNetUnix parses /proc/net/unix:
//
//	Num RefCount Protocol Flags Type St Inode [Path]
//
// Malformed lines are skipped. Paths are sanitized for the terminal.
// Records sharing an inode are marked Suspicious (see UnixSocket).
func ParseProcNetUnix(data []byte) []UnixSocket {
	var out []UnixSocket
	seen := make(map[uint64]int)
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		u, err := parseUnixLine(line)
		if err == nil {
			out = append(out, u)
			seen[u.Inode]++
		}
	}
	for i := range out {
		if seen[out[i].Inode] > 1 {
			out[i].Suspicious = true
		}
	}
	return out
}

func parseUnixLine(line string) (UnixSocket, error) {
	f := strings.Fields(line)
	if len(f) < 7 {
		return UnixSocket{}, fmt.Errorf("short line: %d fields", len(f))
	}
	hex := func(s string, bits int) (uint64, error) { return strconv.ParseUint(s, 16, bits) }
	ref, err1 := hex(f[1], 32)
	flags, err2 := hex(f[3], 32)
	typ, err3 := hex(f[4], 16)
	st, err4 := hex(f[5], 8)
	inode, err5 := strconv.ParseUint(f[6], 10, 64)
	for _, err := range []error{err1, err2, err3, err4, err5} {
		if err != nil {
			return UnixSocket{}, err
		}
	}
	u := UnixSocket{RefCount: uint32(ref), Flags: uint32(flags), Type: UnixType(typ), State: UnixState(st), Inode: inode}
	if len(f) > 7 {
		// The path is the rest of the line (it may contain spaces).
		rest := line
		for range 7 {
			rest = strings.TrimLeft(rest, " ")
			if i := strings.IndexByte(rest, ' '); i >= 0 {
				rest = rest[i:]
			}
		}
		u.Path = termsafe.String(strings.TrimSpace(rest))
	}
	return u, nil
}

// UnixSource collects Unix sockets per network namespace.
type UnixSource interface {
	CollectUnix(ctx context.Context, targets []Target) ([]UnixSocket, error)
}

// CollectUnix reads /proc/<pid>/net/unix of each namespace.
func (p ProcSource) CollectUnix(ctx context.Context, targets []Target) ([]UnixSocket, error) {
	root := p.Root
	if root == "" {
		root = "/proc"
	}
	var out []UnixSocket
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		for _, pid := range t.PIDs {
			data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "net", "unix"))
			if err != nil {
				continue // process gone: try the next one
			}
			for _, u := range ParseProcNetUnix(data) {
				u.NetNS = t.NetNS
				out = append(out, u)
			}
			break
		}
	}
	return out, nil
}
