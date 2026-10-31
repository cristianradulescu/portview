package portsview

import (
	"strings"
	"testing"

	"github.com/cristianradulescu/portview/internal/sockets"
)

func TestDetailsExplainSocketAndProcess(t *testing.T) {
	a, g := newHeadless(t, 160, 60)
	snap := testSnapshot()
	snap.Privileged = true
	a.apply(snap, nil)
	selectPort(t, a, 80, sockets.IPv4)
	draw(t, g)
	details := strings.Join(strings.Fields(viewText(t, g, viewDetails)), " ") // ignore wrapping
	for _, want := range []string{
		"State LISTEN waiting for incoming connections",
		"Service http (registered for 80/tcp in /etc/services)",
		"Port Port 80 is below 1024 (net.ipv4.ip_unprivileged_port_start)",
		"PID 100 has it because it runs as root",
		"Resources 12.0 MiB memory · 1m CPU · 23 of 1024 open files",
		"Caps all (it runs as root)",
		"Ancestry systemd(1) → nginx(100)",
		"Children 1: 101 nginx",
		"Isolation none: the same namespaces as portview",
		"$ getent services 80/tcp",
		"$ pstree -sp 100",
		"$ getpcaps 100",
	} {
		if !strings.Contains(details, want) {
			t.Errorf("details missing %q:\n%s", want, details)
		}
	}
}
