//go:build linux

package identity

import (
	"golang.org/x/sys/unix"
	"net"
)

func peerGroup(conn net.Conn) *int {
	c, ok := conn.(*net.UnixConn)
	if !ok {
		return nil
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return nil
	}
	var gid *int
	raw.Control(func(fd uintptr) {
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
			value := int(cred.Gid)
			gid = &value
		}
	})
	return gid
}
