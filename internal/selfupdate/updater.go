// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import (
	"archive/zip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrNoSpace means the disk cannot hold the package and the unpacked folder.
var ErrNoSpace = errors.New("磁盘空间不足，无法下载更新")

// spaceMargin is kept free on top of the package and the unpacked files.
const spaceMargin = 64 << 20

// Updater checks GitHub Releases for a signed manifest and stages new app
// folders next to the running one. It never modifies the running version.
type Updater struct {
	HTTP       *http.Client
	Repository string
	PublicKey  ed25519.PublicKey
	Layout     Layout
	// FreeSpace reports free bytes on the volume holding a path; nil uses
	// the operating system.
	FreeSpace func(string) (uint64, error)
}

func (u *Updater) assetURL(tag, name string) string {
	if tag == "" {
		return "https://github.com/" + u.Repository + "/releases/latest/download/" + name
	}
	return "https://github.com/" + u.Repository + "/releases/download/" + tag + "/" + name
}

// Check fetches the latest signed manifest and reports whether it is newer
// than the running version. A build without a signing key never fetches.
func (u *Updater) Check(ctx context.Context) (Manifest, bool, error) {
	if len(u.PublicKey) != ed25519.PublicKeySize {
		return Manifest{}, false, errMissing
	}
	data, err := u.fetch(ctx, u.assetURL("", ManifestAsset), maxManifest)
	if err != nil {
		return Manifest{}, false, err
	}
	sig, err := u.fetch(ctx, u.assetURL("", SignatureAsset), maxSignature)
	if err != nil {
		return Manifest{}, false, err
	}
	m, err := Verify(data, sig, u.PublicKey, u.Repository)
	if err != nil {
		return Manifest{}, false, err
	}
	v, _ := ParseVersion(m.Version)
	return m, v.Compare(u.Layout.Version) > 0, nil
}

// Stage makes app-<version> ready beside the running version: it downloads
// the package, checks its digest, unpacks exactly the listed files, checks
// each one and only then renames the folder into place. progress, if set,
// receives downloaded bytes.
func (u *Updater) Stage(ctx context.Context, m Manifest, progress func(done, total int64)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := m.validate(u.Repository); err != nil {
		return "", err
	}
	root := u.Layout.Root
	dir := filepath.Join(root, appPrefix+m.Version)
	if filepath.Clean(dir) == filepath.Clean(u.Layout.Dir) {
		return "", errors.New("selfupdate: refusing to restage the running version")
	}
	if _, err := os.Lstat(dir); err == nil {
		if verifyDir(dir, m) == nil {
			return dir, nil
		}
		if err := removeOwned(root, dir); err != nil {
			return "", err
		}
	}
	if err := u.ensureSpace(root, m); err != nil {
		return "", err
	}
	pkg, err := u.download(ctx, m, progress)
	if err != nil {
		return "", err
	}
	defer os.Remove(pkg)
	staging, err := os.MkdirTemp(root, appPrefix+m.Version+partialInfix+"*")
	if err != nil {
		return "", err
	}
	if err := unpack(ctx, pkg, staging, m); err != nil {
		removeOwned(root, staging)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(staging, readyMarker), []byte(m.Version), 0o600); err != nil {
		removeOwned(root, staging)
		return "", err
	}
	if err := os.Rename(staging, dir); err != nil {
		removeOwned(root, staging)
		return "", err
	}
	return dir, nil
}

func (u *Updater) ensureSpace(root string, m Manifest) error {
	need := uint64(m.Package.Size) + spaceMargin
	for _, f := range m.Files {
		need += uint64(f.Size)
	}
	free := u.FreeSpace
	if free == nil {
		free = freeSpace
	}
	have, err := free(root)
	if err != nil {
		return err
	}
	if have < need {
		return fmt.Errorf("%w：需要 %d MB，可用 %d MB", ErrNoSpace, need>>20, have>>20)
	}
	return nil
}

func (u *Updater) client() *http.Client {
	c := *u.HTTP
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errors.New("selfupdate: refusing a non-HTTPS redirect")
		}
		if len(via) >= 10 {
			return errors.New("selfupdate: too many redirects")
		}
		return nil
	}
	return &c
}

func (u *Updater) open(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Oddward/"+u.Layout.Version.String())
	res, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("selfupdate: %s returned %s", url, res.Status)
	}
	return res, nil
}

func (u *Updater) fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	res, err := u.open(ctx, url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errUnsafe
	}
	return body, nil
}

// download writes the package under .updates and returns its path once its
// size and digest match the manifest.
func (u *Updater) download(ctx context.Context, m Manifest, progress func(done, total int64)) (string, error) {
	dir := filepath.Join(u.Layout.Root, updatesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	res, err := u.open(ctx, u.assetURL("v"+m.Version, m.Package.Name))
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	f, err := os.CreateTemp(dir, m.Package.SHA256+".zip"+partialInfix+"*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	h := sha256.New()
	w := &progressWriter{ctx: ctx, w: io.MultiWriter(f, h), total: m.Package.Size, report: progress}
	n, err := io.Copy(w, io.LimitReader(res.Body, m.Package.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && (n != m.Package.Size || hex.EncodeToString(h.Sum(nil)) != m.Package.SHA256) {
		err = errors.New("selfupdate: package does not match the signed manifest")
	}
	if err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

type progressWriter struct {
	ctx    context.Context
	w      io.Writer
	done   int64
	total  int64
	report func(done, total int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.w.Write(b)
	p.done += int64(n)
	if p.report != nil {
		p.report(p.done, p.total)
	}
	return n, err
}

// unpack extracts exactly the files the manifest lists into dir.
func unpack(ctx context.Context, pkg, dir string, m Manifest) error {
	r, err := zip.OpenReader(pkg)
	if err != nil {
		return err
	}
	defer r.Close()
	want := map[string]FileEntry{}
	for _, f := range m.Files {
		want[strings.ToLower(f.Path)] = f
	}
	seen := map[string]bool{}
	for _, zf := range r.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasSuffix(zf.Name, "/") && zf.UncompressedSize64 == 0 {
			continue // directory entry; files below are checked on their own
		}
		key := strings.ToLower(zf.Name)
		entry, ok := want[key]
		if !ok || seen[key] || path.Clean(zf.Name) != zf.Name || !zf.Mode().IsRegular() {
			return fmt.Errorf("selfupdate: package holds an unlisted entry %q", zf.Name)
		}
		seen[key] = true
		if err := extract(zf, filepath.Join(dir, filepath.FromSlash(entry.Path)), entry); err != nil {
			return err
		}
	}
	if len(seen) != len(want) {
		return errors.New("selfupdate: package is missing listed files")
	}
	return nil
}

func extract(zf *zip.File, dst string, entry FileEntry) error {
	if zf.UncompressedSize64 != uint64(entry.Size) {
		return fmt.Errorf("selfupdate: %s does not match the signed manifest", entry.Path)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	src, err := zf.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(src, entry.Size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && (n != entry.Size || hex.EncodeToString(h.Sum(nil)) != entry.SHA256) {
		err = fmt.Errorf("selfupdate: %s does not match the signed manifest", entry.Path)
	}
	return err
}

// verifyDir accepts an existing folder only if it is ready, not failed, and
// holds exactly the manifest's files with matching digests.
func verifyDir(dir string, m Manifest) error {
	if !regular(filepath.Join(dir, readyMarker)) || regular(filepath.Join(dir, failedMarker)) {
		return errors.New("selfupdate: folder is not ready")
	}
	want := map[string]FileEntry{}
	for _, f := range m.Files {
		want[strings.ToLower(f.Path)] = f
	}
	found := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Type()&fs.ModeSymlink != 0 {
				return errors.New("selfupdate: link inside app folder")
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		switch rel {
		case readyMarker, healthyMarker:
			return nil
		}
		entry, ok := want[strings.ToLower(rel)]
		if !ok || !d.Type().IsRegular() {
			return fmt.Errorf("selfupdate: unexpected %s", rel)
		}
		found++
		return fileMatches(p, entry)
	})
	if err != nil {
		return err
	}
	if found != len(want) {
		return errors.New("selfupdate: app folder is incomplete")
	}
	return nil
}

func fileMatches(p string, entry FileEntry) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, entry.Size+1))
	if err != nil {
		return err
	}
	if n != entry.Size || hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
		return fmt.Errorf("selfupdate: %s differs from the manifest", entry.Path)
	}
	return nil
}
