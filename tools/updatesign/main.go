// SPDX-License-Identifier: GPL-3.0-or-later

// Updatesign creates the update signing key and signs release manifests.
// It is release tooling, not part of the shipped launcher.
//
//	updatesign keygen
//	updatesign release -app DIR -version X.Y.Z -notes FILE -out DIR
//
// release reads the private key from ODDWARD_UPDATE_KEY and refuses to sign
// unless it matches the public key compiled into this build.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nerv-base/Oddward/internal/buildinfo"
	"github.com/nerv-base/Oddward/internal/selfupdate"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen()
	case "release":
		fs := flag.NewFlagSet("release", flag.ExitOnError)
		var o options
		fs.StringVar(&o.app, "app", "", "app folder to package")
		fs.StringVar(&o.version, "version", "", "release version without v")
		fs.StringVar(&o.notes, "notes", "", "UTF-8 release notes file")
		fs.StringVar(&o.out, "out", "", "output folder")
		fs.Parse(os.Args[2:])
		o.repository, o.publicKey, o.privateKey = buildinfo.Repository, buildinfo.UpdatePublicKey, os.Getenv("ODDWARD_UPDATE_KEY")
		o.now = time.Now
		err = release(o)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "updatesign:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: updatesign keygen | release -app DIR -version X.Y.Z -notes FILE -out DIR")
	os.Exit(2)
}

func keygen() error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Println("public:  " + base64.StdEncoding.EncodeToString(pub))
	fmt.Println("private: " + base64.StdEncoding.EncodeToString(priv.Seed()))
	return nil
}

type options struct {
	app, version, notes, out string
	repository, publicKey    string
	privateKey               string
	now                      func() time.Time
}

// release packs the app folder and writes the package, update.json and
// update.json.sig into out.
func release(o options) error {
	pub, err := selfupdate.ParsePublicKey(o.publicKey)
	if err != nil {
		return err
	}
	if pub == nil {
		return errors.New("this build has no update public key; run scripts/new-update-key.ps1 and commit the public key first")
	}
	seed, err := base64.StdEncoding.DecodeString(o.privateKey)
	if err != nil || len(seed) != ed25519.SeedSize {
		return errors.New("ODDWARD_UPDATE_KEY must be the base64 private seed printed by keygen")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	if !pub.Equal(priv.Public()) {
		return errors.New("ODDWARD_UPDATE_KEY does not match the public key compiled into this build")
	}
	if _, err := selfupdate.ParseVersion(o.version); err != nil {
		return err
	}
	notes, err := os.ReadFile(o.notes)
	if err != nil {
		return err
	}
	var pkg bytes.Buffer
	files, err := selfupdate.Pack(o.app, &pkg)
	if err != nil {
		return err
	}
	name := "oddward-" + o.version + "-app.zip"
	m := selfupdate.Manifest{Schema: 1, Repository: o.repository, Version: o.version,
		Published: o.now().UTC().Format(time.RFC3339), Notes: string(bytes.TrimSpace(notes)),
		Package: selfupdate.Asset{Name: name, Size: int64(pkg.Len()), SHA256: sha(pkg.Bytes())}, Files: files}
	data, sig, err := selfupdate.Sign(priv, m)
	if err != nil {
		return err
	}
	if _, err := selfupdate.Verify(data, sig, pub, o.repository); err != nil {
		return err
	}
	if err := os.MkdirAll(o.out, 0o755); err != nil {
		return err
	}
	for file, body := range map[string][]byte{name: pkg.Bytes(), selfupdate.ManifestAsset: data, selfupdate.SignatureAsset: sig} {
		if err := os.WriteFile(filepath.Join(o.out, file), body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
