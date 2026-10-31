package docker

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const id1 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// A trimmed real response of GET /containers/json.
const containersJSON = `[{
  "Id": "` + id1 + `",
  "Names": ["/web-1"],
  "Image": "nginx:1.29",
  "State": "running",
  "Labels": {"com.docker.compose.project": "shop", "com.docker.compose.service": "web"},
  "Ports": [
    {"IP": "0.0.0.0", "PrivatePort": 80, "PublicPort": 8080, "Type": "tcp"},
    {"IP": "::", "PrivatePort": 80, "PublicPort": 8080, "Type": "tcp"},
    {"PrivatePort": 443, "Type": "tcp"}
  ],
  "NetworkSettings": {"Networks": {"shop_default": {"IPAddress": "172.18.0.2", "GlobalIPv6Address": ""}}}
}, {
  "Id": "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
  "Names": ["/evil\u001b[2Jname"],
  "Image": "alpine",
  "State": "running",
  "Ports": [],
  "NetworkSettings": {"Networks": {}}
}, {
  "Id": "x;touch /tmp/pwned",
  "Names": ["/not-an-id"],
  "Image": "alpine",
  "State": "running",
  "Ports": [],
  "NetworkSettings": {"Networks": {}}
}]`

// serve starts an HTTP server on a unix socket and returns its path.
func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pvdock") // short path: unix socket paths are limited to 108 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "docker.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return path
}

func TestContainers(t *testing.T) {
	path := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/json" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(containersJSON))
	}))
	cs, err := New(path).Containers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("got %d containers, want 2 (the entry with a malformed ID is dropped)", len(cs))
	}
	web := cs[0]
	if web.ID != id1 || web.ShortID() != "0123456789ab" || web.Name != "web-1" || web.Image != "nginx:1.29" ||
		web.Project != "shop" || web.Service != "web" {
		t.Errorf("web = %+v", web)
	}
	if len(web.Ports) != 3 || web.Ports[0].PublicPort != 8080 || web.Ports[0].IP != netip.MustParseAddr("0.0.0.0") ||
		web.Ports[2].PublicPort != 0 || web.Ports[2].IP.IsValid() {
		t.Errorf("ports = %+v", web.Ports)
	}
	if len(web.IPs) != 1 || web.IPs[0] != netip.MustParseAddr("172.18.0.2") {
		t.Errorf("IPs = %v", web.IPs)
	}
	if cs[1].Name != `evil\x1b[2Jname` {
		t.Errorf("container name not sanitized: %q", cs[1].Name)
	}
}

func TestStop(t *testing.T) {
	var gotPath, gotQuery string
	path := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		switch {
		case r.Method != http.MethodPost:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.URL.Path == "/containers/stopped/stop":
			w.WriteHeader(http.StatusNotModified)
		case r.URL.Path == "/containers/missing/stop":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"No such container: missing"}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	c := New(path)
	if err := c.Stop(context.Background(), id1, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/containers/"+id1+"/stop" || gotQuery != "t=10" {
		t.Errorf("request = %s?%s", gotPath, gotQuery)
	}
	if err := c.Stop(context.Background(), "stopped", time.Second); err != nil {
		t.Errorf("already stopped: %v", err)
	}
	err := c.Stop(context.Background(), "missing", time.Second)
	if err == nil || err.Error() != "docker: No such container: missing" {
		t.Errorf("missing: %v", err)
	}
}

func TestErrors(t *testing.T) {
	_, err := New(filepath.Join(t.TempDir(), "nope.sock")).Containers(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("missing socket: %v, want ErrUnavailable", err)
	}

	if os.Geteuid() != 0 {
		path := serve(t, http.NotFoundHandler())
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		_, err = New(path).Containers(context.Background())
		if !errors.Is(err, ErrPermission) {
			t.Errorf("unreadable socket: %v, want ErrPermission", err)
		}
	}
}

func TestSocketPath(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	if got := SocketPath(); got != DefaultSocket {
		t.Errorf("unset: %q", got)
	}
	t.Setenv("DOCKER_HOST", "unix:///run/user/1000/docker.sock")
	if got := SocketPath(); got != "/run/user/1000/docker.sock" {
		t.Errorf("unix: %q", got)
	}
	t.Setenv("DOCKER_HOST", "tcp://10.0.0.1:2376")
	if got := SocketPath(); got != "" {
		t.Errorf("tcp: %q", got)
	}
}
