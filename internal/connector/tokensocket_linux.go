package connector

import (
	"net"

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
		cred, credOK = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return PeerCredentials{}, err
	}
	if credOK != nil {
		return PeerCredentials{}, credOK
	}
	return PeerCredentials{PID: int(cred.Pid), UID: int(cred.Uid)}, nil
}

func processGroupOf(pid int) (int, error) { return unix.Getpgid(pid) }
