// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// checkDataPath validates that path can only ever resolve to a plain local file.
// The database holds account ciphertext, so following a junction, a symlink or a
// network redirect out of the user's profile is refused rather than repaired.
func checkDataPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("%w: %s is not absolute", ErrUnsafePath, path)
	}
	volume := filepath.VolumeName(clean)
	root := volume + `\`
	if volume == "" {
		return "", fmt.Errorf("%w: %s has no volume", ErrUnsafePath, path)
	}
	// A database directly on a volume root would be created next to the system
	// directory and would make ensureDataDir modify the root's own DACL.
	if strings.EqualFold(clean, root) {
		return "", fmt.Errorf("%w: %s is a volume root", ErrUnsafePath, path)
	}
	if strings.EqualFold(filepath.Dir(clean), root) {
		return "", fmt.Errorf("%w: %s is directly on a volume root", ErrUnsafePath, path)
	}
	wide, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", err
	}
	kind := windows.GetDriveType(wide)
	if kind != windows.DRIVE_FIXED && kind != windows.DRIVE_REMOVABLE {
		return "", fmt.Errorf("%w: %s is not on a local fixed or removable volume", ErrUnsafePath, path)
	}
	current := root
	parts := strings.Split(clean[len(root):], `\`)
	for i, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		last := i == len(parts)-1
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			// The remaining components do not exist yet. ensureDataDir creates
			// them and re-runs the same checks on what it created.
			return clean, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return "", fmt.Errorf("%w: %s is a link or irregular file", ErrUnsafePath, current)
		}
		if err := checkReparseFree(current, !last); err != nil {
			return "", err
		}
		if last && !info.Mode().IsRegular() {
			return "", fmt.Errorf("%w: %s is not a regular file", ErrUnsafePath, current)
		}
	}
	return clean, nil
}

// checkReparseFree opens path without following the final component and rejects
// reparse points. A directory that is really a junction is reported as one.
func checkReparseFree(path string, directory bool) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		attrs |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, attrs, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%w: %s is a reparse point", ErrUnsafePath, path)
	}
	if (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return fmt.Errorf("%w: %s has the wrong type", ErrUnsafePath, path)
	}
	if !directory && info.NumberOfLinks != 1 {
		return fmt.Errorf("%w: %s has hard links", ErrUnsafePath, path)
	}
	return nil
}

// ensureDataDir creates the missing components of dir and gives the directory
// that will hold the database a protected DACL: only the current user and SYSTEM
// may read it, which covers oddward.db, -wal and -shm through inheritance.
func ensureDataDir(dir string, writable bool) error {
	if dir == "" || dir == "." {
		return fmt.Errorf("%w: empty data directory", ErrUnsafePath)
	}
	if filepath.Dir(dir) == dir {
		// Never replace the DACL of a volume root.
		return fmt.Errorf("%w: %s is a volume root", ErrUnsafePath, dir)
	}
	missing := make([]string, 0, 4)
	current := dir
	for {
		_, err := os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("%w: %s has no existing ancestor", ErrUnsafePath, dir)
		}
		current = parent
	}
	if len(missing) > 0 && !writable {
		return &os.PathError{Op: "open", Path: dir, Err: os.ErrNotExist}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := privateMkdirIfMissing(missing[i]); err != nil {
			return err
		}
	}
	if !writable {
		return nil
	}
	if err := checkReparseFree(dir, true); err != nil {
		return err
	}
	return protectDirectory(dir)
}

// privateMkdirIfMissing creates one directory with the private DACL.
func privateMkdirIfMissing(path string) error {
	sd, err := privateSecurity(true)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	err = windows.CreateDirectory(name, &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	})
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

// protectFiles re-applies the private DACL to the database file and to the WAL
// and shared-memory files when they already exist. They are not part of the
// database inside SQLite; a checkpoint can leave account ciphertext pages in
// them, so they are covered by the same protection as the main file.
func (db *DB) protectFiles() error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := db.path + suffix
		if _, err := os.Lstat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if err := checkReparseFree(path, false); err != nil {
			return err
		}
		if err := protectFile(path); err != nil {
			return err
		}
	}
	return nil
}

// privateSecurity builds a protected DACL granting full control to the current
// user and SYSTEM only. Directories pass the inherited ACEs on to their children.
func privateSecurity(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	return windows.SecurityDescriptorFromString("D:P(A;" + flags + ";FA;;;" + user.User.Sid.String() + ")(A;" + flags + ";FA;;;SY)")
}

// setPrivateHandle replaces the DACL of an open handle and marks it protected so
// the object stops inheriting from its parent.
func setPrivateHandle(h windows.Handle, directory bool) error {
	sd, err := privateSecurity(directory)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// openReparseSafe opens path without following its final component.
func openReparseSafe(path string, access uint32, directory bool) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	attrs := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		attrs |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	h, err := windows.CreateFile(name, access|windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, attrs, 0)
	if err != nil {
		return 0, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return h, nil
}

// protectDirectory gives an existing directory the private DACL.
func protectDirectory(path string) error {
	h, err := openReparseSafe(path, windows.FILE_READ_ATTRIBUTES, true)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return setPrivateHandle(h, true)
}

// protectFile gives a regular file the private DACL.
func protectFile(path string) error {
	h, err := openReparseSafe(path, windows.FILE_READ_ATTRIBUTES, false)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%w: %s is a reparse point", ErrUnsafePath, path)
	}
	return setPrivateHandle(h, false)
}
