// Package procs scans /proc to map socket inodes to the processes holding them and
// reads per-process details (cmdline, cwd, start time, cgroup / systemd unit).
// Processes that vanish or cannot be inspected mid-scan are tolerated silently.
package procs
