package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dougmb/rcss/backup"
	"github.com/dougmb/rcss/config"
	"github.com/dougmb/rcss/rclone"
)

// Clean screen: opens on an intro that spells out exactly what gets deleted
// (CLOUD files only) and under what conditions, makes clear that LOCAL files
// are pruned elsewhere (Back Up Now), and exposes a Force toggle. Real deletion
// always requires a dry-run preview first; deleting with Force on additionally
// requires a second, explicit confirmation. The safety lock itself lives in
// backup.Clean.

type cleanState int

const (
	clIntro cleanState = iota
	clDryRunning
	clDryDone
	clConfirmForce
	clRunning
	clDone
	clError
)

type cleanModel struct {
	cfg config.Config
	rc  *rclone.Client

	state   cleanState
	force   bool
	spinner spinner.Model
	stream  *opStream
	output  []string
	err     error
	height  int
}

func newCleanModel(cfg config.Config, rc *rclone.Client) cleanModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return cleanModel{cfg: cfg, rc: rc, spinner: sp, state: clIntro}
}

func (c *cleanModel) setHeight(h int) { c.height = h }

// remoteDest renders the remote base path for display.
func (c cleanModel) remoteDest() string {
	return c.cfg.RemoteBase()
}

// localRule describes what Back Up Now does to local files, for the intro text.
func (c cleanModel) localRule() string {
	if !c.cfg.DeleteAfterUpload {
		return "local files are kept (delete-after-upload is off)"
	}
	if c.cfg.RetentionDays == 0 {
		return "all local files removed after a successful upload (delete-after-upload is on)"
	}
	return fmt.Sprintf("local files older than %d day(s) removed after a successful upload", c.cfg.RetentionDays)
}

// run launches backup.Clean (dry-run or real) in a goroutine, streaming output.
// The current Force toggle is applied to both, so the preview matches the run.
func (c cleanModel) run(dry bool) (cleanModel, tea.Cmd) {
	if dry {
		c.state = clDryRunning
	} else {
		c.state = clRunning
	}
	c.output = nil
	stream := newOpStream()
	c.stream = stream

	cfg, rc, force := c.cfg, c.rc, c.force
	go func() {
		logPath, _ := cfg.ResolveLogFile()
		log, _ := backup.NewLogger(logPath, stream.sink(), false)
		err := backup.Clean(context.Background(), cfg, rc, log, backup.CleanOptions{DryRun: dry, Force: force})
		log.Close()
		stream.finish(err)
	}()

	return c, tea.Batch(stream.wait(), c.spinner.Tick)
}

func (c cleanModel) Update(msg tea.Msg) (cleanModel, tea.Cmd) {
	switch msg := msg.(type) {
	case opEvent:
		if msg.done {
			switch {
			case msg.err != nil:
				c.state, c.err = clError, msg.err
			case c.state == clDryRunning:
				c.state = clDryDone
			default:
				c.state = clDone
			}
			return c, nil
		}
		c.output = append(c.output, msg.line)
		return c, c.stream.wait()

	case spinner.TickMsg:
		var cmd tea.Cmd
		c.spinner, cmd = c.spinner.Update(msg)
		return c, cmd

	case tea.KeyMsg:
		return c.handleKey(msg)
	}
	return c, nil
}

func (c cleanModel) handleKey(msg tea.KeyMsg) (cleanModel, tea.Cmd) {
	switch msg.String() {
	case "q":
		return c, tea.Quit

	case "esc", "backspace":
		switch c.state {
		case clRunning, clDryRunning:
			return c, nil // don't leave mid-operation
		case clConfirmForce:
			c.state = clDryDone // cancel the forced deletion
			return c, nil
		default:
			return c, func() tea.Msg { return goBackMsg{} }
		}

	case "f": // toggle Force where it's safe to do so
		if c.state == clIntro || c.state == clDryDone || c.state == clError {
			c.force = !c.force
		}
		return c, nil

	case "enter":
		switch c.state {
		case clIntro:
			return c.run(true) // start the dry-run preview
		case clConfirmForce:
			return c.run(false) // confirmed forced deletion
		case clDone, clError:
			return c, func() tea.Msg { return goBackMsg{} }
		}
		return c, nil

	case "r": // re-run the dry-run preview
		if c.state == clDryDone || c.state == clError {
			return c.run(true)
		}
		return c, nil

	case "x": // execute the real deletion
		if c.state == clDryDone {
			if c.force {
				c.state = clConfirmForce // double-confirm a safety-lock bypass
				return c, nil
			}
			return c.run(false)
		}
		return c, nil

	case "y": // confirm forced deletion
		if c.state == clConfirmForce {
			return c.run(false)
		}
		return c, nil

	case "n": // decline forced deletion
		if c.state == clConfirmForce {
			c.state = clDryDone
			return c, nil
		}
		return c, nil
	}
	return c, nil
}

// forceLabel renders the current Force state.
func (c cleanModel) forceLabel() string {
	if c.force {
		return warnStyle.Render("ON  ⚠ bypasses the safety lock")
	}
	return "OFF"
}

func (c cleanModel) View() string {
	switch c.state {
	case clIntro:
		return c.introView()
	case clDryRunning:
		return outputView(c.spinner.View()+" Previewing deletions (dry-run)…", c.output, c.height)
	case clDryDone:
		heading := titleStyle.Render("Dry-run complete — nothing deleted yet.") + "\n" +
			subtitleStyle.Render("Force: ") + c.forceLabel()
		return outputView(heading, c.output, c.height)
	case clConfirmForce:
		return c.confirmForceView()
	case clRunning:
		return outputView(c.spinner.View()+" Deleting old remote backups…", c.output, c.height)
	case clDone:
		return outputView(titleStyle.Render("✓ Cleanup complete."), c.output, c.height)
	case clError:
		heading := errorStyle.Render("✗ Clean aborted: "+c.err.Error()) + "\n" +
			subtitleStyle.Render("Force: ") + c.forceLabel()
		return outputView(heading, c.output, c.height)
	}
	return ""
}

// introView explains what Clean deletes and where, the safety lock, that local
// files are handled elsewhere, and the Force toggle.
func (c cleanModel) introView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Clean — remove old CLOUD backups"))
	b.WriteString("\n\n")

	b.WriteString(subtitleStyle.Render("Deletes (cloud):"))
	b.WriteString(fmt.Sprintf("\n  • Files at %s older than %d day(s).",
		c.remoteDest(), c.cfg.RemoteRetentionDays))
	b.WriteString("\n\n")

	b.WriteString(subtitleStyle.Render("Safety lock:"))
	b.WriteString(fmt.Sprintf("\n  • Aborts unless a backup newer than %d day(s) exists on the remote,",
		c.cfg.RemoteCleanupSafetyDays))
	b.WriteString("\n    guarding history if uploads silently stopped.")
	b.WriteString("\n\n")

	b.WriteString(subtitleStyle.Render("Local files are NOT touched here:"))
	b.WriteString("\n  • " + c.localRule() + " — via Back Up Now.")
	b.WriteString("\n\n")

	b.WriteString(subtitleStyle.Render("Force: ") + c.forceLabel())
	return b.String()
}

// confirmForceView is the second, explicit confirmation before a forced delete.
func (c cleanModel) confirmForceView() string {
	var b strings.Builder
	b.WriteString(warnStyle.Render("⚠ FORCE DELETE — confirm"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("Force mode is ON. This BYPASSES the safety lock and deletes cloud files\n"+
		"older than %d day(s) at %s even if NO recent backup exists.\n"+
		"This can remove your only copies.",
		c.cfg.RemoteRetentionDays, c.remoteDest()))
	b.WriteString("\n\n")
	b.WriteString(errorStyle.Render("Press y to proceed, or n / esc to cancel."))
	return b.String()
}

func (c cleanModel) footerHint() string {
	switch c.state {
	case clIntro:
		return "enter dry-run • f toggle force • esc back"
	case clDryDone:
		return "x execute • r re-run dry-run • f toggle force • esc back"
	case clConfirmForce:
		return "y confirm force delete • n/esc cancel"
	case clError:
		return "r re-run dry-run • f toggle force • enter/esc back"
	case clDone:
		return "enter/esc back • q quit"
	default:
		return "working… • q quit"
	}
}
