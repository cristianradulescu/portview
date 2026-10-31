package actions

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/cristianradulescu/portview/internal/procs"
)

// start runs a child process and returns its PID and start ticks. The
// child is reaped in the background, like a daemon's parent would.
func start(t *testing.T, script string) (int, uint64) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	ticks, err := procs.Scanner{}.StartTicks(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid, ticks
}

func TestTerminate(t *testing.T) {
	pid, ticks := start(t, "exec sleep 60")
	p, err := OpenProcess(procs.Scanner{}, pid, ticks)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()
	if !p.Alive() {
		t.Fatal("process not alive after start")
	}
	sent, err := SignalAll([]Process{p}, syscall.SIGTERM)
	if err != nil || len(sent) != 1 {
		t.Fatalf("SignalAll = %d sent, %v", len(sent), err)
	}
	if left := Wait(context.Background(), sent, 2*time.Second, 10*time.Millisecond); len(left) != 0 {
		t.Fatalf("still alive after SIGTERM: %s", PIDs(left))
	}
	if err := p.Signal(syscall.SIGTERM); !errors.Is(err, ErrGone) {
		t.Errorf("signal after exit: %v, want ErrGone", err)
	}
	// Signalling an exited process is not an error for SignalAll.
	if sent, err := SignalAll([]Process{p}, syscall.SIGKILL); len(sent) != 0 || err != nil {
		t.Errorf("SignalAll on exited = %d, %v", len(sent), err)
	}
}

func TestKillAfterIgnoredTerm(t *testing.T) {
	// The shell ignores SIGTERM; only SIGKILL stops it.
	pid, ticks := start(t, `trap "" TERM; echo ready; while :; do sleep 1; done`)
	p, err := OpenProcess(procs.Scanner{}, pid, ticks)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()
	time.Sleep(50 * time.Millisecond) // let the trap be installed
	if _, err := SignalAll([]Process{p}, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	left := Wait(context.Background(), []Process{p}, 200*time.Millisecond, 10*time.Millisecond)
	if len(left) != 1 {
		t.Fatalf("survivors = %d, want 1 (SIGTERM is ignored)", len(left))
	}
	if _, err := SignalAll(left, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if left := Wait(context.Background(), left, 2*time.Second, 10*time.Millisecond); len(left) != 0 {
		t.Fatal("still alive after SIGKILL")
	}
}

func TestOpenProcessIdentity(t *testing.T) {
	pid, ticks := start(t, "exec sleep 60")
	if _, err := OpenProcess(procs.Scanner{}, pid, ticks+1); !errors.Is(err, ErrGone) {
		t.Errorf("wrong start time: err = %v, want ErrGone (PID reused)", err)
	}
	if _, err := OpenProcess(procs.Scanner{}, 1, 0); !errors.Is(err, ErrProtected) {
		t.Errorf("PID 1: err = %v, want ErrProtected", err)
	}
	if _, err := OpenProcess(procs.Scanner{}, os.Getpid(), 0); !errors.Is(err, ErrProtected) {
		t.Errorf("self: err = %v, want ErrProtected", err)
	}
	// A PID that does not exist (above the default pid_max on 64-bit).
	if _, err := OpenProcess(procs.Scanner{}, 1<<22+1, 0); !errors.Is(err, ErrGone) {
		t.Errorf("missing PID: err = %v, want ErrGone", err)
	}
}

func TestPermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may signal anything")
	}
	// PID 1 is refused before trying; find another process owned by root.
	ticks, err := procs.Scanner{}.StartTicks(2) // kthreadd
	if err != nil {
		t.Skip("no kthreadd visible:", err)
	}
	p, err := OpenProcess(procs.Scanner{}, 2, ticks)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()
	if err := p.Signal(syscall.Signal(0)); !errors.Is(err, ErrPermission) {
		t.Errorf("signal root process: %v, want ErrPermission", err)
	}
	if !p.Alive() {
		t.Error("EPERM must count as alive")
	}
}
