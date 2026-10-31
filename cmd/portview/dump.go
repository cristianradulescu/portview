package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/sockets"
)

// dump prints one snapshot as a table, mirroring the information of
// `ss -tulpn` plus exposure, user and network namespace.
func dump(ctx context.Context, c *inventory.Collector, filter inventory.Filter, stdout, stderr io.Writer) error {
	snap, err := c.Collect(ctx)
	if err != nil {
		return err
	}
	rows := filter.Apply(snap.Entries)
	writeDump(stdout, snap, rows)
	if snap.Hidden > 0 && !snap.Privileged {
		fmt.Fprintf(stderr, "\n%d socket(s) belong to processes you cannot inspect (PID shown as ?); rerun with sudo for full details.\n", snap.Hidden)
	}
	return nil
}

func writeDump(w io.Writer, snap *inventory.Snapshot, rows []inventory.Entry) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NETID\tSTATE\tLOCAL\tREMOTE\tEXPOSURE\tPID\tPROCESS\tUSER\tNETNS")
	for i := range rows {
		e := &rows[i]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			e.Netid(),
			e.State,
			sockets.FormatAddrPort(e.Local),
			sockets.FormatAddrPort(e.Remote),
			e.Exposure,
			pidColumn(e, snap.Privileged),
			processColumn(e, snap.Privileged),
			e.User,
			netnsColumn(e, snap),
		)
	}
	tw.Flush()
}

func pidColumn(e *inventory.Entry, privileged bool) string {
	switch {
	case e.Primary != nil && len(e.Owners) > 1:
		return fmt.Sprintf("%d+%d", e.Primary.PID, len(e.Owners)-1)
	case e.Primary != nil:
		return strconv.Itoa(e.Primary.PID)
	case e.Inode == 0 || privileged:
		return "-" // no file (TIME-WAIT) or kernel-owned
	default:
		return "?"
	}
}

func processColumn(e *inventory.Entry, privileged bool) string {
	switch {
	case e.Primary != nil && e.Primary.Comm != "":
		return e.Primary.Comm
	case e.Primary != nil:
		return "?" // exited before its details were read
	case e.Inode == 0 || privileged:
		return "-"
	default:
		return "?"
	}
}

// netnsColumn names a namespace "host", or after its first process, e.g.
// "bwrap(4242)", which is meaningful even without privileges.
func netnsColumn(e *inventory.Entry, snap *inventory.Snapshot) string {
	if e.HostNetNS {
		return "host"
	}
	ns, ok := snap.Procs.Namespace(e.NetNS)
	if !ok || len(ns.PIDs) == 0 {
		return "?"
	}
	leader := ns.PIDs[0]
	name := "?"
	if p := snap.Procs.Procs[leader]; p != nil && p.Comm != "" {
		name = p.Comm
	}
	return fmt.Sprintf("%s(%d)", name, leader)
}
