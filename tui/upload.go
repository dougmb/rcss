package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dougmb/rcss/backup"
	"github.com/dougmb/rcss/config"
	"github.com/dougmb/rcss/rclone"
)

// Upload screen: runs backup.Upload, streaming rclone progress and the log
// lines to the UI. Same code path as `rcss upload` (headless/cron).

type uploadState int

const (
	upIdle uploadState = iota
	upRunning
	upDone
	upError
)

type uploadModel struct {
	cfg config.Config
	rc  *rclone.Client

	state uploadState
	// destInput edits the remote destination folder before the run. It is
	// pre-filled from the active config but the edit applies to this run only —
	// Settings stays the source of truth (it is not written back here).
	destInput textinput.Model
	spinner   spinner.Model
	stream    *opStream
	output    []string
	result    backup.UploadResult
	err       error
	width     int
	height    int
}

func newUploadModel(cfg config.Config, rc *rclone.Client) uploadModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = "(account root)"
	ti.SetValue(cfg.RemoteDestination)
	ti.Focus()

	return uploadModel{cfg: cfg, rc: rc, spinner: sp, state: upIdle, destInput: ti}
}

func (u uploadModel) Init() tea.Cmd { return textinput.Blink }

func (u *uploadModel) setSize(w, h int) {
	u.width, u.height = w, h
	iw := w - 2
	if iw < 10 {
		iw = 10
	}
	u.destInput.Width = iw
}

// start launches backup.Upload in a goroutine, streaming its output.
func (u uploadModel) start() (uploadModel, tea.Cmd) {
	u.state = upRunning
	u.output = nil
	stream := newOpStream()
	u.stream = stream

	cfg, rc := u.cfg, u.rc
	go func() {
		logPath, _ := cfg.ResolveLogFile()
		log, _ := backup.NewLogger(logPath, stream.sink(), false)
		res, err := backup.Upload(context.Background(), cfg, rc, log,
			backup.UploadOptions{ShowProgress: true})
		log.Close()
		stream.finishWith(res, err)
	}()

	return u, tea.Batch(stream.wait(), u.spinner.Tick)
}

func (u uploadModel) Update(msg tea.Msg) (uploadModel, tea.Cmd) {
	switch msg := msg.(type) {
	case opEvent:
		if msg.done {
			if res, ok := msg.payload.(backup.UploadResult); ok {
				u.result = res
			}
			if msg.err != nil {
				u.state, u.err = upError, msg.err
			} else {
				u.state = upDone
			}
			return u, nil
		}
		u.output = append(u.output, msg.line)
		return u, u.stream.wait()

	case spinner.TickMsg:
		var cmd tea.Cmd
		u.spinner, cmd = u.spinner.Update(msg)
		return u, cmd

	case tea.KeyMsg:
		// On the idle screen the destination field has focus, so most keys are
		// typed into it; only enter (confirm + start) and esc (back) are control
		// keys here. q/backspace must reach the input so folder names can contain
		// them. (ctrl+c still quits — the root handles it before we get here.)
		if u.state == upIdle {
			switch msg.String() {
			case "enter":
				u.cfg.RemoteDestination = strings.TrimSpace(u.destInput.Value())
				return u.start()
			case "esc":
				return u, func() tea.Msg { return goBackMsg{} }
			}
			var cmd tea.Cmd
			u.destInput, cmd = u.destInput.Update(msg)
			return u, cmd
		}
		switch msg.String() {
		case "q":
			return u, tea.Quit
		case "esc", "backspace":
			if u.state == upRunning {
				return u, nil // don't leave mid-upload
			}
			return u, func() tea.Msg { return goBackMsg{} }
		case "enter":
			if u.state == upDone || u.state == upError {
				return u, func() tea.Msg { return goBackMsg{} }
			}
		}
	}
	return u, nil
}

func (u uploadModel) View() string {
	switch u.state {
	case upIdle:
		var b strings.Builder
		b.WriteString(titleStyle.Render("Back Up Now"))
		b.WriteString("\n\n")
		b.WriteString(subtitleStyle.Render("Uploads each configured source folder to the cloud (one-way copy)."))
		b.WriteString("\n\n")
		b.WriteString(subtitleStyle.Render(fmt.Sprintf("Source: %d folder(s)", len(u.cfg.SourceFolders))))
		b.WriteString("\n")
		b.WriteString(subtitleStyle.Render("Remote: " + u.cfg.RemoteName))
		b.WriteString("\n")
		b.WriteString(lastBackupLine(u.cfg))
		b.WriteString("\n\n")
		b.WriteString(subtitleStyle.Render("Destination folder for this run only (blank = account root):"))
		b.WriteString("\n")
		b.WriteString(u.destInput.View())
		b.WriteString("\n\n")
		b.WriteString("Press enter to start the backup.")
		return b.String()
	case upRunning:
		return outputView(u.spinner.View()+" Backing up…", u.output, u.height)
	case upDone:
		heading := titleStyle.Render(fmt.Sprintf("✓ Backup %s in %s — %d removed, %d errors",
			u.result.Status, u.result.Duration.Round(1e9), u.result.FilesDeleted, u.result.UploadErrors))
		return outputView(heading, u.output, u.height)
	case upError:
		return outputView(errorStyle.Render("✗ Backup failed: "+u.err.Error()), u.output, u.height)
	}
	return ""
}

func (u uploadModel) footerHint() string {
	switch u.state {
	case upIdle:
		return "type to edit destination • enter start • esc back"
	case upRunning:
		return "backing up… • q quit"
	default:
		return "enter/esc back • q quit"
	}
}

// outputView renders a heading and the tail of streamed output that fits the
// available height. Shared by the upload and clean screens.
func outputView(heading string, output []string, height int) string {
	lines := output
	maxLines := height - 4
	if maxLines < 1 {
		maxLines = 1
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return heading + "\n\n" + subtitleStyle.Render(strings.Join(lines, "\n"))
}
