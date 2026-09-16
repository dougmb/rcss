package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dougmb/rcss/config"
	"github.com/dougmb/rcss/scheduler"
)

func keyType(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func sendSched(s scheduleModel, k tea.KeyMsg) scheduleModel {
	s, _ = s.Update(k)
	return s
}

// TestScheduleEditorBuildsJob drives the editor headless: enable Clean, set its
// weekday and time, and check the job it would register. It never calls save()/
// apply(), so the real crontab is untouched. The account name is unlikely to
// have existing jobs, so pre-population stays at defaults regardless of host.
func TestScheduleEditorBuildsJob(t *testing.T) {
	s := newScheduleModel(config.Config{RemoteName: "test-rcss-zzz:"})

	// Fields: 0 U.enabled 1 U.cadence 2 U.time | 3 C.enabled 4 C.cadence
	//         5 C.weekday 6 C.time | 7 Save  (Clean defaults to weekly).
	down := keyType(tea.KeyDown)
	right := keyType(tea.KeyRight)
	space := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}

	s = sendSched(s, down)  // 1
	s = sendSched(s, down)  // 2
	s = sendSched(s, down)  // 3 → Clean Enabled
	s = sendSched(s, space) // enable Clean
	s = sendSched(s, down)  // 4 cadence
	s = sendSched(s, down)  // 5 weekday
	s = sendSched(s, right) // Sunday → Monday
	s = sendSched(s, down)  // 6 time
	for _, d := range "0730" {
		s = sendSched(s, keyRune(d))
	}
	s = sendSched(s, down) // 7 Save — commits the typed time

	jobs := s.buildJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected only Clean enabled, got %d jobs: %+v", len(jobs), jobs)
	}
	want := scheduler.Job{Kind: scheduler.Clean, Hour: 7, Min: 30, Weekly: true, Weekday: time.Monday}
	if jobs[0] != want {
		t.Fatalf("built job = %+v, want %+v", jobs[0], want)
	}
}

// TestScheduleCadenceToggleShowsWeekday checks toggling a job to weekly adds the
// weekday field to the focus order, and back to daily removes it.
func TestScheduleCadenceToggleShowsWeekday(t *testing.T) {
	s := newScheduleModel(config.Config{RemoteName: "test-rcss-zzz:"})

	// Upload starts daily: Enabled, Cadence, Time (no weekday).
	if n := countWeekdayFields(s); n != 1 {
		// Clean defaults to weekly, so exactly one weekday field exists.
		t.Fatalf("expected 1 weekday field initially, got %d", n)
	}
	// Move to Upload's Cadence (index 1) and switch it to weekly.
	s = sendSched(s, keyType(tea.KeyDown))
	s = sendSched(s, keyType(tea.KeyRight))
	if n := countWeekdayFields(s); n != 2 {
		t.Fatalf("expected 2 weekday fields after making Upload weekly, got %d", n)
	}

	if !strings.Contains(s.View(), "Schedule —") {
		t.Error("View should render the Schedule header")
	}
}

func countWeekdayFields(s scheduleModel) int {
	n := 0
	for _, f := range s.fields() {
		if f.field == fWeekday {
			n++
		}
	}
	return n
}

// schedCfg is a config with two source folders, used by the per-folder tests.
// The account name is unlikely to have real jobs, so scheduler.Current returns
// nothing and pre-population stays at defaults regardless of host.
func schedCfg() config.Config {
	return config.Config{
		RemoteName:    "test-rcss-zzz:",
		SourceFolders: []string{"/srv/alpha", "/srv/beta"},
	}
}

// TestScheduleBlocksPerFolder checks the screen offers one block per source
// folder plus the all-folders upload and Clean, in that order, and that the
// per-folder defaults are staggered rather than all landing on the same minute.
func TestScheduleBlocksPerFolder(t *testing.T) {
	s := newScheduleModel(schedCfg())

	wantLabels := []string{"All folders", "alpha", "beta", "Clean"}
	if len(s.jobs) != len(wantLabels) {
		t.Fatalf("got %d blocks, want %d", len(s.jobs), len(wantLabels))
	}
	for i, want := range wantLabels {
		if s.jobs[i].label != want {
			t.Errorf("block[%d].label = %q, want %q", i, s.jobs[i].label, want)
		}
	}
	if s.jobs[0].folder != "" {
		t.Error("the all-folders block must carry no folder")
	}
	if s.jobs[1].folder != "/srv/alpha" || s.jobs[2].folder != "/srv/beta" {
		t.Errorf("folder blocks target the wrong paths: %q, %q", s.jobs[1].folder, s.jobs[2].folder)
	}
	if s.jobs[1].timeText() == s.jobs[2].timeText() {
		t.Errorf("per-folder defaults must be staggered, both at %s", s.jobs[1].timeText())
	}
}

// TestScheduleBuildsPerFolderJob enables only the second source folder's block
// and checks the job carries that folder — the whole point of per-folder
// scheduling.
func TestScheduleBuildsPerFolderJob(t *testing.T) {
	s := newScheduleModel(schedCfg())

	// Blocks: All(0) alpha(1) beta(2) Clean(3). Each contributes 3 focus stops
	// while daily, so beta's Enabled is stop 6.
	for i := 0; i < 6; i++ {
		s = sendSched(s, keyType(tea.KeyDown))
	}
	if cur := s.fields()[s.focus]; cur.job != 2 || cur.field != fEnabled {
		t.Fatalf("focus landed on %+v, want beta's Enabled", cur)
	}
	s = sendSched(s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})

	s = sendSched(s, keyType(tea.KeyDown)) // cadence
	s = sendSched(s, keyType(tea.KeyDown)) // time
	for _, d := range "0415" {
		s = sendSched(s, keyRune(d))
	}
	s = sendSched(s, keyType(tea.KeyDown)) // commit the typed time

	jobs := s.buildJobs()
	want := scheduler.Job{Kind: scheduler.Upload, Hour: 4, Min: 15, Folder: "/srv/beta"}
	if len(jobs) != 1 || jobs[0] != want {
		t.Fatalf("built %+v, want exactly [%+v]", jobs, want)
	}
}

// TestScheduleOrphanJob checks a job scheduled for a folder that has since been
// removed from the config: it gets its own read-only block, is not focusable,
// and is dropped from what the next save registers.
func TestScheduleOrphanJob(t *testing.T) {
	orphan := scheduler.Job{Kind: scheduler.Upload, Hour: 6, Min: 30, Folder: "/srv/gone"}
	live := scheduler.Job{Kind: scheduler.Upload, Hour: 2, Min: 0, Folder: "/srv/alpha"}

	s := scheduleModel{
		cfg:     schedCfg(),
		current: []scheduler.Job{live, orphan},
		jobs:    mergeScheduled(defaultJobForms(schedCfg()), []scheduler.Job{live, orphan}),
		width:   70, height: 20,
	}

	if got := s.orphanCount(); got != 1 {
		t.Fatalf("orphanCount = %d, want 1", got)
	}
	// The orphan sits before Clean, keeping the upload blocks together.
	if s.jobs[len(s.jobs)-1].kind != scheduler.Clean {
		t.Error("Clean must stay the last block")
	}
	// It is informational only — no focus stop may address it.
	for _, f := range s.fields() {
		if f.job >= 0 && s.jobs[f.job].orphan {
			t.Fatal("an orphan block must not be focusable")
		}
	}
	// Saving drops it but keeps the still-valid folder job.
	jobs := s.buildJobs()
	if len(jobs) != 1 || jobs[0] != live {
		t.Fatalf("buildJobs = %+v, want exactly [%+v]", jobs, live)
	}
	// And the user is told, rather than it vanishing silently.
	if v := s.View(); !strings.Contains(v, "no longer configured") {
		t.Errorf("the view must flag the orphaned job, got:\n%s", v)
	}
}

// TestScheduleWarnsWhenDaemonStopped checks the screen says so when the OS has
// no cron daemon running. Registering a job then succeeds and silently never
// runs, so the warning has to appear before the user schedules anything, and
// again on the save confirmation.
func TestScheduleWarnsWhenDaemonStopped(t *testing.T) {
	const hint = "No cron daemon is running, so saved jobs will not execute."
	s := scheduleModel{
		cfg:           schedCfg(),
		jobs:          defaultJobForms(schedCfg()),
		daemonWarning: hint,
		width:         80, height: 30,
	}
	if v := s.View(); !strings.Contains(v, "No cron daemon is running") {
		t.Errorf("editor must warn about the stopped daemon, got:\n%s", v)
	}
	s.done = true
	if v := s.View(); !strings.Contains(v, "No cron daemon is running") {
		t.Errorf("save confirmation must repeat the warning, got:\n%s", v)
	}

	// With a healthy daemon the screen stays quiet.
	s.done, s.daemonWarning = false, ""
	if v := s.View(); strings.Contains(v, "cron daemon") {
		t.Errorf("no warning expected when the daemon is fine, got:\n%s", v)
	}
}
