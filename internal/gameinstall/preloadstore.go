// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nerv-base/Oddward/internal/gameres"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type PreloadStore struct {
	StateRoot, WorkRoot string
	// EngineCache retains a dedicated downloader's cache in place beneath the
	// per-version subtree. The default remains the standalone blobs layout.
	EngineCache bool
}

// Transition persists an allowed state edge. The caller owns all side effects:
// only transition applying→completed after its installation transaction commits.
func (s PreloadStore) Transition(ctx context.Context, r PreloadRecord, next PreloadState, now time.Time) (PreloadRecord, error) {
	if e := ctx.Err(); e != nil {
		return r, e
	}
	if e := preloadValidateRecord(r); e != nil {
		return r, e
	}
	if now.IsZero() || now.Before(r.UpdatedAt) {
		return r, ErrPreloadRecord
	}
	allowed := false
	switch r.State {
	case PreloadUnavailable:
		allowed = next == PreloadAvailable
	case PreloadAvailable:
		allowed = next == PreloadPlanning || next == PreloadUnavailable
	case PreloadPlanning:
		allowed = next == PreloadDownloading || next == PreloadFailed || next == PreloadStale
	case PreloadDownloading:
		allowed = next == PreloadPaused || next == PreloadVerifying || next == PreloadFailed || next == PreloadStale
	case PreloadPaused:
		allowed = next == PreloadDownloading || next == PreloadStale
	case PreloadVerifying:
		allowed = next == PreloadStaged || next == PreloadDownloading || next == PreloadPaused || next == PreloadFailed
	case PreloadStaged:
		allowed = next == PreloadWaitingRelease
	case PreloadWaitingRelease:
		allowed = next == PreloadReconcile || next == PreloadStale
	case PreloadReconcile:
		allowed = next == PreloadApplicable || next == PreloadPlanning || next == PreloadWaitingRelease || next == PreloadStale
	case PreloadApplicable:
		allowed = next == PreloadApplying || next == PreloadReconcile || next == PreloadStale
	case PreloadApplying:
		allowed = next == PreloadCompleted || next == PreloadStale || next == PreloadRecoveryRequired
	case PreloadStale:
		allowed = next == PreloadAvailable || next == PreloadUnavailable || next == PreloadPlanning
	case PreloadFailed:
		allowed = next == PreloadPlanning || next == PreloadStale
	case PreloadRecoveryRequired:
		allowed = next == PreloadStale || next == PreloadCompleted
	}
	if !allowed {
		return r, fmt.Errorf("%w: transition %s to %s", ErrPreloadBlocked, r.State, next)
	}
	if next == PreloadStaged {
		for _, f := range r.Files {
			if f.State != PreloadFileStaged {
				return r, ErrPreloadBlocked
			}
		}
	}
	r.State = next
	r.UpdatedAt = now
	if e := s.Save(ctx, r); e != nil {
		return r, e
	}
	return r, nil
}

var ErrPreloadRecord = errors.New("invalid preload record")

func preloadMD5OK(s string) bool {
	if len(s) != 32 || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func preloadObjectOK(o gameres.Object) bool {
	return preloadMD5OK(o.MD5) && o.Size > 0 && o.Size <= 1<<40
}
func preloadStateOK(s PreloadState) bool {
	switch s {
	case PreloadUnavailable, PreloadAvailable, PreloadPlanning, PreloadDownloading, PreloadPaused, PreloadVerifying, PreloadStaged, PreloadWaitingRelease, PreloadReconcile, PreloadApplicable, PreloadApplying, PreloadCompleted, PreloadStale, PreloadRecoveryRequired, PreloadFailed:
		return true
	}
	return false
}
func preloadValidateRecord(r PreloadRecord) error {
	if len(r.TaskID) > 128 || len(r.ApplyTxnID) > 128 || strings.ContainsAny(r.TaskID+r.ApplyTxnID, "/\\\\\x00\r\n") || len(r.ObservedFormalVersions) > 64 {
		return ErrPreloadRecord
	}
	observed := map[string]bool{}
	for _, v := range r.ObservedFormalVersions {
		if _, ok := preloadCompareVersion(v, "0"); !ok || observed[v] {
			return ErrPreloadRecord
		}
		observed[v] = true
	}
	if r.Schema != 1 || !preloadStateOK(r.State) || !preloadBranchOK(r.Branch) || !preloadMD5OK(r.ListHash) || r.SavedAt.IsZero() || r.UpdatedAt.Before(r.SavedAt) || len(r.Targets) == 0 || len(r.Targets) > 65536 || len(r.Files) > 131072 {
		return ErrPreloadRecord
	}
	if _, ok := preloadCompareVersion(r.Version, "0"); !ok {
		return ErrPreloadRecord
	}
	if r.Route != PreloadRouteFull && r.Route != PreloadRoutePatch && r.Route != PreloadRouteMixed {
		return ErrPreloadRecord
	}
	seen := map[string]bool{}
	for _, t := range r.Targets {
		p, e := gameres.NormalizePath(t.Path)
		if e != nil || p != t.Path || seen[strings.ToLower(p)] || !preloadObjectOK(t.Object) {
			return ErrPreloadRecord
		}
		seen[strings.ToLower(p)] = true
		if t.Action < gameres.ActionReuseInstalled || t.Action > gameres.ActionFull {
			return ErrPreloadRecord
		}
	}
	files := map[string]bool{}
	for _, f := range r.Files {
		if !preloadObjectOK(f.Object) || f.VerificationFailures < 0 || f.VerificationFailures > 3 || f.State != PreloadFileDownloading && f.State != PreloadFileStaged {
			return ErrPreloadRecord
		}
		if f.Kind == PreloadBlobFull {
			p, e := gameres.NormalizePath(f.Path)
			if e != nil || p != f.Path {
				return ErrPreloadRecord
			}
		} else if f.Kind != PreloadBlobPatch || f.Path != "" {
			return ErrPreloadRecord
		}
		key := string(f.Kind) + "/" + strings.ToLower(f.Path) + "/" + f.Object.String()
		if files[key] {
			return ErrPreloadRecord
		}
		files[key] = true
	}
	for _, o := range r.Patches {
		if !preloadObjectOK(o) {
			return ErrPreloadRecord
		}
	}
	return nil
}

// preloadDirectory refuses links/reparse points in every existing component.
// Roots are caller-owned state/work roots and must never be installation roots.
func preloadDirectory(root, branch, version string, create bool) (string, error) {
	if root == "" || !preloadBranchOK(branch) {
		return "", ErrPreloadRecord
	}
	if _, ok := preloadCompareVersion(version, "0"); !ok {
		return "", ErrPreloadRecord
	}
	abs, e := filepath.Abs(root)
	if e != nil {
		return "", e
	}
	dir := abs
	for n, part := range []string{"", "preload", branch, version} {
		if n > 0 {
			dir = filepath.Join(dir, part)
		}
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) && create {
			err = os.MkdirAll(dir, 0700)
			if err == nil {
				info, err = os.Lstat(dir)
			}
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", ErrPreloadRecord
		}
	}
	return dir, nil
}
func preloadNoLinks(dir string) error {
	return filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return ErrPreloadRecord
		}
		return nil
	})
}
func (s PreloadStore) Save(ctx context.Context, r PreloadRecord) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := preloadValidateRecord(r); e != nil {
		return e
	}
	dir, e := preloadDirectory(s.StateRoot, r.Branch, r.Version, true)
	if e != nil {
		return e
	}
	snapshot := struct {
		Schema   int             `json:"schema"`
		Branch   string          `json:"branch"`
		Version  string          `json:"version"`
		ListHash string          `json:"listHash"`
		Route    PreloadRoute    `json:"route"`
		Targets  []PreloadTarget `json:"targets"`
		Budget   PreloadBudget   `json:"budget"`
	}{r.Schema, r.Branch, r.Version, r.ListHash, r.Route, r.Targets, r.Budget}
	planData, e := json.Marshal(snapshot)
	if e != nil {
		return e
	}
	if e = preloadAtomicJSON(ctx, dir, "plan.json", planData); e != nil {
		return e
	}
	data, e := json.Marshal(r)
	if e != nil {
		return e
	}
	return preloadAtomicJSON(ctx, dir, "preload.json", data)
}

// preload.json is the authority; a crash between these two independently atomic
// writes may leave an older advisory plan.json which callers must not execute.
func preloadAtomicJSON(ctx context.Context, dir, name string, data []byte) error {
	if len(data) > 16<<20 {
		return ErrPreloadRecord
	}
	f, e := os.CreateTemp(dir, ".preload-*.tmp")
	if e != nil {
		return e
	}
	tempName := f.Name()
	defer os.Remove(tempName)
	if _, e = f.Write(data); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	return os.Rename(tempName, filepath.Join(dir, name))
}
func (s PreloadStore) Load(ctx context.Context, branch, version string) (PreloadRecord, error) {
	if e := ctx.Err(); e != nil {
		return PreloadRecord{}, e
	}
	dir, e := preloadDirectory(s.StateRoot, branch, version, false)
	if e != nil {
		return PreloadRecord{}, e
	}
	path := filepath.Join(dir, "preload.json")
	info, e := os.Lstat(path)
	if e != nil {
		return PreloadRecord{}, e
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return PreloadRecord{}, ErrPreloadRecord
	}
	f, e := os.Open(path)
	if e != nil {
		return PreloadRecord{}, e
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, 16<<20+1))
	if e != nil {
		return PreloadRecord{}, e
	}
	var r PreloadRecord
	if json.Unmarshal(data, &r) != nil || r.Branch != branch || r.Version != version {
		return PreloadRecord{}, ErrPreloadRecord
	}
	if e = preloadValidateRecord(r); e != nil {
		return PreloadRecord{}, e
	}
	return r, ctx.Err()
}
func (s PreloadStore) BlobPath(r PreloadRecord, o gameres.Object) (string, error) {
	if !preloadObjectOK(o) {
		return "", ErrPreloadRecord
	}
	dir, e := preloadDirectory(s.WorkRoot, r.Branch, r.Version, true)
	if e != nil {
		return "", e
	}
	blobDir := dir
	parts := []string{"blobs"}
	if s.EngineCache {
		parts = []string{"cache", "blobs"}
	}
	for _, part := range parts {
		blobDir = filepath.Join(blobDir, part)
		if e = os.Mkdir(blobDir, 0700); e != nil && !os.IsExist(e) {
			return "", e
		}
		info, err := os.Lstat(blobDir)
		if err != nil || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || !info.IsDir() {
			return "", ErrPreloadRecord
		}
	}
	return filepath.Join(blobDir, o.String()), nil
}
func (s PreloadStore) Recover(ctx context.Context, r PreloadRecord, now time.Time) (PreloadRecord, error) {
	if e := ctx.Err(); e != nil {
		return r, e
	}
	if e := preloadValidateRecord(r); e != nil {
		return r, e
	}
	if r.State == PreloadApplying {
		return r, nil
	}
	switch r.State {
	case PreloadStaged, PreloadWaitingRelease, PreloadReconcile, PreloadApplicable:
	default:
		return r, nil
	}
	// Copy slices before changing state: callers may retain their input snapshot.
	r.Files = append([]PreloadFile(nil), r.Files...)
	for n, f := range r.Files {
		if f.State != PreloadFileStaged {
			if r.State != PreloadFailed {
				r.State = PreloadDownloading
			}
			continue
		}
		path, e := s.BlobPath(r, f.Object)
		valid := false
		if e == nil {
			valid, e = preloadVerifyBlob(ctx, path, f.Object)
		}
		if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
			return r, e
		}
		if !valid {
			r.Files[n].State = PreloadFileDownloading
			r.Files[n].VerificationFailures++
			if r.Files[n].VerificationFailures > 2 {
				r.State = PreloadFailed
			} else if r.State != PreloadFailed {
				r.State = PreloadDownloading
			}
		}
	}
	r.UpdatedAt = now
	if e := s.Save(ctx, r); e != nil {
		return r, e
	}
	return r, nil
}
func preloadVerifyBlob(ctx context.Context, path string, o gameres.Object) (bool, error) {
	info, e := os.Lstat(path)
	if e != nil {
		return false, e
	}
	if !info.Mode().IsRegular() || info.Size() != o.Size {
		return false, nil
	}
	f, e := os.Open(path)
	if e != nil {
		return false, e
	}
	defer f.Close()
	h := md5.New()
	buf := make([]byte, 128<<10)
	var total int64
	for {
		if e = ctx.Err(); e != nil {
			return false, e
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			total += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return false, readErr
		}
	}
	after, e := f.Stat()
	if e != nil {
		return false, e
	}
	return total == o.Size && after.Size() == info.Size() && after.ModTime() == info.ModTime() && fmt.Sprintf("%x", h.Sum(nil)) == o.MD5, nil
}

// DiscardPreload requires a fresh reference scan while the caller holds the
// shared task/GC exclusion lock. References must exclude this discarded task.
// Unknown files are retained, and referenced blobs preserve metadata ownership.
func DiscardPreload(ctx context.Context, s PreloadStore, r PreloadRecord, rescan func(context.Context) (map[gameres.Object]bool, error)) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := preloadValidateRecord(r); e != nil {
		return e
	}
	if rescan == nil || r.State == PreloadApplying {
		return ErrPreloadBlocked
	}
	refs, e := rescan(ctx)
	if e != nil {
		return e
	}
	if refs == nil {
		return ErrPreloadBlocked
	}
	dir, e := preloadDirectory(s.WorkRoot, r.Branch, r.Version, false)
	if os.IsNotExist(e) {
		r.State = PreloadStale
		return s.Save(ctx, r)
	}
	if e != nil {
		return e
	}
	if e = preloadNoLinks(dir); e != nil {
		return e
	}
	retained := false
	for _, f := range r.Files {
		if e = ctx.Err(); e != nil {
			return e
		}
		if refs[f.Object] {
			retained = true
			continue
		}
		path := filepath.Join(dir, "blobs", f.Object.String())
		if s.EngineCache {
			path = filepath.Join(dir, "cache", "blobs", f.Object.String())
		}
		if e = os.Remove(path); e != nil && !os.IsNotExist(e) {
			return e
		}
		if s.EngineCache {
			partial := filepath.Join(dir, "cache", "tmp", "obj-"+f.Object.String())
			if e = os.RemoveAll(partial); e != nil {
				return e
			}
		}
	}
	// Partial identity belongs to the downloader's bitmap. Retain it whenever
	// another task references any blob until the caller can prove it unreferenced.
	if !retained && len(refs) == 0 {
		partial := filepath.Join(dir, "partial")
		if e = os.RemoveAll(partial); e != nil {
			return e
		}
	}
	r.State = PreloadStale
	return s.Save(ctx, r)
}
