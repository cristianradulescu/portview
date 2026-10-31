// Command portview is a terminal UI for inspecting open TCP/UDP sockets and
// the processes that own them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cristianradulescu/portview/internal/inventory"
	"github.com/cristianradulescu/portview/internal/ui/portsview"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// config holds the parsed command-line options.
type config struct {
	interval time.Duration
	noDocker bool
	dump     bool
	state    inventory.StateFilter
	version  bool
}

func parseFlags(args []string, errOut io.Writer) (config, error) {
	var (
		cfg   config
		state string
	)
	fs := flag.NewFlagSet("portview", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.DurationVar(&cfg.interval, "interval", 2*time.Second, "auto-refresh interval (e.g. 2s, 500ms)")
	fs.BoolVar(&cfg.noDocker, "no-docker", false, "do not query the Docker daemon for container names")
	fs.BoolVar(&cfg.dump, "dump", false, "print sockets to stdout and exit instead of starting the TUI")
	fs.StringVar(&state, "state", "listen", "initial socket state filter: listen, established or all")
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
	st, ok := inventory.ParseStateFilter(state)
	if !ok {
		return cfg, fmt.Errorf("--state must be one of listen, established, all; got %q", state)
	}
	cfg.state = st
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

	if cfg.version {
		fmt.Fprintln(stdout, "portview", version)
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	collector := inventory.NewCollector()
	filter := inventory.Filter{State: cfg.state}

	if cfg.dump {
		err = dump(ctx, collector, filter, stdout, stderr)
	} else {
		err = portsview.Run(ctx, portsview.Options{Collector: collector, Users: collector.Users, Interval: cfg.interval, Filter: filter})
	}
	if err != nil {
		fmt.Fprintln(stderr, "portview:", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
