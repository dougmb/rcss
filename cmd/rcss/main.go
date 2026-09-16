// Command rcss is the RCSS backup manager. Run without arguments it opens the
// terminal UI; with a subcommand it runs headless (for cron):
//
//	rcss upload [-v] [-p] [--folder DIR]     upload the configured folders
//	rcss clean  [-v] [--dry-run] [--force]   clean old remote backups
//
// Both subcommands reuse the same backup package as the TUI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dougmb/rcss/backup"
	"github.com/dougmb/rcss/config"
	"github.com/dougmb/rcss/rclone"
	"github.com/dougmb/rcss/tui"
)

func main() {
	args := os.Args[1:]

	// No subcommand → launch the TUI.
	if len(args) == 0 {
		if err := runTUI(); err != nil {
			fmt.Fprintln(os.Stderr, "rcss:", err)
			os.Exit(1)
		}
		return
	}

	switch args[0] {
	case "upload":
		os.Exit(runUpload(args[1:]))
	case "clean":
		os.Exit(runClean(args[1:]))
	case "version", "--version":
		fmt.Println("rcss", tui.Version())
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "rcss: unknown command %q\n\n", args[0])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `rcss — Rclone Cloud Simple Scripts

Usage:
  rcss                 open the terminal UI
  rcss upload [-v] [-p] [--folder DIR] [--account NAME]
  rcss clean  [-v] [--dry-run] [--force] [--account NAME]
  rcss version
  rcss help

Flags:
  -v             verbose output
  -p             show rclone transfer progress (upload)
  --folder DIR   back up only this source folder; repeat for several
                 (default: every folder configured for the account)
  --dry-run      preview deletions without removing anything (clean)
  --force        bypass the safety lock (clean) — dangerous
  --account NAME run for this account (rclone remote); defaults to the active one
`)
}

// newContext returns a context cancelled on SIGINT/SIGTERM, so headless runs
// stop rclone cleanly when interrupted (e.g. by cron killing the job).
func newContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// setup loads the chosen account, verifies rclone is installed, and builds a
// logger that writes to the account's sync log and echoes every line to stdout
// (for cron logs). When account is empty the active account is used.
func setup(verbose bool, account string) (config.Config, *rclone.Client, *backup.Logger, error) {
	store, err := config.LoadStore()
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	cfg, ok := pickAccount(store, account)
	if !ok {
		if account != "" {
			return config.Config{}, nil, nil, fmt.Errorf("account %q not found", account)
		}
		return config.Config{}, nil, nil, errors.New("no account configured; run the TUI to add one")
	}

	rc := rclone.New()
	if err := rc.EnsureInstalled(); err != nil {
		return cfg, nil, nil, err
	}

	logPath, err := cfg.ResolveLogFile()
	if err != nil {
		return cfg, nil, nil, err
	}
	log, err := backup.NewLogger(logPath, func(line string) { fmt.Println(line) }, verbose)
	if err != nil {
		return cfg, nil, nil, err
	}
	return cfg, rc, log, nil
}

// pickAccount selects the named account, or the active one when name is empty.
func pickAccount(store *config.Store, name string) (config.Config, bool) {
	if name != "" {
		return store.Get(name)
	}
	return store.Active()
}

// stringList collects a flag that may be repeated, e.g. `--folder a --folder b`.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ", ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func runUpload(argv []string) int {
	fs := flag.NewFlagSet("upload", flag.ExitOnError)
	verbose := fs.Bool("v", false, "verbose output")
	progress := fs.Bool("p", false, "show rclone transfer progress")
	account := fs.String("account", "", "account (rclone remote) to run; defaults to active")
	var folders stringList
	fs.Var(&folders, "folder", "back up only this source folder (repeatable); default: all")
	_ = fs.Parse(argv)

	cfg, rc, log, err := setup(*verbose, *account)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rcss:", err)
		return 1
	}
	defer log.Close()

	ctx, cancel := newContext()
	defer cancel()

	if _, err := backup.Upload(ctx, cfg, rc, log, backup.UploadOptions{
		ShowProgress: *progress,
		Folders:      folders,
	}); err != nil {
		return 1
	}
	return 0
}

func runClean(argv []string) int {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	verbose := fs.Bool("v", false, "verbose output")
	dryRun := fs.Bool("dry-run", false, "preview deletions without removing anything")
	force := fs.Bool("force", false, "bypass the safety lock (dangerous)")
	account := fs.String("account", "", "account (rclone remote) to run; defaults to active")
	_ = fs.Parse(argv)

	cfg, rc, log, err := setup(*verbose, *account)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rcss:", err)
		return 1
	}
	defer log.Close()

	ctx, cancel := newContext()
	defer cancel()

	if err := backup.Clean(ctx, cfg, rc, log, backup.CleanOptions{DryRun: *dryRun, Force: *force}); err != nil {
		return 1
	}
	return 0
}

// runTUI loads the config and rclone client, then launches the terminal UI.
// Unlike the headless subcommands, a missing rclone binary is NOT fatal here:
// the UI still opens and warns about the dependency (so the user can review
// settings/logs and find install instructions) instead of refusing to start.
func runTUI() error {
	store, err := config.LoadStore()
	if err != nil {
		return err
	}
	rc := rclone.New()
	return tui.Run(store, rc)
}
