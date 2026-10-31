// Package docker is a minimal Docker Engine API client over the unix
// socket. It covers the two calls portview needs — listing running
// containers and stopping one — with net/http only, instead of pulling in
// the Docker SDK and its large dependency tree.
//
// Requests use unversioned API paths, which the daemon serves with its
// current API version; only long-stable fields are decoded.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cristianradulescu/portview/internal/termsafe"
)

// DefaultSocket is where dockerd listens by default.
const DefaultSocket = "/var/run/docker.sock"

var (
	// ErrUnavailable means no daemon socket was found (Docker not installed
	// or not running). Callers should stay silent about it.
	ErrUnavailable = errors.New("docker socket not found")
	// ErrPermission means the socket exists but the user may not use it.
	ErrPermission = errors.New("permission denied on the docker socket (join the docker group or use sudo)")
)

// Container is a running container.
type Container struct {
	ID    string // full 64-hex ID
	Name  string // without the leading "/"
	Image string
	State string
	Ports []Port
	// IPs are the container's addresses on its networks.
	IPs []netip.Addr
	// Project and Service are the docker compose labels, if any.
	Project string
	Service string
}

// ShortID is the 12-character ID shown by `docker ps`.
func (c Container) ShortID() string {
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

// Port is a port mapping. PublicPort is 0 when the port is exposed but not
// published on the host.
type Port struct {
	IP          netip.Addr // host address the port is published on; invalid if unset
	PrivatePort uint16
	PublicPort  uint16
	Proto       string // "tcp", "udp", "sctp"
}

// Client talks to the Docker Engine API over a unix socket.
type Client struct {
	socket string
	hc     *http.Client
}

// New returns a client for the unix socket at path.
func New(path string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		MaxIdleConns:    1,
		IdleConnTimeout: 30 * time.Second,
	}
	return &Client{socket: path, hc: &http.Client{Transport: tr, Timeout: 5 * time.Second}}
}

// SocketPath returns the daemon socket from DOCKER_HOST when it is a unix://
// URL, DefaultSocket when DOCKER_HOST is unset, and "" when DOCKER_HOST
// points somewhere this client cannot reach (tcp://, ssh://).
func SocketPath() string {
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		return DefaultSocket
	}
	if p, ok := strings.CutPrefix(host, "unix://"); ok {
		return p
	}
	return ""
}

// Socket returns the socket path the client uses.
func (c *Client) Socket() string { return c.socket }

type apiContainer struct {
	ID     string   `json:"Id"`
	Names  []string `json:"Names"`
	Image  string   `json:"Image"`
	State  string   `json:"State"`
	Labels map[string]string
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort uint16 `json:"PrivatePort"`
		PublicPort  uint16 `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress         string `json:"IPAddress"`
			GlobalIPv6Address string `json:"GlobalIPv6Address"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

// Containers lists running containers.
func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	var raw []apiContainer
	if err := c.do(ctx, http.MethodGet, "/containers/json", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		ct := Container{
			ID:      r.ID,
			Image:   termsafe.String(r.Image),
			State:   termsafe.String(r.State),
			Project: termsafe.String(r.Labels["com.docker.compose.project"]),
			Service: termsafe.String(r.Labels["com.docker.compose.service"]),
		}
		if len(r.Names) > 0 {
			ct.Name = termsafe.String(strings.TrimPrefix(r.Names[0], "/"))
		}
		for _, p := range r.Ports {
			port := Port{PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, Proto: p.Type}
			if a, err := netip.ParseAddr(p.IP); err == nil {
				port.IP = a
			}
			ct.Ports = append(ct.Ports, port)
		}
		for _, n := range r.NetworkSettings.Networks {
			for _, s := range []string{n.IPAddress, n.GlobalIPv6Address} {
				if a, err := netip.ParseAddr(s); err == nil {
					ct.IPs = append(ct.IPs, a)
				}
			}
		}
		sort.Slice(ct.IPs, func(i, j int) bool { return ct.IPs[i].Less(ct.IPs[j]) })
		out = append(out, ct)
	}
	return out, nil
}

// Stop asks the daemon to stop a container: SIGTERM (or the image's
// STOPSIGNAL), then SIGKILL after timeout. Stopping an already stopped
// container is not an error.
func (c *Client) Stop(ctx context.Context, id string, timeout time.Duration) error {
	q := url.Values{"t": {strconv.Itoa(int(timeout.Seconds()))}}
	// The daemon waits up to timeout before answering.
	ctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop?"+q.Encode(), nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
	if err != nil {
		return err
	}
	hc := c.hc
	if _, ok := ctx.Deadline(); ok {
		// The context bounds the request; don't let the default client
		// timeout cut long operations such as stop short.
		cp := *c.hc
		cp.Timeout = 0
		hc = &cp
	}
	resp, err := hc.Do(req)
	if err != nil {
		return classify(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil // e.g. container already stopped
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Message string `json:"message"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if json.Unmarshal(b, &e) == nil && e.Message != "" {
			return fmt.Errorf("docker: %s", termsafe.String(e.Message))
		}
		return fmt.Errorf("docker: %s %s: %s", method, path, resp.Status)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out); err != nil {
		return fmt.Errorf("docker: decoding %s: %w", path, err)
	}
	return nil
}

func classify(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED):
		return ErrUnavailable
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EACCES):
		return ErrPermission
	}
	return fmt.Errorf("docker: %w", err)
}
