// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package gamemedia

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

func validPathSyntax(path string) error {
	path = filepath.FromSlash(path)
	if len(path) < 3 || len(path) > 32768 || !utf8.ValidString(path) || path[1] != ':' || path[2] != '\\' || !((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) {
		return errors.New("gamemedia: expected an ordinary absolute local Windows path")
	}
	for _, part := range strings.Split(path[3:], `\`) {
		if strings.ContainsAny(part, ":*?<>|\"") || strings.IndexFunc(part, unicode.IsControl) >= 0 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return errors.New("gamemedia: unsupported path component")
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		letters := []rune(base)
		portDevice := len(letters) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && strings.ContainsRune("123456789¹²³", letters[3])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" || portDevice {
			return errors.New("gamemedia: reserved Windows device component")
		}
	}
	return nil
}

// Like launchsettings, this accepts missing app-owned components and checks
// every existing ancestor before creating directories. It also checks directory
// handles for reparse attributes instead of relying only on os.FileMode.
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
		return errors.New("gamemedia: cache requires a local fixed or removable volume")
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
			return errors.New("gamemedia: linked or irregular cache path is unsupported")
		}
		wantDirectory := i != len(parts)-1 || directory
		if wantDirectory && !info.IsDir() {
			return errors.New("gamemedia: cache ancestors must be directories")
		}
		if !wantDirectory && !info.Mode().IsRegular() {
			return errors.New("gamemedia: cache target must be a regular file")
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
		if attributes.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (!wantDirectory && (attributes.NumberOfLinks != 1 || attributes.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0)) {
			return errors.New("gamemedia: reparse or multiple-link cache target is unsupported")
		}
	}
	return ctx.Err()
}

// Both names are staged on the same volume; never delete the old name first.
func replaceFile(stage, destination string) error {
	from, err := windows.UTF16PtrFromString(stage)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
