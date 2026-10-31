package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/cristianradulescu/portview/internal/procs"
)

var (
	// ErrGone means the process exited, or its PID now belongs to a
	// different process, since it was shown to the user.
	ErrGone = errors.New("process is gone")
	// ErrPermission means the action needs more privileges than portview
	// has; rerunning with sudo usually helps.
	ErrPermission = errors.New("permission denied")
	// ErrProtected is returned for processes portview refuses to signal
	// (PID 1 and portview itself).
	ErrProtected = errors.New("refusing to signal this process")
)

// Process is a handle on one running process.
type Process interface {
	PID() int
	// Signal sends sig. It returns ErrGone if the process has exited and
	// an error wrapping ErrPermission if the caller may not signal it.
	Signal(sig syscall.Signal) error
	// Alive reports whether the process still runs (zombies count as
	// exited: they hold no sockets).
	Alive() bool
	// Release frees the handle.
	Release()
}

// OpenProcess returns a handle on pid, provided it is still the process
// that started at startTicks (see procs.Details.StartTicks); otherwise it
// returns ErrGone.
//
// On Linux the handle is a pidfd (os.FindProcess uses pidfd_open), so once
// opened, signals can only ever reach this process, never an unrelated one
// that later reuses the PID. The start time check covers the time between
// the user seeing the process and the handle being opened.
func OpenProcess(scanner procs.Scanner, pid int, startTicks uint64) (Process, error) {
	if pid <= 1 || pid == os.Getpid() {
		return nil, fmt.Errorf("%w: PID %d", ErrProtected, pid)
	}
	p, err := os.FindProcess(pid) // never fails on Unix
	if err != nil {
		return nil, err
	}
	// The pidfd is open; if the start time read now still matches, it
	// refers to the process the user confirmed.
	ticks, err := scanner.StartTicks(pid)
	if err != nil || ticks != startTicks {
		_ = p.Release()
		if err != nil && !errors.Is(err, procs.ErrExited) {
			return nil, err
		}
		return nil, fmt.Errorf("PID %d: %w", pid, ErrGone)
	}
	return &osProcess{pid: pid, p: p, scanner: scanner}, nil
}

type osProcess struct {
	pid     int
	p       *os.Process
	scanner procs.Scanner
}

func (p *osProcess) PID() int { return p.pid }

func (p *osProcess) Signal(sig syscall.Signal) error {
	err := p.p.Signal(sig)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrProcessDone), errors.Is(err, syscall.ESRCH):
		return fmt.Errorf("PID %d: %w", p.pid, ErrGone)
	case errors.Is(err, syscall.EPERM):
		return fmt.Errorf("%w: cannot signal PID %d", ErrPermission, p.pid)
	}
	return fmt.Errorf("PID %d: %w", p.pid, err)
}

func (p *osProcess) Alive() bool {
	// Signal 0 checks existence through the pidfd. EPERM means it exists.
	if err := p.p.Signal(syscall.Signal(0)); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	// The process still holds its PID, so /proc/<pid> is its entry.
	st, err := p.scanner.StateCode(p.pid)
	if err != nil {
		return !errors.Is(err, procs.ErrExited)
	}
	return st != 'Z' && st != 'X'
}

func (p *osProcess) Release() { _ = p.p.Release() }

// SignalAll sends sig to every process. Processes that are already gone are
// not an error. It returns the processes that were signalled and the
// errors of those that could not be.
func SignalAll(ps []Process, sig syscall.Signal) (sent []Process, err error) {
	var errs []error
	for _, p := range ps {
		switch e := p.Signal(sig); {
		case e == nil:
			sent = append(sent, p)
		case errors.Is(e, ErrGone):
		default:
			errs = append(errs, e)
		}
	}
	return sent, errors.Join(errs...)
}

// Wait polls until every process has exited or d has elapsed, and returns
// the ones still alive.
func Wait(ctx context.Context, ps []Process, d, poll time.Duration) []Process {
	if poll <= 0 {
		poll = 50 * time.Millisecond
	}
	deadline := time.Now().Add(d)
	for {
		var alive []Process
		for _, p := range ps {
			if p.Alive() {
				alive = append(alive, p)
			}
		}
		if len(alive) == 0 || !time.Now().Before(deadline) {
			return alive
		}
		ps = alive
		select {
		case <-ctx.Done():
			return alive
		case <-time.After(min(poll, time.Until(deadline))):
		}
	}
}

// PIDs lists the PIDs of ps for messages.
func PIDs(ps []Process) string {
	s := ""
	for i, p := range ps {
		if i > 0 {
			s += ", "
		}
		s += strconv.Itoa(p.PID())
	}
	return s
}
