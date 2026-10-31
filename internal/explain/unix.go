package explain

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/cristianradulescu/portview/internal/sockets"
	"github.com/cristianradulescu/portview/internal/termsafe"
)

// UnixTypeHelp says what a Unix socket type is.
func UnixTypeHelp(t sockets.UnixType) string {
	switch t {
	case sockets.UnixStream:
		return "a connection carrying a byte stream, like TCP (SOCK_STREAM)"
	case sockets.UnixDgram:
		return "separate messages, like UDP, but never lost or reordered on one machine (SOCK_DGRAM)"
	case sockets.UnixSeqPacket:
		return "a connection carrying messages that keep their boundaries (SOCK_SEQPACKET)"
	}
	return ""
}

// UnixStateHelp says what a Unix socket's state means.
func UnixStateHelp(u *sockets.UnixSocket) string {
	switch {
	case u.Listen():
		return "listen() was called: clients connect to it, and each accepted connection is a new, unnamed socket"
	case u.State == sockets.UnixUnconnected && u.Type == sockets.UnixDgram && u.Path != "":
		return "bound and not connected: receives messages from anyone who can reach its name"
	case u.State == sockets.UnixUnconnected:
		return "not connected"
	case u.State == sockets.UnixConnected:
		return "connected to a peer"
	case u.State == sockets.UnixConnecting:
		return "connecting"
	}
	return ""
}

// UnixPathHelp says what kind of name the socket has.
func UnixPathHelp(u *sockets.UnixSocket) string {
	switch {
	case u.Path == "":
		return "unnamed: one end of a connection (each accepted connection gets one) or of a socketpair(). " +
			"/proc/net/unix does not say which socket is its peer; ss -xp finds it through netlink"
	case u.Abstract():
		return "abstract: the name lives only in the kernel, not in the filesystem (the leading @ stands for a NUL byte). " +
			"It is scoped to the network namespace, and since there is no file there are no permissions: " +
			"anyone in the namespace can connect, unless the server checks the client's credentials (SO_PEERCRED)"
	}
	return "a file in the filesystem: connecting needs write permission on it"
}

// Access describes who may connect to a socket file.
type Access struct {
	Mode       string // e.g. "srw-rw----"
	Owner      string // "root:docker"
	Text       string // who can connect, and why
	CanConnect bool
	Missing    bool // the file is gone
}

// UnixAccess explains who may connect to the socket file at path, as the
// user euid with the given groups. names resolves UIDs and GIDs.
func UnixAccess(path string, euid int, groups []int, userName, groupName func(int) string) Access {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Access{Missing: true, Text: "The file no longer exists: it was deleted (or replaced) after the socket was bound. " +
				"Existing connections keep working, but nobody can connect by this name any more."}
		}
		return Access{Text: "Cannot check the file: " + err.Error()}
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Access{Text: "Cannot read the file's owner."}
	}
	uid, gid := int(st.Uid), int(st.Gid)
	perm := fi.Mode().Perm()
	// fs.FileMode writes a socket as "S"; ls writes "s".
	a := Access{Mode: "s" + strings.TrimPrefix(fi.Mode().String(), "S"), Owner: userName(uid) + ":" + groupName(gid)}
	if fi.Mode()&fs.ModeSocket == 0 {
		a.Text = "This path is not a socket file now: something else replaced it after the socket was bound."
		return a
	}
	w := func(bit fs.FileMode) bool { return perm&bit != 0 }
	switch {
	case euid == 0:
		a.CanConnect = true
		a.Text = "You are root: file permissions do not stop you."
	case euid == uid:
		a.CanConnect = w(0o200)
		a.Text = fmt.Sprintf("You own the file; the owner %s write permission.", hasOrNot(a.CanConnect))
	case slices.Contains(groups, gid):
		a.CanConnect = w(0o020)
		a.Text = fmt.Sprintf("You are in its group %s, which %s write permission.", groupName(gid), hasOrNot(a.CanConnect))
	default:
		a.CanConnect = w(0o002)
		a.Text = fmt.Sprintf("You are neither its owner (%s) nor in its group (%s); others %s write permission.",
			userName(uid), groupName(gid), hasOrNot(a.CanConnect))
	}
	if a.CanConnect {
		a.Text += " You can connect (the directories on the way must also be searchable)."
	} else {
		a.Text += " You cannot connect."
	}
	return a
}

func hasOrNot(b bool) string {
	if b {
		return "has"
	}
	return "lacks"
}

// ReadUnix finds the socket with inode in the /proc/net/unix table at path
// and explains its line.
func ReadUnix(path string, inode uint64) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if i == 0 {
			continue
		}
		f := strings.Fields(l)
		if len(f) >= 7 && f[6] == strconv.FormatUint(inode, 10) {
			fields, err := UnixFields(l)
			if err != nil {
				return nil, err
			}
			// The path is free text chosen by whoever bound the socket:
			// never print it raw.
			return &Report{Path: path, LineNo: i + 1, Header: termsafe.String(lines[0]), Line: termsafe.String(l), Fields: fields}, nil
		}
	}
	return nil, ErrNotFound
}

// UnixFields explains one line of /proc/net/unix (net/unix/af_unix.c,
// unix_seq_show).
func UnixFields(line string) ([]Field, error) {
	f := strings.Fields(line)
	if len(f) < 7 {
		return nil, fmt.Errorf("short line: %d fields", len(f))
	}
	us := sockets.ParseProcNetUnix([]byte("header\n" + line))
	if len(us) != 1 {
		return nil, fmt.Errorf("cannot parse %q", line)
	}
	u := &us[0]
	var out []Field
	add := func(name, raw, meaning string) { out = append(out, Field{Name: name, Raw: raw, Meaning: meaning}) }

	num := "kernel address of the socket"
	if strings.Trim(strings.TrimSuffix(f[0], ":"), "0") == "" {
		num += "; all zeros because kernel addresses are hidden from you (kernel.kptr_restrict, CAP_SYSLOG)"
	}
	add("Num", f[0], num)
	add("RefCount", f[1], fmt.Sprintf("%d references the kernel holds to the socket (in hex)", u.RefCount))
	add("Protocol", f[2], "always 0 for Unix sockets")
	flags := "no flags"
	if u.Listen() {
		flags = "0x10000 = __SO_ACCEPTCON: listen() was called on it"
	}
	add("Flags", f[3], flags)
	add("Type", f[4], fmt.Sprintf("%d = %s: %s", u.Type, u.Type, UnixTypeHelp(u.Type)))
	states := map[sockets.UnixState]string{
		sockets.UnixUnconnected: "SS_UNCONNECTED", sockets.UnixConnecting: "SS_CONNECTING",
		sockets.UnixConnected: "SS_CONNECTED", sockets.UnixDisconnecting: "SS_DISCONNECTING",
	}
	st := fmt.Sprintf("%d = %s", u.State, states[u.State])
	if u.Listen() {
		st += " (a listening socket is not connected to anyone; ss shows LISTEN from the flag)"
	}
	add("St", f[5], st)
	add("Inode", f[6], "the socket's inode: /proc/<pid>/fd/N → socket:["+f[6]+"] in the processes holding it")
	if u.Path != "" {
		add("Path", u.Path, UnixPathHelp(u))
	} else {
		add("Path", "(none)", UnixPathHelp(u))
	}
	return out, nil
}
