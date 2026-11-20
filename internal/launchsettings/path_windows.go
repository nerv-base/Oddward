// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package launchsettings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Missing components are allowed for retained choices and app-owned parents.
// Every existing ancestor is checked before any directory creation. Like the
// project's other local-state guards, this is not a hostile concurrent-swap jail.
func checkLocalPath(ctx context.Context, path string, directory bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root := filepath.VolumeName(path) + `\`
	wide, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return err
	}
	kind := windows.GetDriveType(wide)
	if kind != windows.DRIVE_FIXED && kind != windows.DRIVE_REMOVABLE {
		return errors.New("launch settings require a local fixed or removable volume")
	}
	current := root
	parts := strings.Split(path[len(root):], `\`)
	for i, part := range parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return errors.New("linked or irregular launch settings path is unsupported")
		}
		if i != len(parts)-1 || directory {
			if !info.IsDir() {
				return errors.New("launch settings ancestors and game root must be directories")
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return errors.New("launch settings target must be a regular file")
		}
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			return err
		}
		var attributes windows.ByHandleFileInformation
		infoErr := windows.GetFileInformationByHandle(handle, &attributes)
		closeErr := windows.CloseHandle(handle)
		if err := errors.Join(infoErr, closeErr); err != nil {
			return err
		}
		if attributes.NumberOfLinks != 1 || attributes.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
			return errors.New("launch settings target must be a regular single-link file")
		}
	}
	return ctx.Err()
}
