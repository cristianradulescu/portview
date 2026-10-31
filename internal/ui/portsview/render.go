package portsview

import (
	"fmt"
	"strings"
	"time"

	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/sockets"
	"github.com/cristianradulescu/portview/internal/ui/tui"
)

type column struct {
	title    string
	min, max int
	// shrink is the width the column may be squeezed to when the panel is
	// too narrow; 0 means it is never squeezed.
	shrink int
	// middle truncates in the middle instead of at the end (addresses: keep
	// the port visible).
	middle bool
}

const (
	colProto = iota
	colState
	colLocal
	colRemote
	colExposure
	colPID
	colProcess
	colUser
	numCols
)

var columns = [numCols]column{
	colProto:    {title: "PROTO", min: 5, max: 5},
	colState:    {title: "STATE", min: 5, max: 10},
	colLocal:    {"LOCAL", 5, 47, 21, true},
	colRemote:   {"REMOTE", 6, 47, 15, true},
	colExposure: {"EXPOSURE", 8, 8, 0, false},
	colPID:      {"PID", 3, 12, 0, false},
	colProcess:  {"PROCESS", 7, 16, 8, false},
	colUser:     {"USER", 4, 16, 8, false},
}

// shrinkGroups give up space in order; within a group the widest column
// shrinks first, so the two address columns end up balanced.
var shrinkGroups = [][]int{{colLocal, colRemote}, {colUser, colProcess}}

// cells returns the plain text of each column for e.
func cells(e *inventory.Entry, privileged bool) [numCols]string {
	return [numCols]string{
		colProto:    e.Netid(),
		colState:    e.State.String(),
		colLocal:    sockets.FormatAddrPort(e.Local),
		colRemote:   sockets.FormatAddrPort(e.Remote),
		colExposure: exposureLabel(e.Exposure),
		colPID:      e.PIDLabel(privileged),
		colProcess:  e.ProcessLabel(privileged),
		colUser:     e.User,
	}
}

func exposureLabel(x inventory.Exposure) string {
	if x == inventory.ExposureNone {
		return ""
	}
	return x.String()
}

// exposureColor maps exposure to a color: red for every interface (the one
// to look at), yellow for one interface, green for loopback, cyan for
// another namespace.
func exposureColor(x inventory.Exposure) string {
	switch x {
	case inventory.ExposureAll:
		return tui.FgRed
	case inventory.ExposureInterface:
		return tui.FgYellow
	case inventory.ExposureLoopback:
		return tui.FgGreen
	case inventory.ExposureNamespace:
		return tui.FgCyan
	}
	return ""
}

// widths computes column widths from the rows (all filtered rows, not just
// the visible ones, so widths do not change while scrolling), squeezed to
// fit avail cells when possible.
func widths(rows []inventory.Entry, privileged bool, avail int) [numCols]int {
	var w [numCols]int
	for i, c := range columns {
		w[i] = max(c.min, tui.StringWidth(c.title))
	}
	for i := range rows {
		cs := cells(&rows[i], privileged)
		for j, s := range cs {
			w[j] = max(w[j], min(tui.StringWidth(s), columns[j].max))
		}
	}
	total := numCols - 1 // separators
	for _, x := range w {
		total += x
	}
	excess := total - avail
	for _, group := range shrinkGroups {
		for excess > 0 {
			widest := -1
			for _, c := range group {
				floor := max(columns[c].shrink, tui.StringWidth(columns[c].title))
				if w[c] > floor && (widest < 0 || w[c] > w[widest]) {
					widest = c
				}
			}
			if widest < 0 {
				break // group cannot shrink further
			}
			w[widest]--
			excess--
		}
	}
	return w
}

func headerLine(w [numCols]int) string {
	parts := make([]string, numCols)
	for i, c := range columns {
		parts[i] = tui.Fit(c.title, w[i])
	}
	return tui.Color(strings.TrimRight(strings.Join(parts, " "), " "), "1", "4")
}

func rowLine(e *inventory.Entry, w [numCols]int, privileged, fresh bool) string {
	cs := cells(e, privileged)
	parts := make([]string, numCols)
	for i, s := range cs {
		var codes []string
		switch i {
		case colLocal, colExposure:
			if c := exposureColor(e.Exposure); c != "" {
				codes = append(codes, c)
			}
		case colState:
			switch e.State {
			case sockets.StateEstablished:
				codes = append(codes, tui.FgBlue)
			case sockets.StateListen, sockets.StateClose:
			default:
				codes = append(codes, tui.FgGray)
			}
		case colPID, colProcess:
			if s == "?" || s == "-" {
				codes = append(codes, tui.FgGray)
			}
		}
		if fresh {
			codes = append(codes, "1")
		}
		if columns[i].middle {
			s = tui.TruncateMiddle(s, w[i])
		}
		parts[i] = tui.Color(tui.Fit(s, w[i]), codes...)
	}
	return strings.Join(parts, " ")
}

// renderList returns the list view content: a header line followed by the
// visible window of rows.
func renderList(m *Model, width int) string {
	snap := m.Snapshot()
	if snap == nil {
		return tui.Dim("collecting sockets…")
	}
	rows := m.Rows()
	w := widths(rows, snap.Privileged, width)
	var sb strings.Builder
	sb.WriteString(headerLine(w))
	if len(rows) == 0 {
		sb.WriteString("\n" + tui.Dim("no sockets match the current filter"))
		return sb.String()
	}
	l := m.List()
	end := min(len(rows), l.Origin+m.Height())
	for i := l.Origin; i < end; i++ {
		sb.WriteString("\n")
		sb.WriteString(rowLine(&rows[i], w, snap.Privileged, m.IsFresh(&rows[i])))
	}
	return sb.String()
}

func exposureDescription(x inventory.Exposure) string {
	switch x {
	case inventory.ExposureAll:
		return "all interfaces (reachable from the network unless firewalled)"
	case inventory.ExposureInterface:
		return "one interface address"
	case inventory.ExposureLoopback:
		return "loopback only (this machine)"
	case inventory.ExposureNamespace:
		return "inside another network namespace"
	default:
		return "n/a (not listening)"
	}
}

// renderDetails is the details pane content for the selected entry.
func renderDetails(m *Model) string {
	e := m.Selected()
	snap := m.Snapshot()
	if e == nil || snap == nil {
		return tui.Dim("nothing selected")
	}
	var sb strings.Builder
	kv := func(k, v string) { fmt.Fprintf(&sb, "  %s %s\n", tui.Dim(tui.PadRight(k, 10)), v) }

	sb.WriteString(tui.Bold("Socket") + "\n")
	kv("Protocol", e.Netid())
	kv("State", e.State.String())
	kv("Local", tui.Color(sockets.FormatAddrPort(e.Local), exposureColor(e.Exposure)))
	kv("Remote", sockets.FormatAddrPort(e.Remote))
	kv("Exposure", tui.Color(exposureDescription(e.Exposure), exposureColor(e.Exposure)))
	kv("Netns", snap.NetNSLabel(e.NetNS))
	if e.Inode != 0 {
		kv("Inode", fmt.Sprint(e.Inode))
	}
	kv("Queues", fmt.Sprintf("recv %d  send %d", e.RxQueue, e.TxQueue))

	sb.WriteString("\n" + tui.Bold("Owners") + "\n")
	switch {
	case len(e.Owners) > 0:
		for _, p := range e.Owners {
			line := fmt.Sprintf("%-7d %-16s", p.PID, tui.Truncate(orQ(p.Comm), 16))
			if p == e.Primary && len(e.Owners) > 1 {
				line += tui.Color(" primary", tui.FgGreen)
			}
			sb.WriteString("  " + line + "\n")
		}
	case e.Inode == 0:
		sb.WriteString(tui.Dim("  none (socket has no file, e.g. TIME-WAIT)") + "\n")
	case snap.Privileged:
		sb.WriteString(tui.Dim("  none (kernel socket)") + "\n")
	default:
		sb.WriteString(tui.Color("  unknown: owned by a process you cannot inspect.\n  Rerun with sudo to see it.", tui.FgYellow) + "\n")
	}
	return sb.String()
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// statusLeft summarizes the filters, counts and refresh state.
func statusLeft(m *Model, refreshed time.Time) string {
	f := m.Filter()
	on := func(s string, active bool) string {
		if active {
			return tui.Bold(s)
		}
		return tui.Dim(s)
	}
	parts := []string{
		tui.Color(" "+f.State.String()+" ", "7"),
		on("TCP", f.OnlyProto != sockets.UDP) + " " + on("UDP", f.OnlyProto != sockets.TCP),
		on("v4", f.OnlyFamily != sockets.IPv6) + " " + on("v6", f.OnlyFamily != sockets.IPv4),
	}
	if f.Text != "" {
		parts = append(parts, tui.Color("/"+f.Text, tui.FgYellow))
	}
	parts = append(parts, fmt.Sprintf("%d/%d", len(m.Rows()), m.Total()))
	if snap := m.Snapshot(); snap != nil && !snap.Privileged && snap.Hidden > 0 {
		parts = append(parts, tui.Color(fmt.Sprintf("%d owners hidden: run with sudo", snap.Hidden), tui.FgYellow))
	}
	if err := m.Err(); err != nil {
		parts = append(parts, tui.Color("refresh failed: "+err.Error(), tui.FgRed))
	} else if !refreshed.IsZero() {
		parts = append(parts, tui.Dim(refreshed.Format("15:04:05")))
	}
	return strings.Join(parts, "  ")
}
