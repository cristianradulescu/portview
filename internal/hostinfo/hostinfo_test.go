package hostinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRead(t *testing.T) {
	root := t.TempDir()
	write := func(rel, data string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/sys/net/ipv4/ip_unprivileged_port_start", "1024\n")
	write("proc/sys/net/ipv4/ip_local_port_range", "32768\t60999\n")
	write("etc/services", `# comment
http		80/tcp		www		# WorldWideWeb HTTP
https		443/tcp
https		443/udp				# HTTP/3
domain		53/udp
alias-dup	80/tcp
broken
`)
	in := Read(root)
	if in.UnprivPortStart != 1024 || in.EphemeralLow != 32768 || in.EphemeralHigh != 60999 {
		t.Errorf("sysctls = %+v", in)
	}
	for _, tt := range []struct {
		port  uint16
		proto string
		want  string
	}{{80, "tcp", "http"}, {443, "udp", "https"}, {53, "udp", "domain"}, {53, "tcp", ""}, {8080, "tcp", ""}} {
		if got := in.Service(tt.port, tt.proto); got != tt.want {
			t.Errorf("Service(%d/%s) = %q, want %q", tt.port, tt.proto, got, tt.want)
		}
	}
	if !in.Ephemeral(40000) || in.Ephemeral(22) || in.Ephemeral(61000) {
		t.Error("Ephemeral")
	}

	empty := Read(t.TempDir())
	if empty.UnprivPortStart != -1 || empty.Service(80, "tcp") != "" || empty.Ephemeral(40000) {
		t.Errorf("missing files: %+v", empty)
	}
}

func TestReadLive(t *testing.T) {
	in := Read("")
	if in.UnprivPortStart < 0 || in.EphemeralLow == 0 {
		t.Skipf("sysctls not readable here: %+v", in)
	}
	if _, err := os.Stat("/etc/services"); err == nil && in.Service(22, "tcp") != "ssh" {
		t.Errorf("22/tcp = %q", in.Service(22, "tcp"))
	}
}
