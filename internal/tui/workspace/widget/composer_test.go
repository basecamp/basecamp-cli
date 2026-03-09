package widget

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

func TestComposerResetReturnsToQuickMode(t *testing.T) {
	c := NewComposer(testStyles(), WithMode(ComposerRich))
	c.SetValue("some text")
	c.Reset()
	if c.Mode() != ComposerQuick {
		t.Errorf("mode after Reset = %d, want ComposerQuick", c.Mode())
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
	msg := tea.KeyPressMsg{Code: '*', Text: "*"}
	c.Update(msg)

	if c.Mode() != ComposerRich {
		t.Errorf("should have expanded to rich mode, got %d", c.Mode())
	}
}

func TestComposerNoAutoExpand(t *testing.T) {
	c := NewComposer(testStyles(), WithAutoExpand(false))
	c.Focus()
	c.SetSize(80, 20)

	msg := tea.KeyPressMsg{Code: '*', Text: "*"}
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

func TestComposerSubmitInterceptsFilePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.png")
	os.WriteFile(path, []byte("PNG"), 0o644)

	uploaded := false
	upload := func(ctx context.Context, p, fn, ct string) (string, error) {
		uploaded = true
		return "sgid-456", nil
	}

	c := NewComposer(testStyles(), WithUploadFn(upload))
	// Simulate dragged path arriving as typed text (no bracketed paste)
	c.SetValue("'" + path + "'")

	cmd := c.Submit()
	if cmd == nil {
		t.Fatal("expected submit command")
	}

	// Value should be cleared (path was intercepted, not sent as text)
	if c.Value() != "" {
		t.Errorf("value should be empty after file interception, got %q", c.Value())
	}

	// Execute the command — uploads and submits in one step
	msg := cmd()
	submitMsg, ok := msg.(ComposerSubmitMsg)
	if !ok {
		t.Fatalf("expected ComposerSubmitMsg, got %T", msg)
	}
	if submitMsg.Err != nil {
		t.Fatalf("unexpected error: %v", submitMsg.Err)
	}
	if !uploaded {
		t.Error("upload function should have been called")
	}
	if len(submitMsg.Content.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(submitMsg.Content.Attachments))
	}
	if submitMsg.Content.Attachments[0].Filename != "photo.png" {
		t.Errorf("filename = %q, want photo.png", submitMsg.Content.Attachments[0].Filename)
	}
	if submitMsg.Content.Attachments[0].SGID != "sgid-456" {
		t.Errorf("SGID = %q, want sgid-456", submitMsg.Content.Attachments[0].SGID)
	}
}

func TestComposerSubmitInterceptsMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.png")
	path2 := filepath.Join(dir, "b.pdf")
	os.WriteFile(path1, []byte("PNG"), 0o644)
	os.WriteFile(path2, []byte("%PDF"), 0o644)

	c := NewComposer(testStyles())
	// Terminal.app multi-file drag format: 'path1' 'path2'
	c.SetValue("'" + path1 + "' '" + path2 + "'")

	cmd := c.Submit()
	if cmd == nil {
		t.Fatal("expected submit command")
	}

	if c.Value() != "" {
		t.Errorf("value should be empty, got %q", c.Value())
	}

	msg := cmd()
	submitMsg := msg.(ComposerSubmitMsg)
	if len(submitMsg.Content.Attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(submitMsg.Content.Attachments))
	}
	if submitMsg.Content.Attachments[0].Filename != "a.png" {
		t.Errorf("attachment[0] = %q, want a.png", submitMsg.Content.Attachments[0].Filename)
	}
	if submitMsg.Content.Attachments[1].Filename != "b.pdf" {
		t.Errorf("attachment[1] = %q, want b.pdf", submitMsg.Content.Attachments[1].Filename)
	}
}

func TestComposerSubmitDoesNotInterceptPlainText(t *testing.T) {
	c := NewComposer(testStyles())
	c.SetValue("just a message")

	cmd := c.Submit()
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	msg := cmd()
	submitMsg, ok := msg.(ComposerSubmitMsg)
	if !ok {
		t.Fatalf("expected ComposerSubmitMsg, got %T", msg)
	}
	if submitMsg.Content.Markdown != "just a message" {
		t.Errorf("markdown = %q, want %q", submitMsg.Content.Markdown, "just a message")
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

func TestComposerProcessPasteShellEscaped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only test")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "my file (1).pdf")
	os.WriteFile(path, []byte("%PDF"), 0o644)

	// Shell-escaped form: spaces and parens escaped with backslashes
	escaped := strings.ReplaceAll(dir, " ", `\ `) + `/my\ file\ \(1\).pdf`

	c := NewComposer(testStyles())
	text, _ := c.ProcessPaste(escaped)
	if text != "" {
		t.Errorf("remaining text = %q, want empty (file should be attached)", text)
	}
	if len(c.Attachments()) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(c.Attachments()))
	}
	if c.Attachments()[0].Filename != "my file (1).pdf" {
		t.Errorf("filename = %q, want %q", c.Attachments()[0].Filename, "my file (1).pdf")
	}
}

func TestComposerProcessPasteQuoted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "my file.pdf")
	os.WriteFile(path, []byte("%PDF"), 0o644)

	quoted := "'" + path + "'"

	c := NewComposer(testStyles())
	text, _ := c.ProcessPaste(quoted)
	if text != "" {
		t.Errorf("remaining text = %q, want empty", text)
	}
	if len(c.Attachments()) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(c.Attachments()))
	}
}

func TestComposerProcessPasteFileURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only test")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "my file.pdf")
	os.WriteFile(path, []byte("%PDF"), 0o644)

	fileURL := "file://" + strings.ReplaceAll(path, " ", "%20")

	c := NewComposer(testStyles())
	text, _ := c.ProcessPaste(fileURL)
	if text != "" {
		t.Errorf("remaining text = %q, want empty", text)
	}
	if len(c.Attachments()) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(c.Attachments()))
	}
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

func TestComposerRichModeEnterSendsWithAttachments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	os.WriteFile(path, []byte("%PDF"), 0o644)

	c := NewComposer(testStyles(), WithMode(ComposerRich))
	c.Focus()
	c.SetSize(80, 20)
	c.AddAttachment(path)

	// With empty text and attachments, Enter should trigger Submit
	cmd := c.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected submit command from Enter with empty text + attachments")
	}
}

func TestComposerRichModeEnterNewlineWithText(t *testing.T) {
	c := NewComposer(testStyles(), WithMode(ComposerRich))
	c.Focus()
	c.SetSize(80, 20)
	c.SetValue("some text")

	// With text, Enter should insert newline (not send)
	cmd := c.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// The command should be from textarea.Update, not Submit
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(ComposerSubmitMsg); ok {
			t.Error("Enter with text in rich mode should NOT submit")
		}
	}
}

func TestAutoDetectAttachesFileOnLastKeystroke(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.png")
	os.WriteFile(path, []byte("PNG"), 0o644)

	c := NewComposer(testStyles())
	c.Focus()
	c.SetSize(80, 20)

	// Simulate typing a quoted path (as Terminal.app would emit).
	// The auto-detect fires synchronously on the closing quote keystroke.
	quotedPath := "'" + path + "'"
	for _, r := range quotedPath {
		c.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	// Text should have been cleared and file attached
	if c.Value() != "" {
		t.Errorf("value = %q, want empty (file should be auto-attached)", c.Value())
	}
	if len(c.Attachments()) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(c.Attachments()))
	}
	if c.Attachments()[0].Filename != "test.png" {
		t.Errorf("filename = %q, want test.png", c.Attachments()[0].Filename)
	}
}

func TestAutoDetectAttachesBarePathFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	os.WriteFile(path, []byte("JPEG"), 0o644)

	c := NewComposer(testStyles())
	c.Focus()
	c.SetSize(80, 20)

	// Bare path (no quotes) — starts with '/' trigger
	for _, r := range path {
		c.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	if len(c.Attachments()) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(c.Attachments()))
	}
	if c.Attachments()[0].Filename != "photo.jpg" {
		t.Errorf("filename = %q, want photo.jpg", c.Attachments()[0].Filename)
	}
}

func TestAutoDetectLeavesNonFilesAsText(t *testing.T) {
	c := NewComposer(testStyles())
	c.Focus()
	c.SetSize(80, 20)

	for _, r := range "/not-a-real-path" {
		c.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	// Text should remain — not a file
	if c.Value() != "/not-a-real-path" {
		t.Errorf("value = %q, want /not-a-real-path", c.Value())
	}
	if len(c.Attachments()) != 0 {
		t.Error("should have no attachments for non-file path")
	}
}

func TestAutoDetectIgnoresNonTriggerChars(t *testing.T) {
	c := NewComposer(testStyles())
	c.Focus()
	c.SetSize(80, 20)

	c.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	c.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})

	if c.Value() != "hi" {
		t.Errorf("value = %q, want hi", c.Value())
	}
	if len(c.Attachments()) != 0 {
		t.Error("should not auto-attach for non-trigger text")
	}
}

func TestSubmitReUploadsWithEmptySGID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello"), 0o644)

	uploadCount := 0
	upload := func(ctx context.Context, p, fn, ct string) (string, error) {
		uploadCount++
		return "sgid-reup", nil
	}

	c := NewComposer(testStyles(), WithUploadFn(upload))
	_ = c.AddAttachment(path) // Don't execute async upload cmd

	// Attachment has Status=AttachUploading, SGID=""
	cmd := c.Submit()
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	msg := cmd()
	submitMsg := msg.(ComposerSubmitMsg)
	if submitMsg.Err != nil {
		t.Fatalf("unexpected error: %v", submitMsg.Err)
	}
	if uploadCount != 1 {
		t.Errorf("uploadCount = %d, want 1 (should re-upload in Submit closure)", uploadCount)
	}
	if submitMsg.Content.Attachments[0].SGID != "sgid-reup" {
		t.Errorf("SGID = %q, want sgid-reup", submitMsg.Content.Attachments[0].SGID)
	}
}

func TestSubmitInterceptsFilePathsWithExistingAttachments(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "first.txt")
	dragged := filepath.Join(dir, "second.png")
	os.WriteFile(existing, []byte("hello"), 0o644)
	os.WriteFile(dragged, []byte("PNG"), 0o644)

	var uploadedFiles []string
	upload := func(ctx context.Context, p, fn, ct string) (string, error) {
		uploadedFiles = append(uploadedFiles, fn)
		return "sgid-" + fn, nil
	}

	c := NewComposer(testStyles(), WithUploadFn(upload))
	// Pre-attach one file (simulates a previously auto-detected drag)
	c.AddAttachment(existing)

	// Simulate a second dragged path sitting as text (auto-detect didn't fire yet)
	c.SetValue("'" + dragged + "'")

	cmd := c.Submit()
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	msg := cmd()
	submitMsg := msg.(ComposerSubmitMsg)
	if submitMsg.Err != nil {
		t.Fatalf("unexpected error: %v", submitMsg.Err)
	}
	if len(submitMsg.Content.Attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(submitMsg.Content.Attachments))
	}
	if submitMsg.Content.Attachments[0].Filename != "first.txt" {
		t.Errorf("attachment[0] = %q, want first.txt", submitMsg.Content.Attachments[0].Filename)
	}
	if submitMsg.Content.Attachments[1].Filename != "second.png" {
		t.Errorf("attachment[1] = %q, want second.png", submitMsg.Content.Attachments[1].Filename)
	}
	if submitMsg.Content.Markdown != "" {
		t.Errorf("markdown = %q, want empty (path should be intercepted)", submitMsg.Content.Markdown)
	}
}

func TestSubmitInterceptRejectsInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.txt")
	os.WriteFile(good, []byte("ok"), 0o644)
	missing := filepath.Join(dir, "nope.txt") // does not exist

	c := NewComposer(testStyles(), WithUploadFn(
		func(ctx context.Context, p, fn, ct string) (string, error) {
			return "sgid-" + fn, nil
		},
	))

	// Compose text that looks like two dragged paths, one invalid
	c.SetValue(good + "\n" + missing)

	cmd := c.Submit()
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	msg := cmd().(ComposerSubmitMsg)
	// Invalid file means interception is skipped — text sent as markdown
	if msg.Content.Markdown == "" {
		t.Error("expected text to be sent as markdown when file validation fails")
	}
	if len(msg.Content.Attachments) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(msg.Content.Attachments))
	}
}

func TestCampfireFlowAutoDetectThenSubmit(t *testing.T) {
	// End-to-end test mimicking the campfire drag-and-submit flow:
	// 1. Quick mode composer with upload function (like campfire)
	// 2. Type a quoted path character by character (Terminal.app drag)
	// 3. Auto-detect fires, AddAttachment starts async upload
	// 4. Execute async upload → attachUploadedMsg routes back
	// 5. Hit Enter → Submit
	// 6. Verify ComposerSubmitMsg has attachment with SGID
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.png")
	os.WriteFile(path, []byte("PNG data"), 0o644)

	upload := func(ctx context.Context, p, fn, ct string) (string, error) {
		return "sgid-photo-123", nil
	}

	c := NewComposer(testStyles(), WithUploadFn(upload), WithAutoExpand(true))
	c.Focus()
	c.SetSize(80, 20)

	// Step 1: Type the quoted path character by character
	quotedPath := "'" + path + "'"
	var lastCmd tea.Cmd
	for _, r := range quotedPath {
		lastCmd = c.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	// After the closing quote, auto-detect should have fired
	if c.Value() != "" {
		t.Fatalf("value = %q, want empty (auto-detect should have cleared it)", c.Value())
	}
	if len(c.Attachments()) != 1 {
		t.Fatalf("expected 1 attachment after auto-detect, got %d", len(c.Attachments()))
	}
	if c.Mode() != ComposerRich {
		t.Fatalf("mode = %d, want ComposerRich (AddAttachment expands)", c.Mode())
	}

	// Step 2: Execute the async upload command
	if lastCmd == nil {
		t.Fatal("expected upload command from auto-detect")
	}
	uploadResult := lastCmd()
	uploadMsg, ok := uploadResult.(attachUploadedMsg)
	if !ok {
		t.Fatalf("expected attachUploadedMsg, got %T", uploadResult)
	}

	// Step 3: Route the upload result back through Update
	c.Update(uploadMsg)
	if c.Attachments()[0].Status != AttachUploaded {
		t.Fatalf("status = %d, want AttachUploaded", c.Attachments()[0].Status)
	}
	if c.Attachments()[0].SGID != "sgid-photo-123" {
		t.Fatalf("SGID = %q, want sgid-photo-123", c.Attachments()[0].SGID)
	}

	// Step 4: Hit Enter — in rich mode with empty text + attachments, Enter sends
	submitCmd := c.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if submitCmd == nil {
		t.Fatal("expected submit command from Enter")
	}

	// Step 5: Execute the submit command
	submitResult := submitCmd()
	submitMsg, ok := submitResult.(ComposerSubmitMsg)
	if !ok {
		t.Fatalf("expected ComposerSubmitMsg, got %T", submitResult)
	}
	if submitMsg.Err != nil {
		t.Fatalf("unexpected submit error: %v", submitMsg.Err)
	}
	if len(submitMsg.Content.Attachments) != 1 {
		t.Fatalf("expected 1 attachment in submit, got %d", len(submitMsg.Content.Attachments))
	}
	if submitMsg.Content.Attachments[0].SGID != "sgid-photo-123" {
		t.Errorf("submit SGID = %q, want sgid-photo-123", submitMsg.Content.Attachments[0].SGID)
	}
	if submitMsg.Content.Attachments[0].Status != AttachUploaded {
		t.Errorf("submit status = %d, want AttachUploaded", submitMsg.Content.Attachments[0].Status)
	}
	if submitMsg.Content.Markdown != "" {
		t.Errorf("submit markdown = %q, want empty", submitMsg.Content.Markdown)
	}
}

func TestComposerAttachmentsDisabled(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "photo.png")
	os.WriteFile(f, []byte("png"), 0o644)

	c := NewComposer(testStyles(),
		WithMode(ComposerQuick),
		WithAttachmentsDisabled(),
	)
	c.Focus()

	// AddAttachment is a no-op
	cmd := c.AddAttachment(f)
	if cmd != nil {
		t.Error("AddAttachment should return nil when disabled")
	}
	if len(c.attachments) != 0 {
		t.Errorf("attachments = %d, want 0", len(c.attachments))
	}

	// ProcessPaste returns text as-is, no attachment extraction
	text, cmd := c.ProcessPaste(f)
	if text != f {
		t.Errorf("ProcessPaste text = %q, want %q", text, f)
	}
	if cmd != nil {
		t.Error("ProcessPaste should return nil cmd when disabled")
	}

	// tryAutoAttachIfDrag is a no-op
	c.SetValue(f)
	cmd = c.tryAutoAttachIfDrag()
	if cmd != nil {
		t.Error("tryAutoAttachIfDrag should return nil when disabled")
	}
	if c.Value() != f {
		t.Error("tryAutoAttachIfDrag should not clear text when disabled")
	}

	// Submit does not intercept file paths
	submitCmd := c.Submit()
	if submitCmd == nil {
		t.Fatal("Submit should return a cmd")
	}
	msg := submitCmd().(ComposerSubmitMsg)
	if msg.Content.Markdown != f {
		t.Errorf("Submit markdown = %q, want file path as text", msg.Content.Markdown)
	}
	if len(msg.Content.Attachments) != 0 {
		t.Errorf("Submit attachments = %d, want 0", len(msg.Content.Attachments))
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
		msg := tea.KeyPressMsg{Code: tt.r, Text: string(tt.r)}
		if got := shouldExpand(msg); got != tt.want {
			t.Errorf("shouldExpand(%c) = %v, want %v", tt.r, got, tt.want)
		}
	}
}
