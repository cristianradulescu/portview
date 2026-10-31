// Command portview is a terminal UI for inspecting open TCP/UDP sockets and
// the processes that own them.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// config holds the parsed command-line options.
type config struct {
	interval time.Duration
	noDocker bool
	dump     bool
	state    string
	version  bool
}

var validStates = map[string]bool{"listen": true, "established": true, "all": true}

func parseFlags(args []string, errOut io.Writer) (config, error) {
	var cfg config
	fs := flag.NewFlagSet("portview", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.DurationVar(&cfg.interval, "interval", 2*time.Second, "auto-refresh interval (e.g. 2s, 500ms)")
	fs.BoolVar(&cfg.noDocker, "no-docker", false, "do not query the Docker daemon for container names")
	fs.BoolVar(&cfg.dump, "dump", false, "print sockets to stdout and exit instead of starting the TUI")
	fs.StringVar(&cfg.state, "state", "listen", "socket state filter for --dump: listen, established or all")
	fs.BoolVar(&cfg.version, "version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: portview [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if fs.NArg() > 0 {
		return cfg, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if cfg.interval < 100*time.Millisecond {
		return cfg, fmt.Errorf("--interval must be at least 100ms, got %s", cfg.interval)
	}
	if !validStates[cfg.state] {
		return cfg, fmt.Errorf("--state must be one of listen, established, all; got %q", cfg.state)
	}
	return cfg, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseFlags(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "portview:", err)
		return 2
	}

	switch {
	case cfg.version:
		fmt.Fprintln(stdout, "portview", version)
		return 0
	case cfg.dump:
		fmt.Fprintln(stderr, "portview: --dump is not implemented yet")
		return 1
	default:
		fmt.Fprintln(stderr, "portview: the TUI is not implemented yet")
		return 1
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
