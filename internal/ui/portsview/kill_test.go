package portsview

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jesseduffield/gocui"

	"github.com/cristianradulescu/portview/internal/actions"
	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
	"github.com/cristianradulescu/portview/internal/ui/tui"
)

type fakeProc struct {
	pid        int
	alive      bool
	ignoreTerm bool
	signals    []syscall.Signal
	released   bool
}

func (p *fakeProc) PID() int { return p.pid }
func (p *fakeProc) Signal(sig syscall.Signal) error {
	if !p.alive {
		return fmt.Errorf("PID %d: %w", p.pid, actions.ErrGone)
	}
	p.signals = append(p.signals, sig)
	if sig == syscall.SIGKILL || !p.ignoreTerm {
		p.alive = false
	}
	return nil
}
func (p *fakeProc) Alive() bool { return p.alive }
func (p *fakeProc) Release()    { p.released = true }

type fakeRunner struct {
	procs      map[int]*fakeProc
	opened     []uint64 // start ticks passed to OpenProcess
	units      []string
	containers []string
	restart    string
	stopErr    error
	docker     bool
}

func (r *fakeRunner) OpenProcess(pid int, ticks uint64) (actions.Process, error) {
	r.opened = append(r.opened, ticks)
	p, ok := r.procs[pid]
	if !ok || !p.alive {
		return nil, actions.ErrGone
	}
	return p, nil
}

func (r *fakeRunner) StopUnit(_ context.Context, u procs.Unit) error {
	r.units = append(r.units, u.Name)
	return r.stopErr
}

func (r *fakeRunner) RestartPolicy(context.Context, procs.Unit) (string, error) {
	return r.restart, nil
}

func (r *fakeRunner) StopContainer(_ context.Context, id string) error {
	r.containers = append(r.containers, id)
	return r.stopErr
}

func (r *fakeRunner) CanStopContainers() bool { return r.docker }

// killDetails extends fakeDetails with the nginx worker.
func killDetails(pid int) (*procs.Details, error) {
	if pid == 101 {
		return &procs.Details{PID: 101, Comm: "nginx", UID: 33, Cmdline: []string{"nginx: worker process"}, StartTicks: 1010}, nil
	}
	d, err := fakeDetails(pid)
	if d != nil {
		d.StartTicks = 1000
	}
	return d, err
}

func newKillApp(t *testing.T, r *fakeRunner) (*App, *gocui.Gui) {
	t.Helper()
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 160, Height: 40, OutputMode: gocui.OutputTrue})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	a, err := newApp(g, Options{
		Collector: staticCollector{testSnapshot()},
		Interval:  time.Hour,
		Details:   killDetails,
		Users:     testUsers{},
		Actions:   r,
		Host:      testHost,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Run background work inline and shorten the waits.
	a.async = func(work func() func() error) {
		if err := work()(); err != nil {
			t.Fatal(err)
		}
	}
	a.graceScale = 0.01
	snap := testSnapshot()
	snap.Privileged = true
	a.apply(snap, nil)
	draw(t, g)
	return a, g
}

// selectPort selects the first row listening on port with the given family.
func selectPort(t *testing.T, a *App, port uint16, fam sockets.Family) {
	t.Helper()
	a.model.Top()
	for i, e := range a.model.Rows() {
		if e.Local.Port() == port && e.Family == fam {
			a.model.Move(i)
			return
		}
	}
	t.Fatalf("no row for port %d", port)
}

func currentView(g *gocui.Gui) string {
	if v := g.CurrentView(); v != nil {
		return v.Name()
	}
	return ""
}

func refreshRequested(a *App) bool {
	select {
	case <-a.refreshReq:
		return true
	default:
		return false
	}
}

func TestKillConfirmAndTerminate(t *testing.T) {
	r := &fakeRunner{procs: map[int]*fakeProc{100: {pid: 100, alive: true}}, restart: "always"}
	a, g := newKillApp(t, r)
	selectPort(t, a, 80, sockets.IPv4)

	if err := press(t, a, viewList, 'x'); err != nil {
		t.Fatal(err)
	}
	out := draw(t, g)
	if currentView(g) != viewDialog {
		t.Fatalf("dialog not focused:\n%s", out)
	}
	for _, want := range []string{
		"Stop tcp 0.0.0.0:80", "Processes", "primary", "nginx: master process", "www-data", "nginx: worker process",
		"nginx.service", "Restart=always", "1. SIGTERM PID 100 nginx (primary)", "2. SIGTERM PID 101 nginx",
		"3. SIGTERM all 2 processes", "4. systemctl stop nginx.service", "5. Cancel",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dialog missing %q:\n%s", want, out)
		}
	}

	// Enter confirms the default: SIGTERM to the primary.
	if err := press(t, a, viewDialog, gocui.KeyEnter); err != nil {
		t.Fatal(err)
	}
	out = draw(t, g)
	p := r.procs[100]
	if fmt.Sprint(p.signals) != "[terminated]" || !p.released {
		t.Errorf("signals = %v, released = %v", p.signals, p.released)
	}
	if fmt.Sprint(r.opened) != "[1000]" {
		t.Errorf("OpenProcess start ticks = %v, want the ones shown in the dialog", r.opened)
	}
	if a.dialog.Active() || currentView(g) != viewList {
		t.Errorf("dialog should close after success (current %q)", currentView(g))
	}
	if !strings.Contains(out, "PID 100 exited after SIGTERM") {
		t.Errorf("status should report the result:\n%s", out)
	}
	if !refreshRequested(a) {
		t.Error("no refresh requested after the kill")
	}
	if a.kill != nil {
		t.Error("flow not cleaned up")
	}
}

func TestKillSurvivorThenSigkill(t *testing.T) {
	r := &fakeRunner{procs: map[int]*fakeProc{100: {pid: 100, alive: true, ignoreTerm: true}}, restart: "no"}
	a, g := newKillApp(t, r)
	selectPort(t, a, 80, sockets.IPv4)
	_ = press(t, a, viewList, 'x')
	_ = press(t, a, viewDialog, gocui.KeyEnter)

	out := draw(t, g)
	if !strings.Contains(out, "Still running") || !strings.Contains(out, "PID 100 is still running") || !strings.Contains(out, "1. SIGKILL PID 100") {
		t.Fatalf("survivor dialog:\n%s", out)
	}
	if currentView(g) != viewDialog {
		t.Fatal("survivor dialog not focused")
	}
	_ = press(t, a, viewDialog, gocui.KeyEnter) // SIGKILL is the default
	out = draw(t, g)
	p := r.procs[100]
	if fmt.Sprint(p.signals) != "[terminated killed]" || !p.released {
		t.Errorf("signals = %v, released = %v", p.signals, p.released)
	}
	if !strings.Contains(out, "PID 100 exited after SIGKILL") || a.dialog.Active() {
		t.Errorf("after SIGKILL:\n%s", out)
	}
}

func TestKillLeaveRunning(t *testing.T) {
	r := &fakeRunner{procs: map[int]*fakeProc{100: {pid: 100, alive: true, ignoreTerm: true}}}
	a, g := newKillApp(t, r)
	selectPort(t, a, 80, sockets.IPv4)
	_ = press(t, a, viewList, 'x')
	_ = press(t, a, viewDialog, gocui.KeyEnter)
	draw(t, g)
	_ = press(t, a, viewDialog, gocui.KeyEsc)
	draw(t, g)
	p := r.procs[100]
	if !p.alive || !p.released || a.kill != nil || currentView(g) != viewList {
		t.Errorf("Esc should leave the process alone and clean up: %+v, flow %v", p, a.kill)
	}
}

func TestKillSystemctl(t *testing.T) {
	r := &fakeRunner{procs: map[int]*fakeProc{100: {pid: 100, alive: true}}, restart: "on-failure"}
	a, g := newKillApp(t, r)
	selectPort(t, a, 80, sockets.IPv4)
	_ = press(t, a, viewList, 'x')
	draw(t, g)
	if err := press(t, a, viewDialog, '4'); err != nil {
		t.Fatal(err)
	}
	out := draw(t, g)
	if fmt.Sprint(r.units) != "[nginx.service]" || len(r.procs[100].signals) != 0 {
		t.Errorf("units = %v, signals = %v", r.units, r.procs[100].signals)
	}
	if !strings.Contains(out, "nginx.service stopped") || a.dialog.Active() {
		t.Errorf("after systemctl stop:\n%s", out)
	}

	// Failure without root: error dialog with the sudo hint.
	r.stopErr = fmt.Errorf("%w: Interactive authentication required.", actions.ErrPermission)
	snap := testSnapshot() // not privileged
	a.apply(snap, nil)
	selectPort(t, a, 80, sockets.IPv4)
	_ = press(t, a, viewList, 'x')
	draw(t, g)
	_ = press(t, a, viewDialog, '4')
	out = draw(t, g)
	for _, want := range []string{"Error", "Stopping nginx.service failed", "Interactive authentication", "Rerun portview with sudo"} {
		if !strings.Contains(out, want) {
			t.Errorf("error dialog missing %q:\n%s", want, out)
		}
	}
	_ = press(t, a, viewDialog, gocui.KeyEnter)
	draw(t, g)
	if a.dialog.Active() || a.kill != nil {
		t.Error("Enter should close the error and end the flow")
	}
}

func TestKillContainerDefault(t *testing.T) {
	r := &fakeRunner{procs: map[int]*fakeProc{}, docker: true}
	a, g := newKillApp(t, r)
	snap := testSnapshot()
	for i := range snap.Entries {
		if snap.Entries[i].Local.Port() == 631 {
			snap.Entries[i].Container = &inventory.ContainerRef{ID: "abc123", Name: "web-1", Runtime: "docker"}
		}
	}
	a.apply(snap, nil)
	selectPort(t, a, 631, sockets.IPv4)
	_ = press(t, a, viewList, 'x')
	out := draw(t, g)
	if !strings.Contains(out, "1. docker stop web-1") || !strings.Contains(out, "Container") {
		t.Fatalf("container dialog:\n%s", out)
	}
	_ = press(t, a, viewDialog, gocui.KeyEnter)
	out = draw(t, g)
	if fmt.Sprint(r.containers) != "[abc123]" || !strings.Contains(out, "container web-1 stopped") {
		t.Errorf("containers = %v:\n%s", r.containers, out)
	}
}

func TestKillNothingToStop(t *testing.T) {
	a, g := newKillApp(t, &fakeRunner{})
	a.apply(testSnapshot(), nil) // unprivileged: port 631's owner is hidden
	selectPort(t, a, 631, sockets.IPv4)
	_ = press(t, a, viewList, 'x')
	out := draw(t, g)
	if !strings.Contains(out, "not visible to you") || !strings.Contains(out, "Nothing to stop") {
		t.Errorf("hidden owner dialog:\n%s", out)
	}
	// Digits do nothing in a message box; Enter closes it.
	_ = press(t, a, viewDialog, '1')
	if !a.dialog.Active() {
		t.Error("a digit closed the message box")
	}
	_ = press(t, a, viewDialog, gocui.KeyEnter)
	draw(t, g)
	if a.dialog.Active() || currentView(g) != viewList {
		t.Error("Enter should close the message box")
	}
}

func TestKillAlreadyExited(t *testing.T) {
	r := &fakeRunner{procs: map[int]*fakeProc{}} // OpenProcess fails with ErrGone
	a, g := newKillApp(t, r)
	selectPort(t, a, 80, sockets.IPv4)
	_ = press(t, a, viewList, 'x')
	_ = press(t, a, viewDialog, gocui.KeyEnter)
	out := draw(t, g)
	if !strings.Contains(out, "PID 100 already exited") || a.dialog.Active() {
		t.Errorf("already exited:\n%s", out)
	}
}

func TestKillPermissionDenied(t *testing.T) {
	r := &fakeRunner{procs: map[int]*fakeProc{}}
	a, g := newKillApp(t, r)
	a.opts.Actions = &denyRunner{r}
	selectPort(t, a, 80, sockets.IPv4)
	a.apply(testSnapshot(), nil) // unprivileged
	_ = press(t, a, viewList, 'x')
	_ = press(t, a, viewDialog, gocui.KeyEnter)
	out := draw(t, g)
	if !strings.Contains(out, "Could not send SIGTERM") || !strings.Contains(out, "Rerun portview with sudo") {
		t.Errorf("permission error:\n%s", out)
	}
}

// denyRunner opens processes that refuse signals.
type denyRunner struct{ *fakeRunner }

func (d *denyRunner) OpenProcess(pid int, _ uint64) (actions.Process, error) {
	return &deniedProc{pid}, nil
}

type deniedProc struct{ pid int }

func (p *deniedProc) PID() int { return p.pid }
func (p *deniedProc) Signal(syscall.Signal) error {
	return fmt.Errorf("%w: cannot signal PID %d", actions.ErrPermission, p.pid)
}
func (p *deniedProc) Alive() bool { return true }
func (p *deniedProc) Release()    {}

func TestPlanBodyWrapsLongCommands(t *testing.T) {
	long := "python3 /tmp/" + strings.Repeat("very-long-directory-name/", 6) + "server.py --port 8080"
	f := &killFlow{plan: &actions.Plan{
		Entry:  testSnapshot().Entries[0],
		Owners: []actions.Owner{{PID: 4242, User: "alice", Command: long, Primary: true}},
	}}
	body := strings.Join(planBody(f), "\n")
	if !strings.Contains(body, "server.py --port 8080") {
		t.Errorf("end of the command lost:\n%s", body)
	}
	for _, l := range strings.Split(body, "\n") {
		if w := tui.StringWidth(l); w > 10+commandWidth {
			t.Errorf("line too wide (%d): %q", w, l)
		}
	}
}

func TestKillChooseSignal(t *testing.T) {
	r := &fakeRunner{procs: map[int]*fakeProc{100: {pid: 100, alive: true, ignoreTerm: true}}, restart: "no"}
	a, g := newKillApp(t, r)
	selectPort(t, a, 80, sockets.IPv4)
	_ = press(t, a, viewList, 'x')
	out := draw(t, g)
	if !strings.Contains(out, "Signal SIGTERM: asks the process to exit") || !strings.Contains(out, "s change signal") {
		t.Fatalf("signal footer:\n%s", out)
	}
	_ = press(t, a, viewDialog, 'j') // keep a non-default selection across the switch
	if err := press(t, a, viewDialog, 's'); err != nil {
		t.Fatal(err)
	}
	out = draw(t, g)
	for _, want := range []string{"1. SIGKILL PID 100 nginx (primary)", "3. SIGKILL all 2 processes", "4. systemctl stop nginx.service", "Signal SIGKILL: kills at once"} {
		if !strings.Contains(out, want) {
			t.Errorf("after s, missing %q:\n%s", want, out)
		}
	}
	if a.dialog.Selected() != 1 {
		t.Errorf("selection reset to %d", a.dialog.Selected())
	}
	_ = press(t, a, viewDialog, '1')
	out = draw(t, g)
	p := r.procs[100]
	if fmt.Sprint(p.signals) != "[killed]" {
		t.Errorf("signals = %v, want SIGKILL only", p.signals)
	}
	if !strings.Contains(out, "PID 100 exited after SIGKILL") || a.dialog.Active() {
		t.Errorf("after SIGKILL:\n%s", out)
	}
}

func TestKillSignalCycleAndScope(t *testing.T) {
	r := &fakeRunner{procs: map[int]*fakeProc{100: {pid: 100, alive: true, ignoreTerm: true}}}
	a, g := newKillApp(t, r)
	selectPort(t, a, 80, sockets.IPv4)
	_ = press(t, a, viewList, 'x')
	var seen []string
	for range 5 {
		_ = press(t, a, viewDialog, 's')
		seen = append(seen, actions.SignalName(a.kill.sig))
	}
	if got := strings.Join(seen, " "); got != "SIGKILL SIGINT SIGHUP SIGQUIT SIGTERM" {
		t.Errorf("cycle = %s", got)
	}
	_ = press(t, a, viewDialog, 's')
	_ = press(t, a, viewDialog, 's')
	_ = press(t, a, viewDialog, 's') // SIGHUP
	_ = press(t, a, viewDialog, gocui.KeyEnter)
	out := draw(t, g)
	if fmt.Sprint(r.procs[100].signals) != "[hangup]" || !strings.Contains(out, "after SIGHUP.") {
		t.Fatalf("signals = %v:\n%s", r.procs[100].signals, out)
	}
	// s does nothing outside the confirmation step.
	_ = press(t, a, viewDialog, 's')
	if a.kill.sig != syscall.SIGHUP || !strings.Contains(draw(t, g), "Still running") {
		t.Error("s changed the survivor dialog")
	}
	// "Wait 2s more" waits for the same signal's effect.
	_ = press(t, a, viewDialog, '2')
	if out := draw(t, g); !strings.Contains(out, "after SIGHUP") {
		t.Errorf("wait more:\n%s", out)
	}
}
