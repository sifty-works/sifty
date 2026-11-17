package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// run is the testable entry point for the sifty binary.
// It parses args, selects a command, and executes it.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage())
		return fmt.Errorf("no command given")
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "version":
		return cmdVersion(stdout)
	case "serve":
		return cmdServe(ctx, rest, stdout, stderr)
	case "config":
		return cmdConfig(rest, stdout, stderr)
	case "import-v1":
		return cmdImportV1(ctx, rest, stdout, stderr)
	default:
		fmt.Fprintln(stderr, usage())
		return fmt.Errorf("unknown command: %s", cmd)
	}
}

// cmdVersion prints the build version.
func cmdVersion(stdout io.Writer) error {
	fmt.Fprintln(stdout, version)
	return nil
}

// cmdConfig dispatches config sub-commands.
func cmdConfig(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: sifty config <subcommand>")
		return fmt.Errorf("no config subcommand given")
	}
	switch args[0] {
	case "defaults":
		return cmdConfigDefaults(stdout)
	default:
		fmt.Fprintf(stderr, "unknown config subcommand: %s\n", args[0])
		return fmt.Errorf("unknown config subcommand: %s", args[0])
	}
}

// serveFlags holds the parsed flags for the serve command.
// port == -1 means --port was not given (use the settings value).
// port == 0 means --port 0 was given explicitly (take a free port).
type serveFlags struct {
	dataDir   string
	port      int // -1 when not set
	offline   bool
	debug     bool
	replayDir string // non-empty when --replay is given
}

// parseServeFlags parses serve command flags and returns them.
func parseServeFlags(args []string) (serveFlags, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	f := serveFlags{port: -1} // -1 = not provided
	var portVal int
	fs.StringVar(&f.dataDir, "data-dir", "", "data folder (default: $SIFTY_DATA_DIR, then ./data)")
	fs.IntVar(&portVal, "port", 0, "listen port (0 = take a free port; omit = use settings or default 7373)")
	fs.BoolVar(&f.offline, "offline", false, "disable all network requests and model calls")
	fs.BoolVar(&f.debug, "debug", false, "enable debug mode")
	fs.StringVar(&f.replayDir, "replay", "", "answer requests from recorded responses in this folder")

	if err := fs.Parse(args); err != nil {
		return f, err
	}

	// Record portVal only when --port was explicitly given.
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == "port" {
			f.port = portVal
		}
	})

	return f, nil
}

func usage() string {
	return `sifty <command> [flags]

Commands:
  serve          Start the application server
  import-v1      Import data from the previous application
  config         Manage configuration
  version        Print the version

Run 'sifty serve --help' for serve options.`
}

// resolveDataDir returns the absolute data directory, chosen by flags,
// then SIFTY_DATA_DIR, then ./data.
func resolveDataDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("SIFTY_DATA_DIR"); v != "" {
		return v
	}
	return "./data"
}
