package inventory

import (
	"strconv"
	"strings"

	"github.com/cristianradulescu/portview/internal/sockets"
)

// StateFilter selects sockets by connection state.
type StateFilter uint8

const (
	StateListening StateFilter = iota
	StateEstablished
	StateAll
)

func (s StateFilter) String() string {
	switch s {
	case StateListening:
		return "listening"
	case StateEstablished:
		return "established"
	default:
		return "all"
	}
}

// ParseStateFilter parses "listen", "established" or "all".
func ParseStateFilter(s string) (StateFilter, bool) {
	switch s {
	case "listen", "listening":
		return StateListening, true
	case "established", "estab":
		return StateEstablished, true
	case "all":
		return StateAll, true
	}
	return StateAll, false
}

// Filter decides which entries are shown. The zero value shows listening
// TCP and UDP sockets of both families.
type Filter struct {
	State StateFilter
	// OnlyProto restricts to one protocol; 0 means both.
	OnlyProto sockets.Proto
	// OnlyFamily restricts to one address family; 0 means both.
	OnlyFamily sockets.Family
	// Text matches case-insensitively against ports, addresses, process
	// name, PID, user and state. Several space-separated terms must all
	// match.
	Text string
}

// Match reports whether e passes the filter.
func (f Filter) Match(e *Entry) bool {
	switch f.State {
	case StateListening:
		if !e.Listening() {
			return false
		}
	case StateEstablished:
		if e.State != sockets.StateEstablished {
			return false
		}
	}
	if f.OnlyProto != 0 && e.Proto != f.OnlyProto {
		return false
	}
	if f.OnlyFamily != 0 && e.Family != f.OnlyFamily {
		return false
	}
	for term := range strings.FieldsSeq(strings.ToLower(f.Text)) {
		if !matchTerm(e, term) {
			return false
		}
	}
	return true
}

// Apply returns the entries passing the filter, preserving order.
func (f Filter) Apply(entries []Entry) []Entry {
	out := make([]Entry, 0, len(entries))
	for i := range entries {
		if f.Match(&entries[i]) {
			out = append(out, entries[i])
		}
	}
	return out
}

func matchTerm(e *Entry, term string) bool {
	// A bare number (optionally prefixed with ':') matches a port exactly,
	// so "80" does not match 8080. It may still match a PID.
	if n, err := strconv.ParseUint(strings.TrimPrefix(term, ":"), 10, 32); err == nil {
		if uint64(e.Local.Port()) == n || uint64(e.Remote.Port()) == n {
			return true
		}
		if !strings.HasPrefix(term, ":") {
			for _, p := range e.Owners {
				if uint64(p.PID) == n {
					return true
				}
			}
		}
		return false
	}
	fields := []string{
		sockets.FormatAddrPort(e.Local),
		sockets.FormatAddrPort(e.Remote),
		e.Netid(),
		e.State.String(),
		e.User,
		containerName(e),
	}
	for _, p := range e.Owners {
		fields = append(fields, p.Comm)
	}
	for _, s := range fields {
		if strings.Contains(strings.ToLower(s), term) {
			return true
		}
	}
	return false
}
