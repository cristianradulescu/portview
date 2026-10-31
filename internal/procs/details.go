package procs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cristianradulescu/portview/internal/termsafe"
)

// Details is what the details pane shows about one process. It is read on
// demand for the selected process only. Fields that could not be read are
// left empty and the reason is recorded in the matching *Err field.
type Details struct {
	PID        int
	Comm       string
	Cmdline    []string // each argument sanitized; empty for kernel threads
	Exe        string
	ExeErr     error
	Cwd        string
	CwdErr     error
	State      string // e.g. "S (sleeping)"
	PPID       int
	ParentComm string
	UID        int
	Threads    int
	StartTime  time.Time // zero if unknown
	// StartTicks is the raw start time in clock ticks since boot. Together
	// with the PID it identifies a process even after the PID is reused.
	StartTicks uint64
	Cgroup     string
	Unit       Unit
	Container  Container
}

// ErrExited is returned when the process no longer exists.
var ErrExited = errors.New("process exited")

// Details reads the details of pid. Fields guarded by ptrace access (exe,
// cwd) fail with a permission error for other users' processes when not
// root; the rest is world-readable.
func (s Scanner) Details(pid int) (*Details, error) {
	root := s.root()
	pdir := filepath.Join(root, strconv.Itoa(pid))
	status, err := os.ReadFile(filepath.Join(pdir, "status"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrExited
		}
		return nil, err
	}
	d := &Details{PID: pid}
	d.PPID, d.UID = parseStatus(status)
	d.State, d.Threads = parseStatusExtra(status)

	if b, err := os.ReadFile(filepath.Join(pdir, "comm")); err == nil {
		d.Comm = termsafe.String(strings.TrimRight(string(b), "\n"))
	}
	if b, err := os.ReadFile(filepath.Join(pdir, "cmdline")); err == nil {
		d.Cmdline = ParseCmdline(b)
	}
	if link, err := os.Readlink(filepath.Join(pdir, "exe")); err == nil {
		d.Exe = termsafe.String(link)
	} else {
		d.ExeErr = simplifyErr(err)
	}
	if link, err := os.Readlink(filepath.Join(pdir, "cwd")); err == nil {
		d.Cwd = termsafe.String(link)
	} else {
		d.CwdErr = simplifyErr(err)
	}
	if b, err := os.ReadFile(filepath.Join(pdir, "stat")); err == nil {
		if ticks, ok := parseStartTicks(b); ok {
			d.StartTicks = ticks
			if boot, ok := bootTime(root); ok {
				d.StartTime = boot.Add(time.Duration(ticks) * time.Second / time.Duration(clockTicks(root)))
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(pdir, "cgroup")); err == nil {
		d.Cgroup = termsafe.String(ParseCgroup(b))
		d.Unit, d.Container = ParseCgroupPath(d.Cgroup)
	}
	if d.PPID > 0 {
		if b, err := os.ReadFile(filepath.Join(root, strconv.Itoa(d.PPID), "comm")); err == nil {
			d.ParentComm = termsafe.String(strings.TrimRight(string(b), "\n"))
		}
	}
	return d, nil
}

// StartTicks reads the start time of pid in clock ticks since boot (see
// Details.StartTicks). It returns ErrExited when the process is gone.
func (s Scanner) StartTicks(pid int) (uint64, error) {
	b, err := os.ReadFile(filepath.Join(s.root(), strconv.Itoa(pid), "stat"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return 0, ErrExited
		}
		return 0, err
	}
	ticks, ok := parseStartTicks(b)
	if !ok {
		return 0, errors.New("malformed stat of PID " + strconv.Itoa(pid))
	}
	return ticks, nil
}

// StateCode reads the one-letter state of pid from /proc/<pid>/stat (e.g.
// 'S', 'R', 'D', 'Z' for a zombie). It returns ErrExited when the process
// is gone.
func (s Scanner) StateCode(pid int) (byte, error) {
	b, err := os.ReadFile(filepath.Join(s.root(), strconv.Itoa(pid), "stat"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return 0, ErrExited
		}
		return 0, err
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 >= len(b) {
		return 0, errors.New("malformed stat of PID " + strconv.Itoa(pid))
	}
	return b[i+2], nil
}

func simplifyErr(err error) error {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return fs.ErrPermission
	case errors.Is(err, fs.ErrNotExist):
		return fs.ErrNotExist
	}
	return err
}

// ParseCmdline splits /proc/<pid>/cmdline (NUL-separated) into sanitized
// arguments. Processes that rewrite their argv into one space-separated
// string (e.g. "nginx: master process") come back as a single argument.
func ParseCmdline(b []byte) []string {
	b = bytes.TrimRight(b, "\x00")
	if len(b) == 0 {
		return nil
	}
	parts := bytes.Split(b, []byte{0})
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = termsafe.String(string(p))
	}
	return out
}

// QuoteArgs joins arguments for display, single-quoting the ones that
// contain whitespace or quotes.
func QuoteArgs(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t'\"") {
			q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			q[i] = a
		}
	}
	return strings.Join(q, " ")
}

// parseStatusExtra extracts State and Threads from /proc/<pid>/status.
func parseStatusExtra(data []byte) (state string, threads int) {
	for line := range strings.SplitSeq(string(data), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "State":
			state = val
		case "Threads":
			threads, _ = strconv.Atoi(val)
		}
	}
	return state, threads
}

// parseStartTicks returns field 22 (starttime, in clock ticks since boot)
// of /proc/<pid>/stat. The comm field (2) may contain spaces and
// parentheses, so parsing starts after the last ')'.
func parseStartTicks(stat []byte) (uint64, bool) {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(string(stat[i+1:]))
	const idx = 22 - 3 // fields after ')' start at field 3 (state)
	if len(f) <= idx {
		return 0, false
	}
	v, err := strconv.ParseUint(f[idx], 10, 64)
	return v, err == nil
}

var (
	bootOnce sync.Map // root → time.Time
	hzOnce   sync.Map // root → int64
)

// bootTime reads btime from <root>/stat.
func bootTime(root string) (time.Time, bool) {
	if v, ok := bootOnce.Load(root); ok {
		return v.(time.Time), true
	}
	b, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return time.Time{}, false
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			sec, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				return time.Time{}, false
			}
			t := time.Unix(sec, 0)
			bootOnce.Store(root, t)
			return t, true
		}
	}
	return time.Time{}, false
}

// clockTicks returns USER_HZ, the unit of starttime. It is read from the
// AT_CLKTCK entry of <root>/self/auxv (no cgo/sysconf needed) and falls back
// to 100, its value on all mainstream Linux architectures.
func clockTicks(root string) int64 {
	if v, ok := hzOnce.Load(root); ok {
		return v.(int64)
	}
	hz := int64(100)
	if b, err := os.ReadFile(filepath.Join(root, "self", "auxv")); err == nil {
		if v, ok := parseAuxv(b, 17 /* AT_CLKTCK */); ok && v > 0 {
			hz = int64(v)
		}
	}
	hzOnce.Store(root, hz)
	return hz
}

// parseAuxv finds key in an auxiliary vector of native-endian word pairs.
func parseAuxv(b []byte, key uint64) (uint64, bool) {
	word := strconv.IntSize / 8
	read := func(p []byte) uint64 {
		if word == 8 {
			return binary.NativeEndian.Uint64(p)
		}
		return uint64(binary.NativeEndian.Uint32(p))
	}
	for i := 0; i+2*word <= len(b); i += 2 * word {
		k, v := read(b[i:]), read(b[i+word:])
		if k == 0 { // AT_NULL
			break
		}
		if k == key {
			return v, true
		}
	}
	return 0, false
}

func itoa(i int) string { return strconv.Itoa(i) }

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

// ClockTicks is the kernel's USER_HZ: the unit of the clock-tick values in
// /proc (timers in /proc/net/tcp, start times), usually 100 per second.
func (s Scanner) ClockTicks() int64 { return clockTicks(s.root()) }
