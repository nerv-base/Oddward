// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package accounts

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// privateSecurity builds a protected DACL that grants full control to the
// current user and SYSTEM only. Containers created inside an inheriting
// directory keep the same two entries and inherit nothing else.
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

func securityAttributes(sd *windows.SECURITY_DESCRIPTOR) *windows.SecurityAttributes {
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
}

// privateMkdir creates path with a protected, private DACL. The parent must
// already exist.
func privateMkdir(path string) error {
	sd, err := privateSecurity(true)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.CreateDirectory(name, securityAttributes(sd)); err != nil {
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

// setPrivateHandle replaces the DACL of an open handle with the private one and
// marks it protected, so the object stops inheriting from its parent.
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

// A non-delete-shared handle pins the object while its type, identity and ACL
// are checked. OPEN_REPARSE_POINT never follows the final path component, so a
// junction or symlink is seen as itself and rejected.
func openPrivate(path string, access uint32, directory bool) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	attrs := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		attrs |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	h, err := windows.CreateFile(name, access|windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, attrs, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	var info windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(h, &info)
	if err == nil && (info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory ||
		(!directory && info.NumberOfLinks != 1)) {
		err = ErrUnsafePath
	}
	if err == nil {
		err = setPrivateHandle(h, directory)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// protectDirectory gives an existing directory the private DACL.
func protectDirectory(path string) error {
	f, err := openPrivate(path, windows.FILE_READ_ATTRIBUTES, true)
	if err != nil {
		return err
	}
	return f.Close()
}

// openRegular opens an existing regular file for reading and re-applies the
// private DACL. Anything that is not a plain regular file is reported as
// errNotRegular so callers count it as corrupt instead of following it.
func openRegular(path string) (*os.File, error) {
	f, err := openPrivate(path, windows.GENERIC_READ, false)
	if errors.Is(err, ErrUnsafePath) {
		return nil, errNotRegular
	}
	return f, err
}

// createPrivate creates a new file with the private DACL, write-through and no
// sharing, then applies the protected DACL to the new handle as well so the
// file never inherits from its directory.
func createPrivate(path string) (*os.File, error) {
	sd, err := privateSecurity(false)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.WRITE_DAC, 0, securityAttributes(sd), windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_WRITE_THROUGH, 0)
	if err != nil {
		return nil, &os.PathError{Op: "create", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	if err := setPrivateHandle(h, false); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// removeRegular deletes a regular file through a handle that cannot be a
// reparse point. expected, when not nil, must describe the same file.
func removeRegular(path string, expected os.FileInfo) error {
	f, err := openPrivate(path, windows.DELETE|windows.FILE_READ_ATTRIBUTES, false)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if expected != nil && !os.SameFile(expected, info) {
		return ErrUnsafePath
	}
	var disposition uint32 = 1
	return windows.SetFileInformationByHandle(windows.Handle(f.Fd()), windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&disposition)), uint32(unsafe.Sizeof(disposition)))
}
