// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// packTime is the fixed timestamp of every packed entry, so the same folder
// always yields the same package bytes.
var packTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// Pack writes the app folder dir as a release package and returns the file
// list for its manifest. It refuses anything an update could not recreate.
func Pack(dir string, out io.Writer) ([]FileEntry, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !safePath(rel) {
			return fmt.Errorf("selfupdate: %q cannot be shipped", rel)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("selfupdate: %q is not a regular file", rel)
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	if len(paths) == 0 || len(paths) > maxFiles {
		return nil, errors.New("selfupdate: app folder has no files or too many")
	}
	zw := zip.NewWriter(out)
	files := make([]FileEntry, 0, len(paths))
	launcher := false
	for _, rel := range paths {
		entry, err := packFile(zw, filepath.Join(dir, filepath.FromSlash(rel)), rel)
		if err != nil {
			return nil, err
		}
		files = append(files, entry)
		launcher = launcher || rel == AppExe
	}
	if !launcher {
		return nil, fmt.Errorf("selfupdate: app folder lacks %s", AppExe)
	}
	return files, zw.Close()
}

func packFile(zw *zip.Writer, src, rel string) (FileEntry, error) {
	f, err := os.Open(src)
	if err != nil {
		return FileEntry{}, err
	}
	defer f.Close()
	w, err := zw.CreateHeader(&zip.FileHeader{Name: rel, Method: zip.Deflate, Modified: packTime})
	if err != nil {
		return FileEntry{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return FileEntry{}, err
	}
	if n > maxFileSize {
		return FileEntry{}, fmt.Errorf("selfupdate: %q is too large", rel)
	}
	return FileEntry{Path: rel, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}
