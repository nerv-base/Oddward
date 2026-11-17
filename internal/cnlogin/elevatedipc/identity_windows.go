// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package elevatedipc

import (
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type identity struct {
	pid        uint32
	creation   uint64
	image, sid string
	session    uint32
	elevated   bool
}

func identify(h windows.Handle) (identity, error) {
	var id identity
	var err error
	id.pid, err = windows.GetProcessId(h)
	if err != nil || id.pid == 0 {
		return id, errIdentity
	}
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &creation, &exit, &kernel, &user) != nil {
		return id, errIdentity
	}
	id.creation = uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)
	if id.creation == 0 {
		return id, errIdentity
	}
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &size) != nil {
		return id, errIdentity
	}
	id.image = windows.UTF16ToString(buf[:size])
	var token windows.Token
	if windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token) != nil {
		return id, errIdentity
	}
	defer token.Close()
	u, err := token.GetTokenUser()
	if err != nil {
		return id, errIdentity
	}
	id.sid = u.User.Sid.String()
	var n uint32
	if windows.GetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&id.session)), 4, &n) != nil || n != 4 {
		return id, errIdentity
	}
	id.elevated = token.IsElevated()
	return id, nil
}

func samePrincipal(a, b identity) bool {
	return a.sid != "" && a.sid == b.sid && a.session == b.session &&
		a.image != "" && strings.EqualFold(filepath.Clean(a.image), filepath.Clean(b.image))
}

// The process handle remains open across the comparison, preventing PID reuse.
func verifyPeer(c *conn, expected, local identity, server bool) error {
	pid, err := c.peerPID(server)
	if err != nil || pid != expected.pid {
		return errIdentity
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return errIdentity
	}
	defer windows.CloseHandle(h)
	actual, err := identify(h)
	if err != nil || actual.creation != expected.creation || !samePrincipal(actual, local) {
		return errIdentity
	}
	return nil
}
