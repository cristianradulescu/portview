package portsview

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/jesseduffield/gocui"

	"github.com/cristianradulescu/portview/internal/commands"
	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/ui/tui"
)

// maxLog caps the command log.
const maxLog = 500

// commandLog is every read and action portview performed, as the command
// that does the same. Reads repeated on every refresh are logged once.
type commandLog struct {
	ops []commands.Op
	// sel is the selected entry while the log has the focus; follow keeps
	// it on the newest entry.
	sel    int
	follow bool
	origin int
	// last* remember what was logged, so refreshes don't repeat it.
	lastNamespaces string
	lastDocker     string
	dockerLogged   bool
	lastDetails    int
}

func newCommandLog() commandLog { return commandLog{follow: true, sel: -1} }

// logOps appends ops, skipping an op identical to the previous one.
func (a *App) logOps(ops ...commands.Op) {
	l := &a.log
	for _, op := range ops {
		if n := len(l.ops); n > 0 && l.ops[n-1].Cmd == op.Cmd && errString(l.ops[n-1].Err) == errString(op.Err) {
			continue
		}
		l.ops = append(l.ops, op)
	}
	if over := len(l.ops) - maxLog; over > 0 {
		l.ops = slices.Delete(l.ops, 0, over)
		l.sel = max(0, l.sel-over)
	}
	if l.follow {
		l.sel = len(l.ops) - 1
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// logSnapshot logs what a collection amounted to, when it changed: ss in
// every network namespace, and the Docker API call.
func (a *App) logSnapshot(snap *inventory.Snapshot) {
	env := a.cmdEnv()
	if snap.Procs != nil {
		keys := make([]string, 0, len(snap.Procs.Namespaces))
		for _, ns := range snap.Procs.Namespaces {
			keys = append(keys, strconv.FormatUint(ns.Key, 10))
		}
		if k := strings.Join(keys, ","); k != a.log.lastNamespaces {
			a.log.lastNamespaces = k
			a.logOps(commands.NamespaceScan(snap, env)...)
		}
	}
	if a.opts.DockerSocket != "" {
		if e := errString(snap.DockerErr); !a.log.dockerLogged || e != a.log.lastDocker {
			a.log.dockerLogged, a.log.lastDocker = true, e
			a.logOps(commands.ContainerList(env, snap.DockerErr))
		}
	}
}

func (a *App) cmdEnv() commands.Env {
	return commands.Env{
		Privileged:   a.privileged(),
		EUID:         os.Geteuid(),
		Users:        a.opts.Users,
		DockerSocket: a.opts.DockerSocket,
	}
}

// renderLog draws the log; the selected entry is highlighted while the
// log has the focus.
func (a *App) renderLog(v *gocui.View) {
	l := &a.log
	w, h := v.InnerSize()
	focused := a.g.CurrentView() == v
	v.Title = " Command log "
	if len(l.ops) == 0 {
		v.Clear()
		v.WriteString(tui.Dim("Commands for what portview reads and does appear here."))
		return
	}
	var (
		lines    []string
		selStart = -1
		selEnd   int
	)
	for i, op := range l.ops {
		start := len(lines)
		lines = append(lines, logEntryLines(op, w)...)
		if op.Err != nil {
			for _, part := range tui.WrapHanging("✗ "+errorText(op.Err), w-2, "  ") {
				lines = append(lines, "  "+tui.Color(part, tui.FgRed))
			}
		}
		if focused && i == l.sel {
			selStart, selEnd = start, len(lines)
			for j := start; j < len(lines); j++ {
				lines[j] = tui.Color(tui.PadRight(tui.StripANSI(lines[j]), w), "7")
			}
		}
	}
	switch {
	case !focused || l.follow:
		l.origin = max(0, len(lines)-h)
	case selStart >= 0:
		if selStart < l.origin {
			l.origin = selStart
		}
		if selEnd > l.origin+h {
			l.origin = selEnd - h
		}
	}
	l.origin = max(0, min(l.origin, len(lines)-h))
	v.Clear()
	v.WriteString(strings.Join(lines[l.origin:], "\n"))
}

// logEntryLines renders "$ cmd  # desc" in width cells: the command in
// cyan, wrapped under itself, and the comment dim, on the command's last
// line when it fits.
func logEntryLines(op commands.Op, w int) []string {
	cmd := tui.WrapHanging("$ "+op.Cmd, w, "  ")
	out := make([]string, len(cmd))
	for i, l := range cmd {
		out[i] = tui.Color(l, tui.FgCyan)
	}
	comment := "# " + op.Desc
	last := len(out) - 1
	if tui.StringWidth(cmd[last])+2+tui.StringWidth(comment) <= w {
		out[last] += "  " + tui.Dim(comment)
	} else {
		for _, l := range tui.WrapHanging(comment, w-2, "  ") {
			out = append(out, "  "+tui.Dim(l))
		}
	}
	return out
}

func (a *App) moveLog(delta int) {
	l := &a.log
	if len(l.ops) == 0 {
		return
	}
	if l.sel < 0 {
		l.sel = len(l.ops) - 1
	}
	l.sel = max(0, min(l.sel+delta, len(l.ops)-1))
	l.follow = l.sel == len(l.ops)-1
}

// copyLog copies the selected log entry's command.
func (a *App) copyLog() error {
	l := &a.log
	if l.sel < 0 || l.sel >= len(l.ops) {
		return nil
	}
	a.copyText(l.ops[l.sel].Cmd)
	return nil
}

// --- copying ------------------------------------------------------------

// openCopy offers the equivalent commands of the selection for copying.
func (a *App) openCopy() error {
	cmds := a.equivalentCommands()
	if len(cmds) == 0 {
		return nil
	}
	choices := make([]tui.Choice, len(cmds))
	for i, c := range cmds {
		choices[i] = tui.Choice{Label: c.Comment, Commands: []string{c.Cmd}, Handler: func() error {
			a.copyText(c.Cmd)
			return nil
		}}
	}
	a.endKill() // the dialog is shared with the kill action
	a.dialog.Open(" Copy a command ", nil, choices, 0)
	a.dialog.Footer = []string{tui.Dim("j/k select · Enter copy · 1-9 pick · Esc cancel")}
	return a.layout(a.g)
}

func (a *App) copyText(text string) {
	how, err := a.opts.Clipboard(text)
	if err != nil {
		a.setFlash("copy failed: "+err.Error(), tui.FgRed)
		return
	}
	a.setFlash(fmt.Sprintf("copied (%s): %s", how, text), tui.FgGreen)
}

// equivalentCommands are the commands shown for the current selection:
// the list's ss command, then the selected socket's.
func (a *App) equivalentCommands() []commands.Command {
	env := a.cmdEnv()
	out := []commands.Command{commands.View(a.model.Filter(), env)}
	if e := a.model.Selected(); e != nil {
		d, _ := a.selectedDetails()
		out = append(out, commands.Selection(e, a.model.Snapshot(), d, env)...)
	}
	return out
}

// renderCommands is the "Equivalent commands" section of the details pane.
func renderCommands(cmds []commands.Command, width int) string {
	if len(cmds) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n" + tui.Bold("Equivalent commands") + tui.Dim("  (y to copy)") + "\n")
	for _, c := range cmds {
		for _, l := range tui.WrapHanging("# "+c.Comment, width-2, "  ") {
			sb.WriteString("  " + tui.Dim(l) + "\n")
		}
		for _, l := range tui.WrapHanging("$ "+c.Cmd, width-2, "    ") {
			sb.WriteString("  " + tui.Color(l, tui.FgCyan) + "\n")
		}
	}
	return sb.String()
}
