package explain

import (
	"fmt"

	"github.com/cristianradulescu/portview/internal/hostinfo"
	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/sockets"
)

// StateHelp says in a few words what a socket state means.
func StateHelp(st sockets.State, proto sockets.Proto) string {
	if proto == sockets.UDP {
		switch st {
		case sockets.StateClose:
			return "bound, not connected: receives from anyone (a UDP server, or a client before it sends)"
		case sockets.StateEstablished:
			return "connected: connect() fixed its peer"
		}
	}
	switch st {
	case sockets.StateEstablished:
		return "connected; data flows both ways"
	case sockets.StateSynSent:
		return "connecting: SYN sent, waiting for the peer's SYN-ACK"
	case sockets.StateSynRecv, sockets.StateNewSynRecv:
		return "a client's SYN was answered; waiting for its ACK"
	case sockets.StateFinWait1:
		return "this side closed and sent FIN"
	case sockets.StateFinWait2:
		return "this side closed; waiting for the peer to close too"
	case sockets.StateTimeWait:
		return "closed by this side first; kept about 60s so late packets are not mistaken for a new connection. Normal, even in large numbers"
	case sockets.StateClose:
		return "closed"
	case sockets.StateCloseWait:
		return "the peer closed but the program has not: many of these mean it forgets to close() connections"
	case sockets.StateLastAck:
		return "both sides closed; waiting for the last ACK"
	case sockets.StateListen:
		return "waiting for incoming connections"
	case sockets.StateClosing:
		return "both sides closed at the same time"
	}
	return ""
}

// PortInput is what PortReason works from.
type PortInput struct {
	Entry *inventory.Entry
	// Proc is the primary owner's details; nil if not visible.
	Proc *procs.Details
	Host *hostinfo.Info
	// Listener is true when a socket in the same namespace listens on
	// the entry's local port (so a connection on it is an accepted one).
	Listener bool
}

// PortReason explains where the socket's local port comes from and who
// may use it: privileged ports and CAP_NET_BIND_SERVICE, ports picked
// from the ephemeral range, and the server side of accepted connections.
// It returns "" when there is nothing useful to say.
func PortReason(in PortInput) string {
	e, h := in.Entry, in.Host
	port := e.Local.Port()
	if port == 0 || h == nil {
		return ""
	}
	rangeText := ""
	if h.EphemeralLow > 0 {
		rangeText = fmt.Sprintf(" (%d–%d, net.ipv4.ip_local_port_range)", h.EphemeralLow, h.EphemeralHigh)
	}

	if !e.Listening() {
		switch {
		case in.Listener:
			return fmt.Sprintf("The server side of a connection: a client connected to port %d, which a program here listens on.", port)
		case h.Ephemeral(port):
			return fmt.Sprintf("The program connected out without choosing a local port, so the kernel picked %d from the ephemeral range%s.", port, rangeText)
		}
		return ""
	}

	if !e.HostNetNS {
		if h.UnprivPortStart > 0 && int(port) < h.UnprivPortStart {
			return "The socket is in its own network namespace, where the privileged-port limit is set separately: " +
				"Docker sets net.ipv4.ip_unprivileged_port_start to 0 in containers, so any port can be bound there."
		}
		return ""
	}

	if h.UnprivPortStart < 0 {
		return ""
	}
	if int(port) >= h.UnprivPortStart {
		if h.Ephemeral(port) {
			return fmt.Sprintf("Port %d is in the ephemeral range%s: the program may have asked for any free port (bind to port 0) and the kernel chose this one.", port, rangeText)
		}
		return fmt.Sprintf("Ports from %d up (net.ipv4.ip_unprivileged_port_start) can be bound by any user.", h.UnprivPortStart)
	}

	why := fmt.Sprintf("Port %d is below %d (net.ipv4.ip_unprivileged_port_start), so binding it needs the CAP_NET_BIND_SERVICE capability.", port, h.UnprivPortStart)
	d := in.Proc
	if d == nil || !d.Insight.CapsKnown {
		return why
	}
	switch {
	case procs.HasCap(d.Insight.CapEff, procs.CapNetBindService) && d.UID == 0:
		return why + fmt.Sprintf(" PID %d has it because it runs as root.", d.PID)
	case procs.HasCap(d.Insight.CapEff, procs.CapNetBindService):
		return why + fmt.Sprintf(" PID %d has it without being root: it was granted, e.g. with AmbientCapabilities= in its systemd unit or setcap on its executable.", d.PID)
	}
	return why + fmt.Sprintf(" PID %d does not have it now: it bound the port while it had it (as root, or through a parent) and dropped it afterwards, as servers that switch to an unprivileged user do.", d.PID)
}
