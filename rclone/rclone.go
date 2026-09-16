// Package rclone is a thin wrapper over the rclone binary (invoked via
// exec.Command). It exposes the handful of operations RCSS needs —
// ListRemotes, Lsf, Copy, Delete — and a startup check that rclone is on the
// PATH. rclone keeps its own credentials/config; this package only shells out.
package rclone

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultBin is the rclone executable name looked up on the PATH.
const DefaultBin = "rclone"

// Client runs rclone commands. The zero value uses the rclone binary on PATH;
// Bin may be set to an explicit path (useful in tests).
type Client struct {
	Bin string
}

// New returns a Client using the rclone binary found on PATH, falling back to
// the usual install locations that a minimal environment leaves off PATH —
// cron runs jobs with PATH=/usr/bin:/bin, which misses Homebrew's
// /opt/homebrew/bin and per-user installs such as ~/.local/bin.
func New() *Client { return &Client{Bin: findBin()} }

func findBin() string {
	if _, err := exec.LookPath(DefaultBin); err == nil {
		return DefaultBin
	}
	home, _ := os.UserHomeDir()
	var dirs []string
	if runtime.GOOS == "windows" {
		dirs = []string{
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet", "Links"),
			filepath.Join(home, "scoop", "shims"),
			filepath.Join(os.Getenv("ProgramData"), "chocolatey", "bin"),
		}
	} else {
		dirs = []string{
			"/opt/homebrew/bin", "/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin", "/snap/bin",
			filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin"),
		}
	}
	for _, dir := range dirs {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, DefaultBin)
		if runtime.GOOS == "windows" {
			p += ".exe"
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return DefaultBin
}

func (c *Client) bin() string {
	if c == nil || c.Bin == "" {
		return DefaultBin
	}
	return c.Bin
}

// EnsureInstalled reports an error if the rclone binary cannot be found on the
// PATH. Call it at startup, mirroring the scripts' `command -v rclone` guard.
func (c *Client) EnsureInstalled() error {
	if _, err := exec.LookPath(c.bin()); err != nil {
		return fmt.Errorf("rclone not found on PATH: %w", err)
	}
	return nil
}

// ListRemotes returns the configured rclone remotes (e.g. "drive:"), one per
// entry, via `rclone listremotes`.
func (c *Client) ListRemotes(ctx context.Context) ([]string, error) {
	out, err := c.output(ctx, "listremotes")
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// LsfMode selects which entries `rclone lsf` returns.
type LsfMode int

const (
	// LsfAll lists both files and directories.
	LsfAll LsfMode = iota
	// LsfDirsOnly lists directories only (--dirs-only).
	LsfDirsOnly
	// LsfFilesOnly lists files only (--files-only).
	LsfFilesOnly
)

// LsfOptions configures a Lsf call.
type LsfOptions struct {
	Mode      LsfMode
	Recursive bool   // --recursive
	MaxAge    string // --max-age, e.g. "2d" (empty to omit)
}

// Lsf lists the contents of a remote path via `rclone lsf`, returning the
// entries (trailing slash on directories, as rclone prints them).
func (c *Client) Lsf(ctx context.Context, path string, opts LsfOptions) ([]string, error) {
	args := []string{"lsf", path}
	switch opts.Mode {
	case LsfDirsOnly:
		args = append(args, "--dirs-only")
	case LsfFilesOnly:
		args = append(args, "--files-only")
	}
	if opts.Recursive {
		args = append(args, "--recursive")
	}
	if opts.MaxAge != "" {
		args = append(args, "--max-age", opts.MaxAge)
	}
	out, err := c.output(ctx, args...)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// CopyOptions configures a Copy call. Upload uses Update/UseMmap/Retries/
// Stats; restore uses IgnoreTimes/Verbose/DryRun.
type CopyOptions struct {
	Update       bool     // --update
	UseMmap      bool     // --use-mmap
	Retries      int      // --retries N (omitted if <= 0)
	Progress     bool     // -P
	StatsOneLine bool     // --stats-one-line
	Stats        string   // --stats DURATION, e.g. "10s" (empty to omit)
	LogLevel     string   // --log-level LEVEL (empty to omit)
	Excludes     []string // --exclude PATTERN (one flag per pattern)
	IgnoreTimes  bool     // --ignore-times
	Verbose      bool     // -v
	DryRun       bool     // --dry-run
	MaxDepth     int      // --max-depth N (omitted if <= 0)
}

// Copy runs `rclone copy src dst` with the given options. Each line of
// combined stdout/stderr (including \r-delimited progress updates) is passed
// to onLine, which may be nil.
func (c *Client) Copy(ctx context.Context, src, dst string, opts CopyOptions, onLine func(string)) error {
	args := []string{"copy", src, dst}
	if opts.LogLevel != "" {
		args = append(args, "--log-level", opts.LogLevel)
	}
	if opts.StatsOneLine {
		args = append(args, "--stats-one-line")
	}
	if opts.Stats != "" {
		args = append(args, "--stats", opts.Stats)
	}
	if opts.Update {
		args = append(args, "--update")
	}
	if opts.UseMmap {
		args = append(args, "--use-mmap")
	}
	if opts.Retries > 0 {
		args = append(args, "--retries", fmt.Sprintf("%d", opts.Retries))
	}
	if opts.IgnoreTimes {
		args = append(args, "--ignore-times")
	}
	if opts.Progress {
		args = append(args, "-P")
	}
	if opts.Verbose {
		args = append(args, "-v")
	}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	if opts.MaxDepth > 0 {
		args = append(args, "--max-depth", fmt.Sprintf("%d", opts.MaxDepth))
	}
	for _, pat := range opts.Excludes {
		args = append(args, "--exclude", pat)
	}
	return c.stream(ctx, onLine, args...)
}

// DeleteOptions configures a Delete call.
type DeleteOptions struct {
	MinAge   string // --min-age, e.g. "15d" (empty to omit)
	DryRun   bool   // --dry-run
	LogLevel string // --log-level LEVEL (empty to omit)
}

// Delete runs `rclone delete path` with the given options. Each line of
// combined output is passed to onLine, which may be nil.
func (c *Client) Delete(ctx context.Context, path string, opts DeleteOptions, onLine func(string)) error {
	args := []string{"delete", path}
	if opts.MinAge != "" {
		args = append(args, "--min-age", opts.MinAge)
	}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	if opts.LogLevel != "" {
		args = append(args, "--log-level", opts.LogLevel)
	}
	return c.stream(ctx, onLine, args...)
}

// output runs an rclone command and returns its stdout, capturing stderr for
// error context. Use for short, list-style commands.
func (c *Client) output(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("rclone %s: %w: %s", args[0], err, msg)
		}
		return "", fmt.Errorf("rclone %s: %w", args[0], err)
	}
	return string(out), nil
}

// stream runs an rclone command, forwarding each line of combined
// stdout/stderr to onLine as it arrives. Lines are split on both "\n" and
// "\r" so rclone's progress updates surface live.
func (c *Client) stream(ctx context.Context, onLine func(string), args ...string) error {
	cmd := exec.CommandContext(ctx, c.bin(), args...)

	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("creating pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return fmt.Errorf("starting rclone %s: %w", args[0], err)
	}
	// Close the parent's write end so the reader sees EOF when rclone exits.
	pw.Close()

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	scanner.Split(scanLinesOrCR)
	for scanner.Scan() {
		if onLine != nil {
			onLine(scanner.Text())
		}
	}
	pr.Close()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("rclone %s: %w", args[0], err)
	}
	return nil
}

// scanLinesOrCR is a bufio.SplitFunc that yields a token at each "\n" or "\r",
// so carriage-return progress updates are delivered as separate lines.
func scanLinesOrCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// splitLines splits command output into non-empty, trimmed lines.
func splitLines(out string) []string {
	var lines []string
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimRight(ln, "\r")
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, ln)
		}
	}
	return lines
}
