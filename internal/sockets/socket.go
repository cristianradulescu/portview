package sockets

import (
	"fmt"
	"net/netip"
)

// Proto is the transport protocol of a socket.
type Proto uint8

const (
	TCP Proto = iota + 1
	UDP
)

func (p Proto) String() string {
	switch p {
	case TCP:
		return "tcp"
	case UDP:
		return "udp"
	default:
		return fmt.Sprintf("proto(%d)", uint8(p))
	}
}

// Family is the address family of a socket.
type Family uint8

const (
	IPv4 Family = iota + 1
	IPv6
)

func (f Family) String() string {
	switch f {
	case IPv4:
		return "ipv4"
	case IPv6:
		return "ipv6"
	default:
		return fmt.Sprintf("family(%d)", uint8(f))
	}
}

// State is the kernel socket state as reported in /proc/net/{tcp,udp}
// (the TCP_* constants from include/net/tcp_states.h).
type State uint8

const (
	StateEstablished State = 0x01
	StateSynSent     State = 0x02
	StateSynRecv     State = 0x03
	StateFinWait1    State = 0x04
	StateFinWait2    State = 0x05
	StateTimeWait    State = 0x06
	StateClose       State = 0x07 // UNCONN: bound but not connected (typical for UDP "listeners")
	StateCloseWait   State = 0x08
	StateLastAck     State = 0x09
	StateListen      State = 0x0A
	StateClosing     State = 0x0B
	StateNewSynRecv  State = 0x0C
)

// String returns the state name as printed by ss(8), which makes it easy to
// compare output with `ss -tulpn`.
func (s State) String() string {
	switch s {
	case StateEstablished:
		return "ESTAB"
	case StateSynSent:
		return "SYN-SENT"
	case StateSynRecv, StateNewSynRecv:
		return "SYN-RECV"
	case StateFinWait1:
		return "FIN-WAIT-1"
	case StateFinWait2:
		return "FIN-WAIT-2"
	case StateTimeWait:
		return "TIME-WAIT"
	case StateClose:
		return "UNCONN"
	case StateCloseWait:
		return "CLOSE-WAIT"
	case StateLastAck:
		return "LAST-ACK"
	case StateListen:
		return "LISTEN"
	case StateClosing:
		return "CLOSING"
	default:
		return fmt.Sprintf("STATE-%02X", uint8(s))
	}
}

// Socket is a single TCP or UDP socket as seen in one network namespace.
type Socket struct {
	Proto  Proto
	Family Family
	State  State
	Local  netip.AddrPort
	Remote netip.AddrPort

	TxQueue uint64
	RxQueue uint64

	// UID is the owner of the socket inode (the creating process's fsuid).
	// Unlike the owning PID it is visible without privileges.
	UID uint32

	// Inode is the sockfs inode; it links the socket to the file descriptors
	// in /proc/<pid>/fd. It is 0 for sockets not attached to a file, such as
	// TIME-WAIT entries.
	Inode uint64

	// NetNS identifies the network namespace the socket was read from (see
	// procs.Namespace.Key).
	NetNS uint64
}

// Netid returns "tcp", "tcp6", "udp" or "udp6", like the Netid column of ss.
func (s Socket) Netid() string {
	if s.Family == IPv6 {
		return s.Proto.String() + "6"
	}
	return s.Proto.String()
}

// Listening reports whether the socket accepts incoming traffic without a
// fixed peer: TCP sockets in LISTEN and unconnected UDP sockets. This matches
// what `ss -l` considers listening.
func (s Socket) Listening() bool {
	switch s.Proto {
	case TCP:
		return s.State == StateListen
	case UDP:
		return s.State == StateClose
	}
	return false
}

// FormatAddrPort formats an endpoint the way ss does: "addr:port" for IPv4,
// "[addr]:port" for IPv6, with a zero port rendered as "*".
func FormatAddrPort(ap netip.AddrPort) string {
	if !ap.IsValid() {
		return "*:*"
	}
	addr := ap.Addr().String()
	if ap.Addr().Is6() {
		addr = "[" + addr + "]"
	}
	if ap.Port() == 0 {
		return addr + ":*"
	}
	return fmt.Sprintf("%s:%d", addr, ap.Port())
}
