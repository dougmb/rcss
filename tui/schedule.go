package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dougmb/rcss/config"
	"github.com/dougmb/rcss/scheduler"
)

// Schedule screen: a single interactive window that registers RCSS jobs with the
// host OS scheduler (crontab on Unix, Task Scheduler on Windows; no root/admin),
// calling the rcss binary headless — `rcss upload` and/or `rcss clean`.
//
// There is one block per schedulable target: an "All folders" upload, one upload
// per configured source folder (so each folder can run on its own cadence), and
// the account's Clean job. Each block is Daily or Weekly (on a chosen weekday)
// at a time. The editor opens pre-filled with whatever is currently scheduled,
// saves inline, and shows success/failure feedback without leaving the screen.
// Disabling every block and saving removes the account's managed jobs.

// fieldKind identifies an editable field within a job block (or the Save action).
type fieldKind int

const (
	fEnabled fieldKind = iota
	fCadence
	fWeekday
	fTime
	fSave
)

// focusTarget is one stop in the flat navigation order. job is the index into
// scheduleModel.jobs, or -1 for the Save action.
type focusTarget struct {
	job   int
	field fieldKind
}

// jobForm is the editable state of one schedulable target.
type jobForm struct {
	kind   scheduler.Kind
	folder string // upload target; "" means every configured folder
	label  string // block heading, e.g. "All folders" or "alpha"
	path   string // full source path shown under the heading (empty for others)
	// orphan marks a job found in the OS scheduler whose folder is no longer a
	// configured source. Orphans are shown so the user can see them, are not
	// focusable, and are dropped when the schedule is saved.
	orphan bool

	enabled bool
	weekly  bool
	weekday time.Weekday
	hour    int
	min     int
}

// job renders the form as the scheduler job it would register.
func (j jobForm) job() scheduler.Job {
	return scheduler.Job{
		Kind: j.kind, Hour: j.hour, Min: j.min,
		Weekly: j.weekly, Weekday: j.weekday, Folder: j.folder,
	}
}

type scheduleModel struct {
	cfg config.Config

	jobs    []jobForm // "All folders", one per source folder, orphans, then Clean
	focus   int       // index into fields()
	editBuf string    // in-progress digit entry for the focused Time field

	current []scheduler.Job // what is actually scheduled (for the summary)

	// daemonWarning is set when the OS scheduler's daemon is not running, so
	// saving a job would succeed and then silently never run.
	daemonWarning string

	// done flips to true once saved; saveErr is set by apply(). removed records
	// how many orphaned jobs the save dropped, for the confirmation screen.
	done    bool
	saveErr error
	removed int

	width, height int
}

func newScheduleModel(cfg config.Config) scheduleModel {
	current, _ := scheduler.Current(cfg.RemoteName)
	warning := ""
	if state, hint := scheduler.Daemon(); state == scheduler.DaemonStopped {
		warning = hint
	}
	return scheduleModel{
		cfg:           cfg,
		current:       current,
		jobs:          mergeScheduled(defaultJobForms(cfg), current),
		daemonWarning: warning,
	}
}

// mergeScheduled pre-fills the editor blocks from what is actually scheduled, so
// the screen opens reflecting reality. A scheduled job whose folder is no longer
// a configured source has no block: it becomes an orphan block, inserted before
// Clean, so the user can see what is still running behind their back instead of
// it vanishing silently.
func mergeScheduled(forms []jobForm, current []scheduler.Job) []jobForm {
	for _, j := range current {
		if i := indexOfTarget(forms, j); i >= 0 {
			forms[i] = fillForm(forms[i], j)
			continue
		}
		forms = insertBeforeClean(forms, fillForm(jobForm{
			kind:   j.Kind,
			folder: j.Folder,
			label:  j.Label(),
			path:   j.Folder,
			orphan: true,
		}, j))
	}
	return forms
}

// defaultJobForms builds the editable blocks for an account: the all-folders
// upload, one upload per configured source folder, then Clean. Per-folder
// defaults are staggered half an hour apart so enabling several at once doesn't
// queue them all against the remote at the same minute.
func defaultJobForms(cfg config.Config) []jobForm {
	forms := []jobForm{{
		kind: scheduler.Upload, label: "All folders",
		path: fmt.Sprintf("every folder configured for %s", cfg.RemoteName),
		hour: 3, min: 0,
	}}
	for i, f := range cfg.SourceFolders {
		start := 2*60 + i*30
		forms = append(forms, jobForm{
			kind: scheduler.Upload, folder: f, label: filepath.Base(f), path: f,
			hour: (start / 60) % 24, min: start % 60,
		})
	}
	return append(forms, jobForm{
		kind: scheduler.Clean, label: "Clean",
		path:    "removes cloud backups past the retention window (this account)",
		weekly:  true,
		weekday: time.Sunday, hour: 5, min: 0,
	})
}

// fillForm copies a scheduled job's cadence and time into its editor block and
// marks it enabled. A backend that could not recover the time keeps the block's
// default rather than showing a nonsense clock.
func fillForm(f jobForm, j scheduler.Job) jobForm {
	f.enabled = true
	f.weekly, f.weekday = j.Weekly, j.Weekday
	if j.Hour >= 0 && j.Min >= 0 {
		f.hour, f.min = j.Hour, j.Min
	}
	return f
}

// indexOfTarget returns the block addressing the same scheduler entry as j, or
// -1 when none does (an orphaned job).
func indexOfTarget(forms []jobForm, j scheduler.Job) int {
	for i, f := range forms {
		if !f.orphan && f.job().SameTarget(j) {
			return i
		}
	}
	return -1
}

// insertBeforeClean places a block just above the trailing Clean block, keeping
// the upload blocks grouped together.
func insertBeforeClean(forms []jobForm, f jobForm) []jobForm {
	for i, existing := range forms {
		if existing.kind == scheduler.Clean {
			return append(forms[:i:i], append([]jobForm{f}, forms[i:]...)...)
		}
	}
	return append(forms, f)
}

func (s scheduleModel) Init() tea.Cmd { return nil }

func (s *scheduleModel) setSize(w, h int) { s.width, s.height = w, h }

// fields returns the ordered focus targets for the current state. Orphan blocks
// are informational and never focusable; the Weekday field only appears for
// weekly jobs, so navigation and rendering stay in sync.
func (s scheduleModel) fields() []focusTarget {
	var ft []focusTarget
	for i := range s.jobs {
		if s.jobs[i].orphan {
			continue
		}
		ft = append(ft, focusTarget{i, fEnabled}, focusTarget{i, fCadence})
		if s.jobs[i].weekly {
			ft = append(ft, focusTarget{i, fWeekday})
		}
		ft = append(ft, focusTarget{i, fTime})
	}
	return append(ft, focusTarget{-1, fSave})
}

func (s *scheduleModel) clampFocus() {
	if n := len(s.fields()); s.focus >= n {
		s.focus = n - 1
	}
	if s.focus < 0 {
		s.focus = 0
	}
}

func (s scheduleModel) Update(msg tea.Msg) (scheduleModel, tea.Cmd) {
	if s.done {
		switch msg := msg.(type) {
		case doneTimeoutMsg:
			return s, func() tea.Msg { return goBackMsg{} }
		case tea.KeyMsg:
			switch msg.String() {
			case "q":
				return s, tea.Quit
			case "enter", "esc", "backspace":
				return s, func() tea.Msg { return goBackMsg{} }
			}
		}
		return s, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return s, nil
	}
	cur := s.fields()[s.focus]

	switch key.String() {
	case "q":
		return s, tea.Quit
	case "esc":
		return s, func() tea.Msg { return goBackMsg{} }
	case "backspace":
		// Delete a typed digit while editing a time; otherwise go back.
		if cur.field == fTime && s.editBuf != "" {
			s.editBuf = s.editBuf[:len(s.editBuf)-1]
			return s, nil
		}
		return s, func() tea.Msg { return goBackMsg{} }
	case "up", "k", "shift+tab":
		s.commitFocusedTime()
		s.focus--
		s.clampFocus()
		return s, nil
	case "down", "j", "tab":
		s.commitFocusedTime()
		s.focus++
		s.clampFocus()
		return s, nil
	case "left", "h":
		s.changeField(cur, -1)
		return s, nil
	case "right", "l":
		s.changeField(cur, +1)
		return s, nil
	case " ", "space":
		if cur.job >= 0 {
			s.jobs[cur.job].enabled = !s.jobs[cur.job].enabled
		}
		return s, nil
	case "enter":
		return s, s.save()
	}

	// Digit entry builds a 4-digit HHMM time on the focused Time field.
	if cur.field == fTime {
		if d := key.String(); len(d) == 1 && d[0] >= '0' && d[0] <= '9' {
			s.editBuf += d
			if len(s.editBuf) > 4 {
				s.editBuf = s.editBuf[len(s.editBuf)-4:]
			}
		}
	}
	return s, nil
}

// changeField applies a left/right (dir -1/+1) change to the focused field.
func (s *scheduleModel) changeField(t focusTarget, dir int) {
	if t.job < 0 {
		return // Save action: nothing to change
	}
	j := &s.jobs[t.job]
	switch t.field {
	case fEnabled:
		j.enabled = !j.enabled
	case fCadence:
		j.weekly = !j.weekly
		s.clampFocus() // the Weekday field appears/disappears
	case fWeekday:
		j.weekday = time.Weekday(((int(j.weekday)+dir)%7 + 7) % 7)
	case fTime:
		s.commitFocusedTime()
		j.addMinutes(dir * 5)
	}
}

// addMinutes nudges the job's time by d minutes, wrapping within a day.
func (j *jobForm) addMinutes(d int) {
	total := ((j.hour*60+j.min+d)%(24*60) + 24*60) % (24 * 60)
	j.hour, j.min = total/60, total%60
}

// commitFocusedTime applies any in-progress digit entry to the focused Time
// field, then clears the buffer. A no-op when not editing a time.
func (s *scheduleModel) commitFocusedTime() {
	if s.editBuf == "" {
		return
	}
	cur := s.fields()[s.focus]
	if cur.field == fTime && cur.job >= 0 {
		if h, m, ok := parseTimeBuf(s.editBuf); ok {
			s.jobs[cur.job].hour, s.jobs[cur.job].min = h, m
		}
	}
	s.editBuf = ""
}

// parseTimeBuf interprets 1–4 typed digits as a clock entry (right-aligned to
// HHMM), clamping to a valid time.
func parseTimeBuf(buf string) (hour, min int, ok bool) {
	if buf == "" {
		return 0, 0, false
	}
	for len(buf) < 4 {
		buf = "0" + buf
	}
	buf = buf[len(buf)-4:]
	h, _ := strconv.Atoi(buf[:2])
	m, _ := strconv.Atoi(buf[2:])
	if h > 23 {
		h = 23
	}
	if m > 59 {
		m = 59
	}
	return h, m, true
}

// buildJobs collects the enabled blocks as scheduler.Jobs for Apply. Orphans are
// left out, which is what removes them from the OS scheduler on save.
func (s scheduleModel) buildJobs() []scheduler.Job {
	var jobs []scheduler.Job
	for _, jf := range s.jobs {
		if !jf.enabled || jf.orphan {
			continue
		}
		jobs = append(jobs, jf.job())
	}
	return jobs
}

// anyEnabled reports whether saving would leave any job registered.
func (s scheduleModel) anyEnabled() bool { return len(s.buildJobs()) > 0 }

// orphanCount is how many scheduled jobs point at folders that are no longer
// configured, so the view and the save confirmation can call them out.
func (s scheduleModel) orphanCount() int {
	n := 0
	for _, jf := range s.jobs {
		if jf.orphan {
			n++
		}
	}
	return n
}

// apply registers the enabled jobs with the OS scheduler.
func (s scheduleModel) apply() error {
	jobs := s.buildJobs()
	// Removing every job needs no binary, so a `go run` session can still clear
	// its schedule; installing jobs requires a stable, installed rcss.
	var exe string
	if len(jobs) > 0 {
		var err error
		if exe, err = scheduler.Executable(); err != nil {
			return fmt.Errorf("locating rcss binary: %w", err)
		}
	}
	logPath, err := s.cfg.ResolveLogFile()
	if err != nil {
		return err
	}
	return scheduler.Apply(s.cfg.RemoteName, jobs, exe, logPath)
}

// save commits any typed time, applies the schedule, and flips to the done
// state so the confirmation is shown before auto-returning to the menu.
func (s *scheduleModel) save() tea.Cmd {
	s.commitFocusedTime()
	s.removed = s.orphanCount()
	s.saveErr = s.apply()
	if s.saveErr == nil {
		s.current, _ = scheduler.Current(s.cfg.RemoteName)
	}
	s.done = true
	return tea.Tick(saveConfirmationTimeout, func(time.Time) tea.Msg { return doneTimeoutMsg{} })
}

func (s scheduleModel) View() string {
	if s.done {
		return s.doneView()
	}

	cw := s.width - 1
	if cw < 10 {
		cw = 10
	}
	height := s.height - 2 // title + blank line
	if height < 1 {
		height = 1
	}

	lines, focusStart, focusEnd := s.contentLines()

	// Scroll just enough to keep the focused block visible.
	offset := 0
	if focusEnd > height {
		offset = focusEnd - height
	}
	if focusStart < offset {
		offset = focusStart
	}
	if maxOff := len(lines) - height; offset > maxOff {
		offset = maxOff
	}
	if offset < 0 {
		offset = 0
	}

	bar := scrollColumn(height, len(lines), offset)
	rows := make([]string, height)
	for i := 0; i < height; i++ {
		ln := ""
		if offset+i < len(lines) {
			ln = lines[offset+i]
		}
		rows[i] = padLineTo(ln, cw) + bar[i]
	}

	title := titleStyle.Render(fmt.Sprintf("Schedule — %s (%s)", s.cfg.RemoteName, scheduler.Backend()))
	return title + "\n\n" + strings.Join(rows, "\n")
}

// contentLines renders the summary and every job block into a flat line slice,
// reporting the line range [start,end) of the focused block so View can scroll.
func (s scheduleModel) contentLines() (lines []string, focusStart, focusEnd int) {
	lines = append(lines, strings.Split(s.currentScheduleText(), "\n")...)
	// Registering a job with a stopped daemon looks like success and then never
	// runs, so this has to be said before the user schedules anything.
	if s.daemonWarning != "" {
		lines = append(lines, wrapTo(warnStyle.Render("⚠ ")+s.daemonWarning, s.contentWidth())...)
	}
	// The orphan blocks sit further down and can fall below the fold, so the
	// warning goes at the top where it is visible the moment the screen opens.
	if n := s.orphanCount(); n > 0 {
		msg := fmt.Sprintf("⚠ %d job(s) point at folders no longer configured; saving removes them.", n)
		lines = append(lines, warnStyle.Render(clip(msg, s.contentWidth())))
	}
	lines = append(lines, "")

	cur := s.fields()[s.focus]
	for i := range s.jobs {
		block := strings.Split(s.renderJob(i), "\n")
		if cur.job == i {
			focusStart = len(lines)
		}
		lines = append(lines, block...)
		if cur.job == i {
			focusEnd = len(lines)
		}
	}

	lines = append(lines, "")
	onSave := cur.field == fSave
	if onSave {
		focusStart = len(lines)
	}
	save := subtitleStyle.Render("[ Save ]")
	if onSave {
		save = titleStyle.Render("‹ Save ›")
	}
	lines = append(lines, save)
	if onSave {
		focusEnd = len(lines)
	}
	return lines, focusStart, focusEnd
}

// doneView renders the save confirmation (success or error) before the screen
// auto-returns to the menu.
func (s scheduleModel) doneView() string {
	if s.saveErr != nil {
		return errorStyle.Render("✗ Could not update schedule") + "\n\n" +
			subtitleStyle.Render(s.saveErr.Error())
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("✓ Schedule saved"))
	b.WriteString("\n\n")
	if len(s.current) == 0 {
		b.WriteString(infoLine("Status", "RCSS jobs removed"))
	} else {
		b.WriteString(subtitleStyle.Render("Currently scheduled:"))
		for _, j := range s.current {
			b.WriteString("\n" + jobSummaryLine(j))
		}
	}
	if s.daemonWarning != "" {
		b.WriteString("\n\n")
		b.WriteString(warnStyle.Render("⚠ " + s.daemonWarning))
	}
	if s.removed > 0 {
		b.WriteString("\n\n")
		b.WriteString(warnStyle.Render(fmt.Sprintf("Removed %d job(s) for folders that are no longer configured.", s.removed)))
	}
	return b.String()
}

// renderJob renders one bordered job block with the focused field highlighted.
// An orphan block is read-only: it reports a job scheduled for a folder that is
// no longer configured, and says it will go away on save.
func (s scheduleModel) renderJob(i int) string {
	jf := s.jobs[i]
	cur := s.fields()[s.focus]
	active := !jf.orphan && cur.job == i
	focused := func(f fieldKind) bool { return active && cur.field == f }

	width := s.contentWidth() - 4 // two border columns + two padding columns
	if width < 10 {
		width = 10
	}
	textW := width - 2 // the pane pads one column on each side

	if jf.orphan {
		body := warnStyle.Render("⚠ "+jf.label+" — folder no longer configured") + "\n" +
			subtitleStyle.Render(clipPath(jf.path, textW)) + "\n" +
			subtitleStyle.Render(fmt.Sprintf("Scheduled %s at %s — removed when you save.", jf.Cadence(), jf.timeText()))
		return paneStyle(false).Width(width).Render(body)
	}

	check := "[ ]"
	if jf.enabled {
		check = "[x]"
	}
	cad := "Daily"
	if jf.weekly {
		cad = "Weekly"
	}

	var lines []string
	lines = append(lines, fieldValue(check+" Enabled", focused(fEnabled)))

	cadence := "Cadence: " + fieldValue(cad, focused(fCadence))
	if jf.weekly {
		cadence += "  Day: " + fieldValue(wdShort(jf.weekday), focused(fWeekday))
	}
	lines = append(lines, cadence)

	timeStr := jf.timeText()
	if focused(fTime) && s.editBuf != "" {
		timeStr = s.editBuf + "_"
	}
	lines = append(lines, "Time:    "+fieldValue(timeStr, focused(fTime)))

	title := jf.label
	if !jf.enabled {
		title += " (off)"
	}
	head := titleStyle.Render(title)
	if jf.path != "" {
		// A per-folder block shows a real path (clip from the left, keeping the
		// tail); the others show a sentence, which reads better clipped normally.
		label := clip(jf.path, textW)
		if jf.folder != "" {
			label = clipPath(jf.path, textW)
		}
		head += "\n" + subtitleStyle.Render(label)
	}
	return paneStyle(active).Width(width).Render(head + "\n" + strings.Join(lines, "\n"))
}

// timeText renders the block's clock as HH:MM.
func (j jobForm) timeText() string { return fmt.Sprintf("%02d:%02d", j.hour, j.min) }

// Cadence renders the block's recurrence in words, matching scheduler.Job.
func (j jobForm) Cadence() string { return j.job().Cadence() }

// fieldValue renders a focusable value, bracketing and highlighting it when
// focused so the selection is obvious; padding keeps the width steady otherwise.
func fieldValue(v string, focused bool) string {
	if focused {
		return titleStyle.Render("‹ " + v + " ›")
	}
	return "  " + v + "  "
}

// wdShort is the three-letter label for a weekday (e.g. "Wed").
func wdShort(d time.Weekday) string { return d.String()[:3] }

func (s scheduleModel) footerHint() string {
	if s.done {
		return "enter/esc back • q quit"
	}
	return "↑/↓ field • ←/→ change • space toggle • enter save • esc back"
}

// currentScheduleText summarizes the active account's managed jobs.
func (s scheduleModel) currentScheduleText() string {
	if len(s.current) == 0 {
		return fmt.Sprintf("Currently scheduled: none for %s (%s).", s.cfg.RemoteName, scheduler.Backend())
	}
	var b strings.Builder
	b.WriteString("Currently scheduled:")
	for _, j := range s.current {
		b.WriteString("\n" + jobSummaryLine(j))
	}
	return b.String()
}

// jobSummaryLine renders one scheduled job as a bullet. Uploads name the folder
// they cover; Clean is per-account, so naming a folder there would be a lie.
func jobSummaryLine(j scheduler.Job) string {
	if j.Kind == scheduler.Clean {
		return fmt.Sprintf("  • %s — %s at %s", j.Kind.Title(), j.Cadence(), j.Time())
	}
	return fmt.Sprintf("  • %s (%s) — %s at %s", j.Kind.Title(), j.Label(), j.Cadence(), j.Time())
}

// wrapTo breaks a message into lines of at most w display columns, so a long
// warning stays inside the pane instead of running under the scrollbar.
func wrapTo(msg string, w int) []string {
	var (
		out  []string
		line string
	)
	for _, word := range strings.Fields(msg) {
		switch {
		case line == "":
			line = word
		case lipgloss.Width(line)+1+lipgloss.Width(word) <= w:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// contentWidth is the usable text width inside the screen, leaving the last
// column to the scrollbar.
func (s scheduleModel) contentWidth() int {
	if w := s.width - 1; w > 10 {
		return w
	}
	return 10
}
