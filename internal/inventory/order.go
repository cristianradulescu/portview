package inventory

// Less is the stable display order: protocol, local port, family, local
// address, remote endpoint, namespace, inode. It depends only on socket
// identity, never on volatile fields such as queues or PIDs, so rows keep
// their relative position across refreshes.
func Less(a, b *Entry) bool {
	if a.Proto != b.Proto {
		return a.Proto < b.Proto
	}
	if ap, bp := a.Local.Port(), b.Local.Port(); ap != bp {
		return ap < bp
	}
	if a.Family != b.Family {
		return a.Family < b.Family
	}
	if c := a.Local.Addr().Compare(b.Local.Addr()); c != 0 {
		return c < 0
	}
	if c := a.Remote.Compare(b.Remote); c != 0 {
		return c < 0
	}
	if a.NetNS != b.NetNS {
		return a.NetNS < b.NetNS
	}
	return a.Inode < b.Inode
}
