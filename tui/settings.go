package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dougmb/rcss/config"
)

// Settings screen: a single scrollable page editing the active account's
// config.toml. Toggles that have dependent options (e.g. "Delete local after
// upload", "Skip file formats") reveal those sub-settings indented beneath them
// when enabled, marked with a ▾/▸ chevron so it's clear they expand. The
// focused item's help shows in the root status bar. Saving writes the TOML and
// shows a "Saved ✓" confirmation; esc cancels without changes.

// settingsSavedMsg carries the edited config back to the root for persistence.
type settingsSavedMsg struct{ cfg config.Config }

// setFieldKind is the editor type of a settings row.
type setFieldKind int

const (
	fText setFieldKind = iota // free-text input
	fNum                      // non-negative integer input
	fBool                     // on/off toggle
)

const (
	setLeftColW = 28 // gutter + label column, before the value
	setChildPad = 2  // extra indent for a sub-setting row

	// saveConfirmationTimeout is how long a save-confirmation screen stays
	// visible before automatically returning to the menu. It must be long enough
	// to actually read: at zero the tick fires immediately and the "Saved ✓"
	// screen is gone before it is drawn. Any key returns sooner.
	saveConfirmationTimeout = 1500 * time.Millisecond
)

// defaultSkipFormats pre-fills the "Skip file formats" box with common junk
// when the toggle is first enabled.
const defaultSkipFormats = "tmp log cache"

// setField is one editable row. A field with a non-empty parent is a
// sub-setting shown only while its parent toggle is on.
type setField struct {
	key, label, help, section, placeholder string
	kind                                   setFieldKind
	parent                                 string // key of the parent toggle, or ""
	input                                  textinput.Model
	on                                     bool
}

type settingsModel struct {
	// base is the account being edited. toConfig starts from it and overwrites
	// only the fields this form owns, so settings managed on other screens (the
	// remote name, the source folders) — and any field added to Config later —
	// survive a save instead of being silently reset.
	base       config.Config
	remoteName string // chosen on the Account screen, not editable here
	fields     []setField
	focus      int // index into visibleIndices(); == len means the Save action

	status string // inline validation feedback (errors only)
	failed bool

	width, height int

	// done flips to true once saved, so the screen shows a confirmation instead
	// of silently returning to the menu. saveErr is set by the root after it
	// persists the config.
	done    bool
	saveErr error
}

// settingsSectionStyle renders the section headers between field groups.
var settingsSectionStyle = valueStyle

func newSettingsModel(cfg config.Config) settingsModel {
	mkInput := func(val, placeholder string) textinput.Model {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		ti.TextStyle = valueStyle
		ti.PlaceholderStyle = placeholderStyle
		ti.SetValue(val)
		return ti
	}

	fields := []setField{
		{key: "dest", label: "Destination folder", section: "Paths", kind: fText,
			help:        "Remote sub-folder for backups. Example: Backups/Projects (blank = account root).",
			placeholder: "(account root)", input: mkInput(cfg.RemoteDestination, "(account root)")},
		{key: "restore", label: "Restore destination", section: "Paths", kind: fText,
			help:        "Where restored files are written. Example: /data/restored (blank = backup source).",
			placeholder: "(backup source)", input: mkInput(cfg.RestoreDestination, "(backup source)")},

		{key: "rret", label: "Remote retention (days)", section: "Cloud cleanup", kind: fNum,
			help:  "Delete cloud backups older than this when cleaning.",
			input: mkInput(strconv.Itoa(cfg.RemoteRetentionDays), "")},
		{key: "safety", label: "Cleanup safety (days)", section: "Cloud cleanup", kind: fNum,
			help:  "Refuse to clean unless a backup newer than this exists on the remote.",
			input: mkInput(strconv.Itoa(cfg.RemoteCleanupSafetyDays), "")},

		{key: "delaft", label: "Delete local after upload", section: "Local cleanup", kind: fBool,
			help: "When on, prune local backups after a successful upload. Off keeps everything.",
			on:   cfg.DeleteAfterUpload},
		{key: "ret", label: "Keep local files for (days)", section: "Local cleanup", kind: fNum,
			parent: "delaft", help: "Files older than this are deleted (0 = delete all immediately).",
			input: mkInput(strconv.Itoa(cfg.RetentionDays), "")},

		{key: "skipfmt", label: "Skip file formats", section: "Files", kind: fBool,
			help: "When on, exclude matching files from uploads (e.g. tmp, log; .* skips dotfiles).",
			on:   len(cfg.SkipFormats) > 0},
		{key: "formats", label: "Formats to skip", section: "Files", kind: fText,
			parent: "skipfmt", help: "Space-separated: 'tmp' means *.tmp; '.*' skips dotfiles.",
			placeholder: defaultSkipFormats, input: mkInput(strings.Join(cfg.SkipFormats, " "), defaultSkipFormats)},
		{key: "ignored", label: "Ignored folders", section: "Files", kind: fText,
			help:        "Space-separated folder names excluded from inside every backup (e.g. node_modules).",
			placeholder: "(none)", input: mkInput(strings.Join(cfg.IgnoredFolders, " "), "(none)")},

		{key: "log", label: "Log file", section: "Log", kind: fText,
			help:        "Backup log path. Example: /data/logs/backup-drive.log (blank = per-account default).",
			placeholder: "(default)", input: mkInput(cfg.LogFile, "(default)")},
	}

	s := settingsModel{base: cfg, remoteName: cfg.RemoteName, fields: fields}
	s.fields[0].input.Focus() // start editing the first field
	return s
}

func (s settingsModel) Init() tea.Cmd { return textinput.Blink }

func (s *settingsModel) setSize(w, h int) {
	s.width, s.height = w, h
	vw := s.valueWidth()
	for i := range s.fields {
		s.fields[i].input.Width = vw
	}
}

// valueWidth is the column budget for a field's value/input. It leaves one
// extra column of slack so a focused textinput (which renders one cell past its
// Width for the cursor) can't push the scrollbar onto the next line.
func (s settingsModel) valueWidth() int {
	vw := (s.width - 1) - setLeftColW - 3
	if vw < 8 {
		vw = 8
	}
	return vw
}

// fieldIndex returns the index of the field with the given key, or -1.
func (s settingsModel) fieldIndex(key string) int {
	for i := range s.fields {
		if s.fields[i].key == key {
			return i
		}
	}
	return -1
}

// hasSub reports whether the field at i is a toggle with sub-settings.
func (s settingsModel) hasSub(i int) bool {
	for j := range s.fields {
		if s.fields[j].parent == s.fields[i].key {
			return true
		}
	}
	return false
}

// visibleIndices returns the field indices currently shown: every top-level
// field, plus each enabled toggle's children.
func (s settingsModel) visibleIndices() []int {
	var out []int
	for i := range s.fields {
		if s.fields[i].parent == "" {
			out = append(out, i)
			continue
		}
		if pj := s.fieldIndex(s.fields[i].parent); pj >= 0 && s.fields[pj].on {
			out = append(out, i)
		}
	}
	return out
}

// toConfig assembles a config from the current field values. Sub-settings of a
// disabled toggle are dropped (e.g. no skip formats when the toggle is off).
func (s settingsModel) toConfig() config.Config {
	val := func(k string) string {
		if i := s.fieldIndex(k); i >= 0 {
			return strings.TrimSpace(s.fields[i].input.Value())
		}
		return ""
	}
	onOff := func(k string) bool {
		if i := s.fieldIndex(k); i >= 0 {
			return s.fields[i].on
		}
		return false
	}
	atoi := func(v string) int { n, _ := strconv.Atoi(v); return n }

	var skip []string
	if onOff("skipfmt") {
		skip = strings.Fields(val("formats"))
	}

	cfg := s.base
	cfg.RemoteDestination = val("dest")
	cfg.RestoreDestination = val("restore")
	cfg.DeleteAfterUpload = onOff("delaft")
	cfg.RetentionDays = atoi(val("ret"))
	cfg.RemoteRetentionDays = atoi(val("rret"))
	cfg.RemoteCleanupSafetyDays = atoi(val("safety"))
	cfg.SkipFormats = skip
	cfg.IgnoredFolders = strings.Fields(val("ignored"))
	cfg.LogFile = val("log")
	return cfg
}

func (s settingsModel) Update(msg tea.Msg) (settingsModel, tea.Cmd) {
	// Once saved, the screen is a confirmation; any key returns to the menu.
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

	if key, ok := msg.(tea.KeyMsg); ok {
		return s.handleKey(key)
	}
	// Non-key messages (e.g. cursor blink) go to the focused text input.
	vis := s.visibleIndices()
	if s.focus < len(vis) {
		fi := vis[s.focus]
		if s.fields[fi].kind != fBool {
			var cmd tea.Cmd
			s.fields[fi].input, cmd = s.fields[fi].input.Update(msg)
			return s, cmd
		}
	}
	return s, nil
}

// handleKey processes a key for the editor, routing typing to the focused text
// input and reserving navigation/toggle/save keys.
func (s settingsModel) handleKey(key tea.KeyMsg) (settingsModel, tea.Cmd) {
	vis := s.visibleIndices()
	onSave := s.focus >= len(vis)

	switch key.String() {
	case "esc":
		return s, func() tea.Msg { return goBackMsg{} }
	case "ctrl+s":
		return s.save()
	case "up", "shift+tab":
		return s.moveFocus(-1)
	case "down", "tab":
		return s.moveFocus(1)
	case "enter":
		if onSave {
			return s.save()
		}
		return s.moveFocus(1)
	}

	if onSave {
		return s, nil
	}

	fi := vis[s.focus]
	if s.fields[fi].kind == fBool {
		switch key.String() {
		case " ", "space", "left", "right", "h", "l":
			s.toggleField(fi)
		}
		return s, nil
	}

	// Numeric fields accept digits only (control keys still pass through).
	if s.fields[fi].kind == fNum && key.Type == tea.KeyRunes {
		for _, r := range key.Runes {
			if r < '0' || r > '9' {
				return s, nil
			}
		}
	}
	var cmd tea.Cmd
	s.fields[fi].input, cmd = s.fields[fi].input.Update(key)
	return s, cmd
}

// toggleField flips a toggle and pre-fills a freshly enabled "Skip file
// formats" box with sensible defaults.
func (s *settingsModel) toggleField(fi int) {
	f := &s.fields[fi]
	f.on = !f.on
	if f.on && f.key == "skipfmt" {
		if ci := s.fieldIndex("formats"); ci >= 0 && strings.TrimSpace(s.fields[ci].input.Value()) == "" {
			s.fields[ci].input.SetValue(defaultSkipFormats)
		}
	}
	// Visibility may have changed; keep focus in range.
	if n := len(s.visibleIndices()); s.focus > n {
		s.focus = n
	}
}

// moveFocus shifts focus by dir over the visible rows, blurring the old text
// input and focusing the new one so only the active field shows a cursor.
func (s settingsModel) moveFocus(dir int) (settingsModel, tea.Cmd) {
	vis := s.visibleIndices()
	if s.focus < len(vis) && s.fields[vis[s.focus]].kind != fBool {
		s.fields[vis[s.focus]].input.Blur()
	}
	s.focus += dir
	if s.focus < 0 {
		s.focus = 0
	}
	if s.focus > len(vis) {
		s.focus = len(vis)
	}
	var cmd tea.Cmd
	if s.focus < len(vis) && s.fields[vis[s.focus]].kind != fBool {
		cmd = s.fields[vis[s.focus]].input.Focus()
	}
	return s, cmd
}

// save validates numeric fields, then emits the edited config and flips to the
// done state. A bad number shows inline and blocks the save.
func (s settingsModel) save() (settingsModel, tea.Cmd) {
	for i := range s.fields {
		if s.fields[i].kind != fNum {
			continue
		}
		if v := strings.TrimSpace(s.fields[i].input.Value()); v == "" {
			s.status, s.failed = s.fields[i].label+" must be a number", true
			return s, nil
		}
	}
	s.done = true
	cfg := s.toConfig()
	cmd := tea.Batch(
		func() tea.Msg { return settingsSavedMsg{cfg: cfg} },
		tea.Tick(saveConfirmationTimeout, func(time.Time) tea.Msg { return doneTimeoutMsg{} }),
	)
	return s, cmd
}

func (s settingsModel) View() string {
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

	// Scroll just enough to keep the focused field visible.
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

	title := titleStyle.Render("Settings — " + s.remoteName)
	return title + "\n\n" + strings.Join(rows, "\n")
}

// contentLines renders every visible row into a flat line slice and reports the
// line range [start,end) occupied by the focused row, so the view can scroll.
func (s settingsModel) contentLines() (lines []string, focusStart, focusEnd int) {
	vis := s.visibleIndices()
	prevSection := ""
	for p, fi := range vis {
		if s.fields[fi].section != prevSection {
			if prevSection != "" {
				lines = append(lines, "")
			}
			lines = append(lines, settingsSectionStyle.Render(s.fields[fi].section))
			prevSection = s.fields[fi].section
		}
		if p == s.focus {
			focusStart = len(lines)
		}
		lines = append(lines, s.fieldLine(fi, p == s.focus))
		if p == s.focus {
			focusEnd = len(lines)
		}
	}

	lines = append(lines, "")
	onSave := s.focus >= len(vis)
	if onSave {
		focusStart = len(lines)
	}
	save := "[ Save ]"
	if onSave {
		save = titleStyle.Render("‹ Save ›")
	} else {
		save = subtitleStyle.Render(save)
	}
	lines = append(lines, save)
	if onSave {
		focusEnd = len(lines)
	}

	if s.status != "" {
		lines = append(lines, "")
		if s.failed {
			lines = append(lines, errorStyle.Render("✗ "+s.status))
		} else {
			lines = append(lines, okStyle.Render("✓ "+s.status))
		}
	}
	return lines, focusStart, focusEnd
}

// fieldLine renders one row: an optional child indent, a ▾/▸ chevron for toggles
// with sub-settings (or a focus arrow otherwise), the label, and the value.
func (s settingsModel) fieldLine(fi int, focused bool) string {
	f := s.fields[fi]

	indent := ""
	if f.parent != "" {
		indent = strings.Repeat(" ", setChildPad)
	}

	var gutter string
	switch {
	case s.hasSub(fi):
		if f.on {
			gutter = "▾ "
		} else {
			gutter = "▸ "
		}
	case focused:
		gutter = "› "
	default:
		gutter = "  "
	}

	left := padRight(indent+gutter+f.label, setLeftColW)
	if focused {
		left = titleStyle.Render(left)
	}
	return left + "  " + s.fieldValue(f, focused)
}

// fieldValue renders a field's value: the live input when focused, otherwise
// the stored value or a dimmed placeholder; toggles render [x]/[ ].
func (s settingsModel) fieldValue(f setField, focused bool) string {
	if f.kind == fBool {
		txt := "[ ] off"
		if f.on {
			txt = "[x] on"
		}
		if focused {
			return titleStyle.Render(txt)
		}
		return valueStyle.Render(txt)
	}
	if focused {
		return f.input.View()
	}
	if v := f.input.Value(); v != "" {
		return valueStyle.Render(clip(v, s.valueWidth()))
	}
	return placeholderStyle.Render(f.placeholder)
}

// statusHint returns the help text for the focused row, shown in the root status
// bar (the old-browser-style band below the pane).
func (s settingsModel) statusHint() string {
	if s.done {
		return ""
	}
	vis := s.visibleIndices()
	if s.focus < len(vis) {
		return s.fields[vis[s.focus]].help
	}
	return "Save your changes (enter or ctrl+s)."
}

func (s settingsModel) doneView() string {
	if s.saveErr != nil {
		return errorStyle.Render("✗ Could not save settings") + "\n\n" +
			subtitleStyle.Render(s.saveErr.Error())
	}
	cfg := s.toConfig()
	var b strings.Builder
	b.WriteString(titleStyle.Render("✓ Settings saved"))
	b.WriteString("\n\n")
	b.WriteString(infoLine("Remote", cfg.RemoteName) + "\n")
	b.WriteString(infoLine("Backup folders", fmt.Sprintf("%d", len(cfg.SourceFolders))) + "\n")
	b.WriteString(infoLine("Destination", destinationLabel(cfg.RemoteDestination)) + "\n")
	b.WriteString(subtitleStyle.Render(fmt.Sprintf("Remote retention: %dd • safety: %dd",
		cfg.RemoteRetentionDays, cfg.RemoteCleanupSafetyDays)))
	return b.String()
}

// footerHint returns the key hints for the current settings state.
func (s settingsModel) footerHint() string {
	if s.done {
		return "enter/esc back • q quit"
	}
	return "↑/↓ move • space toggle • enter next • ctrl+s save • esc cancel"
}

// scrollColumn renders a height-row vertical scrollbar for content of `total`
// lines scrolled to `offset`. When everything fits it is blank.
func scrollColumn(height, total, offset int) []string {
	col := make([]string, height)
	if total <= height {
		for i := range col {
			col[i] = " "
		}
		return col
	}
	thumb := height * height / total
	if thumb < 1 {
		thumb = 1
	}
	pos := 0
	if maxOff := total - height; maxOff > 0 {
		pos = offset * (height - thumb) / maxOff
	}
	for i := 0; i < height; i++ {
		if i >= pos && i < pos+thumb {
			col[i] = titleStyle.Render("█")
		} else {
			col[i] = subtitleStyle.Render("│")
		}
	}
	return col
}

// padRight pads a plain string with spaces to width w (no truncation needed for
// the short, fixed labels it is used on).
func padRight(s string, w int) string {
	if d := lipgloss.Width(s); d < w {
		return s + strings.Repeat(" ", w-d)
	}
	return s
}

// padLineTo right-pads a (possibly styled) line to display width w so the
// scrollbar lands in a fixed column.
func padLineTo(s string, w int) string {
	if d := lipgloss.Width(s); d < w {
		return s + strings.Repeat(" ", w-d)
	}
	return s
}

// clipPath truncates a filesystem path to at most w runes from the LEFT, so the
// tail — the part that actually tells two paths apart — stays visible:
// "/very/long/prefix/alpha" becomes "…/prefix/alpha". Plain clip would cut the
// tail off and render sibling paths identical.
func clipPath(s string, w int) string {
	if w < 1 {
		w = 1
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return "…" + string(r[len(r)-(w-1):])
}

// clip truncates a plain string to at most w runes, adding an ellipsis when cut.
func clip(s string, w int) string {
	if w < 1 {
		w = 1
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}
