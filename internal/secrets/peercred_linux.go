//go:build linux

package secrets

import (
	"fmt"
	"net"
	"syscall"
)

// peerCred returns the uid and pid of the process on the other end of
// conn, as the kernel recorded them at connect time.
func peerCred(conn net.Conn) (uid, pid int, err error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, 0, fmt.Errorf("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, 0, err
	}
	if credErr != nil {
		return 0, 0, credErr
	}
	return int(cred.Uid), int(cred.Pid), nil
}
