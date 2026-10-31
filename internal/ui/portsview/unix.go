package portsview

import (
	"fmt"
	"os"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jesseduffield/gocui"

	"github.com/cristianradulescu/portview/internal/commands"
	"github.com/cristianradulescu/portview/internal/explain"
	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/termsafe"
	"github.com/cristianradulescu/portview/internal/ui/tui"
)

// unixKey identifies a Unix socket across refreshes.
type unixKey struct{ netns, inode uint64 }

// unixList is the state of the Unix tab.
type unixList struct {
	list   tui.ListState
	height int
	sel    unixKey
	groups map[int]string // group names, cached
}

func newUnixList() unixList { return unixList{list: tui.NewListState(), height: 1} }

func keyOf(u *inventory.UnixEntry) unixKey { return unixKey{u.NetNS, u.Inode} }

// unixRows are the Unix sockets matching the filter.
func (a *App) unixRows() []*inventory.UnixEntry {
	snap := a.model.Snapshot()
	if snap == nil {
		return nil
	}
	f := a.model.Filter()
	var out []*inventory.UnixEntry
	for i := range snap.Unix {
		if f.MatchUnix(&snap.Unix[i]) {
			out = append(out, &snap.Unix[i])
		}
	}
	return out
}

func (a *App) syncUnix(rows []*inventory.UnixEntry) {
	l := &a.ux
	if len(rows) == 0 {
		l.list.Clamp(0, l.height)
		return
	}
	if idx := slices.IndexFunc(rows, func(u *inventory.UnixEntry) bool { return keyOf(u) == l.sel }); idx >= 0 {
		l.list.Reanchor(idx, len(rows), l.height)
	} else {
		l.list.Clamp(len(rows), l.height)
	}
	l.sel = keyOf(rows[l.list.Selected])
}

func (a *App) moveUnix(delta int) {
	rows := a.unixRows()
	a.syncUnix(rows)
	if len(rows) == 0 {
		return
	}
	a.ux.list.Move(delta, len(rows), a.ux.height)
	a.ux.sel = keyOf(rows[a.ux.list.Selected])
}

func (a *App) selectedUnix() *inventory.UnixEntry {
	rows := a.unixRows()
	a.syncUnix(rows)
	if a.ux.list.Selected < 0 {
		return nil
	}
	return rows[a.ux.list.Selected]
}

func (a *App) unixCount() (shown, total int) {
	if snap := a.model.Snapshot(); snap != nil {
		return len(a.unixRows()), len(snap.Unix)
	}
	return 0, 0
}

// renderUnix draws the Unix tab.
func (a *App) renderUnix(v *gocui.View) {
	w, h := v.InnerSize()
	a.ux.height = max(1, h-1)
	v.HighlightInactive = a.g.CurrentView() != v
	v.Tabs, v.TabIndex = a.tabTitles(), tabUnix
	v.Clear()
	v.SetOrigin(0, 0)
	snap := a.model.Snapshot()
	if snap == nil {
		v.WriteString(tui.Dim("collecting sockets…"))
		v.Highlight = false
		return
	}
	rows := a.unixRows()
	a.syncUnix(rows)
	priv := snap.Privileged

	pidW, procW, userW := len("PID"), len("PROCESS"), len("USER")
	for _, u := range rows {
		pidW = max(pidW, len(u.PIDLabel(priv)))
		procW = max(procW, min(16, tui.StringWidth(u.ProcessLabel(priv))))
		userW = max(userW, min(12, tui.StringWidth(u.User)))
	}
	const typeW, stateW = 9, 6
	pathW := max(10, w-typeW-stateW-pidW-procW-userW-5)
	header := strings.Join([]string{tui.Fit("TYPE", typeW), tui.Fit("STATE", stateW), tui.Fit("PATH", pathW),
		tui.Fit("PID", pidW), tui.Fit("PROCESS", procW), "USER"}, " ")
	var sb strings.Builder
	sb.WriteString(tui.Color(header, "1", "4"))
	if len(rows) == 0 {
		sb.WriteString("\n" + tui.Dim("no Unix sockets match the current filter"))
	}
	l := a.ux.list
	end := min(len(rows), l.Origin+a.ux.height)
	for i := l.Origin; i < end; i++ {
		u := rows[i]
		path := tui.Fit(tui.TruncateMiddle(u.Path, pathW), pathW)
		switch {
		case u.Path == "":
			path = tui.Dim(tui.Fit("(unnamed)", pathW))
		case u.Abstract():
			path = tui.Color(path, tui.FgMagenta)
		}
		if !u.HostNetNS {
			path = tui.Color(tui.StripANSI(path), tui.FgCyan)
		}
		state := tui.Fit(u.StateLabel(), stateW)
		if u.Listen() {
			state = tui.Bold(state)
		}
		pid := tui.Fit(u.PIDLabel(priv), pidW)
		proc := tui.Fit(u.ProcessLabel(priv), procW)
		if u.Primary == nil {
			pid, proc = tui.Color(pid, tui.FgGray), tui.Color(proc, tui.FgGray)
		}
		sb.WriteString("\n" + strings.Join([]string{tui.Fit(u.Type.String(), typeW), state, path, pid, proc,
			tui.Fit(u.User, userW)}, " "))
	}
	v.WriteString(sb.String())
	v.Highlight = l.Selected >= 0
	if l.Selected >= 0 {
		v.SetCursor(0, 1+l.Selected-l.Origin)
	} else {
		v.SetCursor(0, 0)
	}
}

// renderUnixDetails describes the selected Unix socket and its owner.
func (a *App) renderUnixDetails(width int) string {
	u := a.selectedUnix()
	snap := a.model.Snapshot()
	if u == nil || snap == nil {
		return tui.Dim("nothing selected")
	}
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
	note := func(text string) {
		for _, l := range tui.WrapHanging(text, width-len(indent), "") {
			sb.WriteString(indent + tui.Dim(l) + "\n")
		}
	}
	section := func(title string) {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(tui.Bold(title) + "\n")
	}

	var d *procs.Details
	if u.Primary != nil {
		d, _ = a.detailsFor(u.Primary.PID)
	}

	section("Unix socket")
	kv("Type", u.Type.String()+tui.Dim(" ("+u.Type.Netid()+" in ss)"))
	note(explain.UnixTypeHelp(u.Type))
	kv("State", u.StateLabel())
	note(explain.UnixStateHelp(&u.UnixSocket))
	if u.Path != "" {
		kv("Path", u.Path)
	} else {
		kv("Path", tui.Dim("(none)"))
	}
	note(explain.UnixPathHelp(&u.UnixSocket))
	if u.Path != "" && !u.Abstract() {
		a.writeUnixAccess(kv, note, u, d)
	}
	kv("Inode", strconv.FormatUint(u.Inode, 10))
	kv("Netns", snap.NetNSLabel(u.NetNS))

	switch {
	case u.Primary == nil && !snap.Privileged:
		section("Process")
		sb.WriteString(tui.Color("  held by a process you cannot inspect. Rerun with sudo.", tui.FgYellow) + "\n")
	case u.Primary == nil:
		section("Process")
		sb.WriteString(tui.Dim("  no process holds it") + "\n")
	case d == nil:
		section(fmt.Sprintf("Process %s · PID %d", u.Primary.Comm, u.Primary.PID))
		sb.WriteString(tui.Dim("  details unavailable") + "\n")
	default:
		in := detailsInput{proc: d, users: a.opts.Users, now: time.Now(), privileged: snap.Privileged, width: width, host: a.host, hz: a.hz}
		writeProcess(&sb, kv, section, in)
		writeInsight(kv, note, in)
	}
	if len(u.Owners) > 1 {
		section(fmt.Sprintf("Shared by %d processes", len(u.Owners)))
		for i, p := range u.Owners {
			if i == maxListed {
				sb.WriteString(tui.Dim(fmt.Sprintf("  … and %d more", len(u.Owners)-maxListed)) + "\n")
				break
			}
			sb.WriteString(fmt.Sprintf("  %-7d %s %s\n", p.PID, tui.Fit(orQ(p.Comm), 16), a.opts.Users.Name(p.UID)))
		}
	}
	return sb.String()
}

// writeUnixAccess explains who may connect to a socket file.
func (a *App) writeUnixAccess(kv func(k, v string), note func(string), u *inventory.UnixEntry, d *procs.Details) {
	ownView := true // whether portview sees the same filesystem as the owner
	known := false
	if d != nil && d.Insight.NSErr == nil {
		for _, ns := range d.Insight.Namespaces {
			if ns.Type == "mnt" {
				ownView, known = ns.Shared, true
			}
		}
	}
	if !u.HostNetNS || !ownView {
		kv("File", tui.Dim("in the process's own filesystem view (mount namespace)"))
		note("The path is resolved inside the process's mount namespace (a container or sandbox), so portview cannot check it from here.")
		return
	}
	groups, _ := os.Getgroups()
	groups = append(groups, os.Getgid())
	acc := a.opts.UnixAccess(u.Path, os.Geteuid(), groups, a.opts.Users.Name, a.groupName)
	switch {
	case acc.Missing && !known:
		kv("File", tui.Dim("not found from portview"))
		note("It was deleted after the socket was bound (existing connections keep working, but nobody can connect by this name), " +
			"or the process sees another filesystem (a sandbox); its namespaces need root to compare.")
	case acc.Missing:
		kv("File", tui.Color("deleted", tui.FgYellow))
		note(acc.Text)
	case acc.Mode != "":
		kv("File", acc.Mode+" "+acc.Owner)
		note(acc.Text)
	default:
		kv("File", tui.Dim("?"))
		note(acc.Text)
	}
}

func (a *App) groupName(gid int) string {
	if a.ux.groups == nil {
		a.ux.groups = make(map[int]string)
	}
	if n, ok := a.ux.groups[gid]; ok {
		return n
	}
	n := strconv.Itoa(gid)
	if g, err := user.LookupGroupId(n); err == nil {
		n = termsafe.String(g.Name)
	}
	a.ux.groups[gid] = n
	return n
}

// unixCommands are the equivalent commands of the Unix tab.
func (a *App) unixCommands(env commands.Env) []commands.Command {
	out := []commands.Command{commands.UnixView(a.model.Filter(), env)}
	u := a.selectedUnix()
	if u == nil {
		return out
	}
	out = append(out, commands.UnixSocket(u, a.model.Snapshot(), env)...)
	if u.Primary != nil {
		if d, _ := a.detailsFor(u.Primary.PID); d != nil {
			out = append(out, commands.Process(&inventory.Entry{Owners: u.Owners}, d, env)...)
		}
	}
	return out
}

// openInspectUnix shows the selected Unix socket's raw kernel line.
func (a *App) openInspectUnix() error {
	u := a.selectedUnix()
	if u == nil {
		return nil
	}
	leader := 0
	if !u.HostNetNS {
		leader = a.model.Snapshot().Leader(u.NetNS)
	}
	path := "/proc/net/unix"
	if leader > 0 {
		path = fmt.Sprintf("/proc/%d/net/unix", leader)
	}
	r, err := a.opts.ReadUnixRaw(path, u.Inode)
	cmd := commands.UnixRawLine(u, leader)
	name := u.Path
	if name == "" {
		name = "unnamed, inode " + strconv.FormatUint(u.Inode, 10)
	}
	title := fmt.Sprintf("%s %s %s", u.Type.Netid(), name, u.StateLabel())
	a.logOps(commands.Op{Desc: "raw kernel line of Unix socket " + name, Cmd: cmd, Err: err})
	maxX, _ := a.g.Size()
	a.inspect.Open(renderInspect(title, r, err, cmd, min(inspectWidth, maxX-6)))
	return a.layout(a.g)
}
