// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nerv-base/Oddward/internal/selfupdate"
)

func TestOnlyInstalledSignedReleasesCheckForUpdates(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(pub)
	root := t.TempDir()
	dir := filepath.Join(root, "app-0.1.0")
	exe := filepath.Join(dir, selfupdate.AppExe)
	for path, body := range map[string]string{filepath.Join(root, selfupdate.StubExe): "MZ", exe: "MZ", filepath.Join(dir, ".ready"): ""} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	portable := filepath.Join(t.TempDir(), "oddward.exe")
	for _, tc := range []struct {
		name, exe, version, key string
		want                    error
	}{
		{"development", exe, "", key, selfupdate.ErrDevelopmentBuild},
		{"unsigned", exe, "0.1.0", "", errNoUpdateKey},
		{"portable", portable, "0.1.0", key, selfupdate.ErrNotInstalled},
		{"wrong folder", exe, "0.2.0", key, selfupdate.ErrNotInstalled},
	} {
		if _, _, err := updateIdentity(tc.exe, tc.version, tc.key); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, err, tc.want)
		}
	}
	if _, _, err := updateIdentity(exe, "0.1.0", "not base64"); err == nil {
		t.Error("accepted a malformed key")
	}
	layout, got, err := updateIdentity(exe, "0.1.0", key)
	if err != nil || layout.Root != root || layout.Dir != dir || !pub.Equal(ed25519.PublicKey(got)) {
		t.Fatalf("installed: %+v %v", layout, err)
	}
}
