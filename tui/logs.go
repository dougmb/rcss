package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dougmb/rcss/config"
)

// Logs screen: a scrollable viewport over the sync log with ERROR/WARN lines
// highlighted. Reads the same file the upload and clean operations append to.

type logsModel struct {
	cfg  config.Config
	vp   viewport.Model
	path string
}

func newLogsModel(cfg config.Config) logsModel {
	return logsModel{cfg: cfg, vp: viewport.New(0, 0)}
}

func (l *logsModel) setSize(w, h int) {
	l.vp.Width = w
	l.vp.Height = h
}

// reload reads the log file and refreshes the viewport, scrolled to the end.
func (l *logsModel) reload() {
	path, err := l.cfg.ResolveLogFile()
	if err != nil {
		l.vp.SetContent(errorStyle.Render(err.Error()))
		return
	}
	l.path = path

	data, err := os.ReadFile(path)
	if err != nil {
		l.vp.SetContent(subtitleStyle.Render("No log yet at " + path))
		return
	}
	l.vp.SetContent(colorizeLog(string(data)))
	l.vp.GotoBottom()
}

func (l logsModel) Update(msg tea.Msg) (logsModel, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "q":
			return l, tea.Quit
		case "esc", "backspace":
			return l, func() tea.Msg { return goBackMsg{} }
		case "r":
			l.reload()
			return l, nil
		}
	}
	var cmd tea.Cmd
	l.vp, cmd = l.vp.Update(msg)
	return l, cmd
}

func (l logsModel) View() string { return l.vp.View() }

// colorizeLog highlights ERROR (red) and WARN (amber) lines in the log dump.
func colorizeLog(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		switch {
		case strings.Contains(line, "[ERROR"):
			lines[i] = errorStyle.Render(line)
		case strings.Contains(line, "[WARN"):
			lines[i] = warnStyle.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}
