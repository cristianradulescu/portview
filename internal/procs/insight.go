package procs

import (
	"bytes"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/cristianradulescu/portview/internal/termsafe"
)

// ProcRef names a process.
type ProcRef struct {
	PID  int
	Comm string
}

// NSInfo is one namespace of a process, compared with portview's own.
type NSInfo struct {
	Type  string // "net", "mnt", "pid", …
	Inode uint64
	// Shared is true when portview is in the same namespace.
	Shared bool
}

// Insight is the extra information about a process shown to explain it:
// resources, privileges, family and isolation. Fields that could not be
// read keep their "unknown" value.
type Insight struct {
	RSS       uint64 // resident memory in bytes; 0 if unknown
	CPUTicks  uint64 // user + system CPU time in clock ticks
	CapEff    uint64 // effective capabilities
	CapsKnown bool
	FDs       int    // open file descriptors; -1 if unknown (other users' processes need root)
	MaxFDs    uint64 // soft RLIMIT_NOFILE; 0 if unknown, math.MaxUint64 for unlimited
	// Ancestors lists the parent, its parent and so on up to PID 1.
	Ancestors []ProcRef
	Children  []ProcRef
	// Namespaces is nil when /proc/<pid>/ns is not readable (other
	// users' processes need root); NSErr says why.
	Namespaces []NSInfo
	NSErr      error
}

// maxAncestors bounds the walk up the process tree.
const maxAncestors = 64

// nsTypes are the namespaces compared, most telling first.
var nsTypes = []string{"net", "mnt", "pid", "uts", "ipc", "user", "cgroup"}

func (s Scanner) readInsight(pid int, status, stat []byte) Insight {
	root := s.root()
	pdir := filepath.Join(root, strconv.Itoa(pid))
	in := Insight{FDs: -1}
	in.RSS, in.CapEff, in.CapsKnown = parseStatusInsight(status)
	in.CPUTicks = parseCPUTicks(stat)
	if ents, err := os.ReadDir(filepath.Join(pdir, "fd")); err == nil {
		in.FDs = len(ents)
	}
	if b, err := os.ReadFile(filepath.Join(pdir, "limits")); err == nil {
		in.MaxFDs = parseMaxOpenFiles(b)
	}
	in.Ancestors = s.ancestors(pid, status)
	in.Children = s.children(pid)
	in.Namespaces, in.NSErr = s.namespaces(pdir)
	return in
}

// parseStatusInsight extracts VmRSS (kB) and CapEff (hex) from status.
func parseStatusInsight(data []byte) (rss, capEff uint64, capsKnown bool) {
	for line := range strings.SplitSeq(string(data), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "VmRSS":
			if kb, err := strconv.ParseUint(strings.TrimSuffix(val, " kB"), 10, 64); err == nil {
				rss = kb * 1024
			}
		case "CapEff":
			if v, err := strconv.ParseUint(val, 16, 64); err == nil {
				capEff, capsKnown = v, true
			}
		}
	}
	return rss, capEff, capsKnown
}

// parseCPUTicks sums utime and stime (fields 14 and 15) of stat.
func parseCPUTicks(stat []byte) uint64 {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(string(stat[i+1:]))
	if len(f) <= 15-3 {
		return 0
	}
	u, _ := strconv.ParseUint(f[14-3], 10, 64)
	s, _ := strconv.ParseUint(f[15-3], 10, 64)
	return u + s
}

// parseMaxOpenFiles returns the soft "Max open files" limit.
func parseMaxOpenFiles(limits []byte) uint64 {
	for line := range strings.SplitSeq(string(limits), "\n") {
		rest, ok := strings.CutPrefix(line, "Max open files")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			return 0
		}
		if f[0] == "unlimited" {
			return math.MaxUint64
		}
		v, _ := strconv.ParseUint(f[0], 10, 64)
		return v
	}
	return 0
}

func (s Scanner) ancestors(pid int, status []byte) []ProcRef {
	var out []ProcRef
	ppid, _ := parseStatus(status)
	seen := map[int]bool{pid: true}
	for ppid > 0 && !seen[ppid] && len(out) < maxAncestors {
		seen[ppid] = true
		b, err := os.ReadFile(filepath.Join(s.root(), strconv.Itoa(ppid), "status"))
		if err != nil {
			out = append(out, ProcRef{PID: ppid, Comm: "?"})
			break
		}
		out = append(out, ProcRef{PID: ppid, Comm: statusName(b)})
		ppid, _ = parseStatus(b)
	}
	return out
}

// children reads /proc/<pid>/task/*/children: the children of every
// thread of the process.
func (s Scanner) children(pid int) []ProcRef {
	files, _ := filepath.Glob(filepath.Join(s.root(), strconv.Itoa(pid), "task", "*", "children"))
	var pids []int
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, w := range strings.Fields(string(b)) {
			if c, err := strconv.Atoi(w); err == nil {
				pids = append(pids, c)
			}
		}
	}
	sort.Ints(pids)
	pids = dedupSorted(pids)
	out := make([]ProcRef, 0, len(pids))
	for _, c := range pids {
		name := "?"
		if b, err := os.ReadFile(filepath.Join(s.root(), strconv.Itoa(c), "comm")); err == nil {
			name = termsafe.String(strings.TrimRight(string(b), "\n"))
		}
		out = append(out, ProcRef{PID: c, Comm: name})
	}
	return out
}

func (s Scanner) namespaces(pdir string) ([]NSInfo, error) {
	self := filepath.Join(s.root(), "self", "ns")
	var out []NSInfo
	for _, t := range nsTypes {
		link, err := os.Readlink(filepath.Join(pdir, "ns", t))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // namespace type not supported by this kernel
			}
			return nil, simplifyErr(err)
		}
		ino, ok := parseBracketInode(link, t+":[")
		if !ok {
			continue
		}
		info := NSInfo{Type: t, Inode: ino}
		if own, err := os.Readlink(filepath.Join(self, t)); err == nil {
			info.Shared = own == link
		}
		out = append(out, info)
	}
	return out, nil
}

func statusName(status []byte) string {
	for line := range strings.SplitSeq(string(status), "\n") {
		if v, ok := strings.CutPrefix(line, "Name:"); ok {
			return termsafe.String(strings.TrimSpace(v))
		}
	}
	return "?"
}

// capNames are the capability bits, in order (include/uapi/linux/capability.h).
var capNames = []string{
	"chown", "dac_override", "dac_read_search", "fowner", "fsetid", "kill", "setgid", "setuid",
	"setpcap", "linux_immutable", "net_bind_service", "net_broadcast", "net_admin", "net_raw",
	"ipc_lock", "ipc_owner", "sys_module", "sys_rawio", "sys_chroot", "sys_ptrace", "sys_pacct",
	"sys_admin", "sys_boot", "sys_nice", "sys_resource", "sys_time", "sys_tty_config", "mknod",
	"lease", "audit_write", "audit_control", "setfcap", "mac_override", "mac_admin", "syslog",
	"wake_alarm", "block_suspend", "audit_read", "perfmon", "bpf", "checkpoint_restore",
}

// CapNetBindService is the capability needed to bind ports below
// net.ipv4.ip_unprivileged_port_start.
const CapNetBindService = 10

// HasCap reports whether capability bit c is in mask.
func HasCap(mask uint64, c int) bool { return mask&(1<<c) != 0 }

// CapNames lists the capabilities in mask ("cap_" prefix dropped). Bits
// this list does not know are shown as numbers.
func CapNames(mask uint64) []string {
	var out []string
	for i := range 64 {
		if mask&(1<<i) == 0 {
			continue
		}
		if i < len(capNames) {
			out = append(out, capNames[i])
		} else {
			out = append(out, "cap"+strconv.Itoa(i))
		}
	}
	return out
}

// AllCaps reports whether mask holds every capability this kernel list
// knows (a process running as root, typically).
func AllCaps(mask uint64) bool {
	all := uint64(1)<<len(capNames) - 1
	return mask&all == all
}
