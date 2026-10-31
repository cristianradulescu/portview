package explain

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/cristianradulescu/portview/internal/hostinfo"
	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

func TestPortReason(t *testing.T) {
	host := hostinfo.New(1024, 32768, 60999, nil)
	mk := func(state sockets.State, local, remote string, hostNS bool) *inventory.Entry {
		e := &inventory.Entry{Socket: sockets.Socket{Proto: sockets.TCP, Family: sockets.IPv4, State: state,
			Local: netip.MustParseAddrPort(local), Remote: netip.MustParseAddrPort(remote)}, HostNetNS: hostNS}
		return e
	}
	root := &procs.Details{PID: 812, UID: 0, Insight: procs.Insight{CapsKnown: true, CapEff: 0x1ffffffffff}}
	granted := &procs.Details{PID: 900, UID: 1000, Insight: procs.Insight{CapsKnown: true, CapEff: 1 << procs.CapNetBindService}}
	dropped := &procs.Details{PID: 813, UID: 33, Insight: procs.Insight{CapsKnown: true}}
	listen80 := mk(sockets.StateListen, "0.0.0.0:80", "0.0.0.0:0", true)
	tests := []struct {
		name string
		in   PortInput
		want []string
	}{
		{"root", PortInput{Entry: listen80, Proc: root, Host: host}, []string{"below 1024", "CAP_NET_BIND_SERVICE", "runs as root"}},
		{"granted", PortInput{Entry: listen80, Proc: granted, Host: host}, []string{"without being root", "AmbientCapabilities"}},
		{"dropped", PortInput{Entry: listen80, Proc: dropped, Host: host}, []string{"does not have it now", "dropped"}},
		{"hidden owner", PortInput{Entry: listen80, Host: host}, []string{"needs the CAP_NET_BIND_SERVICE"}},
		{"high port", PortInput{Entry: mk(sockets.StateListen, "0.0.0.0:8080", "0.0.0.0:0", true), Host: host}, []string{"any user"}},
		{"bind 0", PortInput{Entry: mk(sockets.StateListen, "127.0.0.1:41234", "0.0.0.0:0", true), Host: host}, []string{"ephemeral range (32768–60999", "bind to port 0"}},
		{"container", PortInput{Entry: mk(sockets.StateListen, "0.0.0.0:80", "0.0.0.0:0", false), Proc: dropped, Host: host}, []string{"own network namespace", "Docker"}},
		{"client", PortInput{Entry: mk(sockets.StateEstablished, "192.168.1.5:51234", "140.82.121.4:443", true), Host: host}, []string{"kernel picked 51234"}},
		{"server", PortInput{Entry: mk(sockets.StateEstablished, "192.168.1.5:22", "192.168.1.9:51234", true), Host: host, Listener: true}, []string{"server side", "port 22"}},
	}
	for _, tt := range tests {
		got := PortReason(tt.in)
		for _, w := range tt.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", tt.name, got, w)
			}
		}
	}
	if got := PortReason(PortInput{Entry: mk(sockets.StateEstablished, "192.168.1.5:5000", "1.2.3.4:443", true), Host: host}); got != "" {
		t.Errorf("unexplained connection: %q", got)
	}
	if got := PortReason(PortInput{Entry: listen80}); got != "" {
		t.Errorf("no host info: %q", got)
	}
}

func TestStateHelp(t *testing.T) {
	if !strings.Contains(StateHelp(sockets.StateCloseWait, sockets.TCP), "close()") ||
		!strings.Contains(StateHelp(sockets.StateClose, sockets.UDP), "UDP server") ||
		!strings.Contains(StateHelp(sockets.StateTimeWait, sockets.TCP), "Normal") {
		t.Error("StateHelp")
	}
}
