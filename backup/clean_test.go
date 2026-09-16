package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dougmb/rcss/config"
	"github.com/dougmb/rcss/rclone"
)

// TestMain lets the test binary double as a fake rclone: when FAKE_RCLONE_LOG
// is set it records its arguments and answers like rclone would, instead of
// running the tests. This keeps Clean tests portable (no shell scripts).
func TestMain(m *testing.M) {
	if logPath := os.Getenv("FAKE_RCLONE_LOG"); logPath != "" {
		os.Exit(fakeRclone(logPath, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeRclone appends the call to logPath. `lsf PATH` prints a file when PATH is
// listed in FAKE_RCLONE_RECENT, prints nothing when listed in FAKE_RCLONE_OLD,
// and otherwise exits 3 (directory not found).
func fakeRclone(logPath string, args []string) int {
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 1
	}
	_, _ = f.WriteString(strings.Join(args, " ") + "\n")
	f.Close()
	if len(args) > 1 && args[0] == "lsf" {
		path := strings.TrimSuffix(args[1], "/")
		for _, p := range strings.Split(os.Getenv("FAKE_RCLONE_RECENT"), ",") {
			if p == path {
				os.Stdout.WriteString("recent.txt\n")
				return 0
			}
		}
		for _, p := range strings.Split(os.Getenv("FAKE_RCLONE_OLD"), ",") {
			if p == path {
				return 0
			}
		}
		os.Stderr.WriteString("directory not found\n")
		return 3
	}
	return 0
}

// runFakeClean runs Clean against the fake rclone and returns its calls.
func runFakeClean(t *testing.T, cfg config.Config, opts CleanOptions, recent, old string) ([]string, error) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKE_RCLONE_LOG", logPath)
	t.Setenv("FAKE_RCLONE_RECENT", recent)
	t.Setenv("FAKE_RCLONE_OLD", old)
	log, _ := NewLogger("", nil, false)
	err := Clean(context.Background(), cfg, &rclone.Client{Bin: os.Args[0]}, log, opts)
	data, _ := os.ReadFile(logPath)
	var calls []string
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if ln != "" {
			calls = append(calls, ln)
		}
	}
	return calls, err
}

func deletes(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "delete ") {
			out = append(out, c)
		}
	}
	return out
}

func cleanConfig(dest string) config.Config {
	cfg := config.NewAccount("gdrive:")
	cfg.RemoteDestination = dest
	cfg.SourceFolders = []string{filepath.Join("home", "u", "alpha"), filepath.Join("home", "u", "beta")}
	return cfg
}

// TestCleanOnlyTouchesBackupFolders guards against deleting unrelated files:
// with the destination at the account root, Clean must delete inside each
// backup folder and never at the remote root.
func TestCleanOnlyTouchesBackupFolders(t *testing.T) {
	calls, err := runFakeClean(t, cleanConfig(""), CleanOptions{}, "gdrive:/alpha,gdrive:/beta", "")
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	want := []string{"delete gdrive:/alpha --min-age 15d", "delete gdrive:/beta --min-age 15d"}
	got := deletes(calls)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("deletes = %q, want %q", got, want)
	}
}

// TestCleanSafetyLockPerFolder checks that a folder without a recent backup is
// skipped (and reported) while the healthy folder is still cleaned, and that a
// folder never uploaded is simply skipped.
func TestCleanSafetyLockPerFolder(t *testing.T) {
	cfg := cleanConfig("Backups")
	cfg.SourceFolders = append(cfg.SourceFolders, filepath.Join("home", "u", "gamma"))
	calls, err := runFakeClean(t, cfg, CleanOptions{}, "gdrive:/Backups/alpha", "gdrive:/Backups/beta")
	if !errors.Is(err, ErrNoRecentBackup) {
		t.Fatalf("err = %v, want ErrNoRecentBackup", err)
	}
	got := deletes(calls)
	if len(got) != 1 || got[0] != "delete gdrive:/Backups/alpha --min-age 15d" {
		t.Errorf("deletes = %q, want only alpha", got)
	}
}

// TestCleanRefusesZeroRetention checks that a zero retention, which would make
// rclone delete every backup, is rejected before rclone runs.
func TestCleanRefusesZeroRetention(t *testing.T) {
	cfg := cleanConfig("Backups")
	cfg.RemoteRetentionDays = 0
	calls, err := runFakeClean(t, cfg, CleanOptions{Force: true}, "", "")
	if err == nil || len(calls) != 0 {
		t.Errorf("err = %v, calls = %q; want refusal with no rclone calls", err, calls)
	}
}
