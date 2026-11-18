// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
	"golang.org/x/sys/windows"
)

const downloadContent = "Client/WindowsNoEditor/"

func downloadTargetPath(path string) (string, error) {
	path, err := gameres.NormalizePath(path)
	if err != nil {
		return "", err
	}
	prefix := strings.ToUpper(downloadContent)
	key := strings.ToUpper(path)
	if strings.HasPrefix(key, "CLIENT/") {
		if !strings.HasPrefix(key, prefix) {
			return "", gameres.ErrPathUnsafe
		}
		path = path[len(downloadContent):]
	}
	if strings.EqualFold(path, "PatcherConfig") || strings.HasPrefix(strings.ToUpper(path), "PATCHERCONFIG/") {
		return "", gameres.ErrPathUnsafe
	}
	return gameres.NormalizePath(downloadContent + path)
}

// downloadDirectory checks the local volume and every existing ancestor. A
// nonexistent leaf is allowed only when the immediate parent already exists.
func downloadDirectory(path string, missingLeaf bool) (string, error) {
	if !filepath.IsAbs(path) || len(filepath.VolumeName(path)) != 2 || strings.ContainsAny(path, "\x00\r\n") {
		return "", gameres.ErrPathUnsafe
	}
	clean := filepath.Clean(path)
	if path != clean || filepath.Dir(clean) == clean {
		return "", gameres.ErrPathUnsafe
	}
	for part := clean; ; part = filepath.Dir(part) {
		if filepath.Dir(part) != part {
			name := filepath.Base(part)
			if _, err := gameres.NormalizePath("X/" + name); err != nil {
				return "", err
			}
		}
		fi, err := os.Lstat(part)
		if err != nil {
			if !(missingLeaf && part == clean && errors.Is(err, os.ErrNotExist)) {
				return "", err
			}
		} else if !fi.IsDir() || downloadReparse(fi) {
			return "", gameres.ErrPathUnsafe
		}
		if filepath.Dir(part) == part {
			break
		}
	}
	return clean, nil
}

func downloadReparse(fi os.FileInfo) bool {
	attrs, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	return fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || ok && attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func freshDownloadRoot(path string) (string, error) {
	path, err := downloadDirectory(path, true)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	entries, err := f.Readdirnames(1)
	if len(entries) != 0 {
		return "", errors.New("installation requires an empty directory")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return path, nil
}

func acquireDownloadWork(root string) (*gamedl.Lease, error) {
	if _, err := downloadDirectory(root, false); err != nil {
		return nil, err
	}
	install, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer install.Close()
	if err := install.Mkdir(".oddward", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	work := downloadWork(root)
	if _, err := downloadDirectory(work, false); err != nil {
		return nil, err
	}
	return gamedl.Acquire(work)
}

func downloadFree(path string) (int64, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		path = filepath.Dir(path)
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, nil, nil); err != nil {
		return 0, err
	}
	return int64(min(free, 1<<63-1)), nil
}
