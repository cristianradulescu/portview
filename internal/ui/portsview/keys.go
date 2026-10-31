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
			Label: "4", Description: "IPv4 only (again: both)", Section: filt, Hint: "4/6 ip"},
		tui.Binding{View: viewList, Keys: []any{'6'}, Handler: do(func() { a.toggleFamily(sockets.IPv6) }),
			Label: "6", Description: "IPv6 only (again: both)", Section: filt},

		tui.Binding{View: viewList, Keys: []any{'r'}, Handler: do(a.requestRefresh),
			Label: "r", Description: "refresh now", Section: gen, Hint: "r refresh"},
		tui.Binding{View: viewList, Keys: []any{'?'}, Handler: a.openHelp,
			Label: "?", Description: "this help", Section: gen, Hint: "? help"},
		tui.Binding{View: viewList, Keys: []any{'q'}, Handler: quit,
			Label: "q", Description: "quit", Section: gen, Hint: "q quit"},
		tui.Binding{View: "", Keys: []any{gocui.KeyCtrlC}, Handler: quit,
			Label: "^c", Description: "quit", Section: gen},
	)
	a.keys.Add(a.prompt.Bindings()...)
	a.keys.Add(a.help.Bindings(a.g, viewList, gocui.KeyEsc, 'q', '?')...)
}

func quit() error { return gocui.ErrQuit }

func helpText(k *tui.Keymap) string {
	var sb strings.Builder
	sb.WriteString(k.Help(viewList, ""))
	sb.WriteString("\n" + tui.Bold("Exposure") + "\n")
	for _, x := range []inventory.Exposure{inventory.ExposureAll, inventory.ExposureInterface, inventory.ExposureLoopback, inventory.ExposureNamespace} {
		sb.WriteString("  " + tui.Color(tui.PadRight(x.String(), 9), exposureColor(x)) + exposureDescription(x) + "\n")
	}
	sb.WriteString("\n" + tui.Bold("Columns") + "\n")
	sb.WriteString("  PID 812+4  socket shared by 5 processes; 812 is the primary (master)\n")
	sb.WriteString("  PID ?      owner not visible to you: rerun with sudo\n")
	sb.WriteString("  PID -      no owning process (TIME-WAIT, kernel socket)\n")
	sb.WriteString("  bold row   appeared in the last refresh\n")
	return sb.String()
}
