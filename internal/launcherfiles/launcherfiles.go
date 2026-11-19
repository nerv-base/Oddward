// SPDX-License-Identifier: GPL-3.0-or-later

// Package launcherfiles installs the official launcher components Oddward
// needs to sign in: the launcher executable, the client SDK bridge, the
// channel configuration and the patcher metadata. The files come from the
// vendor's public update manifest at run time; no vendor binary is bundled.
package launcherfiles

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// VersionURL is the vendor's launcher version endpoint. It is fixed
	// metadata, not a credential.
	VersionURL = "https://yhcdn1.wmupd.com/hd/publish_ob/launcher/Version.ini"

	maxVersionBytes = 64 << 10
	maxListBytes    = 16 << 20
	maxZipBytes     = 256 << 20
)

// File is one manifest entry. Path is absolute inside the launcher package,
// with a leading slash.
type File struct {
	Path    string
	MD5     string
	Size    int64
	ZipMD5  string
	ZipSize int64
}

// Manifest is the vendor's launcher package listing.
type Manifest struct {
	Version string
	BaseURL string
	Files   []File
}

// Required lists the files Oddward needs. The CEF/Qt interface assets are not
// included: Oddward never opens the official launcher window.
var Required = []string{
	"/NTEGame.exe",
	"/WPGameClientSDK_64.dll",
	"/Config/Config.ini",
	"/Config/game/zh_cn/game_1289.cfg",
	"/ResData/1289.dat",
	"/ResData/Default.dat",
	"/ResFilesM/1289/PatcherConfig/InstallConfig.json",
	"/ResFilesM/1289/PatcherConfig/PatcherCheckConfig.json",
	"/ResFilesM/1289/PatcherConfig/PatcherConfig.json",
	"/ResFilesM/1289/PatcherConfig/PatcherPath.json",
}

type manifestXML struct {
	Product struct {
		Version string `xml:"Version,attr"`
	} `xml:"ProductVersion"`
	URL struct {
		Base string `xml:"BaseUrl,attr"`
	} `xml:"Url"`
	Files []struct {
		Path        string `xml:"Path,attr"`
		Checksum    string `xml:"Checksum,attr"`
		Size        string `xml:"Size,attr"`
		ZipChecksum string `xml:"ZipChecksum,attr"`
		ZipSize     string `xml:"ZipSize,attr"`
	} `xml:"File"`
}

// Fetch reads the version file, follows FileListURL and parses the manifest.
func Fetch(ctx context.Context, client *http.Client, versionURL string) (Manifest, error) {
	if client == nil || versionURL == "" {
		return Manifest{}, errors.New("launcher components: no client or version URL")
	}
	body, err := get(ctx, client, versionURL, maxVersionBytes)
	if err != nil {
		return Manifest{}, err
	}
	listURL := iniValue(body, "FileListURL")
	if listURL == "" {
		return Manifest{}, errors.New("launcher components: version file has no FileListURL")
	}
	list, err := get(ctx, client, listURL, maxListBytes)
	if err != nil {
		return Manifest{}, err
	}
	var doc manifestXML
	if err := xml.Unmarshal(list, &doc); err != nil {
		return Manifest{}, fmt.Errorf("launcher components: parse manifest: %w", err)
	}
	if doc.URL.Base == "" || doc.Product.Version == "" {
		return Manifest{}, errors.New("launcher components: manifest has no base URL or version")
	}
	m := Manifest{Version: doc.Product.Version, BaseURL: strings.TrimRight(doc.URL.Base, "/")}
	for _, f := range doc.Files {
		size, err := strconv.ParseInt(f.Size, 10, 64)
		if err != nil || size < 0 {
			continue
		}
		zipSize, _ := strconv.ParseInt(f.ZipSize, 10, 64)
		entry := File{Path: f.Path, MD5: strings.ToLower(f.Checksum), Size: size, ZipMD5: strings.ToLower(f.ZipChecksum), ZipSize: zipSize}
		if !validPath(entry.Path) || len(entry.MD5) != 32 {
			continue
		}
		m.Files = append(m.Files, entry)
	}
	return m, nil
}

// Select returns the required files in Required order.
func (m Manifest) Select() ([]File, error) {
	byPath := make(map[string]File, len(m.Files))
	for _, f := range m.Files {
		byPath[strings.ToLower(f.Path)] = f
	}
	out := make([]File, 0, len(Required))
	for _, want := range Required {
		f, ok := byPath[strings.ToLower(want)]
		if !ok {
			return nil, fmt.Errorf("launcher components: manifest is missing %s", want)
		}
		out = append(out, f)
	}
	return out, nil
}

// TotalSize sums the unpacked size of the given files.
func TotalSize(files []File) int64 {
	var total int64
	for _, f := range files {
		if f.Size > 0 && total <= (1<<62)-f.Size {
			total += f.Size
		}
	}
	return total
}

// Present reports whether every required file already matches its manifest
// entry under root\NTELauncher.
func Present(root string, files []File) bool {
	dir := LauncherDir(root)
	for _, f := range files {
		target, err := targetPath(dir, f.Path)
		if err != nil || !sameFile(target, f) {
			return false
		}
	}
	return pointerMatches(root)
}

// Install downloads the missing files and writes GameLauncher.config. progress
// is called with the bytes finished, including the partial current file.
func Install(ctx context.Context, client *http.Client, root string, m Manifest, files []File, progress func(done, total int64)) error {
	if client == nil {
		return errors.New("launcher components: no client")
	}
	dir := LauncherDir(root)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	total := TotalSize(files)
	var done int64
	if progress != nil {
		progress(0, total)
	}
	for _, f := range files {
		target, err := targetPath(dir, f.Path)
		if err != nil {
			return err
		}
		if sameFile(target, f) {
			done += f.Size
			if progress != nil {
				progress(done, total)
			}
			continue
		}
		base := done
		if err := downloadFile(ctx, client, m.BaseURL, f, target, func(written int64) {
			if progress != nil {
				progress(base+min(written, f.Size), total)
			}
		}); err != nil {
			return err
		}
		done = base + f.Size
		if progress != nil {
			progress(done, total)
		}
	}
	return writePointer(root)
}

// LauncherDir is where the vendor's launcher package lives.
func LauncherDir(root string) string { return filepath.Join(root, "NTELauncher") }

func downloadFile(ctx context.Context, client *http.Client, base string, f File, target string, progress func(int64)) error {
	address := base + f.Path + ".zip"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("launcher components: GET %s: %s", address, resp.Status)
	}
	blob, err := io.ReadAll(io.TeeReader(io.LimitReader(resp.Body, maxZipBytes), &countWriter{report: progress}))
	if err != nil {
		return err
	}
	if f.ZipSize > 0 && int64(len(blob)) != f.ZipSize {
		return fmt.Errorf("launcher components: %s: unexpected zip size", f.Path)
	}
	if f.ZipMD5 != "" && md5Hex(blob) != f.ZipMD5 {
		return fmt.Errorf("launcher components: %s: zip checksum mismatch", f.Path)
	}
	zr, err := zip.NewReader(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		return fmt.Errorf("launcher components: %s: %w", f.Path, err)
	}
	if len(zr.File) != 1 {
		return fmt.Errorf("launcher components: %s: archive holds %d entries", f.Path, len(zr.File))
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, f.Size+1))
	if err != nil {
		return err
	}
	if int64(len(data)) != f.Size {
		return fmt.Errorf("launcher components: %s: unexpected size", f.Path)
	}
	if md5Hex(data) != f.MD5 {
		return fmt.Errorf("launcher components: %s: checksum mismatch", f.Path)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".oddward-component-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, target); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func writePointer(root string) error {
	pointer := filepath.Join(root, `Client\WindowsNoEditor\HT\Binaries\Win64\GameLauncher.config`)
	if err := os.MkdirAll(filepath.Dir(pointer), 0700); err != nil {
		return err
	}
	bridge := filepath.Join(LauncherDir(root), "WPGameClientSDK_64.dll")
	text := filepath.ToSlash(bridge)
	tmp, err := os.CreateTemp(filepath.Dir(pointer), ".oddward-pointer-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, pointer); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func pointerMatches(root string) bool {
	pointer := filepath.Join(root, `Client\WindowsNoEditor\HT\Binaries\Win64\GameLauncher.config`)
	data, err := os.ReadFile(pointer)
	if err != nil {
		return false
	}
	bridge, err := filepath.Abs(filepath.FromSlash(strings.TrimSpace(string(data))))
	if err != nil {
		return false
	}
	want, err := filepath.Abs(filepath.Join(LauncherDir(root), "WPGameClientSDK_64.dll"))
	if err != nil {
		return false
	}
	if !strings.EqualFold(filepath.Clean(bridge), filepath.Clean(want)) {
		return false
	}
	_, err = os.Stat(want)
	return err == nil
}

func sameFile(target string, f File) bool {
	s, err := os.Stat(target)
	if err != nil || !s.Mode().IsRegular() || s.Size() != f.Size {
		return false
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return false
	}
	return md5Hex(data) == f.MD5
}

func targetPath(dir, manifestPath string) (string, error) {
	if !validPath(manifestPath) {
		return "", fmt.Errorf("launcher components: unsafe path %q", manifestPath)
	}
	rel := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(manifestPath, "\\", "/")), "/")
	target := filepath.Join(dir, filepath.FromSlash(rel))
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(strings.ToLower(abs), strings.ToLower(base)+string(os.PathSeparator)) {
		return "", fmt.Errorf("launcher components: path escapes the install: %q", manifestPath)
	}
	return abs, nil
}

func validPath(p string) bool {
	if p == "" || strings.ContainsRune(p, 0) || strings.ContainsAny(p, "\r\n") {
		return false
	}
	clean := path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
	if clean == "/" || strings.Contains(clean, "..") || strings.Contains(clean, "//") {
		return false
	}
	return true
}

func get(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("launcher components: GET %s: %s", address, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func iniValue(body []byte, key string) string {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 1024), maxVersionBytes)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+"="))
		}
	}
	return ""
}

type countWriter struct {
	written int64
	report  func(int64)
}

func (w *countWriter) Write(p []byte) (int, error) {
	w.written += int64(len(p))
	if w.report != nil {
		w.report(w.written)
	}
	return len(p), nil
}

func md5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}
