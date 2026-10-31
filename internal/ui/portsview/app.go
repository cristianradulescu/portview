package portsview

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jesseduffield/gocui"

	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/sockets"
	"github.com/cristianradulescu/portview/internal/ui/tui"
)

// View names.
const (
	viewList    = "sockets"
	viewDetails = "details"
	viewStatus  = "status"
	viewFilter  = "filter"
	viewHelp    = "help"
)

// detailsMinScreen is the screen width below which the details pane is
// hidden to leave room for the list.
const detailsMinScreen = 110

// Collector produces snapshots; *inventory.Collector implements it.
type Collector interface {
	Collect(ctx context.Context) (*inventory.Snapshot, error)
}

// Options configure the TUI.
type Options struct {
	Collector Collector
	// Interval between automatic refreshes.
	Interval time.Duration
	// Filter is the initial filter.
	Filter inventory.Filter
}

// App is the portview TUI.
type App struct {
	opts  Options
	g     *gocui.Gui
	model *Model
	keys  tui.Keymap

	prompt *tui.Prompt
	help   *tui.Popup

	refreshed  time.Time
	refreshReq chan struct{}
}

// Run starts the TUI and blocks until the user quits or ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	g, err := gocui.NewGui(gocui.NewGuiOpts{OutputMode: gocui.OutputTrue})
	if err != nil {
		return err
	}
	defer g.Close()

	a, err := newApp(g, opts)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go a.refreshLoop(ctx)
	go func() {
		<-ctx.Done()
		g.Update(func(*gocui.Gui) error { return gocui.ErrQuit })
	}()

	// MainLoop only returns on error; ErrQuit is the normal exit.
	if err := g.MainLoop(); !errors.Is(err, gocui.ErrQuit) {
		return err
	}
	return nil
}

func newApp(g *gocui.Gui, opts Options) (*App, error) {
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second
	}
	a := &App{
		opts:       opts,
		g:          g,
		model:      NewModel(opts.Filter),
		refreshReq: make(chan struct{}, 1),
	}
	a.prompt = &tui.Prompt{
		Name:     viewFilter,
		Title:    " Filter: port, address, process, PID, user · Enter apply · Esc clear ",
		OnChange: a.setFilterText,
		OnSubmit: func(string) error { return a.prompt.Close(g, viewList) },
		OnCancel: func() error {
			a.setFilterText("")
			return a.prompt.Close(g, viewList)
		},
	}
	a.help = &tui.Popup{Name: viewHelp, Title: " Help · Esc to close "}

	g.Highlight = true
	g.SelFrameColor = gocui.ColorGreen
	g.SelFgColor = gocui.ColorGreen
	g.SetManager(gocui.ManagerFunc(a.layout))
	a.registerKeys()
	if err := a.keys.Apply(g); err != nil {
		return nil, err
	}
	return a, nil
}

// --- refresh -------------------------------------------------------------

func (a *App) refreshLoop(ctx context.Context) {
	t := time.NewTicker(a.opts.Interval)
	defer t.Stop()
	for {
		snap, err := a.opts.Collector.Collect(ctx)
		if ctx.Err() != nil {
			return
		}
		a.g.Update(func(*gocui.Gui) error {
			a.apply(snap, err)
			return nil
		})
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.refreshReq:
			t.Reset(a.opts.Interval)
		}
	}
}

// apply installs a collection result. It runs on the UI goroutine.
func (a *App) apply(snap *inventory.Snapshot, err error) {
	if err != nil {
		a.model.SetError(err)
		return
	}
	a.model.SetSnapshot(snap)
	a.refreshed = snap.Taken
}

func (a *App) requestRefresh() {
	select {
	case a.refreshReq <- struct{}{}:
	default: // one is already pending
	}
}

// --- layout --------------------------------------------------------------

func (a *App) layout(g *gocui.Gui) error {
	maxX, maxY := g.Size()
	if maxX < 20 || maxY < 6 {
		return nil
	}
	bottom := maxY - 2
	listX1 := maxX - 1
	showDetails := maxX >= detailsMinScreen
	if showDetails {
		listX1 = maxX - min(64, maxX*2/5) - 1
	}

	list, created, err := tui.SetView(g, viewList, tui.Rect{X0: 0, Y0: 0, X1: listX1, Y1: bottom})
	if err != nil {
		return err
	}
	if created {
		list.Highlight = true
		list.HighlightInactive = true
		list.SelBgColor = gocui.ColorBlue
		list.InactiveViewSelBgColor = gocui.Get256Color(238)
		if _, err := g.SetCurrentView(viewList); err != nil {
			return err
		}
	}
	a.renderList(list)

	if showDetails {
		d, created, err := tui.SetView(g, viewDetails, tui.Rect{X0: listX1 + 1, Y0: 0, X1: maxX - 1, Y1: bottom})
		if err != nil {
			return err
		}
		if created {
			d.Title = " Details "
			d.Wrap = true
		}
		d.Clear()
		d.WriteString(renderDetails(a.model))
	} else if err := tui.DeleteView(g, viewDetails); err != nil {
		return err
	}

	status, created, err := tui.SetView(g, viewStatus, tui.Rect{X0: 0, Y0: maxY - 2, X1: maxX, Y1: maxY})
	if err != nil {
		return err
	}
	if created {
		status.Frame = false
	}
	status.Clear()
	status.WriteString(tui.JoinStatus(statusLeft(a.model, a.refreshed), a.keys.Hints(viewList), maxX-1))

	if err := a.prompt.Layout(g, tui.Rect{X0: 0, Y0: bottom - 2, X1: listX1, Y1: bottom}); err != nil {
		return err
	}
	return a.help.Layout(g)
}

func (a *App) renderList(v *gocui.View) {
	w, h := v.InnerSize()
	a.model.SetHeight(h - 1) // first line is the header
	// gocui leaves focus tracking to the app: dim the selection when
	// another view (prompt, help) has the focus.
	v.HighlightInactive = a.g.CurrentView() != v
	v.Title = " Sockets "
	if snap := a.model.Snapshot(); snap != nil {
		v.Title = " Sockets " + strconv.Itoa(len(a.model.Rows())) + "/" + strconv.Itoa(a.model.Total()) + " "
	}
	v.Clear()
	v.WriteString(renderList(a.model, w))
	l := a.model.List()
	v.SetOrigin(0, 0)
	v.Highlight = l.Selected >= 0
	if l.Selected >= 0 {
		v.SetCursor(0, 1+l.Selected-l.Origin)
	} else {
		v.SetCursor(0, 0)
	}
}

// --- actions -------------------------------------------------------------

func (a *App) setFilterText(text string) {
	a.model.UpdateFilter(func(f *inventory.Filter) { f.Text = text })
}

func (a *App) setState(s inventory.StateFilter) {
	a.model.UpdateFilter(func(f *inventory.Filter) { f.State = s })
}

// toggleProto shows only p, or both protocols if only p is already shown.
func (a *App) toggleProto(p sockets.Proto) {
	a.model.UpdateFilter(func(f *inventory.Filter) {
		if f.OnlyProto == p {
			f.OnlyProto = 0
		} else {
			f.OnlyProto = p
		}
	})
}

// toggleFamily shows only fam, or both families if only fam is shown.
func (a *App) toggleFamily(fam sockets.Family) {
	a.model.UpdateFilter(func(f *inventory.Filter) {
		if f.OnlyFamily == fam {
			f.OnlyFamily = 0
		} else {
			f.OnlyFamily = fam
		}
	})
}

func (a *App) openFilter() error {
	a.prompt.Open(a.model.Filter().Text)
	// Lay out now rather than on the next flush: gocui handles all queued
	// key events before flushing, and keys typed right after "/" must
	// already reach the prompt.
	return a.layout(a.g)
}

func (a *App) openHelp() error {
	a.help.Open(helpText(&a.keys))
	return a.layout(a.g)
}
