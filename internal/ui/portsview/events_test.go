package portsview

import (
	"strings"
	"testing"
	"time"

	"github.com/jesseduffield/gocui"

	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

// eventsApp starts with the test snapshot, then 631 closes and 8080 opens.
func eventsApp(t *testing.T) (*App, *gocui.Gui) {
	t.Helper()
	a, g := newHeadless(t, 170, 40)
	a.opts.Cmdline = func(pid int) string { return "python3 -m http.server 8080" }
	first := testSnapshot()
	a.apply(first, nil)
	draw(t, g)

	second := testSnapshot()
	second.Taken = first.Taken.Add(2 * time.Second)
	var kept []inventory.Entry
	for _, e := range second.Entries {
		if e.Local.Port() != 631 {
			kept = append(kept, e)
		}
	}
	srv := listen(sockets.TCP, 8080)
	py := &procs.Process{PID: 4242, Comm: "python3", UID: 1000, Loaded: true, Inspectable: true}
	srv.Owners, srv.Primary, srv.User = []*procs.Process{py}, py, "alice"
	second.Entries = append(kept, srv)
	a.apply(second, nil)
	draw(t, g)
	return a, g
}

func TestEventsTab(t *testing.T) {
	a, g := eventsApp(t)
	if out := draw(t, g); !strings.Contains(out, "Sockets 4/4 - Events 2") {
		t.Fatalf("tab titles:\n%s", out)
	}
	if err := press(t, a, viewList, ']'); err != nil {
		t.Fatal(err)
	}
	out := draw(t, g)
	if currentView(g) != viewEvents {
		t.Fatalf("focus = %q, want the events view", currentView(g))
	}
	for _, want := range []string{"TIME", "EVENT", "12:00:02 opened", "0.0.0.0:8080", "12:00:02 closed", "0.0.0.0:631"} {
		if !strings.Contains(out, want) {
			t.Errorf("events view missing %q:\n%s", want, out)
		}
	}
	// Newest first: opened events are recorded after closed ones.
	if strings.Index(out, "opened") > strings.Index(out, "closed") {
		t.Errorf("not newest first:\n%s", out)
	}
	details := strings.Join(strings.Fields(viewText(t, g, viewDetails)), " ")
	for _, want := range []string{"What opened at 12:00:02", "PID 4242 python3", "user alice",
		"Command python3 -m http.server 8080", "still open: Enter shows it", "$ watch -d -n 2 'ss -tulnp'", "$ sudo ss -E -tn"} {
		if !strings.Contains(details, want) {
			t.Errorf("event details missing %q:\n%s", want, details)
		}
	}

	// Enter jumps to the still-open socket.
	_ = press(t, a, viewEvents, gocui.KeyEnter)
	draw(t, g)
	if currentView(g) != viewList || a.tab != tabSockets {
		t.Fatal("Enter should switch to the socket list")
	}
	if e := a.model.Selected(); e == nil || e.Local.Port() != 8080 {
		t.Errorf("selected %+v, want :8080", e)
	}

	// The closed socket cannot be shown.
	_ = press(t, a, viewList, '[')
	_ = press(t, a, viewEvents, 'j')
	_ = press(t, a, viewEvents, gocui.KeyEnter)
	if out := draw(t, g); !strings.Contains(out, "that socket is closed now") || a.tab != tabEvents {
		t.Errorf("closed socket:\n%s", out)
	}

	// Filters apply to events too.
	_ = press(t, a, viewEvents, 'e')
	if out := draw(t, g); strings.Contains(out, "0.0.0.0:8080") || !strings.Contains(out, "No socket matching the filter") {
		t.Errorf("established filter should hide listener events:\n%s", out)
	}
	_ = press(t, a, viewEvents, 'l')

	// A popup closed from the events tab gives the focus back to it.
	_ = press(t, a, viewEvents, '?')
	draw(t, g)
	_ = press(t, a, viewHelp, gocui.KeyEsc)
	draw(t, g)
	if currentView(g) != viewEvents {
		t.Errorf("focus after help = %q, want events", currentView(g))
	}
}

func TestEventsSelectionFollowsOrStays(t *testing.T) {
	a, g := eventsApp(t)
	_ = press(t, a, viewList, ']')
	draw(t, g)
	add := func(port uint16) {
		snap := a.model.Snapshot()
		next := *snap
		next.Taken = snap.Taken.Add(2 * time.Second)
		next.Entries = append(append([]inventory.Entry{}, snap.Entries...), listen(sockets.TCP, port))
		a.apply(&next, nil)
		draw(t, g)
	}
	// Following: the newest event stays selected.
	add(9001)
	if ev := a.selectedEvent(); ev == nil || ev.Entry.Local.Port() != 9001 {
		t.Errorf("following: selected %+v", ev)
	}
	// Moved down: the same event stays selected as new ones arrive.
	_ = press(t, a, viewEvents, 'j')
	before := a.selectedEvent().Seq
	add(9002)
	if ev := a.selectedEvent(); ev == nil || ev.Seq != before {
		t.Errorf("selection moved to %+v", ev)
	}
	// g goes back to following.
	_ = press(t, a, viewEvents, 'g')
	add(9003)
	if ev := a.selectedEvent(); ev == nil || ev.Entry.Local.Port() != 9003 {
		t.Errorf("after g: selected %+v", ev)
	}
}

func TestEventsCompleteLateOwnerAndKeepCmdline(t *testing.T) {
	a, g := newHeadless(t, 170, 40)
	alive := true
	a.opts.Cmdline = func(pid int) string {
		if !alive {
			return ""
		}
		return "srv --port 7000"
	}
	base := testSnapshot()
	a.apply(base, nil)
	step := func(mutate func(*inventory.Snapshot)) {
		snap := *a.model.Snapshot()
		snap.Taken = snap.Taken.Add(2 * time.Second)
		snap.Entries = append([]inventory.Entry{}, snap.Entries...)
		mutate(&snap)
		a.apply(&snap, nil)
		draw(t, g)
	}
	// 1. The socket appears before its owner is seen.
	srv := listen(sockets.TCP, 7000)
	step(func(s *inventory.Snapshot) { s.Entries = append(s.Entries, srv) })
	// 2. One refresh later the owner is known.
	owner := &procs.Process{PID: 7000, Comm: "srv", Loaded: true, Inspectable: true}
	step(func(s *inventory.Snapshot) {
		for i := range s.Entries {
			if s.Entries[i].Local.Port() == 7000 {
				s.Entries[i].Owners, s.Entries[i].Primary = []*procs.Process{owner}, owner
			}
		}
	})
	opened := a.ev.events[len(a.ev.events)-1]
	if opened.Entry.Primary == nil || opened.Entry.Primary.PID != 7000 || opened.Cmdline != "srv --port 7000" {
		t.Errorf("opened event not completed: %+v, cmdline %q", opened.Entry.Primary, opened.Cmdline)
	}
	// 3. The process exits: the closed event keeps the command line.
	alive = false
	step(func(s *inventory.Snapshot) {
		var kept []inventory.Entry
		for _, e := range s.Entries {
			if e.Local.Port() != 7000 {
				kept = append(kept, e)
			}
		}
		s.Entries = kept
	})
	closed := a.ev.events[len(a.ev.events)-1]
	if closed.Kind != inventory.Closed || closed.Cmdline != "srv --port 7000" {
		t.Errorf("closed event = %+v", closed)
	}
}
