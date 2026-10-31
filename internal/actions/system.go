package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cristianradulescu/portview/internal/docker"
	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/procs"
	"github.com/cristianradulescu/portview/internal/termsafe"
)

// Runner carries out actions. System is the real implementation; the UI
// depends on this interface so it can be tested without touching
// processes.
type Runner interface {
	OpenProcess(pid int, startTicks uint64) (Process, error)
	StopUnit(ctx context.Context, u procs.Unit) error
	// RestartPolicy returns the Restart= setting of a unit ("no",
	// "always", "on-failure", …).
	RestartPolicy(ctx context.Context, u procs.Unit) (string, error)
	StopContainer(ctx context.Context, id string) error
	// CanStopContainers reports whether the Docker API is configured.
	CanStopContainers() bool
}

// ContainerStopper stops containers; *docker.Client implements it.
type ContainerStopper interface {
	Stop(ctx context.Context, id string, timeout time.Duration) error
}

// System runs actions on the local machine.
type System struct {
	// Procs reads /proc for the process identity checks.
	Procs procs.Scanner
	// Docker stops containers; nil disables `docker stop`.
	Docker ContainerStopper
	// Users resolves UIDs for `systemctl --user --machine=<user>@`.
	Users inventory.UserResolver
	// Systemctl is the systemctl binary; default "systemctl" from PATH.
	Systemctl string
	// ContainerTimeout is the grace period docker gives the container
	// before SIGKILL; default 10s, like `docker stop`.
	ContainerTimeout time.Duration
}

var _ Runner = (*System)(nil)

// OpenProcess implements Runner.
func (s *System) OpenProcess(pid int, startTicks uint64) (Process, error) {
	return OpenProcess(s.Procs, pid, startTicks)
}

// CanStopContainers implements Runner.
func (s *System) CanStopContainers() bool { return s.Docker != nil }

// StopContainer implements Runner.
func (s *System) StopContainer(ctx context.Context, id string) error {
	if s.Docker == nil {
		return errors.New("the Docker API is not available")
	}
	timeout := s.ContainerTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	err := s.Docker.Stop(ctx, id, timeout)
	if errors.Is(err, docker.ErrPermission) {
		return fmt.Errorf("%w: %v", ErrPermission, err)
	}
	return err
}

// StopUnit implements Runner: `systemctl stop`, which waits until the unit
// has stopped. Polkit is never asked for a password (the TUI owns the
// terminal), so without the right privileges this fails with ErrPermission.
func (s *System) StopUnit(ctx context.Context, u procs.Unit) error {
	_, err := s.systemctl(ctx, u, "stop")
	return err
}

// RestartPolicy implements Runner.
func (s *System) RestartPolicy(ctx context.Context, u procs.Unit) (string, error) {
	return s.systemctl(ctx, u, "show", "--property=Restart", "--value")
}

func (s *System) systemctl(ctx context.Context, u procs.Unit, verb ...string) (string, error) {
	bin := s.Systemctl
	if bin == "" {
		bin = "systemctl"
	}
	args := SystemctlArgs(u, os.Geteuid(), s.Users, verb...)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = nil // never prompt on the TUI's terminal
	out, err := cmd.CombinedOutput()
	msg := termsafe.String(strings.TrimSpace(string(out)))
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		if isAuthError(msg) {
			return "", fmt.Errorf("%w: %s", ErrPermission, msg)
		}
		return "", fmt.Errorf("systemctl %s %s: %s", verb[0], u.Name, msg)
	}
	return msg, nil
}

// SystemctlArgs builds the systemctl arguments for verb on unit u, as user
// euid. Units of a user manager use --user; when that manager belongs to
// someone else (portview running as root), it is reached with
// --machine=<user>@.host.
func SystemctlArgs(u procs.Unit, euid int, users inventory.UserResolver, verb ...string) []string {
	args := []string{"--no-ask-password"}
	if u.UserManager {
		args = append(args, "--user")
		if u.User != euid {
			name := ""
			if users != nil {
				name = users.Name(u.User)
			}
			if name == "" || name == "?" {
				name = fmt.Sprint(u.User)
			}
			args = append(args, "--machine="+name+"@.host")
		}
	}
	args = append(args, verb...)
	return append(args, "--", u.Name)
}

// SystemctlCommand is how the stop command is shown to the user.
func SystemctlCommand(u procs.Unit) string {
	if u.UserManager {
		return "systemctl --user stop " + u.Name
	}
	return "systemctl stop " + u.Name
}

func isAuthError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "authentication required") ||
		strings.Contains(m, "access denied") ||
		strings.Contains(m, "permission denied")
}
