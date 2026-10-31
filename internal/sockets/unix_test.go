package sockets

import (
	"testing"
)

// A trimmed capture of /proc/net/unix.
const unixTable = `Num       RefCount Protocol Flags    Type St Inode Path
0000000000000000: 00000002 00000000 00010000 0001 01 10662 /run/systemd/io.systemd.Manager
0000000000000000: 00000002 00000000 00000000 0002 01 9415 /run/systemd/journal/syslog
0000000000000000: 00000003 00000000 00000000 0001 03 430107
0000000000000000: 00000002 00000000 00010000 0001 01 23456 @/tmp/.X11-unix/X0
0000000000000000: 00000003 00000000 00000000 0005 03 777 /run/my dir/with space.sock
0000000000000000: 00000002 00000000 00010000 0001 01 888 /run/evil` + "\x1b[2J" + `.sock
garbage line
`

func TestParseProcNetUnix(t *testing.T) {
	us := ParseProcNetUnix([]byte(unixTable))
	if len(us) != 6 {
		t.Fatalf("got %d sockets", len(us))
	}
	mgr := us[0]
	if !mgr.Listen() || !mgr.Listening() || mgr.Type != UnixStream || mgr.StateLabel() != "LISTEN" ||
		mgr.Inode != 10662 || mgr.Path != "/run/systemd/io.systemd.Manager" || mgr.Type.Netid() != "u_str" {
		t.Errorf("listener = %+v", mgr)
	}
	syslog := us[1]
	if syslog.Listen() || !syslog.Listening() || syslog.StateLabel() != "UNCONN" || syslog.Type.String() != "dgram" {
		t.Errorf("dgram = %+v", syslog)
	}
	conn := us[2]
	if conn.Listening() || conn.StateLabel() != "ESTAB" || conn.Path != "" {
		t.Errorf("unnamed connection = %+v", conn)
	}
	if !us[3].Abstract() || us[3].Path != "@/tmp/.X11-unix/X0" {
		t.Errorf("abstract = %+v", us[3])
	}
	if us[4].Path != "/run/my dir/with space.sock" || us[4].Type != UnixSeqPacket {
		t.Errorf("path with space = %+v", us[4])
	}
	if us[5].Path == "/run/evil\x1b[2J.sock" {
		t.Error("escape sequence in path not sanitized")
	}
	for _, u := range us {
		if u.Suspicious {
			t.Errorf("%s marked suspicious", u.Path)
		}
	}
}

// TestForgedUnixLine: a path containing a newline appends a fake record
// that claims a well-known name for another socket's inode.
func TestForgedUnixLine(t *testing.T) {
	forged := "Num       RefCount Protocol Flags    Type St Inode Path\n" +
		"0000000000000000: 00000002 00000000 00010000 0001 01 26657 /run/docker.sock\n" +
		"0000000000000000: 00000002 00000000 00010000 0001 01 5555 /tmp/x\n" +
		"0000000000000000: 00000002 00000000 00010000 0001 01 26657 /run/important.sock\n"
	us := ParseProcNetUnix([]byte(forged))
	if len(us) != 3 || !us[0].Suspicious || us[1].Suspicious || !us[2].Suspicious {
		t.Errorf("suspicious flags = %+v", us)
	}
}
