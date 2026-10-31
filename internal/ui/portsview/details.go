package portsview

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

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

	// --- socket ---
	section("Socket")
	endpoint := tui.Color(sockets.FormatAddrPort(e.Local), exposureColor(e.Exposure))
	if !e.Listening() {
		endpoint += " → " + sockets.FormatAddrPort(e.Remote)
	}
	kv("Endpoint", e.Netid()+" "+endpoint)
	kv("State", e.State.String())
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
