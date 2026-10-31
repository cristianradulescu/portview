package portsview

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cristianradulescu/portview/internal/commands"
	"github.com/cristianradulescu/portview/internal/explain"
	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
	"github.com/cristianradulescu/portview/internal/ui/tui"
)

// inspectWidth caps the width of the inspect popup's text.
const inspectWidth = 110

// defaultReadRaw reads a socket's raw line from the real /proc.
func defaultReadRaw(users inventory.UserResolver) func(string, sockets.Socket) (*explain.Report, error) {
	hz := procs.Scanner{}.ClockTicks()
	return func(path string, s sockets.Socket) (*explain.Report, error) {
		return explain.Read(path, s, explain.Options{ClockTicks: hz, UserName: users.Name})
	}
}

// openInspect shows the selected socket's raw kernel line, field by field.
func (a *App) openInspect() error {
	if a.tab == tabUnix {
		return a.openInspectUnix()
	}
	e := a.model.Selected()
	if e == nil {
		return nil
	}
	leader := 0
	if !e.HostNetNS {
		leader = a.model.Snapshot().Leader(e.NetNS)
	}
	path := commands.TablePath(e, leader)
	r, err := a.opts.ReadRaw(path, e.Socket)
	a.logOps(commands.Op{
		Desc: fmt.Sprintf("raw kernel line of %s %s", e.Netid(), sockets.FormatAddrPort(e.Local)),
		Cmd:  commands.RawLine(e, leader),
		Err:  err,
	})
	title := fmt.Sprintf("%s %s %s", e.Netid(), sockets.FormatAddrPort(e.Local), e.State)
	if !e.Listening() {
		title = fmt.Sprintf("%s %s → %s %s", e.Netid(), sockets.FormatAddrPort(e.Local), sockets.FormatAddrPort(e.Remote), e.State)
	}
	maxX, _ := a.g.Size()
	a.inspect.Open(renderInspect(title, r, err, commands.RawLine(e, leader), min(inspectWidth, maxX-6)))
	return a.layout(a.g)
}

// renderInspect lays out a report: the raw lines, then each field with
// its meaning wrapped below it.
func renderInspect(title string, r *explain.Report, err error, cmd string, width int) string {
	var sb strings.Builder
	line := func(s string) { sb.WriteString(s + "\n") }
	if err != nil {
		line(tui.Bold(title))
		line("")
		msg := err.Error()
		if errors.Is(err, explain.ErrNotFound) {
			msg = "This socket is no longer in the kernel's table: it closed after the last refresh."
		}
		for _, l := range tui.WrapHanging(msg, width, "") {
			line(tui.Color(l, tui.FgYellow))
		}
		line("")
		line(tui.Dim("$ " + cmd))
		return sb.String()
	}
	line(tui.Bold(title) + tui.Dim(fmt.Sprintf("  ·  line %d of %s, read just now", r.LineNo, r.Path)))
	line("")
	line(tui.Dim("This is the line the kernel prints for the socket; ss and portview parse it:"))
	for _, l := range tui.WrapHanging(strings.TrimRight(r.Header, " "), width, "") {
		line(tui.Dim(l))
	}
	for _, l := range tui.WrapHanging(strings.TrimRight(r.Line, " "), width, "") {
		line(tui.Color(l, tui.FgCyan))
	}
	line("")
	const nameW = 15
	indent := strings.Repeat(" ", nameW)
	for _, f := range r.Fields {
		line(tui.Bold(tui.PadRight(f.Name, nameW)) + tui.Color(f.Raw, tui.FgCyan))
		for _, l := range tui.WrapHanging(f.Meaning, width-nameW, "") {
			if f.Notable {
				l = tui.Color(l, tui.FgYellow)
			}
			line(indent + l)
		}
	}
	line("")
	if strings.Contains(r.Path, "/unix") {
		for _, l := range tui.WrapHanging("The layout follows the kernel's source (net/unix/af_unix.c).", width, "") {
			line(tui.Dim(l))
		}
	} else {
		for _, l := range tui.WrapHanging("Names in parentheses are not in the file's header; they follow the "+
			"kernel's source (net/ipv4/tcp_ipv4.c and udp.c). Yellow marks values worth a look.", width, "") {
			line(tui.Dim(l))
		}
	}
	line("")
	line(tui.Dim("The same line from a shell:"))
	for _, l := range tui.WrapHanging("$ "+cmd, width, "    ") {
		line(tui.Color(l, tui.FgCyan))
	}
	return sb.String()
}
