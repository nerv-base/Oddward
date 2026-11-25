// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package gallery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

// Test hooks; production uses the Windows APIs directly.
var (
	freeSpaceFn        = windowsFreeSpace
	knownFolderPathFn  = windows.KnownFolderPath
	archiveReserveHook = ArchiveFreeReserveBytes
)

// DefaultPicturesDir resolves the user's Pictures folder through the Windows
// Known Folder API (honoring redirected locations like OneDrive or D:\Pictures).
func DefaultPicturesDir() (string, error) {
	dir, err := knownFolderPathFn(windows.FOLDERID_Pictures, 0)
	if err != nil {
		return "", fmt.Errorf("gallery: resolve Pictures known folder: %w", err)
	}
	return filepath.Clean(dir), nil
}

// DefaultVideosDir resolves the user's Videos folder through the Windows
// Known Folder API.
func DefaultVideosDir() (string, error) {
	dir, err := knownFolderPathFn(windows.FOLDERID_Videos, 0)
	if err != nil {
		return "", fmt.Errorf("gallery: resolve Videos known folder: %w", err)
	}
	return filepath.Clean(dir), nil
}

// DefaultArchiveDir returns <Pictures>\Oddward\Gallery.
func DefaultArchiveDir() (string, error) {
	pics, err := DefaultPicturesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(pics, "Oddward", "Gallery"), nil
}

// ValidPathSyntax verifies that path is an ordinary local Windows drive path
// without control characters, trailing dots/spaces or reserved DOS device names.
func ValidPathSyntax(path string) error {
	path = filepath.FromSlash(path)
	if len(path) < 3 || len(path) > 32768 || !utf8.ValidString(path) ||
		path[1] != ':' || path[2] != '\\' ||
		!((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) {
		return fmt.Errorf("%w: expected an ordinary absolute local Windows path", ErrUnsafePath)
	}
	for _, part := range strings.Split(path[3:], `\`) {
		if part == "" {
			continue
		}
		if part == "." || part == ".." {
			return fmt.Errorf("%w: path traversal component is not allowed", ErrUnsafePath)
		}
		if strings.ContainsAny(part, ":*?<>|\"") || strings.IndexFunc(part, unicode.IsControl) >= 0 ||
			strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return fmt.Errorf("%w: unsupported path component", ErrUnsafePath)
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		letters := []rune(base)
		portDevice := len(letters) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
			strings.ContainsRune("123456789¹²³", letters[3])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
			base == "CONIN$" || base == "CONOUT$" || portDevice {
			return fmt.Errorf("%w: reserved Windows device component", ErrUnsafePath)
		}
	}
	return nil
}

// NormalizeRelativePath converts a relative path under a source root into a
// clean slash-separated form and rejects traversal or drive-qualified paths.
func NormalizeRelativePath(rel string) (string, error) {
	if rel == "" || !utf8.ValidString(rel) || strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("%w: empty or invalid relative path", ErrUnsafePath)
	}
	slashed := strings.ReplaceAll(rel, `\`, `/`)
	if strings.HasPrefix(slashed, "/") || strings.Contains(slashed, ":") {
		return "", fmt.Errorf("%w: relative path must not be rooted", ErrUnsafePath)
	}
	for _, rawPart := range strings.Split(slashed, "/") {
		if rawPart == "." || rawPart == ".." {
			return "", fmt.Errorf("%w: traversal segment in relative path", ErrUnsafePath)
		}
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(slashed)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("%w: relative path escapes root", ErrUnsafePath)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("%w: invalid relative path segment", ErrUnsafePath)
		}
		if strings.ContainsAny(part, ":*?<>|\"") || strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return "", fmt.Errorf("%w: invalid characters in relative path", ErrUnsafePath)
		}
	}
	return clean, nil
}

func joinNormalized(root, rel string) string {
	if root == "" || rel == "" {
		return ""
	}
	return filepath.Join(root, filepath.FromSlash(rel))
}

// checkLocalPath validates every existing ancestor of path on a local fixed or
// removable drive, rejecting symlinks, junctions and reparse points.
func checkLocalPath(ctx context.Context, path string, wantDir bool, allowMissing bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := ValidPathSyntax(path); err != nil {
		return "", err
	}
	clean := filepath.Clean(path)
	root := filepath.VolumeName(clean) + `\`
	wide, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsafePath, err)
	}
	kind := windows.GetDriveType(wide)
	if kind != windows.DRIVE_FIXED && kind != windows.DRIVE_REMOVABLE {
		return "", fmt.Errorf("%w: requires a local fixed or removable volume", ErrUnsafePath)
	}
	current := root
	rest := strings.TrimPrefix(clean, root)
	if rest == "" {
		if !wantDir {
			return "", fmt.Errorf("%w: file path cannot be a volume root", ErrUnsafePath)
		}
		return clean, nil
	}
	parts := strings.Split(rest, `\`)
	for i, part := range parts {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if allowMissing {
				return clean, nil
			}
			return "", err
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return "", fmt.Errorf("%w: symlink or irregular entry rejected", ErrUnsafePath)
		}
		isDirStep := i != len(parts)-1 || wantDir
		if isDirStep && !info.IsDir() {
			return "", fmt.Errorf("%w: ancestor must be a directory", ErrUnsafePath)
		}
		if !isDirStep && !info.Mode().IsRegular() {
			return "", fmt.Errorf("%w: target must be a regular file", ErrUnsafePath)
		}
		attrs, err := inspectHandleAttributes(current, isDirStep)
		if err != nil {
			return "", err
		}
		if attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return "", fmt.Errorf("%w: reparse point or junction rejected", ErrUnsafePath)
		}
		if !isDirStep && attrs.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
			return "", fmt.Errorf("%w: expected regular file", ErrUnsafePath)
		}
	}
	return clean, nil
}

// checkSafeSubpath verifies that rel stays inside root and that no component
// from root down to the leaf is a symlink, junction or reparse point.
func checkSafeSubpath(ctx context.Context, root, rel string, wantDir bool, allowMissing bool) (string, error) {
	normRel, err := NormalizeRelativePath(rel)
	if err != nil {
		return "", err
	}
	full := joinNormalized(filepath.Clean(root), normRel)
	cleanRoot := filepath.Clean(root)
	if !isWithinRoot(cleanRoot, full) {
		return "", fmt.Errorf("%w: path escapes root", ErrUnsafePath)
	}
	return checkLocalPath(ctx, full, wantDir, allowMissing)
}

func isWithinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, `..\`) && !filepath.IsAbs(rel)
}

type fileStat struct {
	Size       int64
	ModifiedAt time.Time
	Identity   string
	Links      uint32
	Reparse    bool
	IsDir      bool
}

func inspectHandleAttributes(path string, isDir bool) (windows.ByHandleFileInformation, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.ByHandleFileInformation{}, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if isDir {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(
		name,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
	if err != nil {
		return windows.ByHandleFileInformation{}, err
	}
	var info windows.ByHandleFileInformation
	infoErr := windows.GetFileInformationByHandle(handle, &info)
	closeErr := windows.CloseHandle(handle)
	if err := errors.Join(infoErr, closeErr); err != nil {
		return windows.ByHandleFileInformation{}, err
	}
	return info, nil
}

// statRegularFile reads file identity, size, link count and modification time
// without following reparse points.
func statRegularFile(path string) (fileStat, error) {
	info, err := inspectHandleAttributes(path, false)
	if err != nil {
		return fileStat{}, err
	}
	size := (int64(info.FileSizeHigh) << 32) | int64(info.FileSizeLow)
	mod := time.Unix(0, info.LastWriteTime.Nanoseconds()).UTC()
	return fileStat{
		Size:       size,
		ModifiedAt: mod,
		Identity:   fmt.Sprintf("%08x:%08x:%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow),
		Links:      info.NumberOfLinks,
		Reparse:    info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0,
		IsDir:      info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0,
	}, nil
}

// statOpenFile queries identity and mtime directly from an already-open file
// descriptor so callers can detect concurrent replacement/modification.
func statOpenFile(f *os.File) (fileStat, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return fileStat{}, err
	}
	size := (int64(info.FileSizeHigh) << 32) | int64(info.FileSizeLow)
	mod := time.Unix(0, info.LastWriteTime.Nanoseconds()).UTC()
	return fileStat{
		Size:       size,
		ModifiedAt: mod,
		Identity:   fmt.Sprintf("%08x:%08x:%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow),
		Links:      info.NumberOfLinks,
		Reparse:    info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0,
		IsDir:      info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0,
	}, nil
}

func windowsFreeSpace(path string) (int64, error) {
	probe := path
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	wide, err := windows.UTF16PtrFromString(probe)
	if err != nil {
		return 0, err
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(wide, &free, nil, nil); err != nil {
		return 0, err
	}
	return int64(min(free, 1<<63-1)), nil
}

// replaceFile atomically publishes a same-volume staged file onto destination.
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

func isDirReparse(path string) (bool, error) {
	info, err := inspectHandleAttributes(path, true)
	if err != nil {
		return false, err
	}
	return info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}
