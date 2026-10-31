package portsview

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jesseduffield/gocui"

	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

type testUsers struct{}

func (testUsers) Name(uid int) string {
	switch uid {
	case 0:
		return "root"
	case 33:
		return "www-data"
	}
	return "?"
}

func fakeDetails(pid int) (*procs.Details, error) {
	if pid != 100 {
		return nil, procs.ErrExited
	}
	unit, _ := procs.ParseCgroupPath("/system.slice/nginx.service")
	return &procs.Details{
		PID: 100, Comm: "nginx", PPID: 1, ParentComm: "systemd", UID: 0, Threads: 1,
		Cmdline: []string{"nginx: master process /usr/sbin/nginx", "-g", "daemon on;"},
		Exe:     "/usr/sbin/nginx", Cwd: "/", State: "S (sleeping)",
		StartTime: time.Now().Add(-3*time.Hour - 12*time.Minute - 30*time.Second),
		Unit:      unit,
	}, nil
}

type staticCollector struct{ snap *inventory.Snapshot }

func (c staticCollector) Collect(context.Context) (*inventory.Snapshot, error) { return c.snap, nil }

func testSnapshot() *inventory.Snapshot {
	nginx := &procs.Process{PID: 100, PPID: 1, Comm: "nginx", Loaded: true, Inspectable: true}
	worker := &procs.Process{PID: 101, PPID: 100, Comm: "nginx", UID: 33, Loaded: true, Inspectable: true}
	web := listen(sockets.TCP, 80)
	web.Owners, web.Primary = []*procs.Process{nginx, worker}, nginx
	web6 := listen(sockets.TCP, 80)
	web6.Family, web6.Inode = sockets.IPv6, 9999
	web6.Local = netip.MustParseAddrPort("[::]:80")
	web6.Owners, web6.Primary = []*procs.Process{nginx, worker}, nginx
	dns := listen(sockets.UDP, 53)
	dns.Exposure = inventory.ExposureLoopback
	hidden := listen(sockets.TCP, 631)
	snap := snapshot(web, web6, hidden, dns)
	snap.Hidden = 1
	snap.Taken = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	return snap
}

func newHeadless(t *testing.T, w, h int) (*App, *gocui.Gui) {
	t.Helper()
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: w, Height: h, OutputMode: gocui.OutputTrue})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	a, err := newApp(g, Options{
		Collector: staticCollector{testSnapshot()},
		Interval:  time.Hour,
		Details:   fakeDetails,
		Users:     testUsers{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, g
}

func draw(t *testing.T, g *gocui.Gui) string {
	t.Helper()
	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatal(err)
	}
	return g.Snapshot()
}

// press invokes the handler bound to key on view, as gocui would.
func press(t *testing.T, a *App, view string, key any) error {
	t.Helper()
	for _, b := range a.keys.Bindings() {
		if b.View != view && b.View != "" {
			continue
		}
		for _, k := range b.Keys {
			if k == key {
				return b.Handler()
			}
		}
	}
	t.Fatalf("no binding for %v on %q", key, view)
	return nil
}

func TestAppRendersAndFilters(t *testing.T) {
	a, g := newHeadless(t, 160, 20)
	if out := draw(t, g); !strings.Contains(out, "collecting sockets") {
		t.Errorf("before first refresh:\n%s", out)
	}

	a.model.SetFilter(inventory.Filter{State: inventory.StateListening})
	a.apply(testSnapshot(), nil)
	out := draw(t, g)
	for _, want := range []string{
		"Sockets 4/4", "PROTO", "EXPOSURE", "0.0.0.0:80", "100+1", "nginx", "0.0.0.0:631", "udp",
		"Details",
		"listening", "1 owners hidden", "q quit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("screen missing %q:\n%s", want, out)
		}
	}

	if err := press(t, a, viewList, 't'); err != nil {
		t.Fatal(err)
	}
	out = draw(t, g)
	if !strings.Contains(out, "Sockets 3/4") || strings.Contains(out, "0.0.0.0:53 ") {
		t.Errorf("after t (TCP only):\n%s", out)
	}
	_ = press(t, a, viewList, 't')
	_ = press(t, a, viewList, 'j')
	_ = press(t, a, viewList, 'j')
	if e := a.model.Selected(); e == nil || e.Local.Port() != 631 {
		t.Errorf("j should select the second row, got %+v", e)
	}
	out = draw(t, g)
	if !strings.Contains(out, "Rerun with sudo") {
		t.Errorf("details for a hidden owner should suggest sudo:\n%s", out)
	}
}

func TestAppFilterPromptAndHelp(t *testing.T) {
	a, g := newHeadless(t, 160, 50)
	a.apply(testSnapshot(), nil)
	draw(t, g)

	_ = press(t, a, viewList, '/')
	draw(t, g)
	if v := g.CurrentView(); v == nil || v.Name() != viewFilter {
		t.Fatalf("prompt should be focused, current = %v", v)
	}
	a.prompt.OnChange("nginx") // what the editor calls on typing
	out := draw(t, g)
	if !strings.Contains(out, "Sockets 2/4") || !strings.Contains(out, "/nginx") {
		t.Errorf("live filter:\n%s", out)
	}
	_ = press(t, a, viewFilter, gocui.KeyEnter)
	draw(t, g)
	if v := g.CurrentView(); v == nil || v.Name() != viewList {
		t.Fatalf("Enter should return to the list")
	}
	if a.model.Filter().Text != "nginx" {
		t.Error("Enter must keep the filter")
	}
	_ = press(t, a, viewList, gocui.KeyEsc)
	if a.model.Filter().Text != "" {
		t.Error("Esc on the list clears the text filter")
	}

	_ = press(t, a, viewList, '?')
	out = draw(t, g)
	if !strings.Contains(out, "Help") || !strings.Contains(out, "IPv4 only") || !strings.Contains(out, "loopback only") {
		t.Errorf("help popup:\n%s", out)
	}
	_ = press(t, a, viewHelp, gocui.KeyEsc)
	draw(t, g)
	if v := g.CurrentView(); v == nil || v.Name() != viewList {
		t.Fatal("closing help should focus the list")
	}

	if err := press(t, a, viewList, 'q'); !errors.Is(err, gocui.ErrQuit) {
		t.Errorf("q = %v, want ErrQuit", err)
	}
}

func TestAppNarrowHidesDetails(t *testing.T) {
	a, g := newHeadless(t, 90, 20)
	a.apply(testSnapshot(), nil)
	if out := draw(t, g); strings.Contains(out, "Details") {
		t.Errorf("details pane should be hidden at 90 columns:\n%s", out)
	}
}

func TestAppKeepsDataOnRefreshError(t *testing.T) {
	a, g := newHeadless(t, 160, 20)
	a.apply(testSnapshot(), nil)
	a.apply(nil, errors.New("boom"))
	out := draw(t, g)
	if !strings.Contains(out, "refresh failed: boom") || !strings.Contains(out, "0.0.0.0:80") {
		t.Errorf("error state:\n%s", out)
	}
}

func TestAppDetailsPane(t *testing.T) {
	a, g := newHeadless(t, 170, 50)
	a.apply(testSnapshot(), nil)
	out := draw(t, g)
	for _, want := range []string{
		"Process nginx · PID 100",
		"'nginx: master process /usr/sbin/nginx' -g 'daemon", "             on;'",
		"/usr/sbin/nginx", "root (uid 0)", "3h12m ago", "1 systemd",
		"nginx.service (systemd service)",
		"Shared by 2 processes", "www-data", "primary",
		"Other sockets of PID 100 (1)", "[::]:80",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("details missing %q:\n%s", want, out)
		}
	}

	// Details are read once per snapshot, not on every redraw.
	calls := 0
	a.opts.Details = func(pid int) (*procs.Details, error) { calls++; return fakeDetails(pid) }
	clear(a.details)
	draw(t, g)
	draw(t, g)
	if calls != 1 {
		t.Errorf("details read %d times, want 1", calls)
	}
	a.apply(testSnapshot(), nil)
	draw(t, g)
	if calls != 2 {
		t.Errorf("details should be re-read after a refresh, calls = %d", calls)
	}

	// Tab focuses the pane, j scrolls it, Esc goes back.
	_ = press(t, a, viewList, gocui.KeyTab)
	draw(t, g)
	if v := g.CurrentView(); v == nil || v.Name() != viewDetails {
		t.Fatalf("Tab should focus details, got %v", v)
	}
	a.detailsMax = 5
	_ = press(t, a, viewDetails, 'j')
	if a.detailsOrigin != 1 {
		t.Errorf("detailsOrigin = %d", a.detailsOrigin)
	}
	_ = press(t, a, viewDetails, gocui.KeyEsc)
	draw(t, g)
	if v := g.CurrentView(); v == nil || v.Name() != viewList {
		t.Fatalf("Esc should return to the list")
	}
	// Selecting another socket resets the scroll.
	_ = press(t, a, viewList, 'j')
	draw(t, g)
	if a.detailsOrigin != 0 {
		t.Errorf("scroll not reset on selection change: %d", a.detailsOrigin)
	}
}

func TestAppDetailsExitedProcess(t *testing.T) {
	a, g := newHeadless(t, 170, 40)
	a.opts.Details = func(int) (*procs.Details, error) { return nil, procs.ErrExited }
	a.apply(testSnapshot(), nil)
	if out := draw(t, g); !strings.Contains(out, "process exited") {
		t.Errorf("exited process:\n%s", out)
	}
}
