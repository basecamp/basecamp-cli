package connector

import (
	"errors"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// peerCredentials asks the kernel who is at the other end: SO_PEERCRED.
func peerCredentials(conn *net.UnixConn) (PeerCredentials, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return PeerCredentials{}, err
	}
	var (
		cred   *unix.Ucred
		credOK error
	)
	if err := raw.Control(func(fd uintptr) {
		socket, ok := socketDescriptor(fd)
		if !ok {
			credOK = errUnreadableDescriptor
			return
		}
		cred, credOK = unix.GetsockoptUcred(socket, unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return PeerCredentials{}, err
	}
	if credOK != nil {
		return PeerCredentials{}, credOK
	}
	return PeerCredentials{PID: int(cred.Pid), UID: int(cred.Uid)}, nil
}

func processGroupOf(pid int) (int, error) { return unix.Getpgid(pid) }

// parentProcessOf reads a process's parent from /proc/<pid>/stat.
func parentProcessOf(pid int) (int, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return 0, errors.New("connector: unreadable /proc stat")
	}
	fields := strings.Fields(string(raw)[end+1:])
	if len(fields) < 2 {
		return 0, errors.New("connector: short /proc stat")
	}
	return strconv.Atoi(fields[1])
}
