package sockets

import (
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures encode addresses in little-endian word order, as produced by
// the kernel on x86-64 and arm64.
func requireLittleEndian(t *testing.T) {
	t.Helper()
	var b [2]byte
	binary.NativeEndian.PutUint16(b[:], 1)
	if b[0] != 1 {
		t.Skip("fixtures are little-endian")
	}
}

func parseFixture(t *testing.T, name string, proto Proto, fam Family) []Socket {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	socks, err := ParseProcNet(f, proto, fam)
	if err != nil {
		t.Fatal(err)
	}
	return socks
}

func ap(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

func TestParseProcNet(t *testing.T) {
	requireLittleEndian(t)
	tests := []struct {
		file   string
		proto  Proto
		family Family
		want   []Socket
	}{
		{
			file: "tcp", proto: TCP, family: IPv4,
			want: []Socket{
				{Local: ap("127.0.0.1:631"), Remote: ap("0.0.0.0:0"), State: StateListen, UID: 0, Inode: 24341},
				{Local: ap("0.0.0.0:22"), Remote: ap("0.0.0.0:0"), State: StateListen, RxQueue: 0x80, Inode: 3001},
				{Local: ap("192.168.1.230:50000"), Remote: ap("192.168.1.1:443"), State: StateEstablished, TxQueue: 0x10, UID: 1000, Inode: 98765},
				{Local: ap("127.0.0.1:8080"), Remote: ap("127.0.0.1:54321"), State: StateTimeWait, Inode: 0},
				// lines 4 (garbage) and 5 (bad hex) are skipped
			},
		},
		{
			file: "tcp6", proto: TCP, family: IPv6,
			want: []Socket{
				{Local: ap("[::1]:631"), Remote: ap("[::]:0"), State: StateListen, Inode: 24342},
				{Local: ap("[::]:80"), Remote: ap("[::]:0"), State: StateListen, UID: 33, Inode: 5001},
				{Local: ap("[::ffff:127.0.0.1]:3306"), Remote: ap("[::ffff:127.0.0.1]:45986"), State: StateEstablished, UID: 1000, Inode: 5002},
			},
		},
		{
			file: "udp", proto: UDP, family: IPv4,
			want: []Socket{
				{Local: ap("127.0.0.53:53"), Remote: ap("0.0.0.0:0"), State: StateClose, UID: 991, Inode: 16700},
				{Local: ap("0.0.0.0:5353"), Remote: ap("0.0.0.0:0"), State: StateClose, UID: 108, Inode: 7001},
				{Local: ap("192.168.1.230:40000"), Remote: ap("8.8.8.8:53"), State: StateEstablished, UID: 1000, Inode: 7002},
			},
		},
		{
			file: "udp6", proto: UDP, family: IPv6,
			want: []Socket{
				// first line has a 34-digit address and is skipped
				{Local: ap("[fe80::1]:546"), Remote: ap("[::]:0"), State: StateClose, Inode: 8002},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got := parseFixture(t, tt.file, tt.proto, tt.family)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d sockets, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, w := range tt.want {
				w.Proto, w.Family = tt.proto, tt.family
				if got[i] != w {
					t.Errorf("socket %d:\n got  %+v\n want %+v", i, got[i], w)
				}
			}
		})
	}
}

func TestParseProcNetEmpty(t *testing.T) {
	socks, err := ParseProcNet(strings.NewReader(""), TCP, IPv4)
	if err != nil || len(socks) != 0 {
		t.Fatalf("got %v, %v; want no sockets and no error", socks, err)
	}
}

func TestListening(t *testing.T) {
	tests := []struct {
		s    Socket
		want bool
	}{
		{Socket{Proto: TCP, State: StateListen}, true},
		{Socket{Proto: TCP, State: StateEstablished}, false},
		{Socket{Proto: TCP, State: StateClose}, false},
		{Socket{Proto: UDP, State: StateClose}, true},
		{Socket{Proto: UDP, State: StateEstablished}, false},
	}
	for _, tt := range tests {
		if got := tt.s.Listening(); got != tt.want {
			t.Errorf("%v %v: Listening() = %v, want %v", tt.s.Proto, tt.s.State, got, tt.want)
		}
	}
}

func TestFormatAddrPort(t *testing.T) {
	tests := map[string]string{
		"127.0.0.1:631":           "127.0.0.1:631",
		"0.0.0.0:0":               "0.0.0.0:*",
		"[::]:80":                 "[::]:80",
		"[::]:0":                  "[::]:*",
		"[::ffff:127.0.0.1]:3306": "[::ffff:127.0.0.1]:3306",
	}
	for in, want := range tests {
		if got := FormatAddrPort(ap(in)); got != want {
			t.Errorf("FormatAddrPort(%s) = %q, want %q", in, got, want)
		}
	}
	if got := FormatAddrPort(netip.AddrPort{}); got != "*:*" {
		t.Errorf("zero AddrPort = %q", got)
	}
}

func TestStateString(t *testing.T) {
	if StateClose.String() != "UNCONN" || StateListen.String() != "LISTEN" || StateTimeWait.String() != "TIME-WAIT" {
		t.Error("state names must follow ss(8)")
	}
	if State(0x42).String() != "STATE-42" {
		t.Errorf("unknown state: %q", State(0x42).String())
	}
}
