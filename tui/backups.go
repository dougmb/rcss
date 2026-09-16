package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dougmb/rcss/backup"
	"github.com/dougmb/rcss/config"
	"github.com/dougmb/rcss/rclone"
)

// Backups screen: a file browser for the remote destination. The user can
// navigate into any folder and restore any file or directory shown.

type backupsState int

const (
	bsLoading backupsState = iota
	bsBrowsing
	bsConfirmDest
	bsRestoring
	bsDone
	bsError
)

// --- messages ---

type entriesLoadedMsg struct {
	entries []backup.RemoteEntry
	err     error
}

// entryItem wraps a RemoteEntry for the bubbles list.
type entryItem struct {
	entry backup.RemoteEntry
}

func (e entryItem) Title() string {
	if e.entry.IsDir {
		return "📁 " + e.entry.Name
	}
	return "📄 " + e.entry.Name
}
func (e entryItem) Description() string {
	if e.entry.IsDir {
		return "folder"
	}
	return "file"
}
func (e entryItem) FilterValue() string { return e.entry.Name }

// backupsModel is the backups screen's sub-model.
type backupsModel struct {
	cfg config.Config
	rc  *rclone.Client

	state   backupsState
	entries list.Model
	path    []string // stack of directory segments relative to remote destination

	// destInput edits the local restore destination before the download. It is
	// pre-filled with backup.RestoreTarget and the edit applies to this run only.
	destInput textinput.Model

	spinner spinner.Model
	stream  *opStream
	output  []string

	height int
	err    error
}

func newBackupsModel(cfg config.Config, rc *rclone.Client) backupsModel {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Restore"
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.Styles.Title = titleStyle

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ti := textinput.New()
	ti.Prompt = ""

	return backupsModel{
		cfg:       cfg,
		rc:        rc,
		state:     bsLoading,
		entries:   l,
		destInput: ti,
		spinner:   sp,
	}
}

func (b *backupsModel) setSize(w, h int) {
	b.height = h
	// Leave room for the breadcrumb/header above the list.
	listH := h - 3
	if listH < 1 {
		listH = 1
	}
	b.entries.SetSize(w, listH)
	iw := w - 2
	if iw < 10 {
		iw = 10
	}
	b.destInput.Width = iw
}

// filtering reports whether the list is currently capturing filter text, so
// the root can avoid treating `/` or `?` as a global shortcut.
func (b backupsModel) filtering() bool {
	return b.entries.FilterState() == list.Filtering
}

// relPath returns the current remote path relative to the destination.
func (b backupsModel) relPath() string {
	return strings.Join(b.path, "/")
}

// loadEntries lists the contents of the current path.
func (b backupsModel) loadEntries() tea.Cmd {
	cfg, rc, rel := b.cfg, b.rc, b.relPath()
	return func() tea.Msg {
		es, err := backup.ListEntries(context.Background(), cfg, rc, rel)
		return entriesLoadedMsg{entries: es, err: err}
	}
}

// enterConfirm moves to the destination-confirm step, pre-filling the editable
// local path with backup.RestoreTarget for the currently selected item.
func (b backupsModel) enterConfirm() (backupsModel, tea.Cmd) {
	it, ok := b.entries.SelectedItem().(entryItem)
	if !ok {
		return b, nil
	}
	rel := b.relPath()
	if rel != "" {
		rel = rel + "/" + it.entry.Name
	} else {
		rel = it.entry.Name
	}

	target, err := backup.RestoreTarget(b.cfg, rel)
	if err != nil {
		b.state, b.err = bsError, err
		return b, nil
	}
	b.destInput.SetValue(target)
	b.destInput.CursorEnd()
	b.destInput.Focus()
	b.state = bsConfirmDest
	return b, textinput.Blink
}

// startRestore launches backup.Restore in a goroutine, streaming its output.
// outputPath is the confirmed local destination for this run.
func (b backupsModel) startRestore(item backup.RemoteEntry, outputPath string) (backupsModel, tea.Cmd) {
	b.state = bsRestoring
	b.output = nil
	stream := newOpStream()
	b.stream = stream

	cfg, rc := b.cfg, b.rc
	rel := b.relPath()
	if rel != "" {
		rel = rel + "/" + item.Name
	} else {
		rel = item.Name
	}

	go func() {
		// Restore logs to the terminal only (no backup log), like the original
		// restoreBackup.sh; the sink streams every line to the UI.
		log, _ := backup.NewLogger("", stream.sink(), false)
		err := backup.Restore(context.Background(), cfg, rc, log, rel,
			backup.RestoreOptions{ShowProgress: true, IsDir: item.IsDir, OutputPath: outputPath})
		log.Close()
		stream.finish(err)
	}()

	return b, tea.Batch(stream.wait(), b.spinner.Tick)
}

// Update handles the backups screen's messages and keys.
func (b backupsModel) Update(msg tea.Msg) (backupsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case entriesLoadedMsg:
		if msg.err != nil {
			b.state, b.err = bsError, msg.err
			return b, nil
		}
		items := make([]list.Item, len(msg.entries))
		for i, e := range msg.entries {
			items[i] = entryItem{entry: e}
		}
		b.entries.SetItems(items)
		b.state = bsBrowsing
		return b, nil

	case opEvent:
		if msg.done {
			if msg.err != nil {
				b.state, b.err = bsError, msg.err
			} else {
				b.state = bsDone
			}
			return b, nil
		}
		b.output = append(b.output, msg.line)
		return b, b.stream.wait()

	case spinner.TickMsg:
		var cmd tea.Cmd
		b.spinner, cmd = b.spinner.Update(msg)
		return b, cmd

	case tea.KeyMsg:
		return b.handleKey(msg)
	}
	return b, nil
}

func (b backupsModel) handleKey(msg tea.KeyMsg) (backupsModel, tea.Cmd) {
	// While the list is filtering, forward keys to it.
	if b.state == bsBrowsing && b.entries.FilterState() == list.Filtering {
		var cmd tea.Cmd
		b.entries, cmd = b.entries.Update(msg)
		return b, cmd
	}

	// The destination-confirm step has a focused text field: forward typing to
	// it; only enter (restore) and esc (step back) are control keys. (ctrl+c
	// still quits — the root handles it before we get here.)
	if b.state == bsConfirmDest {
		switch msg.String() {
		case "enter":
			it, ok := b.entries.SelectedItem().(entryItem)
			if !ok {
				return b, nil
			}
			return b.startRestore(it.entry, strings.TrimSpace(b.destInput.Value()))
		case "esc":
			b.state = bsBrowsing
			return b, nil
		}
		var cmd tea.Cmd
		b.destInput, cmd = b.destInput.Update(msg)
		return b, cmd
	}

	switch msg.String() {
	case "q":
		return b, tea.Quit
	case "esc", "backspace":
		if b.state == bsBrowsing {
			if len(b.path) > 0 {
				b.path = b.path[:len(b.path)-1]
				b.state = bsLoading
				return b, tea.Batch(b.loadEntries(), b.spinner.Tick)
			}
		}
		return b, func() tea.Msg { return goBackMsg{} }
	case "enter":
		if b.state == bsDone || b.state == bsError {
			return b, func() tea.Msg { return goBackMsg{} }
		}
		if b.state == bsBrowsing {
			return b.enterConfirm()
		}
		return b, nil
	case "right", "l":
		if b.state == bsBrowsing {
			it, ok := b.entries.SelectedItem().(entryItem)
			if ok && it.entry.IsDir {
				b.path = append(b.path, it.entry.Name)
				b.state = bsLoading
				return b, tea.Batch(b.loadEntries(), b.spinner.Tick)
			}
		}
		return b, nil
	case "left", "h":
		if b.state == bsBrowsing && len(b.path) > 0 {
			b.path = b.path[:len(b.path)-1]
			b.state = bsLoading
			return b, tea.Batch(b.loadEntries(), b.spinner.Tick)
		}
		return b, nil
	}

	// Default: drive the list.
	if b.state == bsBrowsing {
		var cmd tea.Cmd
		b.entries, cmd = b.entries.Update(msg)
		return b, cmd
	}
	return b, nil
}

// breadcrumb renders the current remote path for display.
func (b backupsModel) breadcrumb() string {
	base := destinationLabel(b.cfg.RemoteDestination)
	if len(b.path) == 0 {
		return base
	}
	return base + "/" + strings.Join(b.path, "/")
}

// selectedRelPath returns the relative path of the currently selected item.
func (b backupsModel) selectedRelPath() string {
	it, ok := b.entries.SelectedItem().(entryItem)
	if !ok {
		return ""
	}
	rel := b.relPath()
	if rel != "" {
		return rel + "/" + it.entry.Name
	}
	return it.entry.Name
}

// View renders the inner body of the backups screen.
func (b backupsModel) View() string {
	switch b.state {
	case bsLoading:
		return b.spinner.View() + " Loading backups…"
	case bsBrowsing:
		var sb strings.Builder
		sb.WriteString(titleStyle.Render("Restore"))
		sb.WriteString(" ")
		sb.WriteString(subtitleStyle.Render(b.breadcrumb()))
		sb.WriteString("\n\n")
		if len(b.entries.Items()) == 0 {
			sb.WriteString(subtitleStyle.Render("This folder is empty."))
			return sb.String()
		}
		sb.WriteString(b.entries.View())
		return sb.String()
	case bsConfirmDest:
		var sb strings.Builder
		sb.WriteString(titleStyle.Render("Restore — confirm destination"))
		sb.WriteString("\n\n")
		sb.WriteString(subtitleStyle.Render("Item: " + b.selectedRelPath()))
		sb.WriteString("\n\n")
		sb.WriteString(subtitleStyle.Render("Restore to (local folder):"))
		sb.WriteString("\n")
		sb.WriteString(b.destInput.View())
		sb.WriteString("\n\n")
		sb.WriteString("Press enter to restore.")
		return sb.String()
	case bsRestoring:
		return b.viewOutput(b.spinner.View() + " Restoring…")
	case bsDone:
		return b.viewOutput(titleStyle.Render("✓ Restore complete."))
	case bsError:
		return errorStyle.Render("Error: " + b.err.Error())
	}
	return ""
}

// viewOutput shows a heading and the tail of the streamed restore output that
// fits the available height.
func (b backupsModel) viewOutput(heading string) string {
	lines := b.output
	maxLines := b.height - 4
	if maxLines < 1 {
		maxLines = 1
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return heading + "\n\n" + subtitleStyle.Render(strings.Join(lines, "\n"))
}

// footerHint returns the key hints appropriate to the current state.
func (b backupsModel) footerHint() string {
	switch b.state {
	case bsBrowsing:
		return "↑/↓ move • →/l open folder • ←/h back • enter restore • / filter • esc back • q quit"
	case bsConfirmDest:
		return "edit folder • enter restore • esc back"
	case bsDone, bsError:
		return "enter/esc back • q quit"
	default:
		return "esc back • q quit"
	}
}
