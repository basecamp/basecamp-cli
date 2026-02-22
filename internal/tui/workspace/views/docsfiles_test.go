package views

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func sampleDocsFiles() []data.DocsFilesItemInfo {
	return []data.DocsFilesItemInfo{
		{ID: 1, Title: "Design Assets", Type: "Folder", CreatedAt: "Jan 1, 2025", Creator: "Alice", VaultsCount: 2, DocsCount: 3, UploadsCount: 1},
		{ID: 2, Title: "README", Type: "Document", CreatedAt: "Jan 2, 2025", Creator: "Bob"},
		{ID: 3, Title: "logo.png", Type: "Upload", CreatedAt: "Jan 3, 2025", Creator: "Carol"},
	}
}

func testDocsFilesView() *DocsFiles {
	session := workspace.NewTestSessionWithScope(workspace.Scope{
		AccountID: "acct1",
		ProjectID: 42,
		ToolID:    100, // root vault ID
	})
	styles := tui.NewStyles()
	pool := data.NewPool[[]data.DocsFilesItemInfo](
		"docsfiles:42:100",
		data.PoolConfig{FreshTTL: time.Hour},
		func(context.Context) ([]data.DocsFilesItemInfo, error) {
			return sampleDocsFiles(), nil
		},
	)
	pool.Set(sampleDocsFiles())

	list := widget.NewList(styles)
	list.SetEmptyText("No documents or files found.")
	list.SetFocused(true)
	list.SetSize(80, 24)

	v := &DocsFiles{
		session:        session,
		pool:           pool,
		styles:         styles,
		list:           list,
		currentVaultID: 100,
		currentTitle:   "Docs & Files",
		items:          sampleDocsFiles(),
	}
	v.syncList()
	return v
}

// --- Folder navigation ---

func TestDocsFiles_EnterFolder_PushesStack(t *testing.T) {
	v := testDocsFilesView()

	assert.Equal(t, "Docs & Files", v.Title())
	assert.False(t, v.IsModal())

	// Enter the folder (first item)
	cmd := v.enterFolder(1, "Design Assets")
	require.NotNil(t, cmd)

	assert.Len(t, v.folderStack, 1)
	assert.Equal(t, int64(100), v.folderStack[0].vaultID)
	assert.Equal(t, "Design Assets", v.Title())
	assert.True(t, v.IsModal())
	assert.Equal(t, int64(1), v.currentVaultID)
	// Pool should have been swapped to the new vault's pool
	assert.Contains(t, v.pool.Key(), "docsfiles:42:1")
}

func TestDocsFiles_EscPopsStack(t *testing.T) {
	v := testDocsFilesView()

	// Enter folder
	v.enterFolder(1, "Design Assets")
	require.Len(t, v.folderStack, 1)

	// Go back
	cmd := v.goBackFolder()
	require.NotNil(t, cmd)

	assert.Empty(t, v.folderStack)
	assert.Equal(t, "Docs & Files", v.Title())
	assert.False(t, v.IsModal())
}

func TestDocsFiles_DoubleBackAtRoot_DoesNothing(t *testing.T) {
	v := testDocsFilesView()

	cmd := v.goBackFolder()
	assert.Nil(t, cmd, "goBackFolder at root should return nil")
	assert.Equal(t, "Docs & Files", v.Title())
}

func TestDocsFiles_EscKey_InSubfolder_PopsFolder(t *testing.T) {
	v := testDocsFilesView()

	// Enter folder
	v.enterFolder(1, "Design Assets")
	require.True(t, v.IsModal())

	// Esc should pop back
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyEscape})
	require.NotNil(t, cmd)
	assert.False(t, v.IsModal())
	assert.Equal(t, "Docs & Files", v.Title())
}

func TestDocsFiles_BackspaceKey_InSubfolder_PopsFolder(t *testing.T) {
	v := testDocsFilesView()
	v.enterFolder(1, "Design Assets")

	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	require.NotNil(t, cmd)
	assert.False(t, v.IsModal())
}

func TestDocsFiles_Filter_WorksInSubfolder(t *testing.T) {
	v := testDocsFilesView()
	v.enterFolder(1, "Design Assets")

	// Start filter
	v.list.StartFilter()
	assert.True(t, v.list.Filtering())
	assert.True(t, v.InputActive())
}

func TestDocsFiles_BackspaceDuringFilter_EditsFilterNotPops(t *testing.T) {
	v := testDocsFilesView()
	v.enterFolder(1, "Design Assets")
	require.True(t, v.IsModal())

	// Start filter and type something
	v.list.StartFilter()
	v.handleKey(runeKey('a'))

	// Backspace should edit the filter, not pop the folder
	v.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	assert.True(t, v.IsModal(), "backspace during filter should not pop folder")
	// Filter is still active (either filtering or stopped after clearing)
}

// --- Create ---

func TestDocsFiles_N_EntersCreateDoc(t *testing.T) {
	v := testDocsFilesView()

	cmd := v.handleKey(runeKey('n'))
	require.NotNil(t, cmd, "n should return blink cmd")
	assert.True(t, v.creatingDoc)
	assert.False(t, v.creatingFolder)
	assert.True(t, v.InputActive())
}

func TestDocsFiles_ShiftN_EntersCreateFolder(t *testing.T) {
	v := testDocsFilesView()

	cmd := v.handleKey(runeKey('N'))
	require.NotNil(t, cmd, "N should return blink cmd")
	assert.True(t, v.creatingFolder)
	assert.False(t, v.creatingDoc)
	assert.True(t, v.InputActive())
}

func TestDocsFiles_CreateDoc_EscCancels(t *testing.T) {
	v := testDocsFilesView()
	v.creatingDoc = true

	cmd := v.handleCreateKey(tea.KeyMsg{Type: tea.KeyEscape})
	assert.Nil(t, cmd)
	assert.False(t, v.creatingDoc)
}

func TestDocsFiles_CreateFolder_EscCancels(t *testing.T) {
	v := testDocsFilesView()
	v.creatingFolder = true

	cmd := v.handleCreateKey(tea.KeyMsg{Type: tea.KeyEscape})
	assert.Nil(t, cmd)
	assert.False(t, v.creatingFolder)
}

func TestDocsFiles_CreateDoc_EmptyEnterExits(t *testing.T) {
	v := testDocsFilesView()
	v.creatingDoc = true
	v.createInput = newTextInputWithValue("")

	cmd := v.handleCreateKey(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Nil(t, cmd)
	assert.False(t, v.creatingDoc)
}

func TestDocsFiles_CreateDoc_EnterDispatches(t *testing.T) {
	v := testDocsFilesView()
	v.creatingDoc = true
	v.createInput = newTextInputWithValue("New Doc")

	cmd := v.handleCreateKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd, "enter with content should return cmd")
	assert.False(t, v.creatingDoc)

	msg := cmd()
	result, ok := msg.(docCreatedMsg)
	require.True(t, ok, "cmd should produce docCreatedMsg")
	assert.Equal(t, int64(100), result.vaultID)
	// Error expected since test session has nil SDK
	assert.Error(t, result.err)
}

func TestDocsFiles_CreateFolder_EnterDispatches(t *testing.T) {
	v := testDocsFilesView()
	v.creatingFolder = true
	v.createInput = newTextInputWithValue("New Folder")

	cmd := v.handleCreateKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd, "enter with content should return cmd")
	assert.False(t, v.creatingFolder)

	msg := cmd()
	result, ok := msg.(folderCreatedMsg)
	require.True(t, ok, "cmd should produce folderCreatedMsg")
	assert.Equal(t, int64(100), result.vaultID)
	assert.Error(t, result.err)
}

func TestDocsFiles_DocCreated_Success_RefreshesCurrentVault(t *testing.T) {
	v := testDocsFilesView()

	_, cmd := v.Update(docCreatedMsg{vaultID: 100, err: nil})
	require.NotNil(t, cmd, "success should return batch cmd")
}

func TestDocsFiles_DocCreated_StaleVault_NoLoading(t *testing.T) {
	v := testDocsFilesView()
	v.loading = false

	_, cmd := v.Update(docCreatedMsg{vaultID: 999, err: nil})
	require.NotNil(t, cmd, "should still return status cmd")
	assert.False(t, v.loading, "should not set loading for a different vault")
}

func TestDocsFiles_FolderCreated_Success_RefreshesCurrentVault(t *testing.T) {
	v := testDocsFilesView()

	_, cmd := v.Update(folderCreatedMsg{vaultID: 100, err: nil})
	require.NotNil(t, cmd, "success should return batch cmd")
}

func TestDocsFiles_FolderCreated_StaleVault(t *testing.T) {
	v := testDocsFilesView()
	v.loading = false

	_, cmd := v.Update(folderCreatedMsg{vaultID: 999, err: nil})
	require.NotNil(t, cmd)
	assert.False(t, v.loading)
}

// --- Trash ---

func TestDocsFiles_Trash_DoublePress(t *testing.T) {
	v := testDocsFilesView()

	// First press arms trash
	cmd := v.trashSelected()
	require.NotNil(t, cmd)
	assert.True(t, v.trashPending)
	assert.Equal(t, "1", v.trashPendingID)

	// Second press fires
	cmd = v.trashSelected()
	require.NotNil(t, cmd)
	assert.False(t, v.trashPending)

	msg := cmd()
	result, ok := msg.(docsFilesTrashResultMsg)
	require.True(t, ok)
	assert.Equal(t, int64(100), result.vaultID)
	assert.Equal(t, "1", result.itemID)
}

func TestDocsFiles_Trash_OtherKeyResets(t *testing.T) {
	v := testDocsFilesView()
	v.trashPending = true
	v.trashPendingID = "1"

	// A non-t key should reset trash state
	v.handleKey(runeKey('j'))
	assert.False(t, v.trashPending)
	assert.Empty(t, v.trashPendingID)
}

func TestDocsFiles_Trash_Timeout(t *testing.T) {
	v := testDocsFilesView()
	v.trashPending = true
	v.trashPendingID = "1"

	v.Update(docsFilesTrashTimeoutMsg{})
	assert.False(t, v.trashPending)
	assert.Empty(t, v.trashPendingID)
}

func TestDocsFiles_TrashResult_Success(t *testing.T) {
	v := testDocsFilesView()

	_, cmd := v.Update(docsFilesTrashResultMsg{vaultID: 100, itemID: "1", err: nil})
	require.NotNil(t, cmd)
}

func TestDocsFiles_TrashResult_StaleVault(t *testing.T) {
	v := testDocsFilesView()
	v.loading = false

	_, cmd := v.Update(docsFilesTrashResultMsg{vaultID: 999, itemID: "1", err: nil})
	require.NotNil(t, cmd)
	assert.False(t, v.loading, "should not set loading for a different vault")
}

// --- Folder count rendering ---

func TestDocsFiles_SyncList_FolderCounts(t *testing.T) {
	v := testDocsFilesView()

	items := v.list.Items()
	require.NotEmpty(t, items)

	// First item is the folder with counts
	desc := items[0].Description
	assert.Contains(t, desc, "2 folders")
	assert.Contains(t, desc, "3 docs")
	assert.Contains(t, desc, "1 uploads")
}

// --- ShortHelp ---

func TestDocsFiles_ShortHelp_IncludesCreateAndTrash(t *testing.T) {
	v := testDocsFilesView()
	hints := v.ShortHelp()

	keys := make(map[string]string)
	for _, h := range hints {
		keys[h.Help().Key] = h.Help().Desc
	}
	assert.Equal(t, "new doc", keys["n"])
	assert.Equal(t, "new folder", keys["N"])
	assert.Equal(t, "trash", keys["t"])
}

func TestDocsFiles_ShortHelp_DuringFilter(t *testing.T) {
	v := testDocsFilesView()
	v.list.StartFilter()

	hints := v.ShortHelp()
	keys := make(map[string]string)
	for _, h := range hints {
		keys[h.Help().Key] = h.Help().Desc
	}
	assert.Contains(t, keys, "esc")
	assert.NotContains(t, keys, "n")
}

func TestDocsFiles_ShortHelp_DuringCreate(t *testing.T) {
	v := testDocsFilesView()
	v.creatingDoc = true

	hints := v.ShortHelp()
	keys := make(map[string]string)
	for _, h := range hints {
		keys[h.Help().Key] = h.Help().Desc
	}
	assert.Contains(t, keys, "enter")
	assert.Contains(t, keys, "esc")
}

// --- Title ---

func TestDocsFiles_Title(t *testing.T) {
	v := testDocsFilesView()
	assert.Equal(t, "Docs & Files", v.Title())
}
