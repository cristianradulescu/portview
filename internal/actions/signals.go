package actions

import "syscall"

// Signals are the signals offered by the kill action, in the order the
// user cycles through them. SIGKILL comes second so that a stubborn
// process is one keypress away.
var Signals = []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT}

// NextSignal returns the signal after sig in Signals, wrapping around.
func NextSignal(sig syscall.Signal) syscall.Signal {
	for i, s := range Signals {
		if s == sig {
			return Signals[(i+1)%len(Signals)]
		}
	}
	return Signals[0]
}

// SignalName is the conventional name, e.g. "SIGTERM".
func SignalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	}
	return sig.String()
}

// SignalHelp says what sig usually does to a process.
func SignalHelp(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "asks the process to exit; it can clean up first"
	case syscall.SIGKILL:
		return "kills at once; cannot be caught, no cleanup"
	case syscall.SIGINT:
		return "interrupt, like Ctrl-C"
	case syscall.SIGHUP:
		return "hangup; many daemons reload their config instead of exiting"
	case syscall.SIGQUIT:
		return "quit with a core dump"
	}
	return ""
}
