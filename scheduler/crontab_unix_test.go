//go:build !windows

package scheduler

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFormatParseRoundTrip checks that a job survives being formatted into a
// managed crontab line and parsed back — covering both daily and weekly (on a
// non-Sunday weekday) cadences and the Clean kind. It exercises the pure
// helpers only, so the real crontab is never touched.
func TestFormatParseRoundTrip(t *testing.T) {
	cases := []Job{
		{Kind: Upload, Hour: 3, Min: 0},                                         // daily upload, all folders
		{Kind: Clean, Hour: 7, Min: 30, Weekly: true, Weekday: time.Wednesday},  // weekly clean, Wed
		{Kind: Upload, Hour: 23, Min: 59, Weekly: true, Weekday: time.Sunday},   // weekly upload, Sun
		{Kind: Upload, Hour: 2, Min: 5, Folder: "/srv/alpha"},                   // one folder
		{Kind: Upload, Hour: 4, Min: 45, Folder: "/home/u/My Documents"},        // folder with a space
		{Kind: Upload, Hour: 5, Min: 0, Folder: "/home/u/$x `y` 100% \\ \"q\""}, // shell/cron specials
	}
	for _, want := range cases {
		line := formatJobLine("drive:", want, "/opt/my apps/rcss", "/var/log/my logs/backup.log")
		got, ok := parseManagedLine(line)
		if !ok {
			t.Fatalf("parseManagedLine failed for line: %s", line)
		}
		if got != want {
			t.Errorf("round-trip mismatch:\n line: %s\n want: %+v\n got:  %+v", line, want, got)
		}
		if a := lineAccount(line); a != "drive:" {
			t.Errorf("lineAccount = %q, want drive:", a)
		}
	}
}

// TestParseManagedLineRejectsGarbage checks malformed lines are skipped.
func TestParseManagedLineRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "not a cron line", "x y * * * rcss upload"} {
		if _, ok := parseManagedLine(bad); ok {
			t.Errorf("expected parse to fail for %q", bad)
		}
	}
}

// fakeCrontab is a stand-in `crontab` that stores the crontab in $CRONTAB_STORE,
// mimicking `crontab -l` (print or exit 1 when none) and `crontab -` (read
// stdin). It lets the apply/current path be exercised end-to-end without
// touching the user's real crontab.
const fakeCrontab = `#!/bin/sh
store="$CRONTAB_STORE"
case "$1" in
  -l) if [ -f "$store" ]; then cat "$store"; else exit 1; fi ;;
  -)  cat > "$store" ;;
  *)  exit 0 ;;
esac
`

// TestApplyCurrentFakeCrontab exercises apply/current through the real exec path
// against a fake crontab, validating the weekday round-trip and the safety
// invariant that other accounts' managed lines survive add/clear.
func TestApplyCurrentFakeCrontab(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "crontab")
	if err := os.WriteFile(bin, []byte(fakeCrontab), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CRONTAB_STORE", filepath.Join(dir, "store"))

	exe, log := "/usr/bin/rcss", "/tmp/backup.log"
	drive := []Job{
		{Kind: Upload, Hour: 3, Min: 0},
		{Kind: Clean, Hour: 7, Min: 30, Weekly: true, Weekday: time.Wednesday},
	}
	if err := apply("drive:", drive, exe, log); err != nil {
		t.Fatal(err)
	}
	if got, _ := current("drive:"); !jobsEqual(got, drive) {
		t.Fatalf("drive round-trip: got %+v want %+v", got, drive)
	}

	// Adding a second account must preserve the first account's jobs.
	if err := apply("work:", []Job{{Kind: Upload, Hour: 1, Min: 15}}, exe, log); err != nil {
		t.Fatal(err)
	}
	if got, _ := current("drive:"); !jobsEqual(got, drive) {
		t.Errorf("drive jobs not preserved after adding work:, got %+v", got)
	}
	if got, _ := current("work:"); len(got) != 1 || got[0].Hour != 1 || got[0].Min != 15 {
		t.Errorf("work job wrong: %+v", got)
	}

	// Clearing drive: must leave work: intact.
	if err := apply("drive:", nil, exe, log); err != nil {
		t.Fatal(err)
	}
	if got, _ := current("drive:"); len(got) != 0 {
		t.Errorf("drive not cleared: %+v", got)
	}
	if got, _ := current("work:"); len(got) != 1 {
		t.Errorf("work: removed unexpectedly: %+v", got)
	}
}

func jobsEqual(a, b []Job) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestApplyPerFolderJobs checks that several per-folder upload jobs for one
// account round-trip together, and that rewriting one account leaves another
// account's per-folder jobs untouched (the isolation invariant).
func TestApplyPerFolderJobs(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "crontab")
	if err := os.WriteFile(bin, []byte(fakeCrontab), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CRONTAB_STORE", filepath.Join(dir, "store"))

	exe, log := "/usr/bin/rcss", "/tmp/backup.log"
	drive := []Job{
		{Kind: Upload, Hour: 2, Min: 0, Folder: "/srv/alpha"},
		{Kind: Upload, Hour: 2, Min: 30, Folder: "/srv/my beta"},
		{Kind: Upload, Hour: 3, Min: 0}, // all folders
		{Kind: Clean, Hour: 5, Min: 0, Weekly: true, Weekday: time.Sunday},
	}
	work := []Job{{Kind: Upload, Hour: 1, Min: 15, Folder: "/data/work"}}

	if err := apply("drive:", drive, exe, log); err != nil {
		t.Fatal(err)
	}
	if err := apply("work:", work, exe, log); err != nil {
		t.Fatal(err)
	}
	if got, _ := current("drive:"); !jobsEqual(got, drive) {
		t.Fatalf("drive per-folder round-trip: got %+v want %+v", got, drive)
	}
	if got, _ := current("work:"); !jobsEqual(got, work) {
		t.Fatalf("work per-folder round-trip: got %+v want %+v", got, work)
	}

	// Dropping one folder from drive: must not disturb work:.
	if err := apply("drive:", drive[:1], exe, log); err != nil {
		t.Fatal(err)
	}
	if got, _ := current("drive:"); !jobsEqual(got, drive[:1]) {
		t.Errorf("drive not narrowed to one job: %+v", got)
	}
	if got, _ := current("work:"); !jobsEqual(got, work) {
		t.Errorf("work: disturbed by a drive: rewrite: %+v", got)
	}
}
