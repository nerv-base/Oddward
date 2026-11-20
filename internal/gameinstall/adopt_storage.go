// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/gameres"
)

const stateMaxBytes = 8 << 20

// NewStore uses a shared installs directory; each canonical root gets its own key.
// Records live in the database, the shared directory keeps the journals, the
// verification cache and the legacy mirror of every installation.
func NewStore(db *database.DB, stateRoot string) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: installation store needs an open database", ErrInvalidState)
	}
	if !filepath.IsAbs(stateRoot) {
		return nil, fmt.Errorf("%w: state root must be absolute", ErrInvalidState)
	}
	stateRoot = filepath.Clean(stateRoot)
	if err := os.MkdirAll(stateRoot, 0700); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, err
	}
	r.Close()
	return &Store{db: db, stateRoot: stateRoot}, nil
}

// InstallKey identifies a canonical parent-of-Client path without touching disk.
// Callers normalize an existing selection with gamelocate.Inspect first.
func InstallKey(root string) (string, error) {
	if !filepath.IsAbs(root) || strings.ContainsAny(root, "\x00\r\n") {
		return "", fmt.Errorf("%w: root must be absolute", ErrInvalidState)
	}
	canonical := filepath.Clean(root)
	if runtime.GOOS == "windows" {
		if len(filepath.VolumeName(canonical)) != 2 || strings.HasPrefix(canonical, `\\`) {
			return "", ErrInvalidState
		}
		canonical = strings.ToUpper(canonical)
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:16], nil
}
func stateValidMD5(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 16 && s == strings.ToLower(s)
}
func stateValidateRecord(r InstallRecord) error {
	key, err := InstallKey(r.Root)
	if err != nil || key != r.InstallKey || r.Schema != 2 || r.CreatedAt.IsZero() {
		return ErrInvalidState
	}
	switch r.Mode {
	case "adopted", "oddward", "fresh":
	default:
		return ErrInvalidState
	}
	switch r.State {
	case Detected, Adopted, Verified, "ready", "installing", "incomplete", "recovery_required":
	default:
		return ErrInvalidState
	}
	switch r.VerifyCoverage {
	case "none", "metadata", "full":
	default:
		return ErrInvalidState
	}
	if len(r.Branch) == 0 || len(r.Branch) > 64 || strings.ContainsAny(r.Branch, "/\\:") || strings.Contains(r.Branch, "..") || r.VerifiedTargets < 0 || r.VerifiedTargets > 65536 || len(r.Targets) > 65536 {
		return ErrInvalidState
	}
	if r.ListHash != "" && !stateValidMD5(r.ListHash) || r.ManifestDigest != "" && !stateValidMD5(r.ManifestDigest) {
		return ErrInvalidState
	}
	if r.State == Verified && (len(r.Targets) == 0 || r.VerifyCoverage != "full" || r.VerifiedTargets != len(r.Targets)) {
		return ErrInvalidState
	}
	seen := map[string]bool{}
	for _, t := range r.Targets {
		p, err := gameres.NormalizePath(t.Path)
		key := strings.ToLower(p)
		if err != nil || p != t.Path || !strings.HasPrefix(key, "client/windowsnoeditor/") || !stateValidMD5(t.MD5) || t.Size <= 0 || t.Size > 32<<30 || seen[key] {
			return ErrInvalidState
		}
		seen[key] = true
	}
	return nil
}
func (s *Store) stateWrite(name string, value any, beforeRename func() error) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > stateMaxBytes {
		return ErrInvalidState
	}
	r, err := os.OpenRoot(s.stateRoot)
	if err != nil {
		return err
	}
	defer r.Close()
	dir := filepath.Dir(name)
	if err := r.MkdirAll(dir, 0700); err != nil {
		return err
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := filepath.Join(dir, ".state-"+hex.EncodeToString(nonce[:])+".tmp")
	f, err := r.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer r.Remove(temp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if beforeRename != nil {
		if err := beforeRename(); err != nil {
			return err
		}
	}
	return r.Rename(temp, name)
}
func stateReadBounded(root *os.Root, name string) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > stateMaxBytes {
		return nil, ErrInvalidState
	}
	b, err := io.ReadAll(io.LimitReader(f, stateMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > stateMaxBytes {
		return nil, ErrInvalidState
	}
	return b, nil
}
func stateDecodeJSON(data []byte, out any) error {
	if !utf8.Valid(data) {
		return ErrInvalidState
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := stateJSONValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalidState
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	return nil
}
func stateJSONValue(d *json.Decoder, depth int) error {
	if depth > 32 {
		return ErrInvalidState
	}
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			tok, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := tok.(string)
			if !ok || seen[key] {
				return ErrInvalidState
			}
			seen[key] = true
			if err := stateJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := stateJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrInvalidState
	}
	_, err = d.Token()
	return err
}
func installRootFileID(root string) (string, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer r.Close()
	f, err := r.Open(".")
	if err != nil {
		return "", err
	}
	defer f.Close()
	return verifyFileID(f)
}
