//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

type txnVolume struct {
	GUID   string
	Serial uint32
}

// Volume names come from already opened handles, not drive-letter comparisons.
// GUID names disambiguate serial-number collisions and drive aliases. Unknown
// identity fails closed; mount-point reparses are rejected separately.
func txnVolumeID(root *os.Root) (txnVolume, error) {
	f, err := root.Open(".")
	if err != nil {
		return txnVolume{}, err
	}
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return txnVolume{}, err
	}
	path, err := txnGUIDHandlePath(f)
	if err != nil {
		return txnVolume{}, err
	}
	end := strings.Index(path, `}\`)
	return txnVolume{GUID: strings.ToUpper(path[:end+2]), Serial: info.VolumeSerialNumber}, nil
}
func txnGUIDRootPath(root *os.Root) (string, error) {
	f, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer f.Close()
	return txnGUIDHandlePath(f)
}
func txnGUIDHandlePath(f *os.File) (string, error) {
	const volumeNameGUID = 0x1
	name := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), &name[0], uint32(len(name)), volumeNameGUID)
	if err != nil {
		return "", err
	}
	if n == 0 || n >= uint32(len(name)) {
		return "", ErrUnsafeTarget
	}
	path := windows.UTF16ToString(name[:n])
	if !strings.HasPrefix(strings.ToLower(path), `\\?\volume{`) || !strings.Contains(path, `}\`) {
		return "", ErrUnsafeTarget
	}
	return path, nil
}
func txnPathsOverlap(a, b string) bool {
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		rel, err := filepath.Rel(filepath.Clean(pair[0]), filepath.Clean(pair[1]))
		if err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
func txnSourceRootValid(source, root string) error {
	if err := txnAbsoluteSafe(source); err != nil {
		return err
	}
	if txnPathsOverlap(source, root) {
		return ErrUnsafeTarget
	}
	p, err := windows.UTF16PtrFromString(filepath.VolumeName(source) + `\`)
	if err != nil {
		return err
	}
	kind := windows.GetDriveType(p)
	if kind != windows.DRIVE_FIXED && kind != windows.DRIVE_REMOVABLE {
		return ErrUnsafeTarget
	}
	return nil
}
func txnOpenSource(r *txnRoots, source string) error {
	if source == "" {
		return nil
	}
	external, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	a, ae := txnVolumeID(external)
	b, be := txnVolumeID(r.work)
	if ae != nil || be != nil {
		external.Close()
		return errors.Join(ErrUnsafeTarget, ae, be)
	}
	sourcePath, se := txnGUIDRootPath(external)
	installPath, ie := txnGUIDRootPath(r.install)
	if se != nil || ie != nil || txnPathsOverlap(sourcePath, installPath) {
		external.Close()
		return errors.Join(ErrUnsafeTarget, se, ie)
	}
	r.source = external
	r.sourceDifferentVolume = a != b
	return nil
}

// A copied source never becomes a final stage until complete bytes are flushed
// and checked. A normal failure removes only this invocation's exclusive temp;
// abrupt process death may leave an unreferenced temp, which is never inferred
// as valid staging or used as authority to delete any other file.
func txnCopyExternalTemp(ctx context.Context, sourceRoot, destRoot *os.Root, source, parent string, size int64) (name string, resultErr error) {
	if err := txnSafe(sourceRoot, source, false); err != nil {
		return "", err
	}
	if err := txnSafe(destRoot, parent, false); err != nil {
		return "", err
	}
	src, err := sourceRoot.Open(source)
	if err != nil {
		return "", err
	}
	defer src.Close()
	name = parent + "/copy-" + rand.Text() + ".part"
	dst, err := destRoot.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := dst.Close(); resultErr == nil && err != nil {
			resultErr = err
		}
		if resultErr != nil {
			destRoot.Remove(name)
		}
	}()
	reader := io.LimitReader(src, size+1)
	buffer := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return name, err
		}
		n, err := reader.Read(buffer)
		if n > 0 {
			written, e := dst.Write(buffer[:n])
			if e != nil {
				return name, e
			}
			if written != n {
				return name, io.ErrShortWrite
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return name, err
		}
	}
	return name, dst.Sync()
}
func txnStageExternal(ctx context.Context, r *txnRoots, j *txnJournal, index int, file TxnFile, in TxnInput) error {
	stage := txnStage(j, index)
	if err := txnVerify(ctx, r.source, file.Source, *j.Files[index].Target); err != nil {
		return fmt.Errorf("external source identity: %w", err)
	}
	tempParent := "cache/tmp/txn-copy-" + j.TxnID
	if err := txnSafe(r.work, tempParent, true); err != nil {
		return err
	}
	if err := r.work.MkdirAll(tempParent, 0700); err != nil {
		return err
	}
	temp, err := txnCopyExternalTemp(ctx, r.source, r.work, file.Source, tempParent, file.Size)
	if err != nil {
		return err
	}
	defer r.work.Remove(tempParent)
	defer r.work.Remove(temp)
	if err := txnVerify(ctx, r.work, temp, *j.Files[index].Target); err != nil {
		return fmt.Errorf("external staged identity: %w", err)
	}
	if err := txnVerify(ctx, r.source, file.Source, *j.Files[index].Target); err != nil {
		return fmt.Errorf("external source changed during copy: %w", err)
	}
	if err := txnSafe(r.work, stage, true); err != nil {
		return err
	}
	if err := r.work.Rename(temp, stage); err != nil {
		return err
	}
	return txnCheckpoint(in, "after-source-copy")
}
