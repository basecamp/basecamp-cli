//go:build linux

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
	"github.com/basecamp/basecamp-cli/internal/sysfd"
)

// firstTokenFD is the lowest descriptor a task token may arrive on: below it
// are stdin and stdout, which are the MCP wire, and stderr, which is the log.
const firstTokenFD = 3

// readTaskToken reads the task token from an inherited descriptor and closes
// it. The connector hands the token over as the read end of a pipe, so it never
// exists at a path, in argv or in the environment; once read, the descriptor
// is gone too, and nothing this process starts can inherit it.
//
// This file is built for Linux alone, and shares that constraint with
// internal/cli's inherited_fds_linux.go on purpose: a credential may arrive
// on an inherited descriptor only where startup has already sealed every
// inherited descriptor against the children the pre-command hooks start.
// Everywhere else mcp_token_other.go refuses the handover, and
// TestTheTokenIsOnlyReadWhereItIsSealed holds the two constraints together.
//
// Descriptors below sysfd.FirstNonStandard are refused: stdin and stdout are
// the MCP wire, and stderr is the log. Only a pipe or a socket is taken, and
// anything else is left exactly as it was — not read, not closed: a regular
// file would be the token at a path, and a wrong number could name a
// descriptor this process already uses.
//
// The read ends at the first newline or at end of file, and is bounded in
// size and in time, so a write end left open somewhere cannot hang startup. A
// sender writes "token\n", or closes its end after the token.
func readTaskToken(descriptor sysfd.Descriptor) (string, error) {
	fd := descriptor.Int()
	if fd < firstTokenFD {
		return "", output.ErrUsage(fmt.Sprintf("--connect-token-fd %d is standard I/O; the token descriptor must be %d or above", fd, firstTokenFD))
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return "", output.ErrUsage(fmt.Sprintf("could not read the task token from descriptor %d: it is not open", fd))
	}
	if kind := st.Mode & unix.S_IFMT; kind != unix.S_IFIFO && kind != unix.S_IFSOCK {
		return "", output.ErrUsage(fmt.Sprintf("descriptor %d is not a pipe or a socket; the task token is handed over on one, never from a file", fd))
	}
	// Non-blocking before it is wrapped, which is the order os.NewFile needs
	// to hand back a pollable file, and a read deadline only applies to one.
	// The flag is on the open file description, so anything else sharing it
	// would see it too; the connector's bridge execs this server, so nothing
	// does. From the moment the mode is changed the descriptor is ours, so
	// this path closes it rather than leaving it open through the hooks that
	// follow, where a child could inherit it.
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return "", output.ErrUsage(fmt.Sprintf("could not read the task token from descriptor %d: %v", fd, err))
	}
	file := os.NewFile(descriptor.Uintptr(), "connect-token")
	defer file.Close()
	if err := file.SetReadDeadline(time.Now().Add(taskTokenReadTimeout)); err != nil {
		return "", output.ErrUsage(fmt.Sprintf("could not read the task token from descriptor %d: %v", fd, err))
	}

	var data []byte
	buf := make([]byte, 256)
	for len(data) <= maxTaskTokenBytes && bytes.IndexByte(data, '\n') < 0 {
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
