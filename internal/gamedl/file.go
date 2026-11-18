// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nerv-base/Oddward/internal/gameres"
)

type fileTransfer struct {
	engine         *Engine
	transfer       Transfer
	file           *os.File
	bm             *bitmap
	mu             sync.Mutex
	mirrorMu       sync.Mutex
	mirror         int
	approved       map[int]bool
	urls           []string
	save           func(Transfer) error
	progress       *progressState
	activeRequests int // guarded by engine.admission.mu
}

func (f *fileTransfer) snapshotLocked() Transfer {
	tr := f.transfer
	tr.Marks = append([]ObjectMark(nil), tr.Marks...)
	return tr
}
func (f *fileTransfer) snapshot() Transfer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshotLocked()
}
func (e *Engine) download(ctx context.Context, tr Transfer, progress *progressState, save func(Transfer) error) (Transfer, error) {
	if err := mkdirSafe(e.root, objectDir(tr.Object)); err != nil {
		return tr, err
	}
	file, err := openSafe(e.root, partPath(tr.Object), os.O_CREATE|os.O_RDWR)
	if err != nil {
		return tr, err
	}
	defer func() {
		if file != nil {
			file.Close()
		}
	}()
	bm, err := loadBitmap(e.root, bitmapPath(tr.Object), file, tr.Object, tr.ChunkSize)
	if err != nil {
		return tr, err
	}
	f := &fileTransfer{engine: e, transfer: tr, file: file, bm: bm, save: save, progress: progress, approved: map[int]bool{}}
	f.transfer.BytesDone = bm.downloaded()
	progress.addDownloaded(f.transfer.BytesDone)
	if tr.Object.Size > 0 {
		f.urls, err = e.options.Resolve(ctx, tr.Source)
		if err != nil {
			return f.snapshot(), classify("resolve source", tr.Object.String(), err)
		}
		if len(f.urls) == 0 || len(f.urls) > 8 {
			return f.snapshot(), failure(ErrHTTP, "mirror count", tr.Object.String(), nil)
		}
		for _, raw := range f.urls {
			if _, err = trustedURL(raw); err != nil {
				return f.snapshot(), err
			}
		}
	}
	checksumRounds, rejectedRounds := 0, 0
	for {
		if ctx.Err() != nil {
			return f.snapshot(), classify("download", tr.Object.String(), ctx.Err())
		}
		err = f.ranges(ctx)
		if errors.Is(err, errRangeIgnored) || errors.Is(err, errIdentityChanged) {
			if tr.Source.Kind == gameres.SourcePakRange {
				return f.snapshot(), failure(ErrRangeMismatch, "pak range required", tr.Object.String(), err)
			}
			if err = f.reset(); err != nil {
				return f.snapshot(), err
			}
			// ranges has cancelled and joined every worker before sequential writes.
			f.mu.Lock()
			f.transfer.Marks = nil
			f.mu.Unlock()
			err = f.sequential(ctx)
		}
		if errors.Is(err, errRange416) && rejectedRounds == 0 {
			rejectedRounds++
			if err = f.reset(); err != nil {
				return f.snapshot(), err
			}
			continue
		}
		if err == nil {
			if err = file.Sync(); err != nil {
				return f.snapshot(), classify("sync part", tr.Object.String(), err)
			}
			progress.emit("verifying")
			if err = f.verifyBlocks(ctx); err == nil {
				err = verify(ctx, file, tr.Object)
			}
		}
		if err != nil {
			var te *TaskError
			if errors.As(err, &te) && te.Kind == ErrChecksum && checksumRounds < 1 {
				checksumRounds++
				if err = f.reset(); err != nil {
					return f.snapshot(), err
				}
				continue
			}
			return f.snapshot(), classify("download", tr.Object.String(), err)
		}
		if err = file.Close(); err != nil {
			return f.snapshot(), err
		}
		file = nil
		if err = e.publish(ctx, tr.Object, partPath(tr.Object)); err != nil {
			return f.snapshot(), err
		}
		if err = removeSafe(e.root, bitmapPath(tr.Object)); err != nil {
			return f.snapshot(), err
		}
		result := f.snapshot()
		result.BytesDone = result.Object.Size
		result.State = TransferVerified
		return result, nil
	}
}
func (f *fileTransfer) reset() error {
	f.mu.Lock()
	old := f.bm.downloaded()
	f.bm = newBitmap(f.transfer.Object, f.transfer.ChunkSize)
	f.transfer.BytesDone = 0
	err := removeSafe(f.engine.root, bitmapPath(f.transfer.Object))
	if err == nil {
		err = f.file.Truncate(0)
	}
	if err == nil {
		err = f.file.Truncate(f.transfer.Object.Size)
	}
	snapshot := f.snapshotLocked()
	f.mu.Unlock()
	f.progress.addDownloaded(-old)
	f.progress.emit("restarting")
	if err != nil {
		return classify("reset part", f.transfer.Object.String(), err)
	}
	return f.save(snapshot)
}
func (f *fileTransfer) ranges(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Only the bitmap scales with object size. Workers claim indices from a
	// fixed-size counter rather than allocating one queued job per chunk.
	durable := append([]byte(nil), f.bm.bits...)
	count := f.bm.count
	var next atomic.Uint32
	var wg sync.WaitGroup
	var first error
	var once sync.Once
	for n := 0; n < min(f.engine.options.Parameters.ConnectionsPerFile, int(count)); n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				index := next.Add(1) - 1
				if index >= count {
					return
				}
				if durable[index/8]&(1<<(index%8)) != 0 {
					continue
				}
				if runCtx.Err() != nil {
					return
				}
				if err := f.chunk(runCtx, index); err != nil {
					once.Do(func() { first = err; cancel() })
					return
				}
			}
		}()
	}
	wg.Wait()
	if first == nil && ctx.Err() != nil {
		first = classify("range workers", f.transfer.Object.String(), ctx.Err())
	}
	return first
}
func (f *fileTransfer) selected(ctx context.Context) (int, string, error) {
	f.mirrorMu.Lock()
	defer f.mirrorMu.Unlock()
	for f.mirror < len(f.urls) {
		index := f.mirror
		raw := f.urls[index]
		u, _ := url.Parse(raw)
		f.engine.hostMu.Lock()
		until := f.engine.degraded[u.Host]
		f.engine.hostMu.Unlock()
		if until.After(time.Now()) {
			f.mirror++
			continue
		}
		if !f.approved[index] {
			f.mu.Lock()
			cross := false
			for _, mark := range f.transfer.Marks {
				if mark.Host != u.Host {
					cross = true
				}
			}
			f.mu.Unlock()
			if index > 0 || cross {
				if err := f.head(ctx, raw); err != nil {
					f.mirror++
					if f.mirror == len(f.urls) {
						return index, raw, err
					}
					continue
				}
			}
			f.approved[index] = true
		}
		return index, raw, nil
	}
	return 0, "", failure(ErrNetwork, "no usable mirror", f.transfer.Object.String(), nil)
}
func (f *fileTransfer) degrade(index int) {
	f.mirrorMu.Lock()
	defer f.mirrorMu.Unlock()
	u, _ := url.Parse(f.urls[index])
	f.engine.hostMu.Lock()
	f.engine.degraded[u.Host] = time.Now().Add(15 * time.Minute)
	f.engine.hostMu.Unlock()
	if f.mirror == index {
		f.mirror++
	}
}
func (f *fileTransfer) chunk(ctx context.Context, index uint32) error {
	start := int64(index) * f.transfer.ChunkSize
	size := f.bm.size(index)
	remoteStart := start + f.transfer.Source.Offset
	attempts, shorts, lastMirror := 0, 0, -1
	var last error
	for {
		mirror, raw, err := f.selected(ctx)
		if err != nil {
			if last != nil {
				return last
			}
			return err
		}
		if mirror != lastMirror {
			attempts = 0
			lastMirror = mirror
		}
		digest, err := f.fetch(ctx, raw, remoteStart, size, start, false)
		if err == nil {
			err = f.commit(ctx, index, digest)
		}
		if err == nil {
			u, _ := url.Parse(raw)
			f.engine.hostMu.Lock()
			delete(f.engine.hostFailures, u.Host)
			f.engine.hostMu.Unlock()
			return nil
		}
		last = err
		if ctx.Err() != nil {
			return classify("range get", f.transfer.Object.String(), ctx.Err())
		}
		if errors.Is(err, errRangeIgnored) || errors.Is(err, errRange416) || errors.Is(err, errIdentityChanged) || !retryable(err) {
			return err
		}
		if errors.Is(err, errShortBody) {
			shorts++
			if shorts >= 2 {
				return err
			}
		}
		attempts++
		u, _ := url.Parse(raw)
		f.engine.hostMu.Lock()
		f.engine.hostFailures[u.Host]++
		hostFailures := f.engine.hostFailures[u.Host]
		f.engine.hostMu.Unlock()
		if mirror+1 < len(f.urls) && (hostFailures >= 3 || errors.Is(err, errIdle)) {
			f.degrade(mirror)
			continue
		}
		if attempts >= 3 {
			if mirror+1 >= len(f.urls) {
				return err
			}
			f.degrade(mirror)
			continue
		}
		if err := waitDelay(ctx, retryDelay(attempts-1)); err != nil {
			return classify("retry wait", f.transfer.Object.String(), err)
		}
	}
}
func (f *fileTransfer) commit(ctx context.Context, index uint32, digest string) error {
	f.mu.Lock()
	if err := f.file.Sync(); err != nil {
		f.mu.Unlock()
		return classify("sync chunk", f.transfer.Object.String(), err)
	}
	start := int64(index) * f.transfer.ChunkSize
	size := f.bm.size(index)
	actual, err := hashReader(ctx, io.NewSectionReader(f.file, start, size), size)
	if err == nil && actual != digest {
		err = failure(ErrChecksum, "chunk readback", f.transfer.Object.String(), nil)
	}
	if err == nil && f.blocksTrusted() {
		for _, block := range f.transfer.Blocks {
			if block.Start == start && block.Size == size && block.MD5 != actual {
				err = failure(ErrChecksum, "manifest block", f.transfer.Object.String(), nil)
				break
			}
		}
	}
	if err != nil {
		f.mu.Unlock()
		return classify("chunk verify", f.transfer.Object.String(), err)
	}
	f.bm.set(index)
	if err = atomicWrite(f.engine.root, bitmapPath(f.transfer.Object), f.bm.marshal()); err != nil {
		f.bm.bits[index/8] &= ^(byte(1) << uint(index%8))
		f.mu.Unlock()
		return err
	}
	f.transfer.BytesDone = f.bm.downloaded()
	f.mu.Unlock()
	f.progress.addDownloaded(size)
	return nil
}
func (f *fileTransfer) sequential(ctx context.Context) error {
	_, raw, err := f.selected(ctx)
	if err != nil {
		return err
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		digest, err := f.fetch(ctx, raw, 0, f.transfer.Object.Size, 0, true)
		if err == nil {
			// The whole-response hash and final full-file hash remain independent.
			if digest != f.transfer.Object.MD5 {
				return failure(ErrChecksum, "sequential MD5", f.transfer.Object.String(), nil)
			}
			if err = f.file.Sync(); err != nil {
				return classify("sequential flush", f.transfer.Object.String(), err)
			}
			f.mu.Lock()
			for i := uint32(0); i < f.bm.count; i++ {
				f.bm.set(i)
			}
			err = atomicWrite(f.engine.root, bitmapPath(f.transfer.Object), f.bm.marshal())
			if err == nil {
				f.transfer.BytesDone = f.transfer.Object.Size
			}
			f.mu.Unlock()
			if err != nil {
				return err
			}
			f.progress.addDownloaded(f.transfer.Object.Size)
			return nil
		}
		last = err
		if !retryable(err) || ctx.Err() != nil {
			return classify("sequential get", f.transfer.Object.String(), err)
		}
		if attempt < 2 {
			if err = waitDelay(ctx, retryDelay(attempt)); err != nil {
				return classify("sequential retry", f.transfer.Object.String(), err)
			}
		}
	}
	return last
}
func (f *fileTransfer) blocksTrusted() bool {
	return f.transfer.Source.Kind != gameres.SourcePatchBlob || f.transfer.PatchBlocksTrusted
}
func (f *fileTransfer) verifyBlocks(ctx context.Context) error {
	if !f.blocksTrusted() {
		return nil
	}
	for _, block := range f.transfer.Blocks {
		actual, err := hashReader(ctx, io.NewSectionReader(f.file, block.Start, block.Size), block.Size)
		if err != nil {
			return err
		}
		if actual != block.MD5 {
			return failure(ErrChecksum, "manifest block MD5", f.transfer.Object.String(), nil)
		}
	}
	return nil
}
