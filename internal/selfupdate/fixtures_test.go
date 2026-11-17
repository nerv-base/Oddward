// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

const testRepo = "nerv-base/oddward"

// testKey is a fixed test-only key pair; it signs nothing outside tests.
func testKey() (ed25519.PublicKey, ed25519.PrivateKey) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	return priv.Public().(ed25519.PublicKey), priv
}

type fixtureFile struct {
	path string
	body []byte
}

func appFiles(version string) []fixtureFile {
	return []fixtureFile{
		{AppExe, []byte("MZ launcher " + version)},
		{"licenses/loginhost/Upstream.MIT.txt", []byte("MIT notice")},
	}
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func zipOf(t *testing.T, files []fixtureFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range files {
		e, err := w.Create(f.path)
		if err != nil {
			t.Fatal(err)
		}
		e.Write(f.body)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// release builds a signed manifest and its package for version.
func release(t *testing.T, version string, files []fixtureFile) (Manifest, []byte, []byte, []byte) {
	t.Helper()
	pkg := zipOf(t, files)
	m := Manifest{Schema: 1, Repository: testRepo, Version: version, Published: "2026-11-13T00:00:00Z", Notes: "修复若干问题",
		Package: Asset{Name: "oddward-" + version + "-app.zip", Size: int64(len(pkg)), SHA256: digest(pkg)}}
	for _, f := range files {
		m.Files = append(m.Files, FileEntry{Path: f.path, Size: int64(len(f.body)), SHA256: digest(f.body)})
	}
	_, priv := testKey()
	data, sig, err := Sign(priv, m)
	if err != nil {
		t.Fatal(err)
	}
	return m, data, sig, pkg
}

// installed lays out root/Oddward.exe and a healthy root/app-<version>.
func installed(t *testing.T, version string) (root string, layout Layout) {
	t.Helper()
	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, StubExe), []byte("MZ stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := writeApp(t, root, version, appFiles(version))
	if err := MarkHealthy(dir); err != nil {
		t.Fatal(err)
	}
	l, err := Discover(filepath.Join(dir, AppExe), version)
	if err != nil {
		t.Fatal(err)
	}
	return root, l
}

func writeApp(t *testing.T, root, version string, files []fixtureFile) string {
	t.Helper()
	dir := filepath.Join(root, appPrefix+version)
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.path))
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, f.body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, readyMarker), []byte(version), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
