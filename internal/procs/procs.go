package procs

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/cristianradulescu/portview/internal/termsafe"
)

// Process is what a scan knows about one process. Info fields (Comm, PPID,
// UID, Cgroup) are only loaded for processes that own sockets or lead a
// non-host network namespace; check Loaded.
type Process struct {
	PID int

	// NetNS is the key of the process's network namespace, 0 if unknown
	// (zombies, races). See Namespace.Key.
	NetNS uint64

	// Inspectable reports whether the fd table could be read. When false the
	// process may own sockets we cannot attribute to it.
	Inspectable bool

	Loaded bool
	Comm   string // sanitized for terminal output (see termsafe)
	PPID   int
	UID    int    // effective UID, -1 if unknown
	Cgroup string // cgroup v2 path (or the systemd v1 hierarchy path), "" if unknown
}

// Namespace groups the processes of one network namespace.
type Namespace struct {
	// Key identifies the namespace. It is the inode of /proc/<pid>/net/tcp,
	// a procfs entry the kernel creates once per network namespace. Unlike
	// the /proc/<pid>/ns/net link it can be read for any process without
	// privileges.
	Key uint64
	// Inode is the namespace inode as shown by `lsns` / /proc/<pid>/ns/net,
	// or 0 when no member's ns link was readable.
	Inode uint64
	// PIDs are the member processes in ascending order.
	PIDs []int
}

// Table is the result of one /proc scan.
type Table struct {
	// Procs holds every process seen during the scan.
	Procs map[int]*Process
	// ByInode maps a socket inode to the PIDs holding it, ascending.
	ByInode map[uint64][]int
	// Namespaces is sorted with the host namespace first, then by lowest PID.
	Namespaces []Namespace
	// HostNetNS is the key of the namespace of PID 1 (or of portview itself
	// when PID 1 is not visible).
	HostNetNS uint64
	// Uninspectable counts processes whose fd table could not be read.
	Uninspectable int
}

// Scanner walks a procfs tree.
type Scanner struct {
	// Root is the procfs mount point; empty means "/proc".
	Root string
}

func (s Scanner) root() string {
	if s.Root == "" {
		return "/proc"
	}
	return s.Root
}

// Scan walks /proc once, building the socket-inode → PID map and the set of
// network namespaces. Processes that vanish or cannot be read mid-scan are
// tolerated: they are skipped or marked not inspectable. Only failing to
// list the procfs root is an error.
func (s Scanner) Scan() (*Table, error) {
	root := s.root()
	dir, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	names, err := dir.Readdirnames(-1)
	dir.Close()
	if err != nil {
		return nil, err
	}

	t := &Table{
		Procs:   make(map[int]*Process, len(names)),
		ByInode: make(map[uint64][]int),
	}
	members := make(map[uint64][]int)

	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil || pid <= 0 {
			continue
		}
		pdir := filepath.Join(root, name)
		p := &Process{PID: pid, UID: -1, PPID: -1}
		if key, ok := netKey(pdir); ok {
			p.NetNS = key
			members[key] = append(members[key], pid)
		}
		if inodes, ok := socketInodes(pdir); ok {
			p.Inspectable = true
			for _, ino := range inodes {
				t.ByInode[ino] = append(t.ByInode[ino], pid)
			}
		} else {
			t.Uninspectable++
		}
		t.Procs[pid] = p
	}
	if len(t.Procs) == 0 {
		// Not a procfs, or hidepid hides everything; still try to find
		// ourselves so callers at least get the current namespace.
		if key, ok := netKey(filepath.Join(root, "self")); ok {
			pid := os.Getpid()
			t.Procs[pid] = &Process{PID: pid, NetNS: key, UID: -1, PPID: -1}
			members[key] = append(members[key], pid)
		}
	}

	for ino, pids := range t.ByInode {
		sort.Ints(pids)
		t.ByInode[ino] = dedupSorted(pids)
		for _, pid := range t.ByInode[ino] {
			t.load(root, pid)
		}
	}

	t.HostNetNS = hostKey(root, t)
	for key, pids := range members {
		sort.Ints(pids)
		ns := Namespace{Key: key, PIDs: pids, Inode: nsInode(root, pids)}
		t.Namespaces = append(t.Namespaces, ns)
		if key != t.HostNetNS {
			t.load(root, pids[0])
		}
	}
	sort.Slice(t.Namespaces, func(i, j int) bool {
		a, b := t.Namespaces[i], t.Namespaces[j]
		if (a.Key == t.HostNetNS) != (b.Key == t.HostNetNS) {
			return a.Key == t.HostNetNS
		}
		return a.PIDs[0] < b.PIDs[0]
	})
	return t, nil
}

// Namespace returns the namespace with the given key.
func (t *Table) Namespace(key uint64) (Namespace, bool) {
	for _, ns := range t.Namespaces {
		if ns.Key == key {
			return ns, true
		}
	}
	return Namespace{}, false
}

func (t *Table) load(root string, pid int) {
	p := t.Procs[pid]
	if p == nil || p.Loaded {
		return
	}
	pdir := filepath.Join(root, strconv.Itoa(pid))
	status, err := os.ReadFile(filepath.Join(pdir, "status"))
	if err != nil {
		return // process exited
	}
	p.Loaded = true
	p.PPID, p.UID = parseStatus(status)
	if comm, err := os.ReadFile(filepath.Join(pdir, "comm")); err == nil {
		p.Comm = termsafe.String(strings.TrimRight(string(comm), "\n"))
	}
	if cg, err := os.ReadFile(filepath.Join(pdir, "cgroup")); err == nil {
		p.Cgroup = ParseCgroup(cg)
	}
}

func hostKey(root string, t *Table) uint64 {
	if p, ok := t.Procs[1]; ok && p.NetNS != 0 {
		return p.NetNS
	}
	if key, ok := netKey(filepath.Join(root, "self")); ok {
		return key
	}
	return 0
}

// netKey returns the namespace key for the process directory pdir.
func netKey(pdir string) (uint64, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(filepath.Join(pdir, "net", "tcp"), &st); err != nil {
		return 0, false
	}
	return st.Ino, true
}

// nsInode returns the /proc/<pid>/ns/net inode from the first member whose
// link is readable (requires ptrace access, so typically own processes or
// root).
func nsInode(root string, pids []int) uint64 {
	for _, pid := range pids {
		link, err := os.Readlink(filepath.Join(root, strconv.Itoa(pid), "ns", "net"))
		if err != nil {
			continue
		}
		if ino, ok := parseBracketInode(link, "net:["); ok {
			return ino
		}
	}
	return 0
}

// socketInodes lists the socket inodes referenced by the process's fds.
// ok is false when the fd directory cannot be read (permissions, exited).
func socketInodes(pdir string) (inodes []uint64, ok bool) {
	fdDir := filepath.Join(pdir, "fd")
	d, err := os.Open(fdDir)
	if err != nil {
		return nil, false
	}
	fds, err := d.Readdirnames(-1)
	d.Close()
	if err != nil {
		return nil, false
	}
	for _, fd := range fds {
		link, err := os.Readlink(filepath.Join(fdDir, fd))
		if err != nil {
			continue // fd closed meanwhile
		}
		if ino, ok := parseBracketInode(link, "socket:["); ok {
			inodes = append(inodes, ino)
		}
	}
	return inodes, true
}

// parseBracketInode parses links like "socket:[12345]" or "net:[4026531840]".
func parseBracketInode(link, prefix string) (uint64, bool) {
	if !strings.HasPrefix(link, prefix) || !strings.HasSuffix(link, "]") {
		return 0, false
	}
	ino, err := strconv.ParseUint(link[len(prefix):len(link)-1], 10, 64)
	return ino, err == nil
}

// parseStatus extracts PPid and the effective UID from /proc/<pid>/status.
func parseStatus(data []byte) (ppid, uid int) {
	ppid, uid = -1, -1
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(val)
		switch key {
		case "PPid":
			if len(f) >= 1 {
				if v, err := strconv.Atoi(f[0]); err == nil {
					ppid = v
				}
			}
		case "Uid": // real, effective, saved, fs
			if len(f) >= 2 {
				if v, err := strconv.Atoi(f[1]); err == nil {
					uid = v
				}
			}
		}
	}
	return ppid, uid
}

// ParseCgroup returns the cgroup v2 path ("0::/system.slice/x.service") from
// the contents of /proc/<pid>/cgroup, falling back to the name=systemd v1
// hierarchy on legacy/hybrid setups.
func ParseCgroup(data []byte) string {
	var v1 string
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "0" && parts[1] == "" {
			return parts[2]
		}
		if parts[1] == "name=systemd" {
			v1 = parts[2]
		}
	}
	return v1
}

func dedupSorted(s []int) []int {
	if len(s) < 2 {
		return s
	}
	out := s[:1]
	for _, v := range s[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}
