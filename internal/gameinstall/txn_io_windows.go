//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/nerv-base/Oddward/internal/gameres"
	"golang.org/x/sys/windows"
)

type txnRoots struct {
	install, work         *os.Root
	source                *os.Root
	sourceDifferentVolume bool
	workRel, workPath     string
}

func (r *txnRoots) close() {
	if r.source != nil {
		r.source.Close()
	}
	r.work.Close()
	r.install.Close()
}
func txnReparse(st os.FileInfo) bool {
	if st.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := st.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
func txnAbsoluteSafe(name string) error {
	if !filepath.IsAbs(name) {
		return ErrUnsafeTarget
	}
	clean := filepath.Clean(name)
	volume := filepath.VolumeName(clean)
	if len(volume) != 2 || strings.HasPrefix(clean, `\\`) {
		return ErrUnsafeTarget
	}
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(clean, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		st, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if txnReparse(st) || !st.IsDir() {
			return ErrUnsafeTarget
		}
	}
	return nil
}
func txnOpenRoots(root, work string) (*txnRoots, error) {
	if err := txnAbsoluteSafe(root); err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(work))
	if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		return nil, ErrUnsafeTarget
	}
	rel = filepath.ToSlash(rel)
	if rel != ".oddward" && !strings.HasPrefix(rel, ".oddward/") {
		return nil, ErrUnsafeTarget
	}
	install, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*txnRoots, error) { install.Close(); return nil, err }
	if err := txnSafe(install, rel, true); err != nil {
		return fail(err)
	}
	if err := install.MkdirAll(rel, 0700); err != nil {
		return fail(err)
	}
	wr, err := install.OpenRoot(rel)
	if err != nil {
		return fail(err)
	}
	a, ae := txnVolumeID(install)
	b, be := txnVolumeID(wr)
	if ae != nil || be != nil || a != b {
		wr.Close()
		return fail(ErrUnsafeTarget)
	}
	return &txnRoots{install: install, work: wr, workRel: rel, workPath: work}, nil
}
func txnSafe(r *os.Root, name string, missing bool) error {
	if name == "" || filepath.IsAbs(name) || strings.ContainsAny(name, "\\:") {
		return ErrUnsafeTarget
	}
	parts := strings.Split(name, "/")
	current := ""
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return ErrUnsafeTarget
		}
		if current != "" {
			current += "/"
		}
		current += part
		st, err := r.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && missing {
			return nil
		}
		if err != nil {
			return err
		}
		if txnReparse(st) {
			return ErrUnsafeTarget
		}
		if current != name && !st.IsDir() {
			return ErrUnsafeTarget
		}
	}
	return nil
}
func txnTargetValid(t InstallTarget) error {
	p, err := gameres.NormalizePath(t.Path)
	if err != nil || p != t.Path || !strings.HasPrefix(strings.ToLower(p), "client/windowsnoeditor/") || !stateValidMD5(t.MD5) || t.Size <= 0 || t.Size > 32<<30 {
		return ErrUnsafeTarget
	}
	return nil
}
func txnSourceValid(source string) error {
	p, err := gameres.NormalizePath(source)
	if err != nil || p != source || strings.HasPrefix(strings.ToLower(p), "txn/") {
		return ErrUnsafeTarget
	}
	return nil
}
func txnIDValid(id string) bool {
	if len(id) == 0 || len(id) > 96 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func txnBootstrapPath(name string) bool {
	return name == "Client/WindowsNoEditor/PatcherConfig/Patch_Windows.json" || name == "Client/WindowsNoEditor/PatcherConfig/Patch_Windows.source.json"
}
func txnIdentity(ctx context.Context, r *os.Root, name string) (InstallTarget, error) {
	if err := ctx.Err(); err != nil {
		return InstallTarget{}, err
	}
	if err := txnSafe(r, name, false); err != nil {
		return InstallTarget{}, err
	}
	f, err := r.Open(name)
	if err != nil {
		return InstallTarget{}, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return InstallTarget{}, err
	}
	if !before.Mode().IsRegular() {
		return InstallTarget{}, ErrUnsafeTarget
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return InstallTarget{}, err
	}
	if info.NumberOfLinks != 1 {
		return InstallTarget{}, ErrUnsafeTarget
	}
	h := md5.New()
	buffer := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return InstallTarget{}, err
		}
		n, err := f.Read(buffer)
		if n > 0 {
			h.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return InstallTarget{}, err
		}
	}
	after, err := f.Stat()
	if err != nil {
		return InstallTarget{}, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return InstallTarget{}, errors.New("file changed while hashing")
	}
	return InstallTarget{Path: name, MD5: fmt.Sprintf("%x", h.Sum(nil)), Size: before.Size()}, nil
}
func txnVerify(ctx context.Context, r *os.Root, name string, want InstallTarget) error {
	actual, err := txnIdentity(ctx, r, name)
	if err != nil {
		return err
	}
	actual.Path = want.Path
	if actual != want {
		return fmt.Errorf("checksum mismatch: %s", name)
	}
	return nil
}
func txnCopy(ctx context.Context, r *os.Root, source, dest string) error {
	if err := txnSafe(r, source, false); err != nil {
		return err
	}
	if err := txnSafe(r, dest, true); err != nil {
		return err
	}
	src, err := r.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := r.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer dst.Close()
	buf := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return dst.Sync()
}
func txnEmptyRoot(r *os.Root) error {
	f, err := r.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != ".oddward" {
			return ErrTargetExists
		}
	}
	return nil
}
func txnCheckSpace(ctx context.Context, in TxnInput, r *txnRoots) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	need := in.RequiredFreeBytes
	// All retained source bytes may require separate staging; transfers are renames.
	var copies int64
	for i, f := range append(append([]TxnFile{}, in.Files...), in.Bootstrap...) {
		if (f.Retain || in.SourceRoot != "" && i < len(in.Files)) && f.Source != "" {
			want := InstallTarget{Path: f.Path, MD5: f.MD5, Size: f.Size}
			if txnIDValid(in.TxnID) {
				stage := fmt.Sprintf("txn/%s/staged/%d", in.TxnID, i)
				if txnVerify(ctx, r.work, stage, want) == nil {
					continue
				}
			}
			if in.Candidate.Mode == "fresh" && txnVerify(ctx, r.install, f.Path, want) == nil {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if copies > int64(^uint64(0)>>1)-f.Size {
				return ErrNoSpace
			}
			copies += f.Size
		}
	}
	if in.SourceRoot != "" {
		// External fresh caches allocate new destination bytes. System supplies
		// the reserve separately; only copies not already staged are added.
		if copies > int64(^uint64(0)>>1)-need {
			return ErrNoSpace
		}
		need += copies
	} else if copies > need {
		need = copies
	}
	if need < 1<<20 {
		need = 1 << 20
	}
	var free int64
	var err error
	if in.FreeBytes != nil {
		free, err = in.FreeBytes(ctx)
	} else {
		p, xerr := windows.UTF16PtrFromString(r.workPath)
		if xerr != nil {
			return xerr
		}
		var available uint64
		err = windows.GetDiskFreeSpaceEx(p, &available, nil, nil)
		if available > uint64(^uint64(0)>>1) {
			free = int64(^uint64(0) >> 1)
		} else {
			free = int64(available)
		}
	}
	if err != nil {
		return err
	}
	if free < need {
		return ErrNoSpace
	}
	return nil
}
func txnVerifyCandidate(ctx context.Context, r *txnRoots, j *txnJournal) error {
	for _, e := range j.Files {
		if e.Target != nil {
			if err := txnVerify(ctx, r.install, e.Path, *e.Target); err != nil {
				return err
			}
		}
	}
	return nil
}
func txnValidateBeforeCommit(ctx context.Context, r *txnRoots, j *txnJournal) error {
	for i, e := range j.Files {
		actual, err := txnIdentity(ctx, r.install, e.Path)
		if e.Reuse {
			if err != nil || actual != *e.Target {
				return errors.New("reused file changed before commit")
			}
			continue
		}
		if e.Old != nil {
			if err != nil || actual != *e.Old {
				return errors.New("old file changed before commit")
			}
		} else if err == nil {
			if !(j.Kind == "install" && e.Target != nil && actual == *e.Target) {
				return ErrTargetExists
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if e.Target != nil {
			if j.Kind == "install" && err == nil && actual == *e.Target {
				continue
			}
			if err := txnVerify(ctx, r.work, txnStage(j, i), *e.Target); err != nil {
				return err
			}
		}
	}
	return nil
}
