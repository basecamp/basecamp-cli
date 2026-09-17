package connector

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerCredentials asks the kernel who is at the other end: LOCAL_PEERCRED for
// the user, LOCAL_PEERPID for the process.
func peerCredentials(conn *net.UnixConn) (PeerCredentials, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return PeerCredentials{}, err
	}
	var (
		cred   *unix.Xucred
		pid    int
		credOK error
		pidOK  error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, credOK = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		pid, pidOK = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return PeerCredentials{}, err
	}
	if credOK != nil {
		return PeerCredentials{}, credOK
	}
	if pidOK != nil {
		return PeerCredentials{}, pidOK
	}
	return PeerCredentials{PID: pid, UID: int(cred.Uid)}, nil
}

func processGroupOf(pid int) (int, error) { return unix.Getpgid(pid) }
