// Package hostinfo reads machine-wide settings that explain ports: service
// names from /etc/services and the sysctls that decide who may bind which
// port and which ports the kernel picks for outgoing connections.
package hostinfo

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cristianradulescu/portview/internal/termsafe"
)

// Info is read once per refresh.
type Info struct {
	// UnprivPortStart is net.ipv4.ip_unprivileged_port_start: ports below
	// it need CAP_NET_BIND_SERVICE. -1 if unknown.
	UnprivPortStart int
	// EphemeralLow and EphemeralHigh are net.ipv4.ip_local_port_range:
	// where the kernel picks local ports for connections that did not
	// bind one. 0 if unknown.
	EphemeralLow, EphemeralHigh int

	services map[string]string // "80/tcp" → "http"
}

// Read reads the settings below root ("" means "/").
func Read(root string) *Info {
	if root == "" {
		root = "/"
	}
	in := &Info{UnprivPortStart: -1}
	if b, err := os.ReadFile(filepath.Join(root, "proc/sys/net/ipv4/ip_unprivileged_port_start")); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			in.UnprivPortStart = v
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "proc/sys/net/ipv4/ip_local_port_range")); err == nil {
		if f := strings.Fields(string(b)); len(f) == 2 {
			in.EphemeralLow, _ = strconv.Atoi(f[0])
			in.EphemeralHigh, _ = strconv.Atoi(f[1])
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "etc/services")); err == nil {
		in.services = ParseServices(b)
	}
	return in
}

// ParseServices parses /etc/services: "name port/proto [aliases] [# comment]".
// The first name listed for a port wins, as with getservbyport(3).
func ParseServices(data []byte) map[string]string {
	out := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		f := strings.Fields(line)
		if len(f) < 2 || !strings.Contains(f[1], "/") {
			continue
		}
		key := strings.ToLower(f[1])
		if _, dup := out[key]; !dup {
			out[key] = termsafe.String(f[0])
		}
	}
	return out
}

// Service is the registered name of port/proto ("http"), or "".
func (in *Info) Service(port uint16, proto string) string {
	if in == nil || in.services == nil {
		return ""
	}
	return in.services[strconv.Itoa(int(port))+"/"+strings.ToLower(proto)]
}

// Ephemeral reports whether port is in the ephemeral range.
func (in *Info) Ephemeral(port uint16) bool {
	return in != nil && in.EphemeralLow > 0 && int(port) >= in.EphemeralLow && int(port) <= in.EphemeralHigh
}

// New builds an Info from known values (for tests and other sources).
// services maps "port/proto" to a name.
func New(unprivPortStart, ephemeralLow, ephemeralHigh int, services map[string]string) *Info {
	return &Info{UnprivPortStart: unprivPortStart, EphemeralLow: ephemeralLow, EphemeralHigh: ephemeralHigh, services: services}
}
