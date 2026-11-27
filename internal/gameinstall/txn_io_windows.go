//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"github.com/nerv-base/Oddward/internal/gameres"
	"golang.org/x/sys/windows"
)

const txnIOBufSize = 1 << 20

var txnBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, txnIOBufSize)
		return &b
	},
}

// txnHashObserver is a test-only hook called once per full file MD5 pass.
var txnHashObserver func(name string, size int64)

type txnStamp struct {
	VolumeSerial uint32
	IndexHigh    uint32
	IndexLow     uint32
	Size         int64
	MtimeUnix    int64
}

func (st txnStamp) valid() bool {
	return st.Size > 0 && (st.VolumeSerial != 0 || st.IndexHigh != 0 || st.IndexLow != 0)
}
func (st txnStamp) fileID() string {
	return fmt.Sprintf("%08x:%08x%08x", st.VolumeSerial, st.IndexHigh, st.IndexLow)
}

// txnLedger caches verified (volume, NTFS file ID, size, mtime) -> MD5 mappings
// strictly within a single Commit or cleanup invocation under an unbroken lease.
// Same-volume renames preserve VolumeSerial and FileIndex, so a file verified
// once does not need to be rehashed across staging, backup, placement, and
// post-placement verification unless its metadata changes.
type txnLedger struct {
	mu      sync.Mutex
	byStamp map[txnStamp]string
}

func newTxnLedger() *txnLedger {
	return &txnLedger{byStamp: map[txnStamp]string{}}
}
func (l *txnLedger) get(st txnStamp) (string, bool) {
	if l == nil || !st.valid() {
		return "", false
	}
	l.mu.Lock()
	md5Hex, ok := l.byStamp[st]
	l.mu.Unlock()
	return md5Hex, ok
}
func (l *txnLedger) put(st txnStamp, md5Hex string) {
	if l == nil || !st.valid() || !stateValidMD5(md5Hex) {
		return
	}
	l.mu.Lock()
	l.byStamp[st] = md5Hex
	l.mu.Unlock()
}

func txnWorkerCount(n int) int {
	if n <= 1 {
		return 1
	}
	w := runtime.NumCPU() / 2
	if w < 2 {
		w = 2
	}
	if w > 4 {
		w = 4
	}
	if n < w {
		return n
	}
	return w
}

func txnRunParallel(ctx context.Context, n int, fn func(context.Context, int) error) error {
	if n <= 0 {
		return nil
	}
	workers := txnWorkerCount(n)
	if workers == 1 {
		for i := 0; i < n; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := fn(ctx, i); err != nil {
				return err
			}
		}
		return nil
	}
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if err := pctx.Err(); err != nil {
					return
				}
				if err := fn(pctx, i); err != nil {
					errs[i] = err
					cancel()
					return
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		select {
		case <-pctx.Done():
			i = n
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

type txnRoots struct {
	install, work         *os.Root
	source                *os.Root
	sourceDifferentVolume bool
	workRel, workPath     string
}

func (r *txnRoots) close() {
	if r.source != nil {
		r.source.Close()
	}
	r.work.Close()
	r.install.Close()
}
func txnReparse(st os.FileInfo) bool {
	if st.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := st.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
func txnAbsoluteSafe(name string) error {
	if !filepath.IsAbs(name) {
		return ErrUnsafeTarget
	}
	clean := filepath.Clean(name)
	volume := filepath.VolumeName(clean)
	if len(volume) != 2 || strings.HasPrefix(clean, `\\`) {
		return ErrUnsafeTarget
	}
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(clean, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		st, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if txnReparse(st) || !st.IsDir() {
			return ErrUnsafeTarget
		}
	}
	return nil
}
func txnOpenRoots(root, work string) (*txnRoots, error) {
	if err := txnAbsoluteSafe(root); err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(work))
	if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		return nil, ErrUnsafeTarget
	}
	rel = filepath.ToSlash(rel)
	if rel != ".oddward" && !strings.HasPrefix(rel, ".oddward/") {
		return nil, ErrUnsafeTarget
	}
	install, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*txnRoots, error) { install.Close(); return nil, err }
	if err := txnSafe(install, rel, true); err != nil {
		return fail(err)
	}
	if err := install.MkdirAll(rel, 0700); err != nil {
		return fail(err)
	}
	wr, err := install.OpenRoot(rel)
	if err != nil {
		return fail(err)
	}
	a, ae := txnVolumeID(install)
	b, be := txnVolumeID(wr)
	if ae != nil || be != nil || a != b {
		wr.Close()
		return fail(ErrUnsafeTarget)
	}
	return &txnRoots{install: install, work: wr, workRel: rel, workPath: work}, nil
}
func txnSafe(r *os.Root, name string, missing bool) error {
	if name == "" || filepath.IsAbs(name) || strings.ContainsAny(name, "\\:") {
		return ErrUnsafeTarget
	}
	parts := strings.Split(name, "/")
	current := ""
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return ErrUnsafeTarget
		}
		if current != "" {
			current += "/"
		}
		current += part
		st, err := r.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && missing {
			return nil
		}
		if err != nil {
			return err
		}
		if txnReparse(st) {
			return ErrUnsafeTarget
		}
		if current != name && !st.IsDir() {
			return ErrUnsafeTarget
		}
	}
	return nil
}
func txnTargetValid(t InstallTarget) error {
	p, err := gameres.NormalizePath(t.Path)
	if err != nil || p != t.Path || !strings.HasPrefix(strings.ToLower(p), "client/windowsnoeditor/") || !stateValidMD5(t.MD5) || t.Size <= 0 || t.Size > 32<<30 {
		return ErrUnsafeTarget
	}
	return nil
}
func txnSourceValid(source string) error {
	p, err := gameres.NormalizePath(source)
	if err != nil || p != source || strings.HasPrefix(strings.ToLower(p), "txn/") {
		return ErrUnsafeTarget
	}
	return nil
}
func txnIDValid(id string) bool {
	if len(id) == 0 || len(id) > 96 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func txnBootstrapPath(name string) bool {
	return name == "Client/WindowsNoEditor/PatcherConfig/Patch_Windows.json" || name == "Client/WindowsNoEditor/PatcherConfig/Patch_Windows.source.json"
}

func txnHandleStamp(f *os.File) (txnStamp, error) {
	st, err := f.Stat()
	if err != nil {
		return txnStamp{}, err
	}
	if !st.Mode().IsRegular() {
		return txnStamp{}, ErrUnsafeTarget
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return txnStamp{}, err
	}
	if info.NumberOfLinks != 1 {
		return txnStamp{}, ErrUnsafeTarget
	}
	return txnStamp{
		VolumeSerial: info.VolumeSerialNumber,
		IndexHigh:    info.FileIndexHigh,
		IndexLow:     info.FileIndexLow,
		Size:         st.Size(),
		MtimeUnix:    st.ModTime().UTC().UnixNano(),
	}, nil
}

func txnStatStamp(r *os.Root, name string) (txnStamp, error) {
	if err := txnSafe(r, name, false); err != nil {
		return txnStamp{}, err
	}
	f, err := r.Open(name)
	if err != nil {
		return txnStamp{}, err
	}
	defer f.Close()
	return txnHandleStamp(f)
}

func txnIdentity(ctx context.Context, r *os.Root, name string) (InstallTarget, error) {
	target, _, err := txnIdentityLedger(ctx, r, name, nil, nil)
	return target, err
}

func txnIdentityLedger(ctx context.Context, r *os.Root, name string, ledger *txnLedger, onRead func(int64)) (InstallTarget, txnStamp, error) {
	if err := ctx.Err(); err != nil {
		return InstallTarget{}, txnStamp{}, err
	}
	if err := txnSafe(r, name, false); err != nil {
		return InstallTarget{}, txnStamp{}, err
	}
	f, err := r.Open(name)
	if err != nil {
		return InstallTarget{}, txnStamp{}, err
	}
	defer f.Close()
	before, err := txnHandleStamp(f)
	if err != nil {
		return InstallTarget{}, txnStamp{}, err
	}
	if md5Hex, ok := ledger.get(before); ok {
		return InstallTarget{Path: name, MD5: md5Hex, Size: before.Size}, before, nil
	}
	h := md5.New()
	ptr := txnBufPool.Get().(*[]byte)
	buffer := *ptr
	defer txnBufPool.Put(ptr)
	for {
		if err := ctx.Err(); err != nil {
			return InstallTarget{}, txnStamp{}, err
		}
		n, err := f.Read(buffer)
		if n > 0 {
			h.Write(buffer[:n])
			if onRead != nil {
				onRead(int64(n))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return InstallTarget{}, txnStamp{}, err
		}
	}
	after, err := txnHandleStamp(f)
	if err != nil {
		return InstallTarget{}, txnStamp{}, err
	}
	if before != after {
		return InstallTarget{}, txnStamp{}, errors.New("file changed while hashing")
	}
	digest := fmt.Sprintf("%x", h.Sum(nil))
	if obs := txnHashObserver; obs != nil {
		obs(name, before.Size)
	}
	ledger.put(before, digest)
	return InstallTarget{Path: name, MD5: digest, Size: before.Size}, before, nil
}

func txnVerify(ctx context.Context, r *os.Root, name string, want InstallTarget) error {
	return txnVerifyLedger(ctx, r, name, want, nil, nil)
}

func txnVerifyLedger(ctx context.Context, r *os.Root, name string, want InstallTarget, ledger *txnLedger, onRead func(int64)) error {
	actual, _, err := txnIdentityLedger(ctx, r, name, ledger, onRead)
	if err != nil {
		return err
	}
	actual.Path = want.Path
	if actual != want {
		return fmt.Errorf("checksum mismatch: %s", name)
	}
	return nil
}

func txnCopyHashed(ctx context.Context, r *os.Root, source, dest string, want InstallTarget, ledger *txnLedger) (resultErr error) {
	if err := txnSafe(r, source, false); err != nil {
		return err
	}
	if err := txnSafe(r, dest, true); err != nil {
		return err
	}
	src, err := r.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := r.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			if err := dst.Close(); resultErr == nil && err != nil {
				resultErr = err
			}
		}
		if resultErr != nil {
			_ = r.Remove(dest)
		}
	}()
	h := md5.New()
	ptr := txnBufPool.Get().(*[]byte)
	buf := *ptr
	defer txnBufPool.Put(ptr)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := src.Read(buf)
		if n > 0 {
			w, werr := dst.Write(buf[:n])
			if werr != nil {
				return werr
			}
			if w != n {
				return io.ErrShortWrite
			}
			h.Write(buf[:n])
			written += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if err := dst.Sync(); err != nil {
		return err
	}
	closed = true
	if err := dst.Close(); err != nil {
		return err
	}
	if written != want.Size || fmt.Sprintf("%x", h.Sum(nil)) != want.MD5 {
		return fmt.Errorf("staging checksum mismatch: %s", want.Path)
	}
	st, err := txnStatStamp(r, dest)
	if err != nil {
		return err
	}
	if st.Size != want.Size {
		return fmt.Errorf("staging checksum mismatch: %s", want.Path)
	}
	ledger.put(st, want.MD5)
	return nil
}

func txnEmptyRoot(r *os.Root) error {
	f, err := r.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != ".oddward" {
			return ErrTargetExists
		}
	}
	return nil
}

func txnCheckSpace(ctx context.Context, in TxnInput, r *txnRoots, ledger *txnLedger) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	need := in.RequiredFreeBytes
	// All retained source bytes may require separate staging; transfers are renames.
	var copies int64
	for i, f := range append(append([]TxnFile{}, in.Files...), in.Bootstrap...) {
		if (f.Retain || in.SourceRoot != "" && i < len(in.Files)) && f.Source != "" {
			want := InstallTarget{Path: f.Path, MD5: f.MD5, Size: f.Size}
			if txnIDValid(in.TxnID) {
				stage := fmt.Sprintf("txn/%s/staged/%d", in.TxnID, i)
				if txnVerifyLedger(ctx, r.work, stage, want, ledger, nil) == nil {
					continue
				}
			}
			if in.Candidate.Mode == "fresh" && txnVerifyLedger(ctx, r.install, f.Path, want, ledger, nil) == nil {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if copies > int64(^uint64(0)>>1)-f.Size {
				return ErrNoSpace
			}
			copies += f.Size
		}
	}
	if in.SourceRoot != "" {
		// External fresh caches allocate new destination bytes. System supplies
		// the reserve separately; only copies not already staged are added.
		if copies > int64(^uint64(0)>>1)-need {
			return ErrNoSpace
		}
		need += copies
	} else if copies > need {
		need = copies
	}
	if need < 1<<20 {
		need = 1 << 20
	}
	var free int64
	var err error
	if in.FreeBytes != nil {
		free, err = in.FreeBytes(ctx)
	} else {
		p, xerr := windows.UTF16PtrFromString(r.workPath)
		if xerr != nil {
			return xerr
		}
		var available uint64
		err = windows.GetDiskFreeSpaceEx(p, &available, nil, nil)
		if available > uint64(^uint64(0)>>1) {
			free = int64(^uint64(0) >> 1)
		} else {
			free = int64(available)
		}
	}
	if err != nil {
		return err
	}
	if free < need {
		return ErrNoSpace
	}
	return nil
}

func txnVerifyCandidate(ctx context.Context, r *txnRoots, j *txnJournal, ledger *txnLedger) error {
	var indices []int
	for i, e := range j.Files {
		if e.Target != nil {
			indices = append(indices, i)
		}
	}
	return txnRunParallel(ctx, len(indices), func(pctx context.Context, k int) error {
		e := j.Files[indices[k]]
		if e.Reuse && e.MetaOnly && ledger == nil {
			st, err := txnStatStamp(r.install, e.Path)
			if err != nil || st.Size != e.Target.Size {
				return fmt.Errorf("metadata target mismatch: %s", e.Path)
			}
			return nil
		}
		return txnVerifyLedger(pctx, r.install, e.Path, *e.Target, ledger, nil)
	})
}

func txnValidateBeforeCommit(ctx context.Context, r *txnRoots, j *txnJournal, ledger *txnLedger) error {
	return txnRunParallel(ctx, len(j.Files), func(pctx context.Context, i int) error {
		e := j.Files[i]
		if e.Reuse && e.MetaOnly && ledger == nil {
			st, err := txnStatStamp(r.install, e.Path)
			if err != nil || st.Size != e.Target.Size {
				return errors.New("reused file changed before commit")
			}
			return nil
		}
		if e.Old != nil && e.OldMetaOnly && ledger == nil {
			st, err := txnStatStamp(r.install, e.Path)
			if err != nil || st.Size != e.Old.Size {
				return errors.New("old file changed before commit")
			}
			if e.Target != nil {
				return txnVerifyLedger(pctx, r.work, txnStage(j, i), *e.Target, ledger, nil)
			}
			return nil
		}
		actual, _, err := txnIdentityLedger(pctx, r.install, e.Path, ledger, nil)
		if e.Reuse {
			if err != nil || actual != *e.Target {
				return errors.New("reused file changed before commit")
			}
			return nil
		}
		if e.Old != nil {
			if err != nil || actual != *e.Old {
				return errors.New("old file changed before commit")
			}
		} else if err == nil {
			if !(j.Kind == "install" && e.Target != nil && actual == *e.Target) {
				return ErrTargetExists
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if e.Target != nil {
			if j.Kind == "install" && err == nil && actual == *e.Target {
				return nil
			}
			if err := txnVerifyLedger(pctx, r.work, txnStage(j, i), *e.Target, ledger, nil); err != nil {
				return err
			}
		}
		return nil
	})
}
