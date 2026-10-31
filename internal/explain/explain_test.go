package explain

import (
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/cristianradulescu/portview/internal/sockets"
)

// Lines captured from a live system (x86-64).
const (
	tcpListen = "   0: 0100007F:235A 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 24341 1 0000000000000000 100 0 0 10 0"
	tcpEstab  = "   5: E601A8C0:EB18 0479528C:01BB 01 00000000:00000000 02:00000D76 00000000  1000        0 453374 2 0000000000000000 26 4 25 10 -1"
	// TIME-WAIT, in the layout of the kernel's get_timewait4_sock.
	tcpTimeWait = "   7: E601A8C0:B2C4 22D8B3AC:01BB 06 00000000:00000000 03:00001770 00000000     0        0 0 3 0000000000000000"
	udpUnconn   = "   66: 0100007F:0143 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 22912 2 0000000000000000 17"
	tcp6Listen  = "   0: 00000000000000000000000001000000:0277 00000000000000000000000000000000:0000 0A 00000003:00000002 00:00000000 00000000     0        0 27867 1 0000000000000000 100 0 0 10 0"
)

func little() bool { return binary.NativeEndian.Uint16([]byte{1, 0}) == 1 }

func byName(t *testing.T, fs []Field) map[string]Field {
	t.Helper()
	m := make(map[string]Field, len(fs))
	for _, f := range fs {
		m[f.Name] = f
	}
	return m
}

func check(t *testing.T, m map[string]Field, name string, wants ...string) {
	t.Helper()
	f, ok := m[name]
	if !ok {
		t.Errorf("no field %s", name)
		return
	}
	for _, w := range wants {
		if !strings.Contains(f.Meaning, w) {
			t.Errorf("%s (%s): meaning %q lacks %q", name, f.Raw, f.Meaning, w)
		}
	}
}

func opts() Options {
	return Options{ClockTicks: 100, UserName: func(uid int) string {
		return map[int]string{0: "root", 1000: "alice"}[uid]
	}}
}

func TestTCPListen(t *testing.T) {
	if !little() {
		t.Skip("fixtures are little-endian")
	}
	fs, err := Fields(tcpListen, sockets.TCP, sockets.IPv4, opts())
	if err != nil {
		t.Fatal(err)
	}
	m := byName(t, fs)
	check(t, m, "local_address", "127.0.0.1:9050", "reversed: 0100007F → 7F 00 00 01", "0x235A = 9050")
	check(t, m, "rem_address", "not connected")
	check(t, m, "st", "0x0A = LISTEN", "waiting for incoming connections")
	check(t, m, "rx_queue", "0 connection(s)", "accept()", "Send-Q")
	check(t, m, "tr", "no timer")
	check(t, m, "uid", "root")
	check(t, m, "inode", "socket:[24341]")
	check(t, m, "(address)", "kptr_restrict")
	check(t, m, "(rto)", "100 ticks of 1/100s = 1s")
	check(t, m, "(fastopen)", "Fast Open")
	if _, ok := m["(ssthresh)"]; ok {
		t.Error("a listening socket has no ssthresh field")
	}
	for _, f := range fs {
		if f.Notable {
			t.Errorf("%s notable on an idle listener", f.Name)
		}
	}
}

func TestTCPEstablished(t *testing.T) {
	if !little() {
		t.Skip("fixtures are little-endian")
	}
	m := byName(t, must(Fields(tcpEstab, sockets.TCP, sockets.IPv4, opts())))
	check(t, m, "local_address", "192.168.1.230:60184")
	check(t, m, "rem_address", "140.82.121.4:443")
	check(t, m, "st", "ESTABLISHED", "data flows")
	check(t, m, "tr", "keepalive")
	check(t, m, "tm->when", "3446 ticks of 1/100s = 34.46s") // 0xD76
	check(t, m, "uid", "alice")
	check(t, m, "(rto)", "26 ticks of 1/100s = 260ms")
	check(t, m, "(ssthresh)", "initial slow start")
}

func TestTCPTimeWait(t *testing.T) {
	m := byName(t, must(Fields(tcpTimeWait, sockets.TCP, sockets.IPv4, opts())))
	check(t, m, "st", "TIME_WAIT", "60s")
	check(t, m, "tr", "TIME-WAIT timer")
	check(t, m, "tm->when", "6000 ticks of 1/100s = 1m00s")
	check(t, m, "inode", "no inode", "closed")
	check(t, m, "uid", "no longer belongs")
	if _, ok := m["(rto)"]; ok {
		t.Error("TIME-WAIT lines have no rto field")
	}
}

func TestUDP(t *testing.T) {
	m := byName(t, must(Fields(udpUnconn, sockets.UDP, sockets.IPv4, opts())))
	check(t, m, "sl", "bucket")
	check(t, m, "st", "UNCONN", "UDP server")
	check(t, m, "tr", "no timers")
	check(t, m, "(drops)", "17 datagram(s) dropped")
	if !m["(drops)"].Notable {
		t.Error("drops > 0 should be notable")
	}
}

func TestTCP6(t *testing.T) {
	if !little() {
		t.Skip("fixtures are little-endian")
	}
	m := byName(t, must(Fields(tcp6Listen, sockets.TCP, sockets.IPv6, opts())))
	check(t, m, "local_address", "[::1]:631", "four 32-bit numbers")
	check(t, m, "rx_queue", "2 connection(s)")
	if !m["rx_queue"].Notable {
		t.Error("a non-empty accept queue should be notable")
	}
}

func TestShortLine(t *testing.T) {
	if _, err := Fields("0: 0100007F:0050", sockets.TCP, sockets.IPv4, opts()); err == nil {
		t.Error("short line accepted")
	}
}

// TestReadLive explains a socket this test listens on.
func TestReadLive(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	local := netip.MustParseAddrPort(ln.Addr().String())
	data, err := os.ReadFile("/proc/self/net/tcp")
	if err != nil {
		t.Skip(err)
	}
	var s sockets.Socket
	for _, c := range sockets.ParseProcNetBytes(data, sockets.TCP, sockets.IPv4) {
		if c.Local == local && c.State == sockets.StateListen {
			s = c
		}
	}
	r, err := Read("/proc/self/net/tcp", s, opts())
	if err != nil {
		t.Fatal(err)
	}
	if r.LineNo < 2 || !strings.Contains(r.Header, "local_address") || !strings.Contains(r.Line, sockets.FormatProcNetAddr(local, sockets.IPv4)) {
		t.Errorf("report = %+v", r)
	}
	m := byName(t, r.Fields)
	check(t, m, "local_address", local.String())

	ln.Close()
	if _, err := Read("/proc/self/net/tcp", s, opts()); err != ErrNotFound {
		t.Errorf("closed socket: err = %v, want ErrNotFound", err)
	}
}

func must(fs []Field, err error) []Field {
	if err != nil {
		panic(err)
	}
	return fs
}
