package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dougmb/rcss/backup"
	"github.com/dougmb/rcss/config"
	"github.com/dougmb/rcss/scheduler"
)

// About screen: app identity, version, the rclone dependency status, and where
// RCSS keeps its config and log. Read-only — esc/backspace returns to the menu.

type aboutModel struct {
	cfg           config.Config
	rcloneMissing bool
	accountCount  int
	remoteMissing bool // the active account's rclone remote no longer exists
}

func newAboutModel(cfg config.Config, rcloneMissing bool, accountCount int, remoteMissing bool) aboutModel {
	return aboutModel{
		cfg: cfg, rcloneMissing: rcloneMissing,
		accountCount: accountCount, remoteMissing: remoteMissing,
	}
}

func (a aboutModel) Update(msg tea.Msg) (aboutModel, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "q":
			return a, tea.Quit
		case "esc", "backspace", "enter":
			return a, func() tea.Msg { return goBackMsg{} }
		}
	}
	return a, nil
}

func (a aboutModel) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("RCSS — Rclone Cloud Simple Scripts"))
	b.WriteString("\n")
	b.WriteString(subtitleStyle.Render("Per-project backups to an rclone cloud remote, in your terminal."))
	b.WriteString("\n\n")

	b.WriteString(infoLine("Version", appVersion()))
	b.WriteString("\n")
	active := a.cfg.RemoteName
	if active == "" {
		active = "—"
	}
	b.WriteString(infoLine("Active account", active))
	if a.remoteMissing {
		b.WriteString("  " + warnStyle.Render("⚠ no longer an rclone remote"))
	}
	b.WriteString("\n")
	b.WriteString(infoLine("Accounts", fmt.Sprintf("%d", a.accountCount)))
	b.WriteString("\n")
	if a.cfg.RemoteName != "" {
		b.WriteString(lastBackupLine(a.cfg))
		b.WriteString("\n")
	}

	if a.rcloneMissing {
		b.WriteString(infoLine("rclone", "") + warnStyle.Render("not found on PATH"))
	} else {
		b.WriteString(infoLine("rclone", "installed"))
	}
	b.WriteString("\n")
	b.WriteString(infoLine("Scheduler", scheduler.Backend()))
	b.WriteString("\n\n")

	if path, err := config.Path(); err == nil {
		b.WriteString(infoLine("Config", path))
		b.WriteString("\n")
	}
	if logPath, err := a.cfg.ResolveLogFile(); err == nil {
		b.WriteString(infoLine("Log", logPath))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(infoLine("Project", "https://github.com/dougmb/rcss"))
	b.WriteString("\n")
	b.WriteString(infoLine("rclone", "https://rclone.org"))

	return b.String()
}

// lastBackupLine renders an info line summarizing the most recent backup run
// recorded in the account's log: the timestamp plus a colored SUCCESS/PARTIAL
// tag, or "never" when no run has been recorded yet.
func lastBackupLine(cfg config.Config) string {
	logPath, err := cfg.ResolveLogFile()
	if err != nil {
		return infoLine("Last backup", "unknown")
	}
	info, ok, err := backup.LastRun(logPath)
	if err != nil || !ok {
		return infoLine("Last backup", "never")
	}
	line := infoLine("Last backup", info.Time.Format("2006-01-02 15:04"))
	tag := "(" + info.Status + ")"
	if info.Status == "SUCCESS" {
		return line + "  " + okStyle.Render(tag)
	}
	return line + "  " + warnStyle.Render(tag)
}
