package portsview

import (
	"strings"

	"github.com/jesseduffield/gocui"

	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/sockets"
	"github.com/cristianradulescu/portview/internal/ui/tui"
)

func (a *App) registerKeys() {
	do := func(f func()) func() error { return func() error { f(); return nil } }
	m := a.model
	const nav, filt, gen = "Navigation", "Filters", "General"

	a.keys.Add(
		tui.Binding{View: viewList, Keys: []any{'j', gocui.KeyArrowDown}, Handler: do(func() { m.Move(1) }),
			Label: "j/↓", Description: "next socket", Section: nav},
		tui.Binding{View: viewList, Keys: []any{'k', gocui.KeyArrowUp}, Handler: do(func() { m.Move(-1) }),
			Label: "k/↑", Description: "previous socket", Section: nav},
		tui.Binding{View: viewList, Keys: []any{gocui.KeyPgdn, gocui.KeyCtrlD}, Handler: do(func() { m.Page(1) }),
			Label: "PgDn/^d", Description: "page down", Section: nav},
		tui.Binding{View: viewList, Keys: []any{gocui.KeyPgup, gocui.KeyCtrlU}, Handler: do(func() { m.Page(-1) }),
			Label: "PgUp/^u", Description: "page up", Section: nav},
		tui.Binding{View: viewList, Keys: []any{'g', gocui.KeyHome}, Handler: do(m.Top),
			Label: "g/Home", Description: "first socket", Section: nav},
		tui.Binding{View: viewList, Keys: []any{'G', gocui.KeyEnd}, Handler: do(m.Bottom),
			Label: "G/End", Description: "last socket", Section: nav},

		tui.Binding{View: viewList, Keys: []any{'/'}, Handler: a.openFilter,
			Label: "/", Description: "filter by port, address, process, PID or user", Section: filt, Hint: "/ filter"},
		tui.Binding{View: viewList, Keys: []any{gocui.KeyEsc}, Handler: do(func() { a.setFilterText("") }),
			Label: "Esc", Description: "clear the text filter", Section: filt},
		tui.Binding{View: viewList, Keys: []any{'l'}, Handler: do(func() { a.setState(inventory.StateListening) }),
			Label: "l", Description: "listening sockets (TCP LISTEN, UDP unconnected)", Section: filt, Hint: "l/e/a state"},
		tui.Binding{View: viewList, Keys: []any{'e'}, Handler: do(func() { a.setState(inventory.StateEstablished) }),
			Label: "e", Description: "established connections", Section: filt},
		tui.Binding{View: viewList, Keys: []any{'a'}, Handler: do(func() { a.setState(inventory.StateAll) }),
			Label: "a", Description: "all sockets", Section: filt},
		tui.Binding{View: viewList, Keys: []any{'t'}, Handler: do(func() { a.toggleProto(sockets.TCP) }),
			Label: "t", Description: "TCP only (again: TCP and UDP)", Section: filt, Hint: "t/u proto"},
		tui.Binding{View: viewList, Keys: []any{'u'}, Handler: do(func() { a.toggleProto(sockets.UDP) }),
			Label: "u", Description: "UDP only (again: TCP and UDP)", Section: filt},
		tui.Binding{View: viewList, Keys: []any{'4'}, Handler: do(func() { a.toggleFamily(sockets.IPv4) }),
			Label: "4", Description: "IPv4 only (again: both)", Section: filt},
		tui.Binding{View: viewList, Keys: []any{'6'}, Handler: do(func() { a.toggleFamily(sockets.IPv6) }),
			Label: "6", Description: "IPv6 only (again: both)", Section: filt},

		tui.Binding{View: viewList, Keys: []any{gocui.KeyTab}, Handler: func() error { return a.focus(viewDetails) },
			Label: "Tab", Description: "focus the details pane (scroll with j/k)", Section: nav, Hint: "Tab details"},

		tui.Binding{View: viewList, Keys: []any{'i'}, Handler: a.openInspect,
			Label: "i", Description: "inspect: the socket's raw kernel line (/proc/net/*), every field explained", Section: gen},
		tui.Binding{View: viewList, Keys: []any{'y'}, Handler: a.openCopy,
			Label: "y", Description: "copy an equivalent shell command (also shown in the details pane)", Section: gen},
		tui.Binding{View: viewList, Keys: []any{'x'}, Handler: a.openKill,
			Label: "x", Description: "stop what holds the socket: SIGTERM/SIGKILL, systemctl stop, docker stop", Section: gen, Hint: "x kill"},
		tui.Binding{View: viewList, Keys: []any{'r'}, Handler: do(a.requestRefresh),
			Label: "r", Description: "refresh now", Section: gen, Hint: "r refresh"},
		tui.Binding{View: viewList, Keys: []any{'?'}, Handler: a.openHelp,
			Label: "?", Description: "this help", Section: gen, Hint: "? help"},
		tui.Binding{View: viewList, Keys: []any{'q'}, Handler: quit,
			Label: "q", Description: "quit", Section: gen, Hint: "q quit"},
		tui.Binding{View: "", Keys: []any{gocui.KeyCtrlC}, Handler: quit,
			Label: "^c", Description: "quit", Section: gen},
	)
	// The events tab shares the filter and general keys of the list.
	shared := map[any]bool{'/': true, gocui.KeyEsc: true, 'l': true, 'e': true, 'a': true, 't': true, 'u': true,
		'4': true, '6': true, gocui.KeyTab: true, 'y': true, 'r': true, '?': true, 'q': true}
	ipOnly := map[any]bool{'t': true, 'u': true, '4': true, '6': true}
	for _, b := range a.keys.Bindings() {
		if b.View != viewList || len(b.Keys) == 0 || !shared[b.Keys[0]] {
			continue
		}
		b.Label, b.Description = "", "" // listed once in the help
		ev := b
		ev.View = viewEvents
		a.keys.Add(ev)
		if !ipOnly[b.Keys[0]] {
			ux := b
			ux.View = viewUnix
			a.keys.Add(ux)
		}
	}
	const evSec = "Events tab"
	a.keys.Add(
		tui.Binding{View: "", Keys: []any{']'}, Handler: func() error { return a.cycleTab(1) },
			Label: "[ ]", Description: "previous / next tab: Sockets, Unix, Events", Section: nav},
		tui.Binding{View: "", Keys: []any{'['}, Handler: func() error { return a.cycleTab(-1) }},
		tui.Binding{View: viewEvents, Hint: "[ ] tabs"}, // hint only: the keys are global
		tui.Binding{View: viewUnix, Keys: []any{'j', gocui.KeyArrowDown}, Handler: do(func() { a.moveUnix(1) }),
			Label: "j/k", Description: "next / previous Unix socket", Section: "Unix tab"},
		tui.Binding{View: viewUnix, Keys: []any{'k', gocui.KeyArrowUp}, Handler: do(func() { a.moveUnix(-1) })},
		tui.Binding{View: viewUnix, Keys: []any{gocui.KeyPgdn, gocui.KeyCtrlD}, Handler: do(func() { a.moveUnix(a.ux.height - 1) })},
		tui.Binding{View: viewUnix, Keys: []any{gocui.KeyPgup, gocui.KeyCtrlU}, Handler: do(func() { a.moveUnix(1 - a.ux.height) })},
		tui.Binding{View: viewUnix, Keys: []any{'g', gocui.KeyHome}, Handler: do(func() { a.moveUnix(-1 << 30) })},
		tui.Binding{View: viewUnix, Keys: []any{'G', gocui.KeyEnd}, Handler: do(func() { a.moveUnix(1 << 30) })},
		tui.Binding{View: viewUnix, Keys: []any{'i'}, Handler: a.openInspect, Hint: "i inspect"},
		tui.Binding{View: viewUnix, Hint: "[ ] tabs"}, // hint only: the keys are global
		tui.Binding{View: viewEvents, Keys: []any{'j', gocui.KeyArrowDown}, Handler: do(func() { a.moveEvent(1) }),
			Label: "j/k", Description: "next / previous event (newest first)", Section: evSec},
		tui.Binding{View: viewEvents, Keys: []any{'k', gocui.KeyArrowUp}, Handler: do(func() { a.moveEvent(-1) })},
		tui.Binding{View: viewEvents, Keys: []any{gocui.KeyPgdn, gocui.KeyCtrlD}, Handler: do(func() { a.moveEvent(a.ev.height - 1) })},
		tui.Binding{View: viewEvents, Keys: []any{gocui.KeyPgup, gocui.KeyCtrlU}, Handler: do(func() { a.moveEvent(1 - a.ev.height) })},
		tui.Binding{View: viewEvents, Keys: []any{'g', gocui.KeyHome}, Handler: do(func() { a.moveEvent(-len(a.ev.events)) }),
			Label: "g", Description: "newest event (and follow new ones)", Section: evSec},
		tui.Binding{View: viewEvents, Keys: []any{'G', gocui.KeyEnd}, Handler: do(func() { a.moveEvent(len(a.ev.events)) })},
		tui.Binding{View: viewEvents, Keys: []any{gocui.KeyEnter}, Handler: a.jumpToSocket,
			Label: "Enter", Description: "show the event's socket in the list, if it is still open", Section: evSec, Hint: "Enter socket"},
	)

	// Details pane (focused with Tab).
	toList := func() error { return a.focus(a.leftView()) }
	a.keys.Add(
		tui.Binding{View: viewDetails, Keys: []any{'j', gocui.KeyArrowDown}, Handler: do(func() { a.scrollDetails(1) }),
			Label: "j/k", Description: "scroll", Section: "Details pane"},
		tui.Binding{View: viewDetails, Keys: []any{'k', gocui.KeyArrowUp}, Handler: do(func() { a.scrollDetails(-1) })},
		tui.Binding{View: viewDetails, Keys: []any{gocui.KeyPgdn, gocui.KeyCtrlD}, Handler: do(func() { a.scrollDetails(10) }),
			Label: "PgDn/PgUp", Description: "scroll a page", Section: "Details pane"},
		tui.Binding{View: viewDetails, Keys: []any{gocui.KeyPgup, gocui.KeyCtrlU}, Handler: do(func() { a.scrollDetails(-10) })},
		tui.Binding{View: viewDetails, Keys: []any{'g', gocui.KeyHome}, Handler: do(func() { a.detailsOrigin = 0 })},
		tui.Binding{View: viewDetails, Keys: []any{'G', gocui.KeyEnd}, Handler: do(func() { a.detailsOrigin = a.detailsMax })},
		tui.Binding{View: viewDetails, Keys: []any{gocui.KeyTab}, Handler: func() error {
			if a.logShown {
				return a.focus(viewLog)
			}
			return toList()
		}, Label: "Tab", Description: "focus the command log (or back to the list)", Section: "Details pane", Hint: "Tab log"},
		tui.Binding{View: viewDetails, Keys: []any{gocui.KeyEsc}, Handler: toList,
			Label: "Esc", Description: "back to the socket list", Section: "Details pane", Hint: "Esc back"},
		tui.Binding{View: viewDetails, Keys: []any{'y'}, Handler: a.openCopy, Hint: "y copy"},
		tui.Binding{View: viewDetails, Keys: []any{'i'}, Handler: a.openInspect, Hint: "i inspect"},
		tui.Binding{View: viewDetails, Keys: []any{'x'}, Handler: a.openKill, Hint: "x kill"},
		tui.Binding{View: viewDetails, Keys: []any{'?'}, Handler: a.openHelp, Hint: "? help"},
		tui.Binding{View: viewDetails, Keys: []any{'q'}, Handler: quit, Hint: "q quit"},
	)
	// Command log (focused with Tab from the details pane).
	const logSec = "Command log"
	a.keys.Add(
		tui.Binding{View: viewLog, Keys: []any{'j', gocui.KeyArrowDown}, Handler: do(func() { a.moveLog(1) }),
			Label: "j/k", Description: "select a command", Section: logSec},
		tui.Binding{View: viewLog, Keys: []any{'k', gocui.KeyArrowUp}, Handler: do(func() { a.moveLog(-1) })},
		tui.Binding{View: viewLog, Keys: []any{gocui.KeyPgdn, gocui.KeyCtrlD}, Handler: do(func() { a.moveLog(5) })},
		tui.Binding{View: viewLog, Keys: []any{gocui.KeyPgup, gocui.KeyCtrlU}, Handler: do(func() { a.moveLog(-5) })},
		tui.Binding{View: viewLog, Keys: []any{'g', gocui.KeyHome}, Handler: do(func() { a.moveLog(-len(a.log.ops)) }),
			Label: "g/G", Description: "oldest / newest (G follows new entries)", Section: logSec},
		tui.Binding{View: viewLog, Keys: []any{'G', gocui.KeyEnd}, Handler: do(func() { a.moveLog(len(a.log.ops)) })},
		tui.Binding{View: viewLog, Keys: []any{'y'}, Handler: a.copyLog,
			Label: "y", Description: "copy the selected command", Section: logSec, Hint: "y copy"},
		tui.Binding{View: viewLog, Keys: []any{gocui.KeyTab, gocui.KeyEsc}, Handler: toList,
			Label: "Tab/Esc", Description: "back to the socket list", Section: logSec, Hint: "Tab/Esc back"},
		tui.Binding{View: viewLog, Keys: []any{'?'}, Handler: a.openHelp, Hint: "? help"},
		tui.Binding{View: viewLog, Keys: []any{'q'}, Handler: quit, Hint: "q quit"},
	)
	a.keys.Add(a.prompt.Bindings()...)
	a.keys.Add(a.help.Bindings(a.g, viewList, gocui.KeyEsc, 'q', '?')...)
	a.keys.Add(a.inspect.Bindings(a.g, viewList, gocui.KeyEsc, 'q', 'i')...)
	a.keys.Add(a.dialog.Bindings(a.g, viewList)...)
	a.keys.Add(tui.Binding{View: viewDialog, Keys: []any{'s'}, Handler: a.cycleSignal,
		Label: "s", Description: "in the kill dialog: change the signal (SIGTERM, SIGKILL, SIGINT, SIGHUP, SIGQUIT)", Section: "General"})
}

func quit() error { return gocui.ErrQuit }

func helpText(k *tui.Keymap) string {
	var sb strings.Builder
	sb.WriteString(k.Help(viewList, viewUnix, viewEvents, viewDetails, viewLog, viewDialog, ""))
	sb.WriteString("\n" + tui.Bold("Exposure") + "\n")
	for _, x := range []inventory.Exposure{inventory.ExposureAll, inventory.ExposureInterface, inventory.ExposureLoopback, inventory.ExposureNamespace} {
		sb.WriteString("  " + tui.Color(tui.PadRight(x.String(), 9), exposureColor(x)) + exposureDescription(x) + "\n")
	}
	sb.WriteString("\n" + tui.Bold("Columns") + "\n")
	sb.WriteString("  PID 812+4  socket shared by 5 processes; 812 is the primary (master)\n")
	sb.WriteString("  PID ?      owner not visible to you: rerun with sudo\n")
	sb.WriteString("  PID -      no owning process (TIME-WAIT, kernel socket)\n")
	sb.WriteString("  CONTAINER  web-1      socket inside container web-1\n")
	sb.WriteString("             →web-1:80  host port published by web-1 (docker-proxy forwards to its port 80)\n")
	sb.WriteString("  bold row   appeared in the last refresh\n")
	return sb.String()
}
