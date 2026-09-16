package tui

import (
	"context"
	"os/exec"
	"sort"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dougmb/rcss/rclone"
)

// Account screen: the multi-account manager. It lists the rclone remotes and
// marks which one is the active RCSS account and which already have isolated
// RCSS settings. Selecting a remote switches the active account (creating its
// settings on first use); `d` forgets an account's settings; "Configure a new
// account…" shells out to `rclone config`. Each account (one per rclone remote)
// keeps its own folders, retention, log, and schedule.

// remoteItem is a selectable rclone remote, or an RCSS account whose remote is
// gone (missing) — those are listed so a stale account can be seen and forgotten
// instead of silently pointing nowhere.
type remoteItem struct {
	name       string
	current    bool // the active account
	configured bool // has stored RCSS settings
	missing    bool // configured here, but no longer an rclone remote
}

func (i remoteItem) Title() string {
	switch {
	case i.missing:
		return i.name + "  ⚠ remote missing"
	case i.current:
		return i.name + "  ● active"
	case i.configured:
		return i.name + "  · configured"
	default:
		return i.name
	}
}
func (i remoteItem) Description() string {
	switch {
	case i.missing && i.current:
		return "active account, but this rclone remote no longer exists — press d to forget"
	case i.missing:
		return "RCSS account whose rclone remote no longer exists — press d to forget"
	case i.configured:
		return "rclone remote · RCSS account"
	}
	return "rclone remote"
}
func (i remoteItem) FilterValue() string { return i.name }

// configItem is the special row that launches `rclone config`.
type configItem struct{}

func (configItem) Title() string       { return "＋ Configure a new account…" }
func (configItem) Description() string { return "Opens `rclone config`" }
func (configItem) FilterValue() string { return "configure new account" }

// --- messages ---

// remotesLoadedMsg carries the result of rclone.ListRemotes.
type remotesLoadedMsg struct {
	remotes []string
	err     error
}

// reloadRemotesMsg asks the account screen to re-list remotes (e.g. after
// returning from `rclone config`).
type reloadRemotesMsg struct{}

// remoteChosenMsg tells the root model to make a remote the active account.
type remoteChosenMsg struct{ name string }

// accountForgetMsg asks the root to drop an account's RCSS settings (the rclone
// remote is left intact).
type accountForgetMsg struct{ name string }

// accountModel is the account screen's sub-model.
type accountModel struct {
	rc         *rclone.Client
	current    string          // active account name
	configured map[string]bool // remotes with stored RCSS settings
	list       list.Model
	loading    bool
	err        error

	// done flips to true once the root has saved the account change.
	// saveErr is filled by the root; doneAction describes what changed.
	done       bool
	saveErr    error
	doneAction string
}

// staleAccounts returns the configured account names that are absent from the
// live remote set, sorted so the list order is stable between refreshes.
func (a accountModel) staleAccounts(live map[string]bool) []string {
	var out []string
	for name := range a.configured {
		if !live[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func newAccountModel(rc *rclone.Client, current string, accounts []string) accountModel {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Accounts (rclone remotes)"
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.Styles.Title = titleStyle

	set := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		set[a] = true
	}
	return accountModel{rc: rc, current: current, configured: set, list: l, loading: true}
}

func (a *accountModel) setSize(w, h int) { a.list.SetSize(w, h) }

// load returns a command that fetches the configured remotes.
func (a accountModel) load() tea.Cmd {
	rc := a.rc
	return func() tea.Msg {
		remotes, err := rc.ListRemotes(context.Background())
		return remotesLoadedMsg{remotes: remotes, err: err}
	}
}

// Update handles the account screen's messages and keys.
func (a accountModel) Update(msg tea.Msg) (accountModel, tea.Cmd) {
	if a.done {
		switch msg := msg.(type) {
		case doneTimeoutMsg:
			return a, func() tea.Msg { return goBackMsg{} }
		case tea.KeyMsg:
			switch msg.String() {
			case "q":
				return a, tea.Quit
			case "enter", "esc", "backspace":
				return a, func() tea.Msg { return goBackMsg{} }
			}
		}
		return a, nil
	}

	switch msg := msg.(type) {
	case remotesLoadedMsg:
		a.loading = false
		a.err = msg.err
		live := make(map[string]bool, len(msg.remotes))
		items := make([]list.Item, 0, len(msg.remotes)+len(a.configured)+1)
		for _, r := range msg.remotes {
			live[r] = true
			items = append(items, remoteItem{name: r, current: r == a.current, configured: a.configured[r]})
		}
		// An account can outlive its rclone remote (renamed or deleted in rclone
		// config). Listing only live remotes would hide it entirely, leaving the
		// user with an active account that points nowhere and no way to clear it.
		for _, name := range a.staleAccounts(live) {
			items = append(items, remoteItem{
				name: name, current: name == a.current, configured: true, missing: true,
			})
		}
		items = append(items, configItem{})
		a.list.SetItems(items)
		return a, nil

	case reloadRemotesMsg:
		a.loading = true
		a.err = nil
		return a, a.load()

	case tea.KeyMsg:
		// While filtering, let the list consume keys.
		if a.list.FilterState() == list.Filtering {
			var cmd tea.Cmd
			a.list, cmd = a.list.Update(msg)
			return a, cmd
		}
		switch msg.String() {
		case "esc", "backspace":
			return a, func() tea.Msg { return goBackMsg{} }
		case "q":
			return a, tea.Quit
		case "r":
			a.loading = true
			a.err = nil
			return a, a.load()
		case "d":
			if it, ok := a.list.SelectedItem().(remoteItem); ok && it.configured {
				name := it.name
				a.done = true
				a.doneAction = "forgotten"
				return a, tea.Batch(
					func() tea.Msg { return accountForgetMsg{name: name} },
					tea.Tick(saveConfirmationTimeout, func(time.Time) tea.Msg { return doneTimeoutMsg{} }),
				)
			}
			return a, nil
		case "enter":
			switch it := a.list.SelectedItem().(type) {
			case remoteItem:
				if it.missing {
					return a, nil // nothing to activate; the row says to forget it
				}
				name := it.name
				a.done = true
				a.doneAction = "activated"
				return a, tea.Batch(
					func() tea.Msg { return remoteChosenMsg{name: name} },
					tea.Tick(saveConfirmationTimeout, func(time.Time) tea.Msg { return doneTimeoutMsg{} }),
				)
			case configItem:
				cmd := exec.Command("rclone", "config")
				return a, tea.ExecProcess(cmd, func(error) tea.Msg {
					return reloadRemotesMsg{}
				})
			}
			return a, nil
		}
	}

	var cmd tea.Cmd
	a.list, cmd = a.list.Update(msg)
	return a, cmd
}

// View renders the inner body of the account screen (the root frames it and
// adds the footer).
func (a accountModel) View() string {
	if a.done {
		return a.doneView()
	}
	if a.loading {
		return subtitleStyle.Render("Loading remotes…")
	}
	if a.err != nil {
		return errorStyle.Render("Failed to list remotes: " + a.err.Error())
	}
	return a.list.View()
}

func (a accountModel) footerHint() string {
	if a.done {
		return "enter/esc back • q quit"
	}
	return "↑/↓ move • enter switch • d forget • r refresh • / filter • esc back"
}

// doneView renders the save confirmation before auto-returning to the menu.
func (a accountModel) doneView() string {
	if a.saveErr != nil {
		return errorStyle.Render("✗ Could not save account") + "\n\n" +
			subtitleStyle.Render(a.saveErr.Error())
	}

	var title string
	switch a.doneAction {
	case "forgotten":
		title = "✓ Account settings forgotten"
	default:
		title = "✓ Account activated"
	}
	return titleStyle.Render(title) + "\n\n" +
		infoLine("Active account", a.current)
}
