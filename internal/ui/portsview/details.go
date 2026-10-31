package portsview

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"strings"
	"time"

	"github.com/cristianradulescu/portview/internal/explain"
	"github.com/cristianradulescu/portview/internal/hostinfo"
	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
	"github.com/cristianradulescu/portview/internal/ui/tui"
)

// maxListed caps the "other owners" and "other sockets" lists.
const maxListed = 40

// detailsInput is everything the details pane renders from.
type detailsInput struct {
	entry      *inventory.Entry
	snap       *inventory.Snapshot
	proc       *procs.Details // details of entry.Primary, nil if unavailable
	procErr    error
	users      inventory.UserResolver
	now        time.Time
	privileged bool
	// host holds service names and port sysctls; nil if unknown.
	host *hostinfo.Info
	// hz is USER_HZ, for CPU time.
	hz int64
	// width of the pane in cells; long plain values wrap with a hanging
	// indent so they stay aligned with their label.
	width int
}

func renderDetails(in detailsInput) string {
	e, snap := in.entry, in.snap
	if e == nil || snap == nil {
		return tui.Dim("nothing selected")
	}
	var sb strings.Builder
	const labelW = 9
	indent := strings.Repeat(" ", 2+labelW+1)
	kv := func(k, v string) {
		lines := []string{v}
		if !strings.Contains(v, "\x1b") { // only plain values are wrapped
			lines = tui.WrapHanging(v, in.width-len(indent), "")
		}
		fmt.Fprintf(&sb, "  %s %s\n", tui.Dim(tui.PadRight(k, labelW)), lines[0])
		for _, l := range lines[1:] {
			sb.WriteString(indent + l + "\n")
		}
	}
	section := func(title string) {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(tui.Bold(title) + "\n")
	}
	// note adds a dim explanation under the value column.
	note := func(text string) {
		for _, l := range tui.WrapHanging(text, in.width-len(indent), "") {
			sb.WriteString(indent + tui.Dim(l) + "\n")
		}
	}

	// --- socket ---
	section("Socket")
	endpoint := tui.Color(sockets.FormatAddrPort(e.Local), exposureColor(e.Exposure))
	if !e.Listening() {
		endpoint += " → " + sockets.FormatAddrPort(e.Remote)
	}
	kv("Endpoint", e.Netid()+" "+endpoint)
	kv("State", e.State.String())
	if help := explain.StateHelp(e.State, e.Proto); help != "" {
		note(help)
	}
	if name := in.host.Service(e.Local.Port(), e.Proto.String()); name != "" {
		kv("Service", name+tui.Dim(fmt.Sprintf(" (registered for %d/%s in /etc/services)", e.Local.Port(), e.Proto)))
	}
	if why := explain.PortReason(explain.PortInput{Entry: e, Proc: in.proc, Host: in.host, Listener: hasListener(snap, e)}); why != "" {
		kv("Port", why)
	}
	kv("Exposure", tui.Color(exposureDescription(e.Exposure), exposureColor(e.Exposure)))
	kv("Netns", snap.NetNSLabel(e.NetNS))
	if e.Inode != 0 {
		kv("Inode", fmt.Sprint(e.Inode))
	}
	kv("Queues", fmt.Sprintf("recv %d · send %d", e.RxQueue, e.TxQueue))

	// --- container ---
	if c := e.Container; c != nil {
		title := "Container " + c.Label()
		if c.Name != "" {
			title += " (" + c.ShortID() + ")"
		}
		section(title)
		if c.Forward != nil {
			kv("Forwards", fmt.Sprintf("host port %d/%s → container port %d/%s",
				e.Local.Port(), e.Proto, c.Forward.ContainerPort, c.Forward.Proto))
			sb.WriteString(tui.Dim("            this is the docker-proxy for a published port") + "\n")
		}
		if c.Image != "" {
			kv("Image", c.Image)
		}
		if c.Project != "" {
			kv("Compose", c.Project+" / "+c.Service)
		}
		if c.Runtime != "" && c.Runtime != "docker" {
			kv("Runtime", c.Runtime)
		}
		if c.Name == "" {
			reason := "Docker API not queried (--no-docker)"
			if snap.DockerErr != nil {
				reason = snap.DockerErr.Error()
			} else if c.Runtime == "podman" {
				reason = "podman containers are identified by ID only"
			}
			kv("Name", tui.Dim("unknown: "+reason))
		}
	}

	// --- process ---
	switch {
	case e.Primary == nil:
		section("Process")
		switch {
		case e.Inode == 0:
			sb.WriteString(tui.Dim("  none: the socket has no file (e.g. TIME-WAIT)") + "\n")
		case in.privileged:
			sb.WriteString(tui.Dim("  none: kernel socket") + "\n")
		default:
			sb.WriteString(tui.Color("  owned by a process you cannot inspect\n  (socket user: "+e.User+"). Rerun with sudo.", tui.FgYellow) + "\n")
		}
		return sb.String()
	case in.proc == nil:
		section(fmt.Sprintf("Process %s · PID %d", e.Primary.Comm, e.Primary.PID))
		msg := "details unavailable"
		if errors.Is(in.procErr, procs.ErrExited) {
			msg = "process exited"
		} else if in.procErr != nil {
			msg = in.procErr.Error()
		}
		sb.WriteString(tui.Dim("  "+msg) + "\n")
	default:
		writeProcess(&sb, kv, section, in)
		writeInsight(kv, note, in)
	}

	// --- shared owners ---
	if len(e.Owners) > 1 {
		section(fmt.Sprintf("Shared by %d processes", len(e.Owners)))
		for i, p := range e.Owners {
			if i == maxListed {
				sb.WriteString(tui.Dim(fmt.Sprintf("  … and %d more", len(e.Owners)-maxListed)) + "\n")
				break
			}
			line := fmt.Sprintf("  %-7d %s %s", p.PID, tui.Fit(orQ(p.Comm), 16), in.users.Name(p.UID))
			if p == e.Primary {
				line += tui.Color("  primary", tui.FgGreen)
			}
			sb.WriteString(line + "\n")
		}
	}

	// --- other sockets of the primary ---
	others := otherSockets(snap, e)
	if len(others) > 0 {
		section(fmt.Sprintf("Other sockets of PID %d (%d)", e.Primary.PID, len(others)))
		for i, o := range others {
			if i == maxListed {
				sb.WriteString(tui.Dim(fmt.Sprintf("  … and %d more", len(others)-maxListed)) + "\n")
				break
			}
			line := fmt.Sprintf("  %-4s %-6s %s", o.Netid(), shortState(o), tui.Color(sockets.FormatAddrPort(o.Local), exposureColor(o.Exposure)))
			if !o.Listening() {
				line += " → " + sockets.FormatAddrPort(o.Remote)
			}
			sb.WriteString(line + "\n")
		}
	}
	return sb.String()
}

func writeProcess(sb *strings.Builder, kv func(k, v string), section func(string), in detailsInput) {
	d := in.proc
	section(fmt.Sprintf("Process %s · PID %d", orQ(d.Comm), d.PID))
	switch {
	case len(d.Cmdline) > 0:
		kv("Command", procs.QuoteArgs(d.Cmdline))
	default:
		kv("Command", tui.Dim("["+d.Comm+"] (kernel thread)"))
	}
	kv("Exe", pathOrReason(d.Exe, d.ExeErr, in.privileged))
	kv("Cwd", pathOrReason(d.Cwd, d.CwdErr, in.privileged))
	kv("User", fmt.Sprintf("%s (uid %d)", in.users.Name(d.UID), d.UID))
	if !d.StartTime.IsZero() {
		kv("Started", d.StartTime.Format("2006-01-02 15:04:05")+tui.Dim(" · "+humanDuration(in.now.Sub(d.StartTime))+" ago"))
	}
	if d.PPID > 0 {
		kv("Parent", fmt.Sprintf("%d %s", d.PPID, orQ(d.ParentComm)))
	}
	state := d.State
	if d.Threads > 1 {
		state += fmt.Sprintf(" · %d threads", d.Threads)
	}
	kv("State", state)
	switch {
	case d.Unit.IsService():
		kv("Unit", tui.Color(d.Unit.String(), tui.FgMagenta)+tui.Dim(" (systemd service)"))
	case d.Unit.Name != "":
		kv("Unit", d.Unit.String())
	}
}

// writeInsight adds resources, capabilities, family and namespaces.
func writeInsight(kv func(k, v string), note func(string), in detailsInput) {
	d := in.proc
	ins := d.Insight
	var res []string
	if ins.RSS > 0 {
		res = append(res, humanBytes(ins.RSS)+" memory")
	}
	if in.hz > 0 {
		res = append(res, humanDuration(time.Duration(ins.CPUTicks)*time.Second/time.Duration(in.hz))+" CPU")
	}
	switch {
	case ins.FDs >= 0 && ins.MaxFDs == math.MaxUint64:
		res = append(res, fmt.Sprintf("%d open files (no limit)", ins.FDs))
	case ins.FDs >= 0 && ins.MaxFDs > 0:
		res = append(res, fmt.Sprintf("%d of %d open files", ins.FDs, ins.MaxFDs))
	case ins.FDs >= 0:
		res = append(res, fmt.Sprintf("%d open files", ins.FDs))
	case ins.MaxFDs > 0 && ins.MaxFDs != math.MaxUint64:
		res = append(res, fmt.Sprintf("? of %d open files", ins.MaxFDs))
	}
	if len(res) > 0 {
		kv("Resources", strings.Join(res, " · "))
	}

	if ins.CapsKnown {
		switch {
		case procs.AllCaps(ins.CapEff) && d.UID == 0:
			kv("Caps", "all (it runs as root)")
		case procs.AllCaps(ins.CapEff):
			kv("Caps", "all")
		case ins.CapEff == 0:
			kv("Caps", "none: an ordinary unprivileged process")
		default:
			kv("Caps", strings.Join(procs.CapNames(ins.CapEff), ", "))
		}
	}

	if len(ins.Ancestors) > 0 {
		chain := make([]string, 0, len(ins.Ancestors)+1)
		for i := len(ins.Ancestors) - 1; i >= 0; i-- {
			a := ins.Ancestors[i]
			chain = append(chain, fmt.Sprintf("%s(%d)", a.Comm, a.PID))
		}
		chain = append(chain, fmt.Sprintf("%s(%d)", orQ(d.Comm), d.PID))
		kv("Ancestry", strings.Join(chain, " → "))
	}
	if n := len(ins.Children); n > 0 {
		const maxChildren = 6
		parts := make([]string, 0, min(n, maxChildren))
		for i, c := range ins.Children {
			if i == maxChildren {
				break
			}
			parts = append(parts, fmt.Sprintf("%d %s", c.PID, c.Comm))
		}
		v := fmt.Sprintf("%d: %s", n, strings.Join(parts, ", "))
		if n > maxChildren {
			v += ", …"
		}
		kv("Children", v)
	}

	switch {
	case ins.NSErr != nil && !in.privileged:
		kv("Isolation", tui.Dim("? (its namespaces need root to read: rerun with sudo)"))
	case ins.NSErr != nil:
		kv("Isolation", tui.Dim("? ("+ins.NSErr.Error()+")"))
	case len(ins.Namespaces) > 0:
		var own, shared []string
		for _, ns := range ins.Namespaces {
			if ns.Shared {
				shared = append(shared, ns.Type)
			} else {
				own = append(own, ns.Type)
			}
		}
		if len(own) == 0 {
			kv("Isolation", "none: the same namespaces as portview")
		} else {
			kv("Isolation", "its own "+strings.Join(own, ", ")+" namespaces")
			msg := "Isolated from portview in these namespaces, as containers and sandboxes are."
			if len(shared) > 0 {
				msg += " Shared: " + strings.Join(shared, ", ") + "."
			}
			note(msg)
		}
	}
}

// hasListener reports whether a socket in e's namespace listens on e's
// local port (making e an accepted connection).
func hasListener(snap *inventory.Snapshot, e *inventory.Entry) bool {
	if e.Listening() || snap == nil {
		return false
	}
	for i := range snap.Entries {
		o := &snap.Entries[i]
		if o.Listening() && o.Proto == e.Proto && o.NetNS == e.NetNS && o.Local.Port() == e.Local.Port() {
			return true
		}
	}
	return false
}

// humanBytes renders n in KiB/MiB/GiB with one decimal.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func pathOrReason(path string, err error, privileged bool) string {
	switch {
	case path != "":
		return path
	case errors.Is(err, fs.ErrPermission) && !privileged:
		return tui.Dim("? (permission denied: rerun with sudo)")
	case errors.Is(err, fs.ErrNotExist):
		return tui.Dim("-")
	case err != nil:
		return tui.Dim("? (" + err.Error() + ")")
	}
	return tui.Dim("-")
}

// otherSockets lists the snapshot entries held by e's primary process,
// excluding e itself, in display order.
func otherSockets(snap *inventory.Snapshot, e *inventory.Entry) []*inventory.Entry {
	if e.Primary == nil {
		return nil
	}
	pid, key := e.Primary.PID, e.Key()
	var out []*inventory.Entry
	for i := range snap.Entries {
		o := &snap.Entries[i]
		if o.Key() == key {
			continue
		}
		for _, p := range o.Owners {
			if p.PID == pid {
				out = append(out, o)
				break
			}
		}
	}
	return out
}

func shortState(e *inventory.Entry) string {
	s := e.State.String()
	if len(s) > 6 {
		return s[:6]
	}
	return s
}

// humanDuration renders d coarsely: "45s", "12m", "3h12m", "4d3h".
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		days := int(d.Hours()) / 24
		return fmt.Sprintf("%dd%dh", days, int(d.Hours())%24)
	}
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}
