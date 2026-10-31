package portsview

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/cristianradulescu/portview/internal/actions"
	"github.com/cristianradulescu/portview/internal/commands"
	"github.com/cristianradulescu/portview/internal/sockets"
	"github.com/cristianradulescu/portview/internal/ui/tui"
)

const (
	// termGrace is how long to wait for processes to exit after SIGTERM
	// before offering SIGKILL.
	termGrace = 2 * time.Second
	// killGrace is how long to wait after SIGKILL.
	killGrace = time.Second
	// longOpTimeout bounds systemctl stop and docker stop.
	longOpTimeout = 2 * time.Minute
	// maxDialogOwners caps the owner lines in the confirmation dialog.
	maxDialogOwners = 8
	// noteWidth is the width notes are wrapped to.
	noteWidth = 80
	// commandWidth is the width owner commands are wrapped to, after the
	// 10-cell label column.
	commandWidth = 86
)

// killFlow is one run of the kill action, from confirmation to result.
type killFlow struct {
	plan *actions.Plan
	// sig is the signal the Signal options send; s cycles it.
	sig syscall.Signal
	// confirming is set while the confirmation dialog is shown.
	confirming bool
	restart    string // Restart= of plan.Unit; "" while unknown
	// procs are the handles opened for signalling; released when the flow
	// ends.
	procs []actions.Process
	// waiting is set while background work runs with the progress dialog;
	// cancel stops a wait for processes to exit.
	waiting bool
	cancel  context.CancelFunc
	// partial records processes that could not be signalled while others
	// were; it is reported with the final result.
	partial string
}

func (f *killFlow) release() {
	if f.cancel != nil {
		f.cancel()
	}
	for _, p := range f.procs {
		p.Release()
	}
	f.procs = nil
}

// openKill starts the kill action for the selected socket.
func (a *App) openKill() error {
	e := a.model.Selected()
	snap := a.model.Snapshot()
	if e == nil || snap == nil {
		return nil
	}
	if a.kill != nil && a.kill.waiting {
		a.setFlash("an action is still running", tui.FgYellow)
		return nil
	}
	a.endKill()
	plan := actions.NewPlan(actions.PlanInput{
		Entry:             e,
		Details:           a.opts.Details,
		Users:             a.opts.Users,
		Privileged:        snap.Privileged,
		CanStopContainers: a.opts.Actions.CanStopContainers(),
	})
	f := &killFlow{plan: plan, sig: syscall.SIGTERM}
	a.kill = f
	a.showConfirm(f, plan.Default)

	if plan.Unit.Name != "" {
		u := plan.Unit
		a.async(func() func() error {
			ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
			defer cancel()
			restart, err := a.opts.Actions.RestartPolicy(ctx, u)
			return func() error {
				a.logOps(commands.RestartPolicy(u, a.cmdEnv(), err))
				if a.kill != f || !a.dialog.Active() || !f.confirming {
					return nil // the user moved on
				}
				f.restart = restart
				if err != nil || restart == "" {
					f.restart = "?"
				}
				a.showConfirm(f, a.dialog.Selected())
				return nil
			}
		})
	}
	return a.layout(a.g)
}

// endKill releases the current flow, if any.
func (a *App) endKill() {
	if a.kill != nil {
		a.kill.release()
		a.kill = nil
	}
}

// cycleSignal switches the signal of the confirmation dialog's options.
func (a *App) cycleSignal() error {
	f := a.kill
	if f == nil || !f.confirming || !a.dialog.Active() {
		return nil
	}
	f.sig = actions.NextSignal(f.sig)
	a.showConfirm(f, a.dialog.Selected())
	return nil
}

func (a *App) showConfirm(f *killFlow, sel int) {
	p := f.plan
	f.confirming = true
	choices := make([]tui.Choice, 0, len(p.Options)+1)
	hasSignal := false
	for _, o := range p.Options {
		danger := o.Kind == actions.Signal && f.sig == syscall.SIGKILL
		hasSignal = hasSignal || o.Kind == actions.Signal
		choices = append(choices, tui.Choice{
			Label: o.Text(f.sig), Danger: danger, Commands: commands.Option(&o, f.sig, a.cmdEnv()),
			Handler: func() error { return a.runOption(f, o) },
		})
	}
	body := planBody(f)
	title := " Stop " + p.Entry.Netid() + " " + sockets.FormatAddrPort(p.Entry.Local) + " "
	if len(choices) == 0 {
		body = append(body, "", tui.Dim("Nothing to stop here. Enter/Esc to close."))
		a.dialog.Open(title, body, nil, 0)
	} else {
		choices = append(choices, tui.Choice{Label: "Cancel", Handler: func() error { a.endKill(); return nil }})
		a.dialog.Open(title, body, choices, sel)
		a.dialog.Footer = []string{tui.Dim("j/k select · Enter confirm · 1-9 pick · Esc cancel")}
		if hasSignal {
			sigColor := tui.FgYellow
			if f.sig == syscall.SIGKILL {
				sigColor = tui.FgRed
			}
			a.dialog.Footer = []string{
				tui.Dim("Signal ") + tui.Color(actions.SignalName(f.sig), sigColor, "1") + tui.Dim(": "+actions.SignalHelp(f.sig)),
				tui.Dim("j/k select · Enter confirm · 1-9 pick · s change signal · Esc cancel"),
			}
		}
	}
	a.dialog.OnCancel = func() error { a.endKill(); return nil }
}

// planBody describes the socket and what holds it.
func planBody(f *killFlow) []string {
	p := f.plan
	e := &p.Entry
	var out []string
	kv := func(k, v string) { out = append(out, tui.Dim(tui.PadRight(k, 10))+v) }

	sock := e.Netid() + " " + tui.Color(sockets.FormatAddrPort(e.Local), exposureColor(e.Exposure))
	if !e.Listening() {
		sock += " → " + sockets.FormatAddrPort(e.Remote)
	}
	kv("Socket", sock+" "+e.State.String()+tui.Dim(" · "+exposureDescription(e.Exposure)))

	userW := 0
	for _, o := range p.Owners {
		userW = max(userW, tui.StringWidth(o.User))
	}
	for i, o := range p.Owners {
		if i == maxDialogOwners {
			kv("", tui.Dim(fmt.Sprintf("… and %d more", len(p.Owners)-maxDialogOwners)))
			break
		}
		k := ""
		if i == 0 {
			k = "Process"
			if len(p.Owners) > 1 {
				k = "Processes"
			}
		}
		prefix := fmt.Sprintf("%-7d %s  ", o.PID, tui.PadRight(o.User, userW))
		if o.Primary && len(p.Owners) > 1 {
			prefix += tui.Color("primary ", tui.FgGreen)
		}
		// Long commands wrap under themselves (up to 3 lines when few
		// processes are listed), cut in the middle beyond that, so both
		// the program and its last arguments stay visible.
		indent := strings.Repeat(" ", tui.StringWidth(prefix))
		avail := commandWidth - len(indent)
		maxLines := 1
		if len(p.Owners) <= 3 {
			maxLines = 3
		}
		lines := tui.Chunk(tui.TruncateMiddle(o.Command, avail*maxLines), avail)
		kv(k, prefix+lines[0])
		for _, l := range lines[1:] {
			kv("", indent+l)
		}
	}

	if u := p.Unit; u.Name != "" {
		kv("Unit", tui.Color(u.String(), tui.FgMagenta)+tui.Dim(" (systemd service)"))
		kv("", restartWarning(f.restart))
	}
	if c := p.Container; c != nil {
		s := tui.Color(c.Label(), tui.FgCyan)
		if c.Name != "" {
			s += tui.Dim(" (" + c.ShortID() + ")")
		}
		if c.Image != "" {
			s += tui.Dim(" · " + c.Image)
		}
		if c.Forward != nil {
			s += tui.Dim(fmt.Sprintf(" · published port → %d/%s", c.Forward.ContainerPort, c.Forward.Proto))
		}
		kv("Container", s)
	}
	for _, n := range p.Notes {
		out = append(out, "")
		for _, l := range tui.WrapHanging(n, noteWidth, "") {
			out = append(out, tui.Color(l, tui.FgYellow))
		}
	}
	return out
}

func restartWarning(restart string) string {
	switch restart {
	case "":
		return tui.Dim("systemd may restart a killed process (checking Restart=…)")
	case "?":
		return tui.Color("systemd may restart a killed process", tui.FgYellow)
	case "no":
		return tui.Dim("Restart=no: systemd will not restart a killed process")
	}
	return tui.Color("Restart="+restart+": systemd may restart a killed process; systemctl stop keeps it down", tui.FgYellow)
}

// --- running options ---------------------------------------------------------

func (a *App) runOption(f *killFlow, o actions.Option) error {
	switch o.Kind {
	case actions.Signal:
		return a.sendSignal(f, o.Owners, f.sig)
	case actions.StopUnit:
		u := o.Unit
		logged := commands.Op{Desc: "stop " + u.String(), Cmd: commands.Systemctl(u, a.cmdEnv(), "stop")}
		return a.runLong(f, "Stopping "+u.String(), actions.SystemctlCommand(u), logged, func(ctx context.Context) error {
			return a.opts.Actions.StopUnit(ctx, u)
		}, u.Name+" stopped")
	case actions.StopContainer:
		c := o.Container
		logged := commands.Op{Desc: "stop container " + c.Label() + " (docker stop)", Cmd: commands.ContainerStop(a.cmdEnv(), c.ID)}
		return a.runLong(f, "Stopping container "+c.Label(), "docker stop "+c.Label(), logged, func(ctx context.Context) error {
			return a.opts.Actions.StopContainer(ctx, c.ID)
		}, "container "+c.Label()+" stopped")
	}
	return nil
}

// sendSignal sends sig to the owners and waits for them to exit.
func (a *App) sendSignal(f *killFlow, owners []actions.Owner, sig syscall.Signal) error {
	name := actions.SignalName(sig)
	var errs []error
	for _, o := range owners {
		p, err := a.opts.Actions.OpenProcess(o.PID, o.StartTicks)
		switch {
		case errors.Is(err, actions.ErrGone):
			// Already exited: nothing to do for it.
		case err != nil:
			errs = append(errs, err)
		default:
			f.procs = append(f.procs, p)
		}
	}
	sent, err := actions.SignalAll(f.procs, sig)
	if err != nil {
		errs = append(errs, err)
	}
	a.logSignal(sig, f.procs, err)
	if len(sent) == 0 {
		a.requestRefresh()
		if len(errs) > 0 {
			return a.showError(f, "Could not send "+name, errors.Join(errs...))
		}
		a.endKill()
		a.setFlash(fmt.Sprintf("%s already exited", pidList(owners)), tui.FgGreen)
		return nil
	}
	for _, p := range f.procs {
		if !slices.Contains(sent, p) {
			p.Release()
		}
	}
	f.procs = sent
	if len(errs) > 0 {
		// Some were signalled; report the others with the result.
		f.partial = errorText(errors.Join(errs...))
	}
	grace := termGrace
	if sig == syscall.SIGKILL {
		grace = killGrace
	}
	return a.waitExit(f, sig, grace)
}

// waitExit waits up to grace for f.procs to exit after sig.
func (a *App) waitExit(f *killFlow, sig syscall.Signal, grace time.Duration) error {
	name := actions.SignalName(sig)
	a.showProgress(f, fmt.Sprintf("Sent %s to PID %s; waiting up to %s for it to exit…",
		name, actions.PIDs(f.procs), grace), "Esc stops waiting")
	ctx, cancel := context.WithCancel(a.ctx)
	f.cancel = cancel
	procs := f.procs
	grace = a.graceFor(grace)
	a.async(func() func() error {
		left := actions.Wait(ctx, procs, grace, 50*time.Millisecond)
		return func() error {
			if ctx.Err() != nil || a.kill != f {
				return nil // cancelled by the user
			}
			cancel()
			f.waiting = false
			a.requestRefresh()
			if len(left) == 0 {
				a.closeDialog()
				a.endKill()
				msg := fmt.Sprintf("PID %s exited after %s", actions.PIDs(procs), name)
				if f.partial != "" {
					a.setFlash(msg+"; not signalled: "+f.partial, tui.FgYellow)
				} else {
					a.setFlash(msg, tui.FgGreen)
				}
				return nil
			}
			f.procs = left
			if sig == syscall.SIGKILL {
				a.showMessage(f, " Still running ", []string{
					tui.Color(fmt.Sprintf("PID %s is still running after SIGKILL.", actions.PIDs(left)), tui.FgRed),
					"It is probably stuck in the kernel (uninterruptible sleep, state D)",
					"and exits once the operation it waits for completes.",
				})
				return nil
			}
			a.showSurvivors(f, sig, grace)
			return nil
		}
	})
	return nil
}

// graceFor lets tests shorten the waits.
func (a *App) graceFor(d time.Duration) time.Duration {
	if a.graceScale > 0 {
		return time.Duration(float64(d) * a.graceScale)
	}
	return d
}

func (a *App) showSurvivors(f *killFlow, sig syscall.Signal, waited time.Duration) {
	f.confirming = false
	pids := actions.PIDs(f.procs)
	body := []string{
		tui.Color(fmt.Sprintf("PID %s is still running %s after %s.", pids, waited.Round(100*time.Millisecond), actions.SignalName(sig)), tui.FgYellow),
		tui.Dim("SIGKILL cannot be caught: the process gets no chance to clean up."),
	}
	if f.plan.Unit.Name != "" {
		body = append(body, tui.Dim("Its systemd unit may restart it; consider "+actions.SystemctlCommand(f.plan.Unit)+"."))
	}
	a.dialog.Open(" Still running ", body, []tui.Choice{
		{Label: "SIGKILL PID " + pids, Danger: true, Commands: []string{commands.Kill(syscall.SIGKILL, procPIDs(f.procs)...)}, Handler: func() error {
			_, err := actions.SignalAll(f.procs, syscall.SIGKILL)
			a.logSignal(syscall.SIGKILL, f.procs, err)
			if err != nil {
				return a.showError(f, "Could not send SIGKILL", err)
			}
			return a.waitExit(f, syscall.SIGKILL, killGrace)
		}},
		{Label: "Wait 2s more", Handler: func() error { return a.waitExit(f, sig, termGrace) }},
		{Label: "Leave it running", Handler: func() error { a.endKill(); return nil }},
	}, 0)
	a.dialog.Footer = []string{tui.Dim("Enter confirm · Esc leave it running")}
	a.dialog.OnCancel = func() error { a.endKill(); return nil }
}

// runLong runs a systemctl/docker stop in the background. Esc hides the
// progress dialog; the result then shows in the status bar.
func (a *App) runLong(f *killFlow, title, command string, logged commands.Op, op func(context.Context) error, success string) error {
	a.showProgress(f, "Running "+command+"…", "Esc hides this; the result shows in the status bar")
	a.dialog.OnCancel = nil // keep the flow: the operation continues
	a.async(func() func() error {
		ctx, cancel := context.WithTimeout(a.ctx, longOpTimeout)
		defer cancel()
		err := op(ctx)
		return func() error {
			logged.Err = err
			a.logOps(logged)
			f.waiting = false
			a.requestRefresh()
			visible := a.kill == f && a.dialog.Active()
			if err != nil {
				if visible {
					return a.showError(f, title+" failed", err)
				}
				a.setFlash(command+": "+errorText(err), tui.FgRed)
				return nil
			}
			if visible {
				a.closeDialog()
			}
			if a.kill == f {
				a.endKill()
			}
			a.setFlash(success, tui.FgGreen)
			return nil
		}
	})
	return nil
}

func (a *App) showProgress(f *killFlow, msg, hint string) {
	f.waiting, f.confirming = true, false
	a.dialog.Open(" Working ", []string{msg, "", tui.Dim(hint)}, nil, 0)
	a.dialog.Busy = true
	a.dialog.OnCancel = func() error { a.endKill(); return nil }
}

func (a *App) showError(f *killFlow, title string, err error) error {
	lines := []string{tui.Color(title+":", tui.FgRed)}
	lines = append(lines, tui.WrapHanging(errorText(err), noteWidth, "  ")...)
	if errors.Is(err, actions.ErrPermission) && !a.privileged() {
		lines = append(lines, "", tui.Color("Rerun portview with sudo to do this.", tui.FgYellow))
	}
	a.showMessage(f, " Error ", lines)
	return nil
}

func (a *App) showMessage(f *killFlow, title string, lines []string) {
	f.waiting, f.confirming = false, false
	a.dialog.Open(title, append(lines, "", tui.Dim("Enter/Esc to close")), nil, 0)
	a.dialog.OnCancel = func() error { a.endKill(); return nil }
}

func (a *App) closeDialog() {
	if a.dialog.Active() {
		_ = a.dialog.Close(a.g, viewList)
	}
}

func (a *App) privileged() bool {
	snap := a.model.Snapshot()
	return snap != nil && snap.Privileged
}

func errorText(err error) string {
	// Joined errors are one per line; keep them on one.
	return strings.ReplaceAll(err.Error(), "\n", "; ")
}

func pidList(owners []actions.Owner) string {
	s := make([]string, len(owners))
	for i, o := range owners {
		s[i] = fmt.Sprint(o.PID)
	}
	return "PID " + strings.Join(s, ", ")
}

// --- status bar messages -----------------------------------------------------

const flashDuration = 8 * time.Second

func (a *App) setFlash(msg, color string) {
	a.flash = tui.Color(msg, color)
	a.flashUntil = time.Now().Add(flashDuration)
}

func (a *App) flashMessage() string {
	if a.flash != "" && time.Now().Before(a.flashUntil) {
		return a.flash
	}
	a.flash = ""
	return ""
}

// logSignal logs sending sig to ps as the equivalent kill command.
func (a *App) logSignal(sig syscall.Signal, ps []actions.Process, err error) {
	if len(ps) == 0 {
		return
	}
	pids := procPIDs(ps)
	a.logOps(commands.Op{
		Desc: fmt.Sprintf("send %s to PID %s", actions.SignalName(sig), actions.PIDs(ps)),
		Cmd:  commands.Kill(sig, pids...),
		Err:  err,
	})
}

func procPIDs(ps []actions.Process) []int {
	out := make([]int, len(ps))
	for i, p := range ps {
		out[i] = p.PID()
	}
	return out
}
