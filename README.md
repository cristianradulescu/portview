# portview

A terminal UI for Linux that lists open TCP/UDP sockets, shows the process
behind each one, and lets you stop that process. It works like lazygit, but
for ports.

> **Status:** early development. Only the project scaffolding exists so far.

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
portview --dump               # print listening sockets and exit (not implemented yet)
portview --dump --state all   # print all sockets
portview --interval 5s        # set the refresh interval
portview --no-docker          # do not contact the Docker daemon
```

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
