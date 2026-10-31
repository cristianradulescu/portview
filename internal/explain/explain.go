// Package explain decodes a socket's raw line in /proc/net/{tcp,udp}[6]
// field by field, as a way to learn what the kernel exposes and what ss
// and portview build on.
//
// The field layout follows the kernel's seq_printf calls:
// get_tcp4_sock / get_tcp6_sock for TCP, get_timewait4_sock /
// get_timewait6_sock for TIME-WAIT, and udp4_format_sock /
// udp6_sock_seq_show for UDP.
package explain

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/cristianradulescu/portview/internal/sockets"
)

// Field is one decoded field of a raw line.
type Field struct {
	Name    string // the column name from the file's header, or ours
	Raw     string // as printed by the kernel
	Meaning string
	// Notable marks a value worth attention (a non-empty accept queue,
	// UDP drops, retransmissions).
	Notable bool
}

// Report is a socket's raw line and what it means.
type Report struct {
	Path   string // the file read
	LineNo int    // 1-based; the header is line 1
	Header string
	Line   string
	Fields []Field
}

// Options carry what the meanings need beyond the line.
type Options struct {
	// UserName resolves a UID; nil prints numbers only.
	UserName func(uid int) string
	// ClockTicks is USER_HZ, the unit of the timer fields (usually 100).
	ClockTicks int64
}

// ErrNotFound means the socket is no longer in the table.
var ErrNotFound = errors.New("the socket is no longer in the kernel table")

// Read finds s in the table at path and explains its line.
func Read(path string, s sockets.Socket, opts Options) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	header, _, _ := strings.Cut(string(data), "\n")
	line, n, _, ok := sockets.FindLine(data, s.Proto, s.Family, func(c sockets.Socket) bool {
		if c.Local != s.Local || c.Remote != s.Remote || c.State != s.State {
			return false
		}
		return s.Inode == 0 || c.Inode == s.Inode
	})
	if !ok {
		return nil, ErrNotFound
	}
	fields, err := Fields(line, s.Proto, s.Family, opts)
	if err != nil {
		return nil, err
	}
	return &Report{Path: path, LineNo: n, Header: header, Line: line, Fields: fields}, nil
}

// Fields explains one line of a /proc/net table.
func Fields(line string, proto sockets.Proto, family sockets.Family, opts Options) ([]Field, error) {
	f := strings.Fields(line)
	if len(f) < 12 {
		return nil, fmt.Errorf("short line: %d fields", len(f))
	}
	hz := opts.ClockTicks
	if hz <= 0 {
		hz = 100
	}
	stCode, err := strconv.ParseUint(f[3], 16, 8)
	if err != nil {
		return nil, fmt.Errorf("state %q: %w", f[3], err)
	}
	state := sockets.State(stCode)
	tcp := proto == sockets.TCP
	listen := tcp && state == sockets.StateListen
	timeWait := tcp && state == sockets.StateTimeWait
	txs, rxs, _ := strings.Cut(f[4], ":")
	tr, when, _ := strings.Cut(f[5], ":")
	tx, _ := strconv.ParseUint(txs, 16, 64)
	rx, _ := strconv.ParseUint(rxs, 16, 64)

	var out []Field
	add := func(name, raw, meaning string, notable bool) {
		out = append(out, Field{Name: name, Raw: raw, Meaning: meaning, Notable: notable})
	}

	if tcp {
		add("sl", f[0], "entry number in this listing; it changes from one read to the next", false)
	} else {
		add("sl", f[0], "hash-table bucket the socket is in (UDP lists sockets by bucket)", false)
	}
	add("local_address", f[1], addrMeaning(f[1], family, true), false)
	add("rem_address", f[2], addrMeaning(f[2], family, false), false)
	add("st", f[3], stateMeaning(state, proto), false)

	// queues
	switch {
	case !tcp:
		add("tx_queue", txs, fmt.Sprintf("%d bytes of memory held by datagrams being sent", tx), false)
		add("rx_queue", rxs, fmt.Sprintf("%d bytes of received datagrams waiting for the program to read them", rx), rx > 0)
	case listen:
		add("tx_queue", txs, "unused for a listening socket", false)
		add("rx_queue", rxs, fmt.Sprintf("%d connection(s) past the handshake, waiting for the program to accept() them; "+
			"when this reaches the backlog limit (ss shows it as Send-Q), new connections are dropped", rx), rx > 0)
	case timeWait:
		add("tx_queue", txs, "always 0: the socket has no buffers left", false)
		add("rx_queue", rxs, "always 0", false)
	default:
		add("tx_queue", txs, fmt.Sprintf("%d bytes written by the program and not yet acknowledged by the peer", tx), tx > 0)
		add("rx_queue", rxs, fmt.Sprintf("%d bytes received and not yet read by the program", rx), rx > 0)
	}

	// timer
	if tcp {
		add("tr", tr, timerMeaning(tr), false)
		add("tm->when", when, ticksMeaning(when, hz, "until the timer fires", true), false)
	} else {
		add("tr", tr, "always 0 for UDP: no timers", false)
		add("tm->when", when, "always 0 for UDP", false)
	}

	retr, _ := strconv.ParseUint(f[6], 16, 32)
	switch {
	case !tcp:
		add("retrnsmt", f[6], "always 0 for UDP", false)
	case timeWait:
		add("retrnsmt", f[6], "always 0 in TIME-WAIT", false)
	default:
		add("retrnsmt", f[6], fmt.Sprintf("%d retransmission timeout(s) in a row without an ACK; it rises while the peer is unreachable", retr), retr > 0)
	}

	uidMeaning := "user ID of the socket's owner"
	if uid, err := strconv.Atoi(f[7]); err == nil && opts.UserName != nil {
		uidMeaning += ": " + opts.UserName(uid)
	}
	if timeWait {
		uidMeaning = "always 0: the socket no longer belongs to anyone"
	}
	add("uid", f[7], uidMeaning, false)

	if tcp && !timeWait {
		add("timeout", f[8], "zero-window probes sent without an answer (the peer stopped reading)", f[8] != "0")
	} else {
		add("timeout", f[8], "always 0 here", false)
	}

	if f[9] == "0" {
		add("inode", f[9], "no inode: no file descriptor refers to this socket. In TIME-WAIT the program has closed it, "+
			"and the kernel keeps the connection's state to absorb late packets", false)
	} else {
		add("inode", f[9], "the socket's inode: /proc/<pid>/fd/N → socket:["+f[9]+"] in the owning process; "+
			"this is how ss -p, lsof and portview find the owner", false)
	}
	add("(refcount)", f[10], "references the kernel holds to the socket", false)
	addr := "kernel address of the socket structure"
	if strings.Trim(f[11], "0") == "" {
		addr += "; all zeros because kernel addresses are hidden from you (kernel.kptr_restrict, CAP_SYSLOG)"
	}
	add("(address)", f[11], addr, false)

	rest := f[12:]
	switch {
	case !tcp && len(rest) >= 1:
		drops, _ := strconv.ParseUint(rest[0], 10, 64)
		add("(drops)", rest[0], fmt.Sprintf("%d datagram(s) dropped, e.g. because the receive buffer was full; "+
			"the program does not read fast enough", drops), drops > 0)
	case tcp && !timeWait && len(rest) >= 5:
		add("(rto)", rest[0], ticksMeaning(rest[0], hz, "retransmission timeout: how long to wait for an ACK before resending", false), false)
		add("(ato)", rest[1], ticksMeaning(rest[1], hz, "delayed-ACK timeout: how long an ACK may be held back to ride on a reply", false), false)
		add("(quick/pp)", rest[2], "quick-ACK budget × 2, plus 1 in interactive (ping-pong) mode, where ACKs are delayed to ride on replies", false)
		add("(cwnd)", rest[3], "congestion window: segments that may be in flight unacknowledged", false)
		if listen {
			add("(fastopen)", rest[4], "maximum TCP Fast Open queue length; 0 when Fast Open is off for this socket", false)
		} else if rest[4] == "-1" {
			add("(ssthresh)", rest[4], "-1: still in initial slow start (the window grows exponentially)", false)
		} else {
			add("(ssthresh)", rest[4], "slow-start threshold in segments: past it the window grows linearly", false)
		}
	}
	return out, nil
}

func addrMeaning(raw string, family sockets.Family, local bool) string {
	hexAddr, hexPort, _ := strings.Cut(raw, ":")
	port, _ := strconv.ParseUint(hexPort, 16, 16)
	b, ok := addrBytes(hexAddr)
	if !ok {
		return "address"
	}
	var ip string
	if family == sockets.IPv4 {
		ip = fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3])
	} else {
		ip = "[" + ipv6String(b) + "]"
	}
	var sb strings.Builder
	allZero := strings.Trim(hexAddr, "0") == ""
	switch {
	case allZero && port == 0 && !local:
		return "no peer (all zeros): the socket is not connected"
	case allZero:
		fmt.Fprintf(&sb, "%s:%d, i.e. any address. ", ipWildcard(family), port)
	default:
		fmt.Fprintf(&sb, "%s:%d. ", ip, port)
	}
	if littleEndian() {
		if family == sockets.IPv4 {
			fmt.Fprintf(&sb, "The address is printed as one 32-bit number in this machine's byte order (little-endian), "+
				"so its bytes appear reversed: %s → %s. ", hexAddr, bytesHex(b))
		} else {
			sb.WriteString("The address is printed as four 32-bit numbers in this machine's byte order (little-endian), " +
				"so the bytes of each group of 8 hex digits appear reversed. ")
		}
	}
	fmt.Fprintf(&sb, "The port is plain hex: 0x%s = %d", hexPort, port)
	return sb.String()
}

func ipWildcard(f sockets.Family) string {
	if f == sockets.IPv4 {
		return "0.0.0.0"
	}
	return "[::]"
}

// addrBytes converts the kernel's word-wise hex back to address bytes.
func addrBytes(h string) ([]byte, bool) {
	if len(h)%8 != 0 || len(h) == 0 {
		return nil, false
	}
	b := make([]byte, len(h)/2)
	for i := 0; i < len(b); i += 4 {
		w, err := strconv.ParseUint(h[i*2:i*2+8], 16, 32)
		if err != nil {
			return nil, false
		}
		binary.NativeEndian.PutUint32(b[i:], uint32(w))
	}
	return b, true
}

func bytesHex(b []byte) string {
	parts := make([]string, len(b))
	for i, c := range b {
		parts[i] = fmt.Sprintf("%02X", c)
	}
	return strings.Join(parts, " ")
}

func ipv6String(b []byte) string {
	var a [16]byte
	copy(a[:], b)
	return netip.AddrFrom16(a).String()
}

func littleEndian() bool { return binary.NativeEndian.Uint16([]byte{1, 0}) == 1 }

func stateMeaning(st sockets.State, proto sockets.Proto) string {
	code := fmt.Sprintf("0x%02X = %s", uint8(st), kernelStateName(st))
	if proto == sockets.UDP {
		switch st {
		case sockets.StateClose:
			return code + ": UDP reuses TCP's state numbers; an unconnected UDP socket is \"closed\" " +
				"(ss shows UNCONN). This is what a UDP server looks like"
		case sockets.StateEstablished:
			return code + ": a connected UDP socket (connect() fixed its peer)"
		}
		return code
	}
	desc := StateHelp(st, proto)
	legend := " (01 ESTABLISHED · 06 TIME_WAIT · 08 CLOSE_WAIT · 0A LISTEN)"
	if desc == "" {
		return code + legend
	}
	return code + ": " + desc + legend
}

// kernelStateName is the kernel's name (TCP_ prefix dropped), which ss
// abbreviates differently.
func kernelStateName(st sockets.State) string {
	names := map[sockets.State]string{
		sockets.StateEstablished: "ESTABLISHED", sockets.StateSynSent: "SYN_SENT", sockets.StateSynRecv: "SYN_RECV",
		sockets.StateFinWait1: "FIN_WAIT1", sockets.StateFinWait2: "FIN_WAIT2", sockets.StateTimeWait: "TIME_WAIT",
		sockets.StateClose: "CLOSE", sockets.StateCloseWait: "CLOSE_WAIT", sockets.StateLastAck: "LAST_ACK",
		sockets.StateListen: "LISTEN", sockets.StateClosing: "CLOSING", sockets.StateNewSynRecv: "NEW_SYN_RECV",
	}
	if n, ok := names[st]; ok {
		return n
	}
	return "?"
}

func timerMeaning(tr string) string {
	switch tr {
	case "00":
		return "no timer running"
	case "01":
		return "1 = retransmission (or loss probe) timer: waiting for an ACK"
	case "02":
		return "2 = keepalive timer: will probe an idle connection"
	case "03":
		return "3 = TIME-WAIT timer: the entry disappears when it fires"
	case "04":
		return "4 = zero-window probe timer: the peer's receive window is full"
	}
	return "timer " + tr
}

// ticksMeaning renders a clock-tick count (hex when isHex).
func ticksMeaning(raw string, hz int64, what string, isHex bool) string {
	base := 10
	if isHex {
		base = 16
	}
	v, err := strconv.ParseInt(raw, base, 64)
	if err != nil {
		return what
	}
	if v == 0 {
		return what + ": 0"
	}
	ms := v * 1000 / hz
	return fmt.Sprintf("%s: %d ticks of 1/%ds = %s", what, v, hz, humanMS(ms))
}

func humanMS(ms int64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60_000:
		return strconv.FormatFloat(float64(ms)/1000, 'f', -1, 64) + "s"
	}
	return fmt.Sprintf("%dm%02ds", ms/60_000, ms%60_000/1000)
}
