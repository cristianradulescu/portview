# portview

A terminal UI for Linux that lists open TCP/UDP sockets, shows the process
behind each one, and lets you stop that process. It works like lazygit, but
for ports.

> **Status:** v0.1.0, the first release. See [CHANGELOG.md](CHANGELOG.md).

## Features

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
- Works without root, and says what you would see or be able to do with `sudo`.

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
portview                              # start the TUI
portview --dump                       # print listening sockets and exit
portview --dump --state established   # established connections only
portview --dump --state all           # every socket, TIME-WAIT included
portview --state all                  # start the TUI showing every socket
portview --interval 5s                # set the refresh interval (default 2s)
portview --no-docker                  # do not contact the Docker daemon
```

### TUI

The socket list is on the left and details for the selected socket are on
the right. The details pane is hidden when the terminal is narrower than 110
columns. It shows:

- **The socket:** endpoint, state, exposure, network namespace, inode and queue sizes.
- **The owning process** (the primary one when several share the socket):
  - full command line, executable, working directory, user, start time and parent;
  - its systemd unit, from `/proc/<pid>/cgroup`;
  - its container ID, when it runs in Docker or Podman.
- **All processes sharing the socket**, such as the workers of a forking server.
- **The process's other sockets.**

Process details are read only for the selected process, at most once per
refresh. Without root, the executable and working directory of other users'
processes show as `?`. The status bar at the bottom shows the active filters, the row
count, a hint when some owners are hidden and the time of the last refresh.

The list refreshes every `--interval`. The selected socket stays selected
when rows appear or disappear around it. If it disappears, the next row
moves into its place. Sockets that appeared in the latest refresh are shown
in bold.

| Key | Action |
|---|---|
| `j` / `↓`, `k` / `↑` | next / previous socket |
| `PgDn` / `Ctrl-d`, `PgUp` / `Ctrl-u` | page down / up |
| `g` / `Home`, `G` / `End` | first / last socket |
| `/` | filter by port, address, process name, PID or user. The list updates as you type; `Enter` keeps the filter, `Esc` clears it |
| `Esc` | clear the text filter |
| `l` / `e` / `a` | listening / established / all sockets |
| `t` / `u` | TCP only / UDP only (press again to show both) |
| `4` / `6` | IPv4 only / IPv6 only (press again to show both) |
| `Tab` | focus the details pane (`j`/`k`, `PgUp`/`PgDn` scroll; `Tab`/`Esc` go back) |
| `x` | stop what holds the selected socket (see [Stopping a process](#stopping-a-process)) |
| `r` | refresh now |
| `?` | help |
| `q`, `Ctrl-c` | quit |

In the text filter, a bare number matches a port exactly (`80` does not
match `8080`) or a PID. Text matches a substring of the address, process
name, user or state. Separate several terms with spaces; a row must match
all of them.

Exposure colors: **red** means all interfaces, **yellow** one interface,
**green** loopback only, and **cyan** another network namespace.

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

### Stopping a process

`x` opens a confirmation dialog for the selected socket. It shows the socket,
every process holding it (PID, user and command line, the primary one
marked), its systemd unit and its container, and offers what applies:

| Option | When | What it does |
|---|---|---|
| `SIGTERM PID …` | a process holding the socket is visible | one entry per process (the primary first), plus "all N processes" when the socket is shared; `s` changes the signal |
| `systemctl stop <unit>` | the primary process belongs to a systemd `.service` | `systemctl [--user] stop`; the unit stays stopped |
| `docker stop <name>` | the socket belongs to a Docker container, or is the `docker-proxy` of a published port | stops the container through the Docker API (10s grace, then SIGKILL) |

Pick with `j`/`k` and `Enter`, or with the number keys. `Esc` cancels.
`s` cycles the signal the process options send: SIGTERM (default), SIGKILL,
SIGINT, SIGHUP and SIGQUIT. SIGKILL comes second, so a process you already
know ignores SIGTERM is one keypress away. The footer says what the chosen
signal usually does (SIGHUP, for example, makes many daemons reload their
configuration rather than exit).

- **Default choice.** SIGTERM to the primary process. For a container it
  is `docker stop`: a process killed inside a container is usually
  restarted by its restart policy, and killing a `docker-proxy` only breaks
  the port mapping.
- **Then SIGKILL.** After the signal, portview waits up to 2 seconds. If
  the process is still running, it offers SIGKILL, waiting another 2
  seconds, or leaving it running. After SIGKILL it waits 1 second and
  reports a process that still hasn't exited (stuck in the kernel).
- **systemd services.** The dialog shows the unit's `Restart=` setting
  (read with `systemctl show`). A killed process may be restarted by
  systemd; `systemctl stop` keeps it down. `systemctl` never prompts for a
  password (`--no-ask-password`): without the right privileges it fails
  and portview suggests `sudo`. User services are stopped with
  `systemctl --user`, and as root with `--user --machine=<user>@.host`.
- **What is never offered.** Signalling PID 1 or the systemd user manager
  (they hold sockets for socket activation: stop the `.socket` unit
  instead), and stopping `docker.service` for a `docker-proxy` socket.
- **Safety against PID reuse.** The dialog records each process's start
  time. Before signalling, portview opens a pidfd for the PID and checks
  the start time still matches, so a process that exited while the dialog
  was open is reported as gone instead of an unrelated process that got
  its PID being signalled.

The list refreshes as soon as the action completes, and the result shows
in the status bar. `systemctl stop` and `docker stop` run in the
background; `Esc` hides their progress dialog.

## Docker

Containers have their own network namespaces, so their sockets don't show
up in the host's `/proc/net/tcp` or in a plain `ss`. portview reads each
namespace separately and connects sockets to containers in three ways:

- **Inside a container.** The owning process's cgroup
  (`docker-<id>.scope` or `/docker/<id>`) gives the container ID. When the
  owner can't be inspected, the first process in the socket's namespace is
  used instead, which works without root.
- **A published port.** A host socket listening on a port that a container
  publishes is that container's `docker-proxy`. It shows as `→name:port`,
  even when its PID is hidden.
- **Names.** Container names, images and docker compose project/service
  come from the Docker API on `/var/run/docker.sock`, or from `DOCKER_HOST`
  when it's a `unix://` URL.

The CONTAINER column only appears when the list has container sockets. If
the daemon isn't installed, portview says nothing about it. If the socket
isn't accessible to you, containers are still identified by ID and the
status bar says that names are unavailable. `--no-docker` skips the API
entirely.

The Docker client is about 200 lines of `net/http` over the unix socket,
rather than the Docker SDK and its large dependency tree. It only needs to
list containers and, for the kill action, stop one.

Podman containers (`libpod-<id>.scope`) are identified by ID only.

A container ID is only trusted from cgroups that only root can create.
Inside a user's own systemd subtree (`user@UID.service`), users can name
cgroups however they like, so a `docker-<id>.scope` there is ignored.
Otherwise a user could pass their own socket off as someone else's
container and get `docker stop` offered for it. For the same reason, a
published-port socket is only linked to its container when it belongs to
root, as `docker-proxy`'s sockets do. As a result, containers of rootless
Docker are not linked.

The same rule applies below any unit or container. systemd never places a
unit inside another, so only the outermost unit and container in a cgroup
path count. A process inside container A that creates
`docker-A.scope/docker-B.scope` or `docker-A.scope/nginx.service` stays
attributed to container A, not to container B or the host's nginx.

## Privileges

portview works without root. A socket's user is visible to everyone, and so
are the sockets inside other network namespaces such as containers. Which
process owns a socket can only be resolved for processes you're allowed to
inspect (in practice, your own). Other owners show as `?`, and portview prints
a hint to rerun with `sudo`. As root, everything is resolved.

The same applies to the kill action:

| Action | Without root | With `sudo` |
|---|---|---|
| Signal a process | your own processes | any process |
| `systemctl --user stop` | your own user services | any user's services |
| `systemctl stop` (system services) | only if polkit allows it without a password | yes |
| `docker stop` | if you can use the Docker socket (`docker` group) | yes |

Without root, other users' processes are hidden, so the dialog can only
offer `docker stop` for their sockets (when they belong to a container).

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
internal/docker/       minimal Docker API client over the unix socket (list, stop)
internal/termsafe/     escaping of untrusted strings before they reach the terminal
internal/inventory/    snapshot assembly, filtering, ordering, diff and selection
internal/actions/      what to offer for a socket (Plan) and doing it: signals, systemctl stop, docker stop
internal/ui/tui/       generic gocui building blocks
internal/ui/portsview/ portview-specific panels and dialogs
```
