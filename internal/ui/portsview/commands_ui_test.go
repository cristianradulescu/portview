package portsview

import (
	"strings"
	"testing"

	"github.com/jesseduffield/gocui"

	"github.com/cristianradulescu/portview/internal/sockets"
	"github.com/cristianradulescu/portview/internal/ui/tui"
)

// viewText is a view's whole content (not just the visible part), without
// colors.
func viewText(t *testing.T, g *gocui.Gui, name string) string {
	t.Helper()
	v, err := g.View(name)
	if err != nil {
		t.Fatalf("view %s: %v", name, err)
	}
	return tui.StripANSI(v.Buffer())
}

// logged is the command log as "cmd  # desc" lines.
func logged(a *App) string {
	var sb strings.Builder
	for _, op := range a.log.ops {
		sb.WriteString(op.Cmd + "  # " + op.Desc + "\n")
	}
	return sb.String()
}

// fakeClipboard records copies.
type fakeClipboard struct{ copied []string }

func (c *fakeClipboard) copy(text string) (string, error) {
	c.copied = append(c.copied, text)
	return "fake", nil
}

func TestEquivalentCommandsInDetails(t *testing.T) {
	a, g := newKillApp(t, &fakeRunner{})
	selectPort(t, a, 80, sockets.IPv4)
	draw(t, g)
	details := viewText(t, g, viewDetails)
	for _, want := range []string{
		"Equivalent commands", "# this list (host network namespace)", "$ sudo ss -tulnp",
		"$ sudo ss -tlnpe 'sport = :80'", "/proc/net/tcp", `socket:\[801\]`,
		"$ ps -o pid,ppid,user,lstart,etime,args -p 100", "$ systemctl status -- nginx.service",
	} {
		if !strings.Contains(details, want) {
			t.Errorf("details missing %q:\n%s", want, details)
		}
	}
	// Filters change the list command.
	_ = press(t, a, viewList, 't')
	draw(t, g)
	if details := viewText(t, g, viewDetails); !strings.Contains(details, "$ sudo ss -tlnp\n") {
		t.Errorf("after t, list command not TCP only:\n%s", details)
	}
}

func TestCommandLog(t *testing.T) {
	a, g := newKillApp(t, &fakeRunner{})
	draw(t, g)
	log := viewText(t, g, viewLog)
	for _, want := range []string{"$ sudo ss -tuxanp", "# sockets of the host network namespace", "details of PID 100"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
	// Refreshes don't repeat what was already logged.
	n := len(a.log.ops)
	snap := testSnapshot()
	snap.Privileged = true
	a.apply(snap, nil)
	draw(t, g)
	if len(a.log.ops) != n {
		t.Errorf("refresh added %d log entries: %+v", len(a.log.ops)-n, a.log.ops[n:])
	}

	// Tab, Tab reaches the log; y copies the selected entry.
	cb := &fakeClipboard{}
	a.opts.Clipboard = cb.copy
	_ = press(t, a, viewList, gocui.KeyTab)
	_ = press(t, a, viewDetails, gocui.KeyTab)
	draw(t, g)
	if currentView(g) != viewLog {
		t.Fatalf("focus = %q, want the log", currentView(g))
	}
	_ = press(t, a, viewLog, 'g') // oldest: the namespace scan
	_ = press(t, a, viewLog, 'y')
	if len(cb.copied) != 1 || cb.copied[0] != "sudo ss -tuxanp" {
		t.Errorf("copied %q", cb.copied)
	}
	if out := draw(t, g); !strings.Contains(out, "copied (fake): sudo ss -tuxanp") {
		t.Errorf("no confirmation in the status bar:\n%s", out)
	}
	if a.log.follow {
		t.Error("moving to the oldest entry must stop following")
	}
	_ = press(t, a, viewLog, 'G')
	if !a.log.follow || a.log.sel != len(a.log.ops)-1 {
		t.Error("G must follow the newest entry")
	}
	_ = press(t, a, viewLog, gocui.KeyEsc)
	draw(t, g)
	if currentView(g) != viewList {
		t.Error("Esc should return to the list")
	}
}

func TestCopyPicker(t *testing.T) {
	a, g := newKillApp(t, &fakeRunner{})
	cb := &fakeClipboard{}
	a.opts.Clipboard = cb.copy
	selectPort(t, a, 80, sockets.IPv4)
	_ = press(t, a, viewList, 'y')
	out := draw(t, g)
	if !strings.Contains(out, "Copy a command") || !strings.Contains(out, "1. this list") || !strings.Contains(out, "$ sudo ss -tulnp") {
		t.Fatalf("picker:\n%s", out)
	}
	_ = press(t, a, viewDialog, '2') // this socket
	if len(cb.copied) != 1 || cb.copied[0] != "sudo ss -tlnpe 'sport = :80'" {
		t.Errorf("copied %q", cb.copied)
	}
	if a.dialog.Active() {
		t.Error("picker still open")
	}
}

func TestKillShowsAndLogsCommands(t *testing.T) {
	r := &fakeRunner{procs: map[int]*fakeProc{100: {pid: 100, alive: true}}, restart: "always"}
	a, g := newKillApp(t, r)
	selectPort(t, a, 80, sockets.IPv4)
	_ = press(t, a, viewList, 'x')
	out := draw(t, g)
	if !strings.Contains(out, "$ kill -TERM 100") {
		t.Errorf("dialog should show the selected option's command:\n%s", out)
	}
	_ = press(t, a, viewDialog, 's')
	_ = press(t, a, viewDialog, 'j')
	_ = press(t, a, viewDialog, 'j') // all processes
	if out := draw(t, g); !strings.Contains(out, "$ kill -KILL 100 101") {
		t.Errorf("command must follow the signal and selection:\n%s", out)
	}
	_ = press(t, a, viewDialog, 'j') // systemctl stop
	if out := draw(t, g); !strings.Contains(out, "$ systemctl --no-ask-password stop -- nginx.service") {
		t.Errorf("systemctl command:\n%s", out)
	}
	_ = press(t, a, viewDialog, gocui.KeyEnter)
	draw(t, g)
	log := logged(a)
	for _, want := range []string{
		"systemctl --no-ask-password show --property=Restart --value -- nginx.service  # whether systemd restarts",
		"systemctl --no-ask-password stop -- nginx.service  # stop nginx.service",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}

	// A signal is logged as kill.
	a.apply(testSnapshot(), nil)
	selectPort(t, a, 80, sockets.IPv4)
	_ = press(t, a, viewList, 'x')
	_ = press(t, a, viewDialog, gocui.KeyEnter)
	draw(t, g)
	if log := logged(a); !strings.Contains(log, "kill -TERM 100  # send SIGTERM to PID 100") {
		t.Errorf("signal not logged:\n%s", log)
	}
}
