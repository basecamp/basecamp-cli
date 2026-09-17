//go:build !linux && !darwin

package connector

import (
	"errors"
	"net"
)

var errNoPeerCredentials = errors.New("connector: this platform cannot say who is at the other end of a socket, so no token is handed over")

// peerCredentials cannot answer here, and a token is never handed to a peer
// nobody could identify.
func peerCredentials(*net.UnixConn) (PeerCredentials, error) {
	return PeerCredentials{}, errNoPeerCredentials
}

func processGroupOf(int) (int, error) { return 0, errNoPeerCredentials }
