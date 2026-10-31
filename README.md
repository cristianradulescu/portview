# portview

A terminal UI for Linux that lists open TCP/UDP sockets, shows the process
behind each one, and lets you stop that process. It works like lazygit, but
for ports.

> **Status:** early development. Socket collection and `--dump` work; the TUI is not implemented yet.

## Planned features (v1)

- Socket list for TCP, TCP6, UDP and UDP6: protocol, local and remote address,
  state, PID, process name and user.
- Filters for listening, established or all sockets, for TCP or UDP, for IPv4
  or IPv6, and a free-text search.
- An exposure flag that tells sockets bound to all interfaces apart from
  loopback-only ones.
- A details pane with the process's cmdline, cwd, user, start time, systemd
  unit, Docker container and its other sockets.
- Docker awareness: sockets in container network namespaces, with container
  names resolved through the Docker socket.
- A kill action with a confirmation step. It sends SIGTERM and then offers
  SIGKILL. For a systemd unit it offers `systemctl stop`, and for a container
  it offers `docker stop`.
- Auto-refresh that keeps your selection in place.

## Requirements

- Linux, with Ubuntu as the main target
- Go 1.26 or newer to build

## Build

```sh
make build        # writes ./bin/portview
make install      # go install into $GOBIN
```

## Usage

```sh
portview                      # start the TUI (not implemented yet)
portview --dump                       # print listening sockets and exit
portview --dump --state established   # established connections only
portview --dump --state all           # every socket, TIME-WAIT included
portview --interval 5s        # set the refresh interval
portview --no-docker          # do not contact the Docker daemon
```

### `--dump` output

```
NETID  STATE   LOCAL          REMOTE     EXPOSURE  PID    PROCESS  USER  NETNS
tcp    LISTEN  0.0.0.0:80     0.0.0.0:*  all       812+4  nginx    root  host
tcp    LISTEN  127.0.0.1:631  0.0.0.0:*  loopback  1033   cupsd    root  host
```

- **EXPOSURE:** applies to listening sockets only.
  - `all`: bound to `0.0.0.0` or `::`, so reachable on every interface.
  - `iface`: bound to one specific address.
  - `loopback`: local access only.
  - `netns`: listening inside another network namespace, such as a container.
- **PID:** `812+4` means PID 812 holds the socket together with 4 other
  processes, for example forked workers. The first PID is the master process.
  `?` means the owner can't be inspected without root. `-` means the socket has
  no owning process, as with TIME-WAIT or kernel sockets.
- **NETNS:** `host`, or the first process in the namespace, e.g. `containerd-shim(4242)`.

The output matches `ss -tuln` with two exceptions, because `/proc/net` does
not expose this information:

- A dual-stack IPv6 socket (one that also accepts IPv4) shows as `[::]:port`.
  `ss` shows it as `*:port`.
- `ss` adds the interface name for sockets bound to one device (`%lo`).
  portview leaves it out.

## Privileges

portview works without root. A socket's user is visible to everyone, and so
are the sockets inside other network namespaces such as containers. Which
process owns a socket can only be resolved for processes you're allowed to
inspect (in practice, your own). Other owners show as `?`, and portview prints
a hint to rerun with `sudo`. As root, everything is resolved.

## How it works

1. A single pass over `/proc` maps socket inodes to PIDs through the
   `/proc/<pid>/fd/*` → `socket:[inode]` links. The same pass groups processes
   by network namespace. The namespace key is the inode of
   `/proc/<pid>/net/tcp`, which the kernel creates once per namespace and
   which anyone can read.
2. For each namespace, portview reads `/proc/<pid>/net/{tcp,tcp6,udp,udp6}`
   through one of its member processes. Container sockets are picked up this
   way without `setns()` and without root.
3. Sockets are joined to processes by inode. When a socket is shared, as with
   forking servers, the *primary* owner is the process whose parent isn't
   also an owner. PID 1 is skipped when another owner exists, so a
   socket-activated service resolves to the service, not systemd.

## Development

```sh
make check   # gofmt check, go vet, staticcheck, tests
make test    # tests only, with the race detector
make lint    # staticcheck (pinned as a go.mod tool, no global install needed)
make help    # list all targets
```

## Layout

```
cmd/portview/          entry point and flags
internal/sockets/      socket collection from /proc/<pid>/net/*, per network namespace
internal/procs/        /proc scan: socket inode → PIDs, process details, systemd unit
internal/docker/       minimal Docker API client over the unix socket
internal/inventory/    snapshot assembly, filtering, ordering, diff and selection
internal/actions/      kill / systemctl stop / docker stop
internal/ui/tui/       generic gocui building blocks
internal/ui/portsview/ portview-specific panels and dialogs
```
