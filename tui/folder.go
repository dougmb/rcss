package tui

import (
	"github.com/charmbracelet/bubbles/filepicker"
	tea "github.com/charmbracelet/bubbletea"
)

// Folder picker: a directory-only file picker used by the Backup source screen
// to add a folder to the account's source list. It replaces editing BACKUP_ROOT
// by hand in backup.env.

// folderChosenMsg tells the root model the user picked a directory.
type folderChosenMsg struct{ path string }

// folderModel wraps bubbles/filepicker restricted to directories.
type folderModel struct {
	fp filepicker.Model
}

func newFolderModel(start string) folderModel {
	fp := filepicker.New()
	fp.DirAllowed = true
	fp.FileAllowed = false
	fp.ShowHidden = false
	fp.AutoHeight = false
	if start != "" {
		fp.CurrentDirectory = start
	}
	return folderModel{fp: fp}
}

// Init starts the picker reading its current directory.
func (f folderModel) Init() tea.Cmd { return f.fp.Init() }

func (f *folderModel) setHeight(h int) {
	if h < 1 {
		h = 1
	}
	f.fp.SetHeight(h)
}

// Update drives the picker. enter on a highlighted directory selects it
// (DirAllowed); right/l navigates in, left/h goes up. esc returns to the menu.
func (f folderModel) Update(msg tea.Msg) (folderModel, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			return f, func() tea.Msg { return goBackMsg{} }
		case "q":
			return f, tea.Quit
		}
	}

	var cmd tea.Cmd
	f.fp, cmd = f.fp.Update(msg)
	if ok, path := f.fp.DidSelectFile(msg); ok {
		return f, func() tea.Msg { return folderChosenMsg{path: path} }
	}
	return f, cmd
}

// View renders the current directory header and the picker body. The root
// frames it and adds the footer.
func (f folderModel) View() string {
	header := titleStyle.Render("Add a backup source folder") + "\n" +
		subtitleStyle.Render(f.fp.CurrentDirectory) + "\n\n"
	return header + f.fp.View()
}
