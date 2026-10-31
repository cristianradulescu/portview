package portsview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jesseduffield/gocui"

	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

type staticCollector struct{ snap *inventory.Snapshot }

func (c staticCollector) Collect(context.Context) (*inventory.Snapshot, error) { return c.snap, nil }

func testSnapshot() *inventory.Snapshot {
	nginx := &procs.Process{PID: 100, PPID: 1, Comm: "nginx", Loaded: true, Inspectable: true}
	worker := &procs.Process{PID: 101, PPID: 100, Comm: "nginx", Loaded: true, Inspectable: true}
	web := listen(sockets.TCP, 80)
	web.Owners, web.Primary = []*procs.Process{nginx, worker}, nginx
	dns := listen(sockets.UDP, 53)
	dns.Exposure = inventory.ExposureLoopback
	hidden := listen(sockets.TCP, 631)
	snap := snapshot(web, hidden, dns)
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
	a, err := newApp(g, Options{Collector: staticCollector{testSnapshot()}, Interval: time.Hour})
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
		"Sockets 3/3", "PROTO", "EXPOSURE", "0.0.0.0:80", "100+1", "nginx", "0.0.0.0:631", "udp",
		"Details", "primary", // details pane for the selected (first) row
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
	if !strings.Contains(out, "Sockets 2/3") || strings.Contains(out, "0.0.0.0:53 ") {
		t.Errorf("after t (TCP only):\n%s", out)
	}
	_ = press(t, a, viewList, 't')
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
	if !strings.Contains(out, "Sockets 1/3") || !strings.Contains(out, "/nginx") {
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
