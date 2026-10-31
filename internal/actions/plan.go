package actions

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"syscall"

	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
)

// Kind is what an Option does.
type Kind int

const (
	// Signal sends a signal (SIGTERM by default) to Option.Owners, waits,
	// and offers SIGKILL for the ones still running.
	Signal Kind = iota
	// StopUnit runs `systemctl stop` on Option.Unit.
	StopUnit
	// StopContainer stops Option.Container through the Docker API.
	StopContainer
)

// Owner is a process holding the socket, as confirmed by the user.
type Owner struct {
	PID        int
	Comm       string
	UID        int
	User       string
	Command    string // quoted command line, or "[comm]"
	StartTicks uint64 // identity check before signalling
	Unit       procs.Unit
	Primary    bool
}

// Label is "PID 812 nginx".
func (o *Owner) Label() string { return fmt.Sprintf("PID %d %s", o.PID, o.Comm) }

// Option is one choice offered in the kill dialog.
type Option struct {
	Kind      Kind
	Label     string
	Owners    []Owner // Signal
	Unit      procs.Unit
	Container *inventory.ContainerRef
}

// Text is the option as shown, with sig for Signal options, e.g.
// "SIGTERM PID 812 nginx".
func (o *Option) Text(sig syscall.Signal) string {
	if o.Kind == Signal {
		return SignalName(sig) + " " + o.Label
	}
	return o.Label
}

// Plan describes what can be done about one socket: the processes holding
// it, the unit and container they belong to, warnings, and the options to
// offer. It is built once, when the dialog opens.
type Plan struct {
	Entry inventory.Entry
	// Owners holds the processes that can be signalled, primary first.
	Owners    []Owner
	Unit      procs.Unit // service of the primary owner, if any
	Container *inventory.ContainerRef
	Notes     []string
	Options   []Option
	// Default is the index of the preselected option; -1 when there are
	// no options.
	Default int
}

// MaxOwnerOptions caps the number of per-process options; the rest are
// still covered by "all processes".
const MaxOwnerOptions = 6

// PlanInput is what NewPlan works from.
type PlanInput struct {
	Entry *inventory.Entry
	// Details reads one process; its StartTicks become the identity that
	// is checked before signalling.
	Details func(pid int) (*procs.Details, error)
	Users   inventory.UserResolver
	// Privileged is true when running as root.
	Privileged bool
	// CanStopContainers is true when the Docker API is configured.
	CanStopContainers bool
}

// NewPlan decides what to offer for in.Entry.
func NewPlan(in PlanInput) *Plan {
	e := in.Entry
	p := &Plan{Entry: *e, Container: e.Container, Default: -1}
	note := func(format string, args ...any) { p.Notes = append(p.Notes, fmt.Sprintf(format, args...)) }

	// --- owners ---
	owners := slices.Clone(e.Owners)
	slices.SortStableFunc(owners, func(a, b *procs.Process) int {
		switch {
		case a == e.Primary:
			return -1
		case b == e.Primary:
			return 1
		}
		return a.PID - b.PID
	})
	gone := 0
	for _, op := range owners {
		switch {
		case op.PID == 1:
			note("PID 1 (%s) holds this socket for socket activation. Stop the matching .socket unit with systemctl to close it.", op.Comm)
			continue
		case isUserManager(op):
			note("The systemd user manager (PID %d) holds this socket for socket activation. Stop the matching .socket unit with systemctl --user.", op.PID)
			continue
		}
		d, err := in.Details(op.PID)
		if err != nil || d.Comm != op.Comm {
			// Exited, or the PID was reused since the snapshot.
			if err == nil || errors.Is(err, procs.ErrExited) {
				gone++
				continue
			}
			note("PID %d: %v", op.PID, err)
			continue
		}
		o := Owner{
			PID: op.PID, Comm: d.Comm, UID: d.UID, StartTicks: d.StartTicks,
			Unit: d.Unit, Primary: op == e.Primary,
		}
		if in.Users != nil {
			o.User = in.Users.Name(d.UID)
		}
		if len(d.Cmdline) > 0 {
			o.Command = procs.QuoteArgs(d.Cmdline)
		} else {
			o.Command = "[" + d.Comm + "]"
		}
		p.Owners = append(p.Owners, o)
	}
	if gone > 0 {
		note("%d of the processes holding this socket exited since the last refresh.", gone)
	}

	forward := e.Container != nil && e.Container.Forward != nil
	if forward {
		note("docker-proxy forwards this port to container %s, port %d. Killing the proxy only breaks the port mapping; stop the container instead.",
			e.Container.Label(), e.Container.Forward.ContainerPort)
	}

	// --- unit ---
	// The proxy runs in docker.service: never offer to stop the daemon.
	if len(p.Owners) > 0 && p.Owners[0].Primary && !forward {
		// A user unit must run as its manager's user (a user manager cannot
		// start processes as anyone else); otherwise the cgroup path is not
		// what it claims to be.
		o := p.Owners[0]
		if u := o.Unit; u.IsService() && !strings.HasPrefix(u.Name, "user@") && (!u.UserManager || u.User == o.UID) {
			p.Unit = u
		}
	}

	// --- container ---
	stopContainer := false
	if c := e.Container; c != nil {
		switch {
		case c.Runtime != "" && c.Runtime != "docker":
			note("%s containers cannot be stopped from portview: use `%s stop %s`.", c.Runtime, c.Runtime, c.ShortID())
		case !in.CanStopContainers:
			note("The Docker API is not available, so container %s cannot be stopped from here.", c.Label())
		default:
			stopContainer = true
		}
	}

	switch {
	case len(e.Owners) > 0:
	case e.Inode == 0:
		note("No process owns this socket (e.g. TIME-WAIT); the kernel closes it on its own.")
	case !in.Privileged && !stopContainer:
		note("The process holding this socket is not visible to you. Rerun portview with sudo to stop it.")
	case in.Privileged && e.Container == nil:
		note("No process owns this socket (kernel socket).")
	}

	// --- options ---
	if n := len(p.Owners); n > 0 {
		for i := range min(n, MaxOwnerOptions) {
			o := p.Owners[i]
			label := o.Label()
			if o.Primary && n > 1 {
				label += " (primary)"
			}
			p.Options = append(p.Options, Option{Kind: Signal, Label: label, Owners: []Owner{o}})
		}
		if n > 1 {
			p.Options = append(p.Options, Option{
				Kind: Signal, Label: fmt.Sprintf("all %d processes", n), Owners: slices.Clone(p.Owners),
			})
		}
		p.Default = 0
	}
	if p.Unit.Name != "" {
		p.Options = append(p.Options, Option{Kind: StopUnit, Label: SystemctlCommand(p.Unit), Unit: p.Unit})
	}
	if stopContainer {
		p.Options = append(p.Options, Option{Kind: StopContainer, Label: "docker stop " + e.Container.Label(), Container: e.Container})
		// Killing a process in a container is usually undone by its restart
		// policy, and killing docker-proxy only breaks the mapping.
		p.Default = len(p.Options) - 1
	}
	return p
}

// isUserManager reports whether p is a `systemd --user` instance, which
// lives in user@UID.service/init.scope (older systemd: user@UID.service).
func isUserManager(p *procs.Process) bool {
	u := p.Unit()
	return p.Comm == "systemd" && (u.UserManager && u.Name == "init.scope" || strings.HasPrefix(u.Name, "user@"))
}
