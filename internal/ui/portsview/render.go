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
	// optional columns are hidden when no row has a value.
	optional bool
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
	colContainer
	numCols
)

var columns = [numCols]column{
	colProto:     {title: "PROTO", min: 5, max: 5},
	colState:     {title: "STATE", min: 5, max: 10},
	colLocal:     {title: "LOCAL", min: 5, max: 47, shrink: 21, middle: true},
	colRemote:    {title: "REMOTE", min: 6, max: 47, shrink: 15, middle: true},
	colExposure:  {title: "EXPOSURE", min: 8, max: 8},
	colPID:       {title: "PID", min: 3, max: 12},
	colProcess:   {title: "PROCESS", min: 7, max: 16, shrink: 8},
	colUser:      {title: "USER", min: 4, max: 16, shrink: 8},
	colContainer: {title: "CONTAINER", min: 9, max: 24, shrink: 10, optional: true},
}

// shrinkGroups give up space in order; within a group the widest column
// shrinks first, so the two address columns end up balanced.
var shrinkGroups = [][]int{{colLocal, colRemote}, {colContainer, colUser, colProcess}}

// cells returns the plain text of each column for e.
func cells(e *inventory.Entry, privileged bool) [numCols]string {
	return [numCols]string{
		colProto:     e.Netid(),
		colState:     e.State.String(),
		colLocal:     sockets.FormatAddrPort(e.Local),
		colRemote:    sockets.FormatAddrPort(e.Remote),
		colExposure:  exposureLabel(e.Exposure),
		colPID:       e.PIDLabel(privileged),
		colProcess:   e.ProcessLabel(privileged),
		colUser:      e.User,
		colContainer: e.ContainerLabel(),
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
	var (
		w    [numCols]int
		used [numCols]bool
	)
	for i := range rows {
		cs := cells(&rows[i], privileged)
		for j, s := range cs {
			if s != "" {
				used[j] = true
			}
			w[j] = max(w[j], min(tui.StringWidth(s), columns[j].max))
		}
	}
	total := -1 // n-1 separators
	for i, c := range columns {
		if c.optional && !used[i] {
			w[i] = 0 // hidden
			continue
		}
		w[i] = max(w[i], c.min, tui.StringWidth(c.title))
		total += w[i] + 1
	}
	excess := total - avail
	for _, group := range shrinkGroups {
		for excess > 0 {
			widest := -1
			for _, c := range group {
				if w[c] == 0 {
					continue // hidden column
				}
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
	parts := make([]string, 0, numCols)
	for i, c := range columns {
		if w[i] > 0 {
			parts = append(parts, tui.Fit(c.title, w[i]))
		}
	}
	return tui.Color(strings.TrimRight(strings.Join(parts, " "), " "), "1", "4")
}

func rowLine(e *inventory.Entry, w [numCols]int, privileged, fresh bool) string {
	cs := cells(e, privileged)
	parts := make([]string, 0, numCols)
	for i, s := range cs {
		if w[i] == 0 {
			continue // hidden column
		}
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
		case colContainer:
			codes = append(codes, tui.FgCyan)
		}
		if fresh {
			codes = append(codes, "1")
		}
		if columns[i].middle {
			s = tui.TruncateMiddle(s, w[i])
		}
		parts = append(parts, tui.Color(tui.Fit(s, w[i]), codes...))
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
		return "all interfaces (network-reachable)"
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
	if snap := m.Snapshot(); snap != nil && snap.DockerErr != nil {
		parts = append(parts, tui.Color("docker names unavailable", tui.FgYellow))
	}
	if err := m.Err(); err != nil {
		parts = append(parts, tui.Color("refresh failed: "+err.Error(), tui.FgRed))
	} else if !refreshed.IsZero() {
		parts = append(parts, tui.Dim(refreshed.Format("15:04:05")))
	}
	return strings.Join(parts, "  ")
}
