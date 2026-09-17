//go:build unix

package commands

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// readTaskToken reads the task token from an inherited descriptor and closes
// it. The connector hands the token over as the read end of a pipe, so it never
// exists at a path, in argv or in the environment; once read, the descriptor
// is gone too, and nothing this process starts can inherit it.
//
// Descriptors 0 to 2 are refused: stdin and stdout are the MCP wire and stderr
// is the log. Only a pipe or a socket is taken, and anything else is left
// exactly as it was — not read, not closed: a regular file would be the token
// at a path, and a wrong number could name a descriptor this process already
// uses.
//
// The read ends at the first newline or at end of file, and is bounded in
// size and in time, so a write end left open somewhere cannot hang startup.
func readTaskToken(fd int) (string, error) {
	switch {
	case fd < 0:
		return "", output.ErrUsage("--connect-state needs the task token on an inherited descriptor: pass --connect-token-fd")
	case fd < 3:
		return "", output.ErrUsage(fmt.Sprintf("--connect-token-fd %d is standard I/O; the token descriptor must be 3 or above", fd))
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return "", output.ErrUsage(fmt.Sprintf("could not read the task token from descriptor %d: it is not open", fd))
	}
	if kind := st.Mode & unix.S_IFMT; kind != unix.S_IFIFO && kind != unix.S_IFSOCK {
		return "", output.ErrUsage(fmt.Sprintf("descriptor %d is not a pipe or a socket; the task token is handed over on one, never from a file", fd))
	}
	// Non-blocking before it is wrapped, so the runtime polls it and a read
	// deadline applies.
	if err := unix.SetNonblock(fd, true); err != nil {
		return "", output.ErrUsage(fmt.Sprintf("could not read the task token from descriptor %d: %v", fd, err))
	}
	file := os.NewFile(uintptr(fd), "connect-token")
	defer file.Close()
	if err := file.SetReadDeadline(time.Now().Add(taskTokenReadTimeout)); err != nil {
		return "", output.ErrUsage(fmt.Sprintf("could not read the task token from descriptor %d: %v", fd, err))
	}

	var data []byte
	buf := make([]byte, 256)
	for len(data) <= maxTaskTokenBytes && !bytes.Contains(data, []byte("\n")) {
		n, err := file.Read(buf)
		data = append(data, buf[:n]...)
		if err == nil {
			continue
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return "", output.ErrUsage(fmt.Sprintf("no task token arrived on descriptor %d within %s", fd, taskTokenReadTimeout))
		}
		if errors.Is(err, io.EOF) {
			break
		}
		return "", output.ErrUsage(fmt.Sprintf("could not read the task token from descriptor %d: %v", fd, err))
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		data = data[:i]
	}
	if len(data) > maxTaskTokenBytes {
		return "", output.ErrUsage(fmt.Sprintf("descriptor %d carries more than %d bytes; that is not a task token", fd, maxTaskTokenBytes))
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", output.ErrUsage(fmt.Sprintf("descriptor %d carried an empty task token", fd))
	}
	return token, nil
}
