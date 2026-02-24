package widget

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func testStyles() *tui.Styles {
	return tui.NewStyles()
}

func TestNewComposerDefaults(t *testing.T) {
	c := NewComposer(testStyles())
	if c.Mode() != ComposerQuick {
		t.Errorf("default mode = %d, want ComposerQuick", c.Mode())
	}
	if c.HasContent() {
		t.Error("new composer should have no content")
	}
}

func TestComposerWithMode(t *testing.T) {
	c := NewComposer(testStyles(), WithMode(ComposerRich))
	if c.Mode() != ComposerRich {
		t.Errorf("mode = %d, want ComposerRich", c.Mode())
	}
}

func TestComposerSetValue(t *testing.T) {
	c := NewComposer(testStyles())
	c.SetValue("hello")
	if got := c.Value(); got != "hello" {
		t.Errorf("Value() = %q, want %q", got, "hello")
	}
	if !c.HasContent() {
		t.Error("should have content after SetValue")
	}
}

func TestComposerReset(t *testing.T) {
	c := NewComposer(testStyles())
	c.SetValue("hello")
	c.Reset()
	if c.HasContent() {
		t.Error("should have no content after Reset")
	}
	if len(c.Attachments()) != 0 {
		t.Error("should have no attachments after Reset")
	}
}

func TestComposerFocusBlur(t *testing.T) {
	c := NewComposer(testStyles())
	c.Focus()
	if !c.InputActive() {
		t.Error("should be active after Focus")
	}
	c.Blur()
	if c.InputActive() {
		t.Error("should not be active after Blur")
	}
}

func TestComposerAutoExpand(t *testing.T) {
	c := NewComposer(testStyles(), WithAutoExpand(true))
	c.Focus()
	c.SetSize(80, 20)

	if c.Mode() != ComposerQuick {
		t.Fatal("should start in quick mode")
	}

	// Simulate typing '*' which should trigger auto-expand
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'*'}}
	c.Update(msg)

	if c.Mode() != ComposerRich {
		t.Errorf("should have expanded to rich mode, got %d", c.Mode())
	}
}

func TestComposerNoAutoExpand(t *testing.T) {
	c := NewComposer(testStyles(), WithAutoExpand(false))
	c.Focus()
	c.SetSize(80, 20)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'*'}}
	c.Update(msg)

	if c.Mode() != ComposerQuick {
		t.Error("should stay in quick mode when autoExpand is false")
	}
}

func TestComposerAddAttachment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello"), 0o644)

	uploaded := false
	upload := func(ctx context.Context, p, fn, ct string) (string, error) {
		uploaded = true
		return "sgid-123", nil
	}

	c := NewComposer(testStyles(), WithUploadFn(upload))
	cmd := c.AddAttachment(path)

	if len(c.Attachments()) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(c.Attachments()))
	}
	if c.Attachments()[0].Filename != "test.txt" {
		t.Errorf("filename = %q, want test.txt", c.Attachments()[0].Filename)
	}
	if c.Attachments()[0].Status != AttachUploading {
		t.Errorf("status = %d, want AttachUploading", c.Attachments()[0].Status)
	}

	// Should have auto-expanded to rich mode
	if c.Mode() != ComposerRich {
		t.Error("should expand to rich mode on attachment")
	}

	// Execute the upload command
	if cmd == nil {
		t.Fatal("expected upload command")
	}
	msg := cmd()
	uploadMsg, ok := msg.(attachUploadedMsg)
	if !ok {
		t.Fatalf("expected attachUploadedMsg, got %T", msg)
	}
	if !uploaded {
		t.Error("upload function should have been called")
	}

	// Process the result
	c.Update(uploadMsg)
	if c.Attachments()[0].Status != AttachUploaded {
		t.Errorf("status after upload = %d, want AttachUploaded", c.Attachments()[0].Status)
	}
	if c.Attachments()[0].SGID != "sgid-123" {
		t.Errorf("SGID = %q, want sgid-123", c.Attachments()[0].SGID)
	}
}

func TestComposerAddAttachmentInvalid(t *testing.T) {
	c := NewComposer(testStyles())
	cmd := c.AddAttachment("/nonexistent/file.txt")

	if cmd == nil {
		t.Fatal("expected error command for invalid file")
	}
	msg := cmd()
	submitMsg, ok := msg.(ComposerSubmitMsg)
	if !ok {
		t.Fatalf("expected ComposerSubmitMsg, got %T", msg)
	}
	if submitMsg.Err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestComposerSubmitPlain(t *testing.T) {
	c := NewComposer(testStyles())
	c.SetValue("hello world")
	cmd := c.Submit()
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	msg := cmd()
	submitMsg, ok := msg.(ComposerSubmitMsg)
	if !ok {
		t.Fatalf("expected ComposerSubmitMsg, got %T", msg)
	}
	if submitMsg.Err != nil {
		t.Fatalf("unexpected error: %v", submitMsg.Err)
	}
	if !submitMsg.Content.IsPlain {
		t.Error("should be plain text")
	}
	if submitMsg.Content.Markdown != "hello world" {
		t.Errorf("markdown = %q, want %q", submitMsg.Content.Markdown, "hello world")
	}
}

func TestComposerSubmitRich(t *testing.T) {
	c := NewComposer(testStyles(), WithMode(ComposerRich))
	c.SetValue("**bold** text")
	cmd := c.Submit()
	msg := cmd()
	submitMsg := msg.(ComposerSubmitMsg)
	if submitMsg.Content.IsPlain {
		t.Error("should not be plain text with markdown formatting")
	}
}

func TestComposerSubmitEmpty(t *testing.T) {
	c := NewComposer(testStyles())
	cmd := c.Submit()
	if cmd != nil {
		t.Error("should not submit empty content")
	}
}

func TestComposerProcessPaste(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.pdf")
	os.WriteFile(filePath, []byte("%PDF"), 0o644)

	c := NewComposer(testStyles())

	// Paste mixed text and file path
	text, cmd := c.ProcessPaste("hello\n" + filePath + "\nworld")
	if text != "hello\nworld" {
		t.Errorf("remaining text = %q, want %q", text, "hello\nworld")
	}
	if len(c.Attachments()) != 1 {
		t.Errorf("expected 1 attachment, got %d", len(c.Attachments()))
	}
	_ = cmd // upload command (nil without upload fn)
}

func TestComposerProcessPasteNoFiles(t *testing.T) {
	c := NewComposer(testStyles())

	text, cmd := c.ProcessPaste("just some text\nwith newlines")
	if text != "just some text\nwith newlines" {
		t.Errorf("text = %q, want all text preserved", text)
	}
	if cmd != nil {
		t.Error("should not have upload command for plain text paste")
	}
}

func TestComposerView(t *testing.T) {
	c := NewComposer(testStyles())
	c.SetSize(80, 20)

	view := c.View()
	if view == "" {
		t.Error("view should not be empty with non-zero size")
	}

	// Zero size should return empty
	c.SetSize(0, 0)
	if got := c.View(); got != "" {
		t.Errorf("view with zero size should be empty, got %q", got)
	}
}

func TestComposerHandleEditorReturn(t *testing.T) {
	c := NewComposer(testStyles())
	c.SetSize(80, 20)

	// Single line — stays in quick mode
	c.HandleEditorReturn(EditorReturnMsg{Content: "hello"})
	if c.Value() != "hello" {
		t.Errorf("value after editor return = %q, want hello", c.Value())
	}
	if c.Mode() != ComposerQuick {
		t.Error("should stay in quick mode for single-line non-markdown content")
	}

	// Multi-line — expands to rich
	c.Reset()
	c.HandleEditorReturn(EditorReturnMsg{Content: "line1\nline2"})
	if c.Mode() != ComposerRich {
		t.Error("should expand to rich mode for multi-line content")
	}
}

func TestShouldExpand(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{'*', true},
		{'#', true},
		{'`', true},
		{'>', true},
		{'~', true},
		{'a', false},
		{'1', false},
		{' ', false},
	}

	for _, tt := range tests {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tt.r}}
		if got := shouldExpand(msg); got != tt.want {
			t.Errorf("shouldExpand(%c) = %v, want %v", tt.r, got, tt.want)
		}
	}
}
