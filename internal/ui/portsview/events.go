package portsview

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jesseduffield/gocui"

	"github.com/cristianradulescu/portview/internal/commands"
	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/sockets"
	"github.com/cristianradulescu/portview/internal/ui/tui"
)

// Tabs of the left panel.
const (
	tabSockets = iota
	tabEvents
)

// maxEvents caps the event history.
const maxEvents = 1000

// eventLog is the history of sockets opening and closing.
type eventLog struct {
	events []inventory.Event // oldest first
	seq    uint64
	list   tui.ListState // over the filtered rows, newest first
	// selSeq is the selected event; follow keeps the newest selected.
	selSeq uint64
	follow bool
	height int
	// since is when recording started, baseline how many sockets there
	// were then.
	since    time.Time
	baseline int
}

func newEventLog() eventLog { return eventLog{list: tui.NewListState(), follow: true, height: 1} }

// recordEvents adds the sockets that opened or closed between prev and
// snap.
func (a *App) recordEvents(prev, snap *inventory.Snapshot) {
	l := &a.ev
	if prev == nil {
		l.since, l.baseline = snap.Taken, len(snap.Entries)
		return
	}
	a.completeEvents(prev, snap)
	opened, closed := inventory.Diff(prev, snap)
	add := func(kind inventory.EventKind, es []inventory.Entry) {
		for _, e := range es {
			l.seq++
			ev := inventory.Event{Seq: l.seq, Time: snap.Taken, Kind: kind, Entry: e}
			if e.Listening() && e.Primary != nil {
				ev.Cmdline = a.opts.Cmdline(e.Primary.PID)
			}
			if ev.Cmdline == "" && kind == inventory.Closed {
				// The process may be gone: reuse what was read when the
				// socket opened.
				ev.Cmdline = a.recordedCmdline(e.Key())
			}
			l.events = append(l.events, ev)
		}
	}
	add(inventory.Closed, closed)
	add(inventory.Opened, opened)
	if over := len(l.events) - maxEvents; over > 0 {
		l.events = slices.Delete(l.events, 0, over)
	}
}

// completeEvents fills in the owner of sockets that opened in prev without
// a visible one: a socket created between the process scan and the read of
// the socket tables is seen before its owner, who shows up one refresh
// later.
func (a *App) completeEvents(prev, snap *inventory.Snapshot) {
	var byKey map[inventory.Key]*inventory.Entry
	for i := len(a.ev.events) - 1; i >= 0; i-- {
		ev := &a.ev.events[i]
		if !ev.Time.Equal(prev.Taken) {
			break // only the events of the previous refresh
		}
		if ev.Kind != inventory.Opened || ev.Entry.Primary != nil || ev.Entry.Inode == 0 {
			continue
		}
		if byKey == nil {
			byKey = make(map[inventory.Key]*inventory.Entry, len(snap.Entries))
			for j := range snap.Entries {
				byKey[snap.Entries[j].Key()] = &snap.Entries[j]
			}
		}
		if now := byKey[ev.Entry.Key()]; now != nil && now.Primary != nil {
			ev.Entry = *now
			if now.Listening() {
				ev.Cmdline = a.opts.Cmdline(now.Primary.PID)
			}
		}
	}
}

// recordedCmdline is the command line recorded with an earlier event of
// the same socket.
func (a *App) recordedCmdline(k inventory.Key) string {
	for i := len(a.ev.events) - 1; i >= 0; i-- {
		if ev := &a.ev.events[i]; ev.Cmdline != "" && ev.Entry.Key() == k {
			return ev.Cmdline
		}
	}
	return ""
}

// eventRows are the events matching the filter, newest first.
func (a *App) eventRows() []*inventory.Event {
	f := a.model.Filter()
	var out []*inventory.Event
	for i := len(a.ev.events) - 1; i >= 0; i-- {
		if ev := &a.ev.events[i]; f.Match(&ev.Entry) {
			out = append(out, ev)
		}
	}
	return out
}

// syncEventSelection keeps the selected event selected as rows change, or
// the newest one when following.
func (a *App) syncEventSelection(rows []*inventory.Event) {
	l := &a.ev
	if len(rows) == 0 {
		l.list.Clamp(0, l.height)
		return
	}
	idx := -1
	if !l.follow {
		idx = slices.IndexFunc(rows, func(ev *inventory.Event) bool { return ev.Seq == l.selSeq })
	}
	switch {
	case l.follow || l.selSeq == 0:
		l.list.Select(0, len(rows), l.height)
	case idx >= 0:
		l.list.Reanchor(idx, len(rows), l.height)
	default:
		l.list.Clamp(len(rows), l.height)
	}
	l.selSeq = rows[l.list.Selected].Seq
}

func (a *App) moveEvent(delta int) {
	rows := a.eventRows()
	a.syncEventSelection(rows)
	if len(rows) == 0 {
		return
	}
	a.ev.list.Move(delta, len(rows), a.ev.height)
	a.ev.selSeq = rows[a.ev.list.Selected].Seq
	a.ev.follow = a.ev.list.Selected == 0
}

// selectedEvent returns the selected event, or nil.
func (a *App) selectedEvent() *inventory.Event {
	rows := a.eventRows()
	a.syncEventSelection(rows)
	if a.ev.list.Selected < 0 {
		return nil
	}
	return rows[a.ev.list.Selected]
}

// switchTab shows the sockets or the events in the left panel.
func (a *App) switchTab(tab int) error {
	if a.tab == tab {
		return nil
	}
	focused := a.g.CurrentView() != nil && (a.g.CurrentView().Name() == viewList || a.g.CurrentView().Name() == viewEvents)
	a.tab = tab
	if err := a.layout(a.g); err != nil {
		return err
	}
	if focused {
		return a.focus(a.leftView())
	}
	return nil
}

func (a *App) leftView() string {
	if a.tab == tabEvents {
		return viewEvents
	}
	return viewList
}

// jumpToSocket shows the selected event's socket in the socket list, if
// it is still open.
func (a *App) jumpToSocket() error {
	ev := a.selectedEvent()
	if ev == nil {
		return nil
	}
	key := ev.Entry.Key()
	switch {
	case !a.model.Has(key):
		a.setFlash("that socket is closed now", tui.FgYellow)
		return nil
	case !a.model.SelectKey(key):
		a.setFlash("that socket is hidden by the current filter (its state changed)", tui.FgYellow)
		return nil
	}
	return a.switchTab(tabSockets)
}

func (a *App) tabTitles() []string {
	sockets := "Sockets"
	if a.model.Snapshot() != nil {
		sockets = fmt.Sprintf("Sockets %d/%d", len(a.model.Rows()), a.model.Total())
	}
	return []string{sockets, "Events " + strconv.Itoa(len(a.eventRows()))}
}

// renderEvents draws the events tab.
func (a *App) renderEvents(v *gocui.View) {
	w, h := v.InnerSize()
	a.ev.height = max(1, h-1)
	v.HighlightInactive = a.g.CurrentView() != v
	v.Tabs, v.TabIndex = a.tabTitles(), tabEvents
	rows := a.eventRows()
	a.syncEventSelection(rows)
	v.Clear()
	v.SetOrigin(0, 0)
	snap := a.model.Snapshot()
	if snap == nil {
		v.WriteString(tui.Dim("collecting sockets…"))
		v.Highlight = false
		return
	}
	const prefixW = 8 + 1 + 6 + 1 // "15:04:05 opened "
	entries := make([]inventory.Entry, len(rows))
	for i, ev := range rows {
		entries[i] = ev.Entry
	}
	cw := widths(entries, snap.Privileged, w-prefixW)
	var sb strings.Builder
	sb.WriteString(tui.Color(tui.PadRight("TIME", 8)+" "+tui.PadRight("EVENT", 6)+" ", "1", "4") + headerLine(cw))
	if len(rows) == 0 {
		sb.WriteString("\n" + tui.Dim(fmt.Sprintf("No socket matching the filter has opened or closed since %s (%d sockets then).",
			a.ev.since.Format("15:04:05"), a.ev.baseline)))
		sb.WriteString("\n" + tui.Dim("Events are found by comparing refreshes: a socket that opens and closes between two is not seen."))
	}
	l := a.ev.list
	end := min(len(rows), l.Origin+a.ev.height)
	for i := l.Origin; i < end; i++ {
		ev := rows[i]
		kind := tui.Color(tui.PadRight(ev.Kind.String(), 6), tui.FgGreen)
		if ev.Kind == inventory.Closed {
			kind = tui.Color(tui.PadRight(ev.Kind.String(), 6), tui.FgRed)
		}
		sb.WriteString("\n" + ev.Time.Format("15:04:05") + " " + kind + " " + rowLine(&ev.Entry, cw, snap.Privileged, false))
	}
	v.WriteString(sb.String())
	v.Highlight = l.Selected >= 0
	if l.Selected >= 0 {
		v.SetCursor(0, 1+l.Selected-l.Origin)
	} else {
		v.SetCursor(0, 0)
	}
}

// renderEventDetails describes the selected event.
func (a *App) renderEventDetails(width int) string {
	ev := a.selectedEvent()
	if ev == nil {
		return tui.Dim(fmt.Sprintf("Sockets that open or close after %s show up here, newest first.", a.ev.since.Format("15:04:05")))
	}
	e := &ev.Entry
	var sb strings.Builder
	const labelW = 9
	indent := strings.Repeat(" ", 2+labelW+1)
	kv := func(k, v string) {
		lines := []string{v}
		if !strings.Contains(v, "\x1b") {
			lines = tui.WrapHanging(v, width-len(indent), "")
		}
		fmt.Fprintf(&sb, "  %s %s\n", tui.Dim(tui.PadRight(k, labelW)), lines[0])
		for _, l := range lines[1:] {
			sb.WriteString(indent + l + "\n")
		}
	}
	color := tui.FgGreen
	if ev.Kind == inventory.Closed {
		color = tui.FgRed
	}
	sb.WriteString(tui.Bold("Event") + "\n")
	kv("What", tui.Color(ev.Kind.String(), color)+fmt.Sprintf(" at %s · %s ago", ev.Time.Format("15:04:05"), humanDuration(time.Since(ev.Time))))
	endpoint := e.Netid() + " " + sockets.FormatAddrPort(e.Local)
	if !e.Listening() {
		endpoint += " → " + sockets.FormatAddrPort(e.Remote)
	}
	kv("Socket", endpoint+" "+e.State.String())
	snap := a.model.Snapshot()
	priv := snap != nil && snap.Privileged
	if e.Primary != nil {
		kv("Process", fmt.Sprintf("PID %s %s · user %s", e.PIDLabel(priv), e.ProcessLabel(priv), e.User))
	} else {
		kv("Process", "PID "+e.PIDLabel(priv)+" · user "+e.User)
	}
	if ev.Cmdline != "" {
		kv("Command", ev.Cmdline)
	}
	if c := e.ContainerLabel(); c != "" {
		kv("Container", c)
	}
	if snap != nil {
		kv("Netns", snap.NetNSLabel(e.NetNS))
	}
	switch {
	case a.model.Has(e.Key()):
		kv("Now", "still open: Enter shows it in the socket list")
	case ev.Kind == inventory.Opened:
		kv("Now", "closed since")
	default:
		kv("Now", "closed")
	}
	sb.WriteString("\n" + tui.Dim("Events come from comparing refreshes, so a socket that opens and closes within one is not seen.") + "\n")
	return sb.String()
}

// eventCommands are the equivalents for the events tab: ss keeps no
// history, so the closest commands watch for changes.
func eventCommands(f inventory.Filter, env commands.Env) []commands.Command {
	view := commands.View(f, env)
	return []commands.Command{
		{Comment: "ss keeps no history; watch reruns it every 2 seconds and highlights what changed", Cmd: "watch -d -n 2 " + commands.Quote(view.Cmd)},
		{Comment: "the kernel reports TCP sockets as they are destroyed (sock_diag events; needs root)", Cmd: "sudo ss -E -tn"},
	}
}
