// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/gameres"
)

type GCOptions struct {
	WorkRoot, StateRoot string
	Lease               *Lease
	// Protected includes transaction and Applicable preload references that have
	// not yet been persisted as download tasks. Objects are protected in all roots.
	Protected []gameres.Object
	// Zero disables that eviction criterion; both zero performs a usage scan.
	MaxBlobBytes     int64
	MaxAge           time.Duration
	Now              time.Time
	RecoveryRequired bool
}
type GCResult struct {
	BlobBytes, ProtectedBytes, ReclaimableBytes, DeletedBytes int64
	Deleted                                                   []string
	ScanOnly                                                  bool
}
type gcBlob struct {
	path      string
	object    gameres.Object
	size      int64
	modified  time.Time
	protected bool
}

// Collect only removes unreferenced immutable cache blobs. The caller must hold
// the installation lease and include any references not yet saved as tasks.
// It never removes partial data, rebuilds, staged files, or transaction backups.
func Collect(ctx context.Context, o GCOptions) (GCResult, error) {
	var result GCResult
	if o.Lease == nil {
		return result, failure(ErrLocked, "GC caller lease", "lock", nil)
	}
	if o.Lease.writer.TryLock() {
		defer o.Lease.writer.Unlock()
	} else {
		result.ScanOnly = true
	}
	if o.RecoveryRequired {
		return result, failure(ErrRecoveryRequired, "GC recovery guard", "", nil)
	}
	if o.MaxBlobBytes < 0 || o.MaxAge < 0 {
		return result, failure(ErrPathUnsafe, "GC policy", "", nil)
	}
	root, err := o.Lease.borrow(o.WorkRoot)
	if err != nil {
		return result, err
	}
	defer func() { root.Close(); o.Lease.release() }()
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	store, err := NewStore(o.StateRoot)
	if err != nil {
		return result, err
	}
	defer store.Close()
	tasks, err := store.List(ctx)
	if err != nil {
		return result, failure(ErrRecoveryRequired, "GC task references", "tasks", err)
	}
	marked := map[gameres.Object]bool{}
	for _, obj := range o.Protected {
		if !validObject(obj) {
			return result, failure(ErrPathUnsafe, "GC protected identity", "", nil)
		}
		marked[obj] = true
	}
	for _, task := range tasks {
		if task.State == StateRunning || task.State == StateVerifying {
			result.ScanOnly = true
		}
		for _, tr := range task.Transfers {
			marked[tr.Object] = true
			marked[tr.Source.Object] = true
		}
		for _, target := range task.Targets {
			marked[target.Object] = true
		}
		for _, rebuild := range task.Rebuilds {
			marked[rebuild.Target] = true
			for _, step := range rebuild.Steps {
				marked[step.Blob] = true
				marked[step.Old] = true
				marked[step.New] = true
			}
		}
	}
	var blobs []gcBlob
	scan := func(path string) error {
		return gcEach(root, path, func(entry os.DirEntry) error {
			if err := ctx.Err(); err != nil {
				return classify("GC scan", path, err)
			}
			full := path + "/" + entry.Name()
			if err := checkPath(root, full); err != nil {
				return err
			}
			if entry.IsDir() {
				return failure(ErrPathUnsafe, "GC blob directory", full, nil)
			}
			md5, sizeText, ok := strings.Cut(entry.Name(), ".")
			size, err := strconv.ParseInt(sizeText, 10, 64)
			obj := gameres.Object{MD5: md5, Size: size}
			if !ok || err != nil || !validObject(obj) || obj.String() != entry.Name() {
				return failure(ErrRecoveryRequired, "GC blob identity", full, nil)
			}
			info, err := entry.Info()
			if err != nil {
				return classify("GC stat", full, err)
			}
			if !info.Mode().IsRegular() {
				return failure(ErrPathUnsafe, "GC regular blob", full, nil)
			}
			if len(blobs) >= 131072 {
				return failure(ErrRecoveryRequired, "GC blob limit", path, nil)
			}
			blobs = append(blobs, gcBlob{path: full, object: obj, size: info.Size(), modified: info.ModTime(), protected: marked[obj]})
			return nil
		})
	}
	if err = scan("cache/blobs"); err != nil {
		return result, err
	}
	err = gcEach(root, "preload", func(branch os.DirEntry) error {
		branchPath := "preload/" + branch.Name()
		if err := checkPath(root, branchPath); err != nil {
			return err
		}
		if !branch.IsDir() {
			return nil
		}
		return gcEach(root, branchPath, func(version os.DirEntry) error {
			versionPath := branchPath + "/" + version.Name()
			if err := checkPath(root, versionPath); err != nil {
				return err
			}
			if !version.IsDir() {
				return nil
			}
			for _, suffix := range []string{"/blobs", "/cache/blobs"} {
				if err := scan(versionPath + suffix); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		return result, err
	}
	for _, blob := range blobs {
		result.BlobBytes += blob.size
		if blob.protected {
			result.ProtectedBytes += blob.size
		} else {
			result.ReclaimableBytes += blob.size
		}
	}
	// Complete all reference/path validation before the first destructive action.
	slices.SortFunc(blobs, func(a, b gcBlob) int {
		if order := a.modified.Compare(b.modified); order != 0 {
			return order
		}
		return strings.Compare(a.path, b.path)
	})
	for _, blob := range blobs {
		if blob.protected || result.ScanOnly {
			continue
		}
		expired := o.MaxAge > 0 && !blob.modified.After(o.Now.Add(-o.MaxAge))
		overCap := o.MaxBlobBytes > 0 && result.BlobBytes-result.DeletedBytes > o.MaxBlobBytes
		if !expired && !overCap {
			continue
		}
		if err = ctx.Err(); err != nil {
			return result, classify("GC delete", blob.path, err)
		}
		if err = removeSafe(root, blob.path); err != nil {
			return result, err
		}
		result.DeletedBytes += blob.size
		result.Deleted = append(result.Deleted, blob.path)
	}
	return result, nil
}
func gcEach(root *os.Root, path string, visit func(os.DirEntry) error) error {
	if err := checkPath(root, path); err != nil {
		return err
	}
	dir, err := root.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return classify("GC directory", path, err)
	}
	defer dir.Close()
	for {
		entries, err := dir.ReadDir(256)
		for _, entry := range entries {
			if err := visit(entry); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return classify("GC read directory", path, err)
		}
	}
}
