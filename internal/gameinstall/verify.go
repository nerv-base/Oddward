// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nerv-base/Oddward/internal/gamelocate"
)

type verifyEntry struct {
	Path       string    `json:"path"`
	MD5        string    `json:"md5"`
	Size       int64     `json:"size"`
	Mtime      time.Time `json:"mtime"`
	FileID     string    `json:"fileID"`
	VerifiedAt time.Time `json:"verifiedAt"`
	Source     string    `json:"source"`
}
type verifyDocument struct {
	Schema         int           `json:"schema"`
	InstallKey     string        `json:"installKey"`
	ManifestDigest string        `json:"manifestDigest"`
	Entries        []verifyEntry `json:"entries"`
}

func (s *Store) verifyRead(record InstallRecord) (verifyDocument, error) {
	empty := verifyDocument{Schema: 1, InstallKey: record.InstallKey, ManifestDigest: record.ManifestDigest}
	r, err := os.OpenRoot(s.stateRoot)
	if err != nil {
		return empty, err
	}
	defer r.Close()
	b, err := stateReadBounded(r, record.InstallKey+"/verify.json")
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	var doc verifyDocument
	if err := stateDecodeJSON(b, &doc); err != nil {
		return empty, fmt.Errorf("%w: verification record", ErrInvalidState)
	}
	if doc.Schema != 1 || doc.InstallKey != record.InstallKey || len(doc.Entries) > 65536 {
		return empty, ErrInvalidState
	}
	if doc.ManifestDigest != record.ManifestDigest {
		return empty, nil
	}
	seen := map[string]bool{}
	for _, e := range doc.Entries {
		if !stateValidMD5(e.MD5) || e.Size <= 0 || e.VerifiedAt.IsZero() || e.Mtime.IsZero() || e.FileID == "" || e.Source != "verify" || seen[e.Path] {
			return empty, ErrInvalidState
		}
		seen[e.Path] = true
	}
	return doc, nil
}
func verifyFingerprint(f *os.File) (int64, time.Time, string, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, time.Time{}, "", err
	}
	if !st.Mode().IsRegular() {
		return 0, time.Time{}, "", ErrInvalidState
	}
	id, err := verifyFileID(f)
	return st.Size(), st.ModTime().UTC(), id, err
}
func verifyMatches(r *os.Root, t InstallTarget, e verifyEntry) bool {
	if e.Path != t.Path || e.MD5 != t.MD5 || e.Size != t.Size {
		return false
	}
	f, err := r.Open(t.Path)
	if err != nil {
		return false
	}
	defer f.Close()
	size, mtime, id, err := verifyFingerprint(f)
	return err == nil && size == e.Size && mtime.Equal(e.Mtime) && id == e.FileID
}
func verifyCheckOfficial(r *os.Root, record InstallRecord) error {
	if record.OfficialDigest == "" {
		return nil
	}
	c, err := stateReadBounded(r, adoptOfficialDir+"config.xml")
	if err != nil {
		return fmt.Errorf("%w: official config changed", ErrStaleState)
	}
	m, err := stateReadBounded(r, adoptOfficialDir+"ResList.xml")
	if err != nil {
		return fmt.Errorf("%w: official manifest changed", ErrStaleState)
	}
	sum := adoptDigest(append(append(c, 0), m...))
	if sum != record.OfficialDigest {
		return ErrStaleState
	}
	return nil
}

// TrustedTargets checks persisted full-file verification against current size,
// mtime and file ID. A published full baseline without a matching fingerprint
// entry is rehashed for read-only planning; this method never writes state.
// Neither path grants destructive commit permission.
func (s *Store) TrustedTargets(ctx context.Context, root string) ([]InstallTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, err := s.ReadInstall(root)
	if err != nil {
		return nil, err
	}
	if record.BaselineMismatch {
		return nil, ErrStaleState
	}
	r, err := os.OpenRoot(record.Root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if err := verifyCheckOfficial(r, record); err != nil {
		return nil, err
	}
	doc, err := s.verifyRead(record)
	if err != nil {
		return nil, err
	}
	entries := map[string]verifyEntry{}
	for _, e := range doc.Entries {
		entries[e.Path] = e
	}
	var out []InstallTarget
	for _, t := range record.Targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry, exists := entries[t.Path]
		if verifyMatches(r, t, entry) {
			out = append(out, t)
			continue
		}
		if !exists && record.State == "ready" && record.VerifyCoverage == "full" && record.VerifiedTargets == len(record.Targets) {
			_, mismatch, err := verifyHashFile(ctx, r, t)
			if err != nil {
				return nil, err
			}
			if mismatch == "" {
				out = append(out, t)
			}
		}
	}
	return out, nil
}

// VerifyInstallation persists each completed file before reporting progress.
// Cancelation resumes at file boundaries; a partial file never enters the cache.
func (s *Store) VerifyInstallation(ctx context.Context, selected string, report func(VerifyReport)) (VerifyResult, error) {
	if err := ctx.Err(); err != nil {
		return VerifyResult{}, err
	}
	record, err := s.ReadInstall(selected)
	if err != nil || record.Mode == "adopted" {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return VerifyResult{}, err
		}
		installation, inspectErr := gamelocate.Inspect(ctx, selected)
		if inspectErr != nil {
			return VerifyResult{}, inspectErr
		}
		record, err = s.ReadInstall(installation.Root)
		if err != nil {
			return VerifyResult{}, err
		}
		if record.Branch != installation.Branch {
			return VerifyResult{}, ErrStaleState
		}
	}
	result := VerifyResult{Record: record, Files: len(record.Targets)}
	if len(record.Targets) == 0 {
		return result, fmt.Errorf("%w: no resource baseline", ErrStaleState)
	}
	if record.BaselineMismatch {
		return result, ErrStaleState
	}
	r, err := os.OpenRoot(record.Root)
	if err != nil {
		return result, err
	}
	defer r.Close()
	if err := verifyCheckOfficial(r, record); err != nil {
		return result, err
	}
	doc, err := s.verifyRead(record)
	if err != nil {
		return result, err
	}
	previous := map[string]verifyEntry{}
	for _, entry := range doc.Entries {
		previous[entry.Path] = entry
	}
	// An unfinished audit does not revoke an existing transaction publication.
	// Nonpublished adoption claims are downgraded until the run completes.
	published := record.State == "ready"
	if !published {
		record.State = Adopted
		record.VerifyCoverage = "metadata"
		record.VerifiedTargets = 0
		if err := s.writeInstall(record); err != nil {
			return result, err
		}
	}
	result.Record = record
	var done, total int64
	for _, t := range record.Targets {
		total += t.Size
	}
	for i, target := range record.Targets {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		entry := previous[target.Path]
		reused := verifyMatches(r, target, entry)
		var mismatch string
		if reused {
			result.Reused++
		} else {
			entry, mismatch, err = verifyHashFile(ctx, r, target)
			if err != nil {
				return result, err
			}
		}
		if mismatch != "" {
			delete(previous, target.Path)
			result.Mismatches = append(result.Mismatches, Mismatch{target.Path, mismatch})
		} else {
			previous[target.Path] = entry
			result.Verified++
		}
		// Keep only current baseline paths, including completed files from prior runs.
		doc.Entries = nil
		for _, t := range record.Targets {
			if e, ok := previous[t.Path]; ok {
				doc.Entries = append(doc.Entries, e)
			}
		}
		if err := s.stateWrite(record.InstallKey+"/verify.json", doc, nil); err != nil {
			return result, err
		}
		done += target.Size
		if report != nil {
			report(VerifyReport{Path: target.Path, FilesDone: i + 1, FilesTotal: len(record.Targets), BytesDone: done, BytesTotal: total, Reused: reused})
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := verifyCheckOfficial(r, record); err != nil {
		return result, err
	}
	// Recheck all fingerprints: an early file may change while later files hash.
	for _, t := range record.Targets {
		if !verifyMatches(r, t, previous[t.Path]) && !verifyHasMismatch(result.Mismatches, t.Path) {
			result.Mismatches = append(result.Mismatches, Mismatch{t.Path, "changed_during_verification"})
			result.Verified--
		}
	}
	record.VerifiedTargets = result.Verified
	if len(result.Mismatches) == 0 {
		if published {
			record.State = "ready"
		} else {
			record.State = Verified
		}
		record.VerifyCoverage = "full"
	} else {
		record.State = Detected
	}
	if err := s.writeInstall(record); err != nil {
		return result, err
	}
	result.Record = record
	return result, nil
}
func verifyHasMismatch(m []Mismatch, path string) bool {
	for _, v := range m {
		if v.Path == path {
			return true
		}
	}
	return false
}
func verifyHashFile(ctx context.Context, r *os.Root, target InstallTarget) (verifyEntry, string, error) {
	f, err := r.Open(target.Path)
	if errors.Is(err, os.ErrNotExist) {
		return verifyEntry{}, "missing", nil
	}
	if err != nil {
		return verifyEntry{}, "", err
	}
	defer f.Close()
	size, mtime, id, err := verifyFingerprint(f)
	if errors.Is(err, ErrInvalidState) {
		return verifyEntry{}, "not_regular", nil
	}
	if err != nil {
		return verifyEntry{}, "", err
	}
	if size != target.Size {
		return verifyEntry{}, "size", nil
	}
	h := md5.New()
	buf := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			return verifyEntry{}, "", err
		}
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return verifyEntry{}, "", err
		}
	}
	afterSize, afterTime, afterID, err := verifyFingerprint(f)
	if err != nil {
		return verifyEntry{}, "", err
	}
	if afterSize != size || !afterTime.Equal(mtime) || afterID != id {
		return verifyEntry{}, "changed_during_verification", nil
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != target.MD5 {
		return verifyEntry{}, "md5", nil
	}
	entry := verifyEntry{Path: target.Path, MD5: target.MD5, Size: size, Mtime: mtime, FileID: id, VerifiedAt: time.Now().UTC(), Source: "verify"}
	if !verifyMatches(r, target, entry) {
		return verifyEntry{}, "changed_during_verification", nil
	}
	return entry, "", nil
}
