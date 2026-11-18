//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func verifyFileID(f *os.File) (string, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		if errors.Is(err, windows.ERROR_NOT_SUPPORTED) || errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
			return "unknown", nil
		}
		return "", err
	}
	return fmt.Sprintf("%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}
