package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dougmb/rcss/config"
	"github.com/dougmb/rcss/rclone"
)

// TestRestoreTarget covers the default local restore destination. A configured
// RestoreDestination is always the base; with none, a backup restores to the
// source folder it came from, and only an unmatched remote path falls back to
// the current directory. The returned path is the parent directory the item is
// copied into, preserving the remote's relative structure.
func TestRestoreTarget(t *testing.T) {
	sep := string(os.PathSeparator)

	cfg := config.Config{
		SourceFolders:      []string{"/home/me/work/website", "/home/me/notes"},
		RestoreDestination: "/restore",
	}

	// Root-level folder: restore into the configured base.
	if got, err := RestoreTarget(cfg, "website"); err != nil || got != "/restore" {
		t.Errorf("root folder: got %q err=%v, want %q", got, err, "/restore")
	}
	// Nested file: preserve intermediate directories.
	if got, err := RestoreTarget(cfg, "website/index.html"); err != nil || got != "/restore"+sep+"website" {
		t.Errorf("nested file: got %q err=%v, want %q", got, err, "/restore"+sep+"website")
	}
	// Loose file at the destination root lands directly in the base.
	if got, err := RestoreTarget(cfg, "file.txt"); err != nil || got != "/restore" {
		t.Errorf("loose file: got %q err=%v, want %q", got, err, "/restore")
	}
	// Empty relPath restores the whole destination into the base.
	if got, err := RestoreTarget(cfg, ""); err != nil || got != "/restore" {
		t.Errorf("empty relPath: got %q err=%v, want %q", got, err, "/restore")
	}

	// With no RestoreDestination, a backup goes back to the folder it came from.
	src := config.Config{SourceFolders: []string{"/home/me/work/website", "/home/me/notes"}}
	cwd, _ := os.Getwd()

	// The remote folder "website" maps to the source folder of that base name.
	if got, err := RestoreTarget(src, "website"); err != nil || got != "/home/me/work/website" {
		t.Errorf("no dest, root folder: got %q err=%v, want %q", got, err, "/home/me/work/website")
	}
	// Nested items keep their structure inside that source folder.
	want := "/home/me/work/website" + sep + "css"
	if got, err := RestoreTarget(src, "website/css/main.css"); err != nil || got != want {
		t.Errorf("no dest, nested file: got %q err=%v, want %q", got, err, want)
	}
	// A file directly inside the backup folder lands in the source folder itself.
	if got, err := RestoreTarget(src, "notes/todo.md"); err != nil || got != "/home/me/notes" {
		t.Errorf("no dest, file in source: got %q err=%v, want %q", got, err, "/home/me/notes")
	}
	// A remote folder with no matching source falls back to the current directory.
	if got, err := RestoreTarget(src, "archive/old.zip"); err != nil || got != cwd+sep+"archive" {
		t.Errorf("no dest, unmatched folder: got %q err=%v, want %q", got, err, cwd+sep+"archive")
	}
	// Restoring the whole destination has no folder to map, so it uses the cwd.
	if got, err := RestoreTarget(src, ""); err != nil || got != cwd {
		t.Errorf("no dest, empty relPath: got %q err=%v, want %q", got, err, cwd)
	}
	// An explicit RestoreDestination still wins over the source-folder mapping.
	both := config.Config{SourceFolders: src.SourceFolders, RestoreDestination: "/restore"}
	if got, err := RestoreTarget(both, "website/index.html"); err != nil || got != "/restore"+sep+"website" {
		t.Errorf("explicit dest must win: got %q err=%v", got, err)
	}
}

// TestRestoreBuildsDestination verifies that Restore creates the correct local
// destination for files and directories. It uses the test binary as a fake
// rclone (see TestMain) that records the received src/dst arguments.
func TestRestoreBuildsDestination(t *testing.T) {
	runRestore := func(isDir bool, relPath, restoreDir string) (src, dst string) {
		t.Helper()
		logPath := filepath.Join(t.TempDir(), "calls.log")
		t.Setenv("FAKE_RCLONE_LOG", logPath)

		cfg := config.Config{RemoteName: "drive:", RestoreDestination: restoreDir}
		rc := &rclone.Client{Bin: os.Args[0]}
		log, _ := NewLogger("", func(string) {}, false)
		defer log.Close()

		if err := Restore(t.Context(), cfg, rc, log, relPath, RestoreOptions{IsDir: isDir}); err != nil {
			t.Fatalf("Restore failed: %v", err)
		}

		out, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("reading fake output: %v", err)
		}
		// Output format: "copy <src> <dst> --ignore-times ..."
		parts := strings.Fields(string(out))
		if len(parts) < 3 || parts[0] != "copy" {
			t.Fatalf("unexpected fake output: %q", string(out))
		}
		return parts[1], parts[2]
	}

	restoreDir := t.TempDir()
	src, dst := runRestore(false, "website/index.html", restoreDir)
	if src != "drive:/website/index.html" {
		t.Errorf("file src = %q, want drive:/website/index.html", src)
	}
	want := restoreDir + string(os.PathSeparator) + "website" + string(os.PathSeparator)
	if dst != want {
		t.Errorf("file dst = %q, want %q", dst, want)
	}

	src, dst = runRestore(true, "website", restoreDir)
	if src != "drive:/website" {
		t.Errorf("dir src = %q, want drive:/website", src)
	}
	want = restoreDir + string(os.PathSeparator) + "website"
	if dst != want {
		t.Errorf("dir dst = %q, want %q", dst, want)
	}

	src, dst = runRestore(true, "", restoreDir)
	if src != "drive:" {
		t.Errorf("root src = %q, want drive:", src)
	}
	if dst != restoreDir {
		t.Errorf("root dst = %q, want %q", dst, restoreDir)
	}
}
