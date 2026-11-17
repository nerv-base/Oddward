// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Release asset names. Each release carries the manifest, its signature and
// the app package the manifest describes.
const (
	ManifestAsset  = "update.json"
	SignatureAsset = "update.json.sig"
)

const (
	maxManifest  = 256 << 10
	maxSignature = 1 << 10
	maxNotes     = 16 << 10
	maxFiles     = 256
	maxFileSize  = 256 << 20
	maxPackage   = 512 << 20
	// signingContext separates update signatures from any other use of the key.
	signingContext = "oddward-update-manifest-v1\n"
)

// Manifest is the signed description of one release.
type Manifest struct {
	Schema     int         `json:"schema"`
	Repository string      `json:"repository"`
	Version    string      `json:"version"`
	Published  string      `json:"published"`
	Notes      string      `json:"notes"`
	Package    Asset       `json:"package"`
	Files      []FileEntry `json:"files"`
}

// Asset is the zip release asset holding exactly Files.
type Asset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// FileEntry is one file of the app folder, relative and slash-separated.
type FileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

var (
	assetName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.zip$`)
	pathPart   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9 ._+-]{0,127}$`)
	errUnsafe  = errors.New("selfupdate: manifest is not a safe release description")
	errMissing = errors.New("selfupdate: no update signing key is configured in this build")
)

// Sign validates m and returns its canonical JSON and the base64 signature.
func Sign(priv ed25519.PrivateKey, m Manifest) (data, sig []byte, err error) {
	if err := m.validate(m.Repository); err != nil {
		return nil, nil, err
	}
	data, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return data, signRaw(priv, data), nil
}

func signRaw(priv ed25519.PrivateKey, data []byte) []byte {
	return []byte(encodeSig(ed25519.Sign(priv, append([]byte(signingContext), data...))))
}

func encodeSig(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) + "\n" }

// Verify checks sig over data with pub before parsing anything, then requires
// a well-formed manifest for repository.
func Verify(data, sig []byte, pub ed25519.PublicKey, repository string) (Manifest, error) {
	if len(pub) != ed25519.PublicKeySize {
		return Manifest{}, errMissing
	}
	if len(data) == 0 || len(data) > maxManifest || len(sig) > maxSignature {
		return Manifest{}, errUnsafe
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize || !ed25519.Verify(pub, append([]byte(signingContext), data...), raw) {
		return Manifest{}, errors.New("selfupdate: manifest signature is invalid")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", errUnsafe, err)
	}
	if dec.Decode(new(any)) != io.EOF {
		return Manifest{}, errUnsafe
	}
	if err := m.validate(repository); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// ParsePublicKey decodes a base64 Ed25519 public key; empty yields nil.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("selfupdate: malformed update public key")
	}
	return ed25519.PublicKey(raw), nil
}

func (m Manifest) validate(repository string) error {
	if m.Schema != 1 || repository == "" || m.Repository != repository || !utf8.ValidString(m.Notes) || len(m.Notes) > maxNotes || len(m.Published) > 64 {
		return errUnsafe
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return err
	}
	if !assetName.MatchString(m.Package.Name) || m.Package.Size <= 0 || m.Package.Size > maxPackage || !hexDigest(m.Package.SHA256) {
		return errUnsafe
	}
	if len(m.Files) == 0 || len(m.Files) > maxFiles {
		return errUnsafe
	}
	seen := map[string]bool{}
	launcher := false
	for _, f := range m.Files {
		if !safePath(f.Path) || f.Size < 0 || f.Size > maxFileSize || !hexDigest(f.SHA256) {
			return fmt.Errorf("%w: %q", errUnsafe, f.Path)
		}
		key := strings.ToLower(f.Path)
		if seen[key] {
			return fmt.Errorf("%w: %q", errUnsafe, f.Path)
		}
		seen[key] = true
		launcher = launcher || f.Path == AppExe
	}
	if !launcher {
		return fmt.Errorf("%w: %s missing", errUnsafe, AppExe)
	}
	return nil
}

// safePath allows only plain relative names; markers start with "." and so
// can never be shipped as files.
func safePath(p string) bool {
	if p == "" || len(p) > 260 {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if !pathPart.MatchString(part) || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
	}
	return true
}

func hexDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && strings.ToLower(s) == s
}
