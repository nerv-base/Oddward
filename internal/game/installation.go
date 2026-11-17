// SPDX-License-Identifier: GPL-3.0-or-later

// Package game inspects installation structure without executing or modifying it.
// A valid structure is not proof of authenticity, completeness, channel identity,
// or a supported independent launch/login path.
//
// This milestone supports only local fixed and removable Windows volumes. UNC shares,
// device/extended-length namespaces and mapped network drives are rejected before any
// resolution, and each accepted installation is a snapshot that callers must revalidate
// at use time (see Installation).
package game

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var ErrNotInstallation = errors.New("not a supported NTE installation")

// Installation describes the normalized WindowsNoEditor tree. Branch is metadata,
// not a trusted URL or an assertion that the installed version is current.
//
// An Installation is a snapshot of one moment, not an authorization or a capability:
// the tree can be replaced, linked, renamed or deleted after inspection, and no
// recorded path (including os.Root.Name()) closes that window. Launch and update code
// must therefore open and revalidate the files it uses at use time rather than trusting
// a value returned here.
type Installation struct{ Root, Executable, AppID, Branch string }

const executable = "HT/Binaries/Win64/HTGame.exe"
const maxConfigSize = 1 << 20

// Volume kinds returned by GetDriveTypeW. This milestone supports only local fixed and
// removable volumes; the other kinds are named so a rejection can say why.
const (
	driveUnknown   = 0 // DRIVE_UNKNOWN
	driveNoRootDir = 1 // DRIVE_NO_ROOT_DIR
	driveRemovable = 2 // DRIVE_REMOVABLE
	driveFixed     = 3 // DRIVE_FIXED
	driveRemote    = 4 // DRIVE_REMOTE
	driveCDROM     = 5 // DRIVE_CDROM
	driveRAMDisk   = 6 // DRIVE_RAMDISK
)

var branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// Inspect accepts a WindowsNoEditor directory, its Client parent, or that parent's
// parent. It never scans drives, reads account state, writes files or runs a binary.
// Root-scoped opens keep an inner symlink/junction from escaping the chosen tree for the
// duration of this call; they do not make later use of the result trustworthy.
func Inspect(ctx context.Context, selected string) (Installation, error) {
	if err := ctx.Err(); err != nil {
		return Installation{}, err
	}
	if selected == "" {
		return Installation{}, fmt.Errorf("%w: empty path", ErrNotInstallation)
	}
	// Reject remote/device syntax before resolving the caller's string, then
	// classify its volume. Linked ancestors are refused by resolveLocalPath.
	// These checks are not a defense against malicious concurrent path swaps.
	if err := rejectUnsupportedPathForm(selected); err != nil {
		return Installation{}, fmt.Errorf("%w: %w", ErrNotInstallation, err)
	}
	abs, err := filepath.Abs(selected)
	if err != nil {
		return Installation{}, fmt.Errorf("%w: %w", ErrNotInstallation, fmt.Errorf("selected path is not usable: %w", err))
	}
	if err := checkSupportedPath(abs); err != nil {
		return Installation{}, fmt.Errorf("%w: %w", ErrNotInstallation, err)
	}
	base, err := resolveLocalPath(ctx, abs)
	if err != nil {
		return Installation{}, fmt.Errorf("%w: %w", ErrNotInstallation, fmt.Errorf("resolving %q: %w", abs, err))
	}
	// Keep the resolved-path policy check explicit before opening the selection.
	// Linked ancestors/candidates are conservatively rejected in this milestone.
	if err := checkSupportedPath(base); err != nil {
		return Installation{}, fmt.Errorf("%w: %w", ErrNotInstallation, fmt.Errorf("resolved %q: %w", base, err))
	}
	info, err := os.Stat(base)
	if err != nil {
		return Installation{}, fmt.Errorf("%w: %w", ErrNotInstallation, fmt.Errorf("selected path must be an existing directory: %w", err))
	}
	if !info.IsDir() {
		return Installation{}, fmt.Errorf("%w: selected path must be an existing directory", ErrNotInstallation)
	}
	var failures []error
	for _, candidate := range []string{base, filepath.Join(base, "WindowsNoEditor"), filepath.Join(base, "Client", "WindowsNoEditor")} {
		if err := ctx.Err(); err != nil {
			return Installation{}, err
		}
		got, err := inspectRoot(ctx, candidate)
		if err == nil {
			return got, nil
		}
		if ctx.Err() != nil {
			return Installation{}, ctx.Err()
		}
		// Label every candidate so a rejection is diagnosable without guessing which of
		// the three accepted folder levels failed, and keep the cause wrapped.
		failures = append(failures, fmt.Errorf("%s: %w", candidate, err))
	}
	return Installation{}, fmt.Errorf("%w: %w", ErrNotInstallation, errors.Join(failures...))
}

func inspectRoot(ctx context.Context, path string) (Installation, error) {
	canonical, err := resolveLocalPath(ctx, path)
	if err != nil {
		return Installation{}, err
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return Installation{}, err
	}
	defer root.Close()
	f, err := root.Open(executable)
	if err != nil {
		return Installation{}, err
	}
	err = checkPE64(f)
	f.Close()
	if err != nil {
		return Installation{}, fmt.Errorf("%s: %w", executable, err)
	}
	info, err := root.Stat("HT/Content/Paks")
	if err != nil {
		return Installation{}, fmt.Errorf("HT/Content/Paks: %w", err)
	}
	if !info.IsDir() {
		return Installation{}, errors.New("HT/Content/Paks is not a directory")
	}
	if err := ctx.Err(); err != nil {
		return Installation{}, err
	}
	config, err := root.Open("PatcherConfig/Patch_Windows.json")
	if err != nil {
		return Installation{}, fmt.Errorf("PatcherConfig/Patch_Windows.json: %w", err)
	}
	defer config.Close()
	info, err = config.Stat()
	if err != nil {
		return Installation{}, fmt.Errorf("PatcherConfig/Patch_Windows.json: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfigSize {
		return Installation{}, errors.New("patch configuration is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(config, maxConfigSize+1))
	if err != nil {
		return Installation{}, fmt.Errorf("PatcherConfig/Patch_Windows.json: %w", err)
	}
	if len(data) > maxConfigSize {
		return Installation{}, errors.New("patch configuration exceeds limit")
	}
	var metadata struct {
		AppID  string `json:"appId"`
		Branch string `json:"branchName"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return Installation{}, fmt.Errorf("patch configuration: %w", err)
	}
	// branchPattern already forbids a leading '.', but it permits interior dots, so the
	// explicit ".." substring check is not redundant: it rejects names such as "a..b".
	if metadata.AppID != "1289" || !branchPattern.MatchString(metadata.Branch) || strings.Contains(metadata.Branch, "..") {
		return Installation{}, errors.New("unsupported resource identity or branch")
	}
	if err := ctx.Err(); err != nil {
		return Installation{}, err
	}
	return Installation{Root: canonical, Executable: filepath.Join(canonical, filepath.FromSlash(executable)), AppID: metadata.AppID, Branch: metadata.Branch}, nil
}

// Read only bounded PE headers. Do not let untrusted symbol/section counts cause
// large allocations, and do not interpret a plausible header as a trusted signature.
func checkPE64(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("game executable is not a regular file")
	}
	var dos [64]byte
	if _, err := f.ReadAt(dos[:], 0); err != nil {
		return fmt.Errorf("DOS header: %w", err)
	}
	if dos[0] != 'M' || dos[1] != 'Z' {
		return errors.New("missing DOS signature")
	}
	off := int64(binary.LittleEndian.Uint32(dos[60:]))
	if off < 64 || off > 1<<20 || off > info.Size()-26 {
		return errors.New("PE header offset out of bounds")
	}
	var header [26]byte
	if _, err := f.ReadAt(header[:], off); err != nil {
		return fmt.Errorf("PE header: %w", err)
	}
	if string(header[:4]) != "PE\x00\x00" || binary.LittleEndian.Uint16(header[4:6]) != 0x8664 {
		return errors.New("not a Windows AMD64 executable")
	}
	characteristics := binary.LittleEndian.Uint16(header[22:24])
	if characteristics&0x0002 == 0 || characteristics&0x2000 != 0 || binary.LittleEndian.Uint16(header[20:22]) < 2 || binary.LittleEndian.Uint16(header[24:26]) != 0x20b {
		return errors.New("not an executable PE32+ image")
	}
	return nil
}

// rejectUnsupportedPathForm rejects path syntax this milestone does not support, before
// any filesystem or network access. Supported: drive-absolute local paths ("C:\...") and
// paths relative to the current directory. Rejected: UNC shares ("\\server\share",
// "//server/share"), device and extended-length namespaces ("\\?\", "\\.\") and the
// drive-relative ("C:game") and root-relative ("\game") forms that name no volume.
func rejectUnsupportedPathForm(p string) error {
	if p == "" {
		return errors.New("path is empty")
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("unsupported path form %q: contains a NUL byte", p)
	}
	if len(p) > 1 && (p[0] == '\\' || p[0] == '/') && (p[1] == '\\' || p[1] == '/') {
		return fmt.Errorf("unsupported path form %q: UNC, device and extended-length namespace paths are not supported", p)
	}
	if p[0] == '\\' || p[0] == '/' {
		return fmt.Errorf("unsupported path form %q: root-relative paths are not supported", p)
	}
	if len(p) > 1 && p[1] == ':' && (len(p) < 3 || (p[2] != '\\' && p[2] != '/')) {
		return fmt.Errorf("unsupported path form %q: drive-relative paths are not supported", p)
	}
	return nil
}

// volumeRoot returns the "X:\" root of the volume a drive-absolute path names.
func volumeRoot(p string) (string, error) {
	if len(p) < 3 || !isASCIILetter(p[0]) || p[1] != ':' || (p[2] != '\\' && p[2] != '/') {
		return "", fmt.Errorf("unsupported path form %q: not a drive-absolute local path", p)
	}
	return p[:2] + `\`, nil
}

func isASCIILetter(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// classifyDriveType accepts only the volume kinds this milestone supports. A mapped
// network drive is rejected by kind, before any resolution reaches the share.
func classifyDriveType(kind uint32) error {
	switch kind {
	case driveFixed, driveRemovable:
		return nil
	case driveRemote:
		return errors.New("volume is a mapped network drive, which this milestone does not support")
	case driveCDROM:
		return errors.New("volume is optical media, which this milestone does not support")
	case driveRAMDisk:
		return errors.New("volume is a RAM disk, which this milestone does not support")
	case driveUnknown, driveNoRootDir:
		return fmt.Errorf("volume type %d cannot hold a supported installation", kind)
	default:
		return fmt.Errorf("unrecognized volume type %d", kind)
	}
}

// checkSupportedPath applies the syntactic form check and the local-volume check to one
// path. Inspect calls it on the caller's absolute path before resolution and again on the
// resolved result.
func checkSupportedPath(p string) error {
	if err := rejectUnsupportedPathForm(p); err != nil {
		return err
	}
	return checkLocalVolume(p)
}
