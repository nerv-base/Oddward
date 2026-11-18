// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func safeRelative(path string) bool {
	if path == "" || filepath.IsAbs(path) || strings.ContainsAny(path, `\:`) {
		return false
	}
	for _, p := range strings.Split(path, "/") {
		if p == "" || p == "." || p == ".." || strings.HasSuffix(p, ".") || strings.HasSuffix(p, " ") {
			return false
		}
	}
	return true
}
func reparse(info os.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return false
}
func safeRootPath(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for cursor := absolute; ; cursor = filepath.Dir(cursor) {
		info, err := os.Lstat(cursor)
		if err != nil {
			return err
		}
		if reparse(info) {
			return failure(ErrPathUnsafe, "open root", cursor, nil)
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			break
		}
	}
	return nil
}
func checkPath(root *os.Root, path string) error {
	if !safeRelative(path) {
		return failure(ErrPathUnsafe, "root path", path, nil)
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return classify("lstat", p, err)
		}
		if reparse(info) {
			return failure(ErrPathUnsafe, "reparse point", p, nil)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return failure(ErrPathUnsafe, "parent directory", p, nil)
		}
	}
	return nil
}
func mkdirSafe(root *os.Root, path string) error {
	if err := checkPath(root, path); err != nil {
		return err
	}
	if err := root.MkdirAll(path, 0700); err != nil {
		return classify("mkdir", path, err)
	}
	return checkPath(root, path)
}
func openSafe(root *os.Root, path string, flags int) (*os.File, error) {
	if err := checkPath(root, path); err != nil {
		return nil, err
	}
	f, err := root.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, classify("open", path, err)
	}
	if err = checkPath(root, path); err != nil {
		f.Close()
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, failure(ErrPathUnsafe, "regular file", path, err)
	}
	return f, nil
}
func atomicWrite(root *os.Root, path string, data []byte) error {
	if err := checkPath(root, path); err != nil {
		return err
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := path + "." + hex.EncodeToString(random[:]) + ".tmp"
	f, err := openSafe(root, tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return classify("flush state", path, err)
	}
	if err = checkPath(root, path); err != nil {
		return err
	}
	return classify("replace state", path, root.Rename(tmp, path))
}
func readBounded(root *os.Root, path string, limit int64) ([]byte, error) {
	f, err := openSafe(root, path, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, failure(ErrRecoveryRequired, "state too large", path, nil)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, failure(ErrRecoveryRequired, "state too large", path, nil)
	}
	return data, err
}
func removeSafe(root *os.Root, path string) error {
	if err := checkPath(root, path); err != nil {
		return err
	}
	err := root.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return classify("remove", path, err)
}
