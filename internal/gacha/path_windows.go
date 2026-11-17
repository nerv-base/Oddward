// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package gacha

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Missing components are allowed for app-owned parents. Existing ancestors are
// checked before creation. Reparse points and linked files are never followed.
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
		return errors.New("gacha requires a local fixed or removable volume")
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
			return errors.New("linked or irregular gacha path is unsupported")
		}
		wantDirectory := i != len(parts)-1 || directory
		if wantDirectory && !info.IsDir() {
			return errors.New("gacha ancestors must be directories")
		}
		if !wantDirectory && !info.Mode().IsRegular() {
			return errors.New("gacha target must be a regular file")
		}
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
		if wantDirectory {
			flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
		}
		handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, flags, 0)
		if err != nil {
			return err
		}
		var attributes windows.ByHandleFileInformation
		infoErr := windows.GetFileInformationByHandle(handle, &attributes)
		closeErr := windows.CloseHandle(handle)
		if err := errors.Join(infoErr, closeErr); err != nil {
			return err
		}
		if attributes.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || !wantDirectory && attributes.NumberOfLinks != 1 {
			return errors.New("gacha path must not contain reparse points or multi-link files")
		}
	}
	return ctx.Err()
}

// The stage and destination share a directory. There is no remove-first fallback.
func replaceFile(stage, destination string) error { return moveFile(stage, destination, true) }

func moveFile(from, to string, replace bool) error {
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(source, destination, flags)
}
