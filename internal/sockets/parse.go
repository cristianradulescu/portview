package sockets

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"
)

// ParseProcNet parses the contents of /proc/net/{tcp,tcp6,udp,udp6}.
// proto and family describe which file is being parsed. Malformed lines are
// skipped; an error is only returned when reading fails.
//
// Line layout (fields separated by spaces; only the first ten are used):
//
//	sl local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode ...
func ParseProcNet(r io.Reader, proto Proto, family Family) ([]Socket, error) {
	var out []Socket
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		if first { // header line
			first = false
			continue
		}
		s, err := parseLine(sc.Text(), proto, family)
		if err != nil {
			continue
		}
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("reading %s%s table: %w", proto, familySuffix(family), err)
	}
	return out, nil
}

// ParseProcNetBytes is a convenience wrapper around ParseProcNet.
func ParseProcNetBytes(data []byte, proto Proto, family Family) []Socket {
	s, _ := ParseProcNet(bytes.NewReader(data), proto, family)
	return s
}

func familySuffix(f Family) string {
	if f == IPv6 {
		return "6"
	}
	return ""
}

func parseLine(line string, proto Proto, family Family) (Socket, error) {
	f := strings.Fields(line)
	if len(f) < 10 {
		return Socket{}, fmt.Errorf("short line: %d fields", len(f))
	}
	local, err := parseHexAddrPort(f[1], family)
	if err != nil {
		return Socket{}, fmt.Errorf("local address: %w", err)
	}
	remote, err := parseHexAddrPort(f[2], family)
	if err != nil {
		return Socket{}, fmt.Errorf("remote address: %w", err)
	}
	st, err := strconv.ParseUint(f[3], 16, 8)
	if err != nil {
		return Socket{}, fmt.Errorf("state: %w", err)
	}
	txs, rxs, ok := strings.Cut(f[4], ":")
	if !ok {
		return Socket{}, fmt.Errorf("queues: %q", f[4])
	}
	tx, err := strconv.ParseUint(txs, 16, 64)
	if err != nil {
		return Socket{}, fmt.Errorf("tx_queue: %w", err)
	}
	rx, err := strconv.ParseUint(rxs, 16, 64)
	if err != nil {
		return Socket{}, fmt.Errorf("rx_queue: %w", err)
	}
	uid, err := strconv.ParseUint(f[7], 10, 32)
	if err != nil {
		return Socket{}, fmt.Errorf("uid: %w", err)
	}
	inode, err := strconv.ParseUint(f[9], 10, 64)
	if err != nil {
		return Socket{}, fmt.Errorf("inode: %w", err)
	}
	return Socket{
		Proto:   proto,
		Family:  family,
		State:   State(st),
		Local:   local,
		Remote:  remote,
		TxQueue: tx,
		RxQueue: rx,
		UID:     uint32(uid),
		Inode:   inode,
	}, nil
}

// parseHexAddrPort decodes "0100007F:0277" (IPv4) or a 32-hex-digit IPv6
// address followed by ":PORT". The kernel prints addresses as a sequence of
// 32-bit words in host byte order, so each word is converted back to its
// in-memory (network order) bytes with the native endianness.
func parseHexAddrPort(s string, family Family) (netip.AddrPort, error) {
	host, portHex, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("missing port in %q", s)
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("port %q: %w", portHex, err)
	}
	var addr netip.Addr
	switch family {
	case IPv4:
		var b [4]byte
		if len(host) != 8 {
			return netip.AddrPort{}, fmt.Errorf("ipv4 address %q: want 8 hex digits", host)
		}
		if err := decodeWords(b[:], host); err != nil {
			return netip.AddrPort{}, err
		}
		addr = netip.AddrFrom4(b)
	case IPv6:
		var b [16]byte
		if len(host) != 32 {
			return netip.AddrPort{}, fmt.Errorf("ipv6 address %q: want 32 hex digits", host)
		}
		if err := decodeWords(b[:], host); err != nil {
			return netip.AddrPort{}, err
		}
		addr = netip.AddrFrom16(b)
	default:
		return netip.AddrPort{}, fmt.Errorf("unknown family %v", family)
	}
	return netip.AddrPortFrom(addr, uint16(port)), nil
}

func decodeWords(dst []byte, hexStr string) error {
	for i := 0; i < len(dst); i += 4 {
		w, err := strconv.ParseUint(hexStr[i*2:i*2+8], 16, 32)
		if err != nil {
			return fmt.Errorf("address word %q: %w", hexStr[i*2:i*2+8], err)
		}
		binary.NativeEndian.PutUint32(dst[i:], uint32(w))
	}
	return nil
}

// FormatProcNetAddr encodes ap the way /proc/net/{tcp,udp}[6] print it,
// e.g. 127.0.0.1:8080 as "0100007F:1F90" on little-endian machines. It is
// the inverse of the parser, used to point at a socket's line in the raw
// table.
func FormatProcNetAddr(ap netip.AddrPort, family Family) string {
	var b []byte
	if family == IPv4 {
		a := ap.Addr().Unmap().As4()
		b = a[:]
	} else {
		a := ap.Addr().As16()
		b = a[:]
	}
	var sb strings.Builder
	for i := 0; i < len(b); i += 4 {
		fmt.Fprintf(&sb, "%08X", binary.NativeEndian.Uint32(b[i:]))
	}
	fmt.Fprintf(&sb, ":%04X", ap.Port())
	return sb.String()
}

// ProcNetTable is the /proc/net file name for proto and family ("tcp6").
func ProcNetTable(proto Proto, family Family) string {
	name := "tcp"
	if proto == UDP {
		name = "udp"
	}
	if family == IPv6 {
		name += "6"
	}
	return name
}
