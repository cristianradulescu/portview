package portsview

import (
	"strings"
	"testing"

	"github.com/jesseduffield/gocui"

	"github.com/cristianradulescu/portview/internal/explain"
	"github.com/cristianradulescu/portview/internal/sockets"
)

func TestInspectPopup(t *testing.T) {
	a, g := newKillApp(t, &fakeRunner{})
	var gotPath string
	line := "   0: 00000000:0050 00000000:0000 0A 00000000:00000002 00:00000000 00000000     0        0 801 1 0000000000000000 100 0 0 10 0"
	a.opts.ReadRaw = func(path string, s sockets.Socket) (*explain.Report, error) {
		gotPath = path
		fs, err := explain.Fields(line, s.Proto, s.Family, explain.Options{ClockTicks: 100})
		if err != nil {
			return nil, err
		}
		return &explain.Report{Path: path, LineNo: 2, Header: "  sl  local_address rem_address   st", Line: line, Fields: fs}, nil
	}
	selectPort(t, a, 80, sockets.IPv4)
	if err := press(t, a, viewList, 'i'); err != nil {
		t.Fatal(err)
	}
	out := draw(t, g)
	if currentView(g) != viewInspect {
		t.Fatalf("inspect popup not focused:\n%s", out)
	}
	for _, want := range []string{
		"Kernel view", "tcp 0.0.0.0:80 LISTEN", "line 2 of /proc/net/tcp", "00000000:0050",
		"local_address", "0.0.0.0:80, i.e. any address", "0x0A = LISTEN",
		"2 connection(s) past the handshake",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("popup missing %q:\n%s", want, out)
		}
	}
	if gotPath != "/proc/net/tcp" {
		t.Errorf("read %q, want /proc/net/tcp", gotPath)
	}
	if log := logged(a); !strings.Contains(log, `awk 'NR == 1 || $2 == "00000000:0050"' /proc/net/tcp  # raw kernel line of tcp 0.0.0.0:80`) {
		t.Errorf("read not logged:\n%s", log)
	}
	// Scrolling reaches the equivalent command at the end.
	for range 60 {
		_ = press(t, a, viewInspect, 'j')
	}
	if out := draw(t, g); !strings.Contains(out, "$ awk") {
		t.Errorf("end of the popup:\n%s", out)
	}
	_ = press(t, a, viewInspect, gocui.KeyEsc)
	draw(t, g)
	if currentView(g) != viewList {
		t.Error("Esc should close the popup")
	}

	// A socket gone since the refresh.
	a.opts.ReadRaw = func(string, sockets.Socket) (*explain.Report, error) { return nil, explain.ErrNotFound }
	_ = press(t, a, viewList, 'i')
	if out := draw(t, g); !strings.Contains(out, "no longer in the kernel's table") {
		t.Errorf("gone socket:\n%s", out)
	}
}
