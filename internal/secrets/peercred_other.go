//go:build !linux

package secrets

import (
	"errors"
	"net"
)

func peerCred(net.Conn) (uid, pid int, err error) {
	return 0, 0, errors.New("secrets broker: peer credentials need linux")
}
