package main

import (
	"context"
	"fmt"
	"io"
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
	if snap.DockerErr != nil {
		fmt.Fprintf(stderr, "\ncontainer names unavailable: %v\n", snap.DockerErr)
	}
	if snap.Hidden > 0 && !snap.Privileged {
		fmt.Fprintf(stderr, "\n%d socket(s) belong to processes you cannot inspect (PID shown as ?); rerun with sudo for full details.\n", snap.Hidden)
	}
	return nil
}

func writeDump(w io.Writer, snap *inventory.Snapshot, rows []inventory.Entry) {
	withContainers := false
	for i := range rows {
		if rows[i].Container != nil {
			withContainers = true
			break
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := "NETID\tSTATE\tLOCAL\tREMOTE\tEXPOSURE\tPID\tPROCESS\tUSER\tNETNS"
	if withContainers {
		header += "\tCONTAINER"
	}
	fmt.Fprintln(tw, header)
	for i := range rows {
		e := &rows[i]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s",
			e.Netid(),
			e.State,
			sockets.FormatAddrPort(e.Local),
			sockets.FormatAddrPort(e.Remote),
			e.Exposure,
			e.PIDLabel(snap.Privileged),
			e.ProcessLabel(snap.Privileged),
			e.User,
			snap.NetNSLabel(e.NetNS),
		)
		if withContainers {
			fmt.Fprintf(tw, "\t%s", orDash(e.ContainerLabel()))
		}
		fmt.Fprintln(tw)
	}
	tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
