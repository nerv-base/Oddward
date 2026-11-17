// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/selfupdate"
)

func fixture(t *testing.T) options {
	t.Helper()
	seed := bytes.Repeat([]byte{9}, ed25519.SeedSize)
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	app := t.TempDir()
	os.WriteFile(filepath.Join(app, selfupdate.AppExe), []byte("MZ"), 0o600)
	notes := filepath.Join(t.TempDir(), "notes.md")
	os.WriteFile(notes, []byte("修复启动问题\n"), 0o600)
	return options{app: app, version: "0.2.0", notes: notes, out: t.TempDir(), repository: "nerv-base/oddward",
		publicKey: base64.StdEncoding.EncodeToString(pub), privateKey: base64.StdEncoding.EncodeToString(seed),
		now: func() time.Time { return time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC) }}
}

func TestReleaseWritesAVerifiableManifest(t *testing.T) {
	o := fixture(t)
	if err := release(o); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(o.out, selfupdate.ManifestAsset))
	sig, _ := os.ReadFile(filepath.Join(o.out, selfupdate.SignatureAsset))
	pub, _ := selfupdate.ParsePublicKey(o.publicKey)
	m, err := selfupdate.Verify(data, sig, pub, o.repository)
	if err != nil {
		t.Fatal(err)
	}
	pkg, _ := os.ReadFile(filepath.Join(o.out, "oddward-0.2.0-app.zip"))
	if m.Version != "0.2.0" || m.Notes != "修复启动问题" || m.Published != "2026-11-13T04:00:00Z" || m.Package.SHA256 != sha(pkg) {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestReleaseRefusesWithoutTheMatchingKey(t *testing.T) {
	for name, change := range map[string]func(*options){
		"no public key":  func(o *options) { o.publicKey = "" },
		"no private key": func(o *options) { o.privateKey = "" },
		"other key": func(o *options) {
			o.privateKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, ed25519.SeedSize))
		},
		"bad version": func(o *options) { o.version = "v0.2" },
	} {
		t.Run(name, func(t *testing.T) {
			o := fixture(t)
			change(&o)
			if err := release(o); err == nil {
				t.Fatal("signed")
			}
			if entries, _ := os.ReadDir(o.out); len(entries) != 0 {
				t.Fatalf("wrote %v", entries)
			}
		})
	}
	if !strings.Contains(release(func() options { o := fixture(t); o.publicKey = ""; return o }()).Error(), "new-update-key") {
		t.Fatal("missing key error should point at the key script")
	}
}
