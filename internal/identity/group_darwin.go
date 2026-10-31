//go:build darwin

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
		if cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED); err == nil && cred.Ngroups > 0 {
			value := int(cred.Groups[0])
			gid = &value
		}
	})
	return gid
}
