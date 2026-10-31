package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cristianradulescu/portview/internal/docker"
	"github.com/cristianradulescu/portview/internal/procs"
)

type users map[int]string

func (u users) Name(uid int) string {
	if n, ok := u[uid]; ok {
		return n
	}
	return "?"
}

func TestSystemctlArgs(t *testing.T) {
	sys := procs.Unit{Name: "nginx.service", User: -1}
	user := procs.Unit{Name: "app.service", UserManager: true, User: 1000}
	u := users{1000: "alice"}
	tests := []struct {
		unit procs.Unit
		euid int
		want []string
	}{
		{sys, 0, []string{"--no-ask-password", "stop", "--", "nginx.service"}},
		{sys, 1000, []string{"--no-ask-password", "stop", "--", "nginx.service"}},
		{user, 1000, []string{"--no-ask-password", "--user", "stop", "--", "app.service"}},
		{user, 0, []string{"--no-ask-password", "--user", "--machine=alice@.host", "stop", "--", "app.service"}},
	}
	for _, tt := range tests {
		if got := SystemctlArgs(tt.unit, tt.euid, u, "stop"); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("SystemctlArgs(%v, euid %d) = %q, want %q", tt.unit, tt.euid, got, tt.want)
		}
	}
	// Unknown user names fall back to the UID.
	got := SystemctlArgs(procs.Unit{Name: "x.service", UserManager: true, User: 1001}, 0, u, "stop")
	if !strings.Contains(strings.Join(got, " "), "--machine=1001@.host") {
		t.Errorf("unknown user: %q", got)
	}
	if got := SystemctlCommand(user); got != "systemctl --user stop app.service" {
		t.Errorf("SystemctlCommand = %q", got)
	}
}

// fakeSystemctl writes a script that logs its arguments and prints output
// with the given exit code.
func fakeSystemctl(t *testing.T, output string, code int) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "systemctl"), filepath.Join(dir, "args")
	script := "#!/bin/sh\necho \"$@\" > " + log + "\nprintf '%s\\n' '" + output + "'\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestStopUnit(t *testing.T) {
	unit := procs.Unit{Name: "nginx.service", User: -1}
	ctx := context.Background()

	bin, log := fakeSystemctl(t, "", 0)
	s := &System{Systemctl: bin}
	if err := s.StopUnit(ctx, unit); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); strings.TrimSpace(string(b)) != "--no-ask-password stop -- nginx.service" {
		t.Errorf("args = %q", b)
	}

	bin, _ = fakeSystemctl(t, "Failed to stop nginx.service: Interactive authentication required.", 1)
	s.Systemctl = bin
	if err := s.StopUnit(ctx, unit); !errors.Is(err, ErrPermission) || !strings.Contains(err.Error(), "Interactive authentication") {
		t.Errorf("auth failure: %v", err)
	}

	bin, _ = fakeSystemctl(t, "Unit nginx.service not loaded.", 5)
	s.Systemctl = bin
	if err := s.StopUnit(ctx, unit); err == nil || errors.Is(err, ErrPermission) || !strings.Contains(err.Error(), "not loaded") {
		t.Errorf("other failure: %v", err)
	}

	bin, log = fakeSystemctl(t, "on-failure", 0)
	s.Systemctl = bin
	if got, err := s.RestartPolicy(ctx, unit); got != "on-failure" || err != nil {
		t.Errorf("RestartPolicy = %q, %v", got, err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "show --property=Restart --value -- nginx.service") {
		t.Errorf("show args = %q", b)
	}
}

type fakeStopper struct {
	id      string
	timeout time.Duration
	err     error
}

func (f *fakeStopper) Stop(_ context.Context, id string, timeout time.Duration) error {
	f.id, f.timeout = id, timeout
	return f.err
}

func TestStopContainer(t *testing.T) {
	ctx := context.Background()
	if (&System{}).CanStopContainers() {
		t.Error("CanStopContainers without Docker")
	}
	if err := (&System{}).StopContainer(ctx, "abc"); err == nil {
		t.Error("StopContainer without Docker succeeded")
	}
	f := &fakeStopper{}
	s := &System{Docker: f}
	if err := s.StopContainer(ctx, "abc"); err != nil || f.id != "abc" || f.timeout != 10*time.Second {
		t.Errorf("StopContainer: %v, %+v", err, f)
	}
	f.err = docker.ErrPermission
	if err := s.StopContainer(ctx, "abc"); !errors.Is(err, ErrPermission) {
		t.Errorf("permission: %v", err)
	}
}

func TestNextSignal(t *testing.T) {
	sig := Signals[0]
	var names []string
	for range len(Signals) + 1 {
		names = append(names, SignalName(sig))
		sig = NextSignal(sig)
	}
	if got := strings.Join(names, " "); got != "SIGTERM SIGKILL SIGINT SIGHUP SIGQUIT SIGTERM" {
		t.Errorf("cycle = %s", got)
	}
	for _, s := range Signals {
		if SignalHelp(s) == "" {
			t.Errorf("no help for %s", SignalName(s))
		}
	}
}
