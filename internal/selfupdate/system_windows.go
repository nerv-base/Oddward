// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import (
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func freeSpace(dir string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, nil, nil); err != nil {
		return 0, err
	}
	return free, nil
}

// Restart starts the installation's stub, which picks the newest ready
// version. The caller quits right after.
func (l Layout) Restart() error {
	stub := filepath.Join(l.Root, StubExe)
	if !regular(stub) {
		return os.ErrNotExist
	}
	cmd := exec.Command(stub)
	cmd.Dir = l.Root
	return cmd.Start()
}
