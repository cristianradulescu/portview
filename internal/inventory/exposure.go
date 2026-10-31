package inventory

import "github.com/cristianradulescu/portview/internal/sockets"

// Exposure describes how reachable a listening socket is.
type Exposure uint8

const (
	// ExposureNone: not a listening socket (connected or closing).
	ExposureNone Exposure = iota
	// ExposureLoopback: bound to 127.0.0.0/8 or ::1, local access only.
	ExposureLoopback
	// ExposureInterface: bound to one specific address.
	ExposureInterface
	// ExposureAll: bound to the wildcard address (0.0.0.0 or ::), reachable
	// on every interface.
	ExposureAll
	// ExposureNamespace: listening inside another network namespace (e.g. a
	// container). Not directly reachable from outside the host unless
	// something forwards to it.
	ExposureNamespace
)

func (e Exposure) String() string {
	switch e {
	case ExposureLoopback:
		return "loopback"
	case ExposureInterface:
		return "iface"
	case ExposureAll:
		return "all"
	case ExposureNamespace:
		return "netns"
	default:
		return "-"
	}
}

// ExposureOf classifies a socket by its local address.
func ExposureOf(s sockets.Socket, hostNetNS bool) Exposure {
	if !s.Listening() {
		return ExposureNone
	}
	if !hostNetNS {
		return ExposureNamespace
	}
	addr := s.Local.Addr().Unmap()
	switch {
	case addr.IsUnspecified():
		return ExposureAll
	case addr.IsLoopback():
		return ExposureLoopback
	default:
		return ExposureInterface
	}
}
