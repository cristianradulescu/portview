# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-10-31

portview as a way to learn: every view comes with the shell commands
that show the same, the kernel's raw data explained, and more about
sockets, ports and processes; plus an event timeline and Unix sockets.

### Added

- Equivalent commands in the details pane: the `ss` command matching the
  current filters, and for the selected socket `ss`, `lsof`, its line in
  the raw `/proc/net` table (address decoded from hex), the `find` that
  maps its inode to a process, and `ps`, `/proc`, `systemctl`,
  `journalctl`, `docker` and Docker API (`curl --unix-socket`) commands
  for its process, unit and container. Names are shell-quoted.
- Command log panel: what portview read and did, as commands, including
  every kill-dialog action and its result.
- The kill dialog shows the exact command of the highlighted option.
- `y` copies a command (wl-copy, xclip, xsel, or OSC 52).
- `Tab` cycles the list, the details pane and the command log.
- Kernel view (`i`): the selected socket's raw `/proc/net` line with every
  field decoded and explained, including the unnamed TCP fields (rto,
  ato, congestion window, slow-start threshold or Fast Open queue) and
  UDP's drop counter; values worth a look are highlighted.
- The details pane explains more:
  - what the socket's state means;
  - the service registered for the port in `/etc/services`;
  - where the port comes from: privileged ports and
    `CAP_NET_BIND_SERVICE` (held as root, granted, or dropped after
    binding), ports picked from the ephemeral range, and the server side
    of accepted connections;
  - the process's memory, CPU time, open files and limit, effective
    capabilities, ancestry, children, and namespaces not shared with
    portview.
- Equivalent commands for these: `getent services`, `sysctl`, `pstree`,
  `getpcaps`, `prlimit` and `/proc/<pid>/ns`.
- Events tab (`[`/`]`): sockets that opened and closed while portview
  runs, newest first, filtered like the list, with the owner and (for
  listeners) the command line recorded at the time; `Enter` jumps to a
  socket that is still open. The selection follows new events or stays
  put.
- Unix tab: Unix domain sockets of every network namespace from
  `/proc/net/unix`, with type, state, path (file, abstract or unnamed) and
  owner. The details pane explains the type, state and kind of name, and
  for socket files their permissions and whether you can connect; the
  process section is the same as for IP sockets. `i` explains the raw
  line, and the equivalent commands cover `ss -x`, `ls -l` and the
  kernel table.

### Fixed

- Wrapped text could start with a space when a word exactly filled the
  previous line, misaligning continuation lines in the details pane.

### Security

- Container IDs from the Docker API that are not 64 hex digits are
  ignored, so a malformed ID can never reach a command or an API path.
- Generated `systemctl` and `docker` commands put `--` before unit and
  container names, so a name that looks like an option (users name units
  freely in their own systemd subtree) stays a name.

## [0.1.1] - 2026-10-31

### Fixed

- In dialogs, `Enter` could run the option chosen the first time a
  dialog was confirmed instead of the highlighted one: the binding kept
  the index from its first use. In the kill dialog this could run a
  different action than the one shown selected.

## [0.1.0] - 2026-10-31

First release.

### Added

- Socket list of TCP, TCP6, UDP and UDP6 sockets with protocol, local and
  remote address, state, exposure, PID, process and user, read natively
  from `/proc` without `ss`, `netstat` or `lsof`.
- Sockets of every network namespace, including containers, read through
  `/proc/<pid>/net/*` without `setns()` or root.
- Ownership of shared sockets: all processes holding a socket, with the
  primary (master) process identified; `812+4` in the PID column.
- Exposure flag: all interfaces, one interface, loopback only, or another
  network namespace, color-coded.
- Filters: listening, established or all sockets; TCP or UDP; IPv4 or
  IPv6; and a free-text filter on port, address, process, PID, user and
  container name, applied as you type.
- Details pane with the process's command line, executable, working
  directory, user, start time, parent, systemd unit, container, the other
  processes sharing the socket and the process's other sockets.
- Docker awareness through a minimal Docker Engine API client over the
  unix socket: container names, images and compose project/service, and
  host ports published by a container (`→name:port`). Containers are
  still identified by ID when the API is unavailable; `--no-docker` skips
  it.
- Kill action (`x`): a confirmation dialog listing every process holding
  the socket with its user, command line, unit and container, offering:
  - a signal to one process or all of them: SIGTERM by default, `s`
    cycles SIGKILL, SIGINT, SIGHUP and SIGQUIT;
  - SIGKILL for processes still running 2 seconds after the signal;
  - `systemctl [--user] stop` for systemd services, showing the unit's
    `Restart=` policy;
  - `docker stop` for containers, including `docker-proxy` sockets of
    published ports (preselected for containers).
- Auto-refresh (`--interval`, default 2s) that keeps the selection in
  place and highlights new sockets; `r` refreshes immediately, as does
  every completed action.
- Unprivileged operation: owners that cannot be inspected show as `?`
  with a hint to rerun with `sudo`.
- `--dump` prints the socket table to stdout and exits; `--state` sets
  the initial state filter for both modes.
- Help screen (`?`) generated from the keybindings.

### Security

- Untrusted strings (process names, command lines, paths, unit and
  container names, Docker API messages) are escaped before they reach the
  terminal, so control and bidi characters cannot alter the display.
- Processes are signalled through a pidfd and checked against the start
  time recorded when the dialog opened, so a PID reused by another
  process is never signalled. PID 1 and the systemd user manager are never
  offered.
- Container and unit attribution cannot be spoofed with cgroup names:
  only the outermost unit and container of a cgroup path count, Docker
  container IDs inside a user's delegated subtree are ignored, a
  published-port socket is linked to its container only when it belongs
  to root, and `systemctl stop` is offered for a user service only when
  the process runs as that user. `docker.service` is never offered for a
  `docker-proxy` socket.
- `systemctl` runs with `--no-ask-password` and never prompts on the
  terminal.

[Unreleased]: https://github.com/cristianradulescu/portview/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/cristianradulescu/portview/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/cristianradulescu/portview/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/cristianradulescu/portview/releases/tag/v0.1.0
