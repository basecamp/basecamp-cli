package workspace

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/appctx"
)

const (
	// The key that opens this screen from the folder it edits.
	editFolderKey = "e"

	// The key that deletes the folder, once from the edit screen and once more
	// to mean it.
	deleteFolderKey = "d"
)

// editFolderMsg asks the model to open the edit form over the folder.
type editFolderMsg struct{ folder folder }

// folderSavedMsg is the answer to renaming a folder, and folderDeletedMsg the
// answer to deleting one. Both are the write coming back.
type folderSavedMsg struct {
	name string
	err  error
}

type folderDeletedMsg struct {
	name string
	err  error
}

// folderRenamedMsg and folderGoneMsg are what the modal tells the workspace once
// a write has landed: the screen underneath is showing the folder, so it is the
// one that has to hear about it.
type folderRenamedMsg struct {
	id   int64
	name string
}

type folderGoneMsg struct {
	name string
}

// folderEdit renames a folder, and deletes it.
//
// It shows the folder's color but cannot change it. The color is a per-viewer
// customization on the folder's bucket, set by PUT buckets/:id/user_customizations
// with user_customization[color] — an endpoint the SDK has no operation for, and
// FoldersService.Update takes a name and nothing else. See "[SDK] Let a folder's
// color be set" on the CLIs board. Showing it read-only beats a picker that
// cannot save.
type folderEdit struct {
	ctx    *Context
	folder folder

	name textinput.Model

	// confirming is whether d has been pressed once already. Deleting a folder
	// is not undoable, and one keystroke is too few for that.
	confirming bool

	saving bool
	notice string

	width  int
	height int
}

func newFolderEdit(ctx *Context, edited folder) *folderEdit {
	name := textinput.New()
	name.Prompt = ""
	name.SetValue(edited.name)
	name.CursorEnd()

	return &folderEdit{ctx: ctx, folder: edited, name: name}
}

func (f *folderEdit) Init() tea.Cmd { return f.name.Focus() }

func (f *folderEdit) Title() string { return "Edit folder" }

func (f *folderEdit) Resize(width, height int) {
	f.width = width
	f.height = height
	f.name.SetWidth(max(width, 1))
}

// Update takes the answers to its own writes. Both of them close the modal by
// handing the model a message: renaming a folder and deleting one are things the
// screen underneath has to hear about, and the modal is not the one to tell it.
func (f *folderEdit) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case folderSavedMsg:
		f.saving = false
		if msg.err != nil {
			f.notice = errorNotice("Could not rename the folder", msg.err)
			return nil, true
		}
		return func() tea.Msg { return folderRenamedMsg{id: f.folder.id, name: msg.name} }, true

	case folderDeletedMsg:
		f.saving = false
		if msg.err != nil {
			f.notice = errorNotice("Could not delete the folder", msg.err)
			return nil, true
		}
		return func() tea.Msg { return folderGoneMsg{name: msg.name} }, true
	}

	// The field gets its cursor blink, and the message goes on to the screen
	// underneath: this modal opens over a list that may still be waiting on a
	// read, and claiming every message left that read with nowhere to land.
	name, cmd := f.name.Update(msg)
	f.name = name
	return cmd, false
}

// HandleKey answers whether the modal stays open. Esc closes it; everything else
// is part of the form.
func (f *folderEdit) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	// Anything but a second d is a change of mind.
	confirming := f.confirming
	f.confirming = false

	switch msg.Key().Code {
	case tea.KeyEnter:
		cmd, done := f.save()
		return cmd, !done
	case tea.KeyEsc:
		return nil, false
	}

	if msg.String() == deleteFolderKey && f.name.Value() == f.folder.name {
		if confirming {
			return f.delete(), true
		}
		f.confirming = true
		return nil, true
	}

	name, cmd := f.name.Update(msg)
	f.name = name
	return cmd, true
}

// save writes the new name, and answers whether the modal is finished. A name
// nobody changed is nothing to write, so enter just closes.
func (f *folderEdit) save() (tea.Cmd, bool) {
	named := strings.TrimSpace(f.name.Value())
	switch named {
	case "":
		f.notice = "A folder needs a name."
		return nil, false
	case f.folder.name:
		return nil, true
	}

	f.saving = true
	f.notice = ""
	return renameFolder(f.ctx.Ctx(), f.ctx.app, f.folder.id, named), false
}

func (f *folderEdit) delete() tea.Cmd {
	f.saving = true
	f.notice = ""
	return deleteFolder(f.ctx.Ctx(), f.ctx.app, f.folder.id, f.folder.name)
}

func (f *folderEdit) View() string {
	styles := f.ctx.Styles()
	theme := styles.Theme()
	inner := max(f.width, 1)

	lines := []string{
		styles.Muted.Render("Name"),
		f.name.View(),
		lipgloss.NewStyle().Foreground(theme.Border).Render(strings.Repeat("─", inner)),
		"",
		fitRow(styles.Muted, "Color", f.colorSwatch(), inner),
		"",
	}

	if f.notice != "" {
		lines = append(lines, wrapText(f.notice, f.width)...)
		lines = append(lines, "")
	}

	lines = append(lines, f.deleteRow())
	return strings.Join(lines, "\n")
}

// colorSwatch is the folder's color as a block of it, next to its name. A name
// alone makes the reader picture the color; the block just is it.
func (f *folderEdit) colorSwatch() string {
	styles := f.ctx.Styles()
	named := f.folder.color
	if named == "" {
		named = "white"
	}

	tint, ok := folderColors[named]
	if !ok {
		return styles.Muted.Render(named)
	}
	return lipgloss.NewStyle().Foreground(tint).Render("⬤") + " " + styles.Muted.Render(named)
}

// deleteRow is the way out, and the second press that means it.
func (f *folderEdit) deleteRow() string {
	styles := f.ctx.Styles()

	if f.confirming {
		return lipgloss.NewStyle().Foreground(styles.Theme().Error).Bold(true).
			Render("Press d again to delete “" + f.folder.name + "”. This cannot be undone.")
	}
	if renamed := strings.TrimSpace(f.name.Value()) != f.folder.name; renamed {
		// d is a letter while a name is being typed, and there is no way for the
		// screen to tell the two apart. Saving the name first settles it.
		return styles.Muted.Render("Press enter to save the name, then d to delete the folder.")
	}
	return styles.Muted.Render("d to delete this folder")
}

func (f *folderEdit) HelpBindings() []helpBinding {
	return []helpBinding{{"enter", "save"}, {deleteFolderKey, "delete"}, {"esc", "cancel"}}
}

// --- Writing ---

func renameFolder(ctx context.Context, app *appctx.App, folderID int64, name string) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return folderSavedMsg{err: err}
		}
		if _, err := app.Account().Folders().Update(ctx, folderID, name); err != nil {
			return folderSavedMsg{err: err}
		}
		return folderSavedMsg{name: name}
	}
}

func deleteFolder(ctx context.Context, app *appctx.App, folderID int64, name string) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return folderDeletedMsg{err: err}
		}
		if err := app.Account().Folders().Delete(ctx, folderID); err != nil {
			return folderDeletedMsg{err: err}
		}
		return folderDeletedMsg{name: name}
	}
}
