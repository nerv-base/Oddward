// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"
)

const ioBufferSize = 256 << 10

var buffers = sync.Pool{New: func() any { b := make([]byte, ioBufferSize); return &b }}

type Engine struct {
	options      Options
	root         *os.Root
	store        *Store
	lease        *Lease
	ownsLease    bool
	mu           sync.Mutex
	closed       bool
	connections  chan struct{}
	limiter      rateLimiter
	admission    concurrencyLimits
	hostMu       sync.Mutex
	degraded     map[string]time.Time
	hostFailures map[string]int
}

func New(o Options) (*Engine, error) {
	if o.Client == nil || o.Resolve == nil {
		return nil, failure(ErrPathUnsafe, "client and trusted resolver required", "", nil)
	}
	p := &o.Parameters
	if p.ChunkSize == 0 {
		p.ChunkSize = 8 << 20
	}
	if p.ConnectionsPerFile == 0 {
		p.ConnectionsPerFile = 4
	}
	if p.FilesInFlight == 0 {
		p.FilesInFlight = 4
	}
	if p.ChunkSize < 1<<20 || p.ChunkSize > 64<<20 || p.ConnectionsPerFile < 1 || p.ConnectionsPerFile > 16 || p.FilesInFlight < 1 || p.FilesInFlight > 8 || p.RateLimitKiB < 0 || p.RateLimitKiB > 1048576 {
		return nil, failure(ErrPathUnsafe, "download parameters", "", nil)
	}
	if o.UserAgent == "" {
		o.UserAgent = "Oddward/dev"
	}
	lease := o.Lease
	owned := lease == nil
	var err error
	if owned {
		lease, err = Acquire(o.WorkRoot)
		if err != nil {
			return nil, err
		}
	}
	root, err := lease.borrow(o.WorkRoot)
	if err != nil {
		if owned {
			lease.Close()
		}
		return nil, err
	}
	cleanup := func() {
		root.Close()
		lease.release()
		if owned {
			lease.Close()
		}
	}
	for _, dir := range []string{"cache/blobs", "cache/tmp"} {
		if err = mkdirSafe(root, dir); err != nil {
			cleanup()
			return nil, err
		}
	}
	store, err := NewStore(o.StateRoot)
	if err != nil {
		cleanup()
		return nil, err
	}
	client := *o.Client
	o.Client = &client
	return &Engine{options: o, root: root, store: store, lease: lease, ownsLease: owned, connections: make(chan struct{}, 32), admission: concurrencyLimits{files: p.FilesInFlight, connections: p.ConnectionsPerFile}, limiter: rateLimiter{bytesPerSecond: p.RateLimitKiB * 1024}, degraded: map[string]time.Time{}, hostFailures: map[string]int{}}, nil
}
func (e *Engine) Retire(ctx context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.lease.writer.TryLock() {
		return failure(ErrLocked, "active writer", "lock", nil)
	}
	defer e.lease.writer.Unlock()
	if e.closed {
		return failure(ErrLocked, "closed engine", "", nil)
	}
	if !validID(id) {
		return failure(ErrPathUnsafe, "retire task", id, nil)
	}
	if err := ctx.Err(); err != nil {
		return classify("retire task", id, err)
	}
	e.store.mu.Lock()
	defer e.store.mu.Unlock()
	return removeSafe(e.store.root, "tasks/"+id+".json")
}

func (e *Engine) Store() *Store { return e.store }
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	err := errors.Join(e.store.Close(), e.root.Close())
	e.lease.release()
	if e.ownsLease {
		err = errors.Join(err, e.lease.Close())
	}
	return err
}
func sameTask(a, b Task) bool {
	if a.ListHash != b.ListHash || a.Kind != b.Kind || a.InstallKey != b.InstallKey || a.CacheRoot != b.CacheRoot || a.Branch != b.Branch || a.TargetVersion != b.TargetVersion || len(a.Transfers) != len(b.Transfers) {
		return false
	}
	for i, tr := range a.Transfers {
		other := b.Transfers[i]
		if tr.Object != other.Object || tr.Source != other.Source || tr.PatchBlocksTrusted != other.PatchBlocksTrusted || !slices.Equal(tr.Blocks, other.Blocks) || (tr.ChunkSize != 0 && other.ChunkSize != 0 && tr.ChunkSize != other.ChunkSize) {
			return false
		}
	}
	return true
}

// Run downloads only cache blobs. A successful write task becomes Staged;
// installing or applying those blobs belongs to the caller. Cancellation is a
// durable pause, and reports contain only persisted chunk bytes.
func (e *Engine) Run(ctx context.Context, task Task, report func(Progress)) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.lease.writer.TryLock() {
		return failure(ErrLocked, "active writer", "lock", nil)
	}
	defer e.lease.writer.Unlock()
	if e.closed {
		return failure(ErrLocked, "closed engine", "", nil)
	}
	if err := validateTask(task); err != nil {
		return err
	}
	existing, err := e.store.Load(ctx, task.ID)
	switch {
	case err == nil:
		if !sameTask(task, existing) {
			return failure(ErrStaleTask, "task identity", task.ID, nil)
		}
		if existing.State == StateStale {
			return failure(ErrStaleTask, "stale task", task.ID, nil)
		}
		task = existing
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	task.State = StateRunning
	task.Err = ""
	for i := range task.Transfers {
		tr := &task.Transfers[i]
		if tr.ChunkSize == 0 {
			tr.ChunkSize = min(e.options.Parameters.ChunkSize, tr.Object.Size)
			if tr.Object.Size == 0 {
				tr.ChunkSize = e.options.Parameters.ChunkSize
			}
		}
		if tr.State == 0 {
			tr.State = TransferPending
		}
	}
	if err = e.store.Save(ctx, task); err != nil {
		return err
	}
	progress := newProgress(task.ID, report)
	var stateMu sync.Mutex
	saveTransfer := func(i int, tr Transfer) error {
		stateMu.Lock()
		defer stateMu.Unlock()
		task.Transfers[i] = tr
		return e.store.Save(context.Background(), task)
	}
	pending := make([]int, 0, len(task.Transfers))
	for i, tr := range task.Transfers {
		f, openErr := e.openBlob(ctx, tr.Object)
		if openErr == nil {
			f.Close()
			tr.State = TransferVerified
			tr.BytesDone = tr.Object.Size
			task.Transfers[i] = tr
			progress.addCache(tr.Object.Size)
			continue
		}
		if !errors.Is(openErr, os.ErrNotExist) {
			var te *TaskError
			if !errors.As(openErr, &te) || te.Kind != ErrChecksum {
				return e.finishTask(&task, openErr)
			}
			if err = removeSafe(e.root, blobPath(tr.Object)); err != nil {
				return e.finishTask(&task, err)
			}
		}
		if task.Kind == TaskVerifyOnly {
			return e.finishTask(&task, failure(ErrChecksum, "verify missing blob", tr.Object.String(), openErr))
		}
		pending = append(pending, i)
		progress.total += tr.Object.Size
	}
	progress.emit("downloading")
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	indices := make(chan int, len(pending))
	for _, i := range pending {
		indices <- i
	}
	close(indices)
	var workers sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	for n := 0; n < min(e.options.Parameters.FilesInFlight, len(pending)); n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range indices {
				if runCtx.Err() != nil {
					return
				}
				if err := e.admission.acquire(runCtx, nil); err != nil {
					errOnce.Do(func() { firstErr = classify("file admission", task.ID, err); cancel() })
					return
				}
				tr := task.Transfers[i]
				tr.State = TransferDownloading
				if err := saveTransfer(i, tr); err != nil {
					e.admission.release(nil)
					errOnce.Do(func() { firstErr = err; cancel() })
					return
				}
				updated, err := e.download(runCtx, tr, progress, func(current Transfer) error { return saveTransfer(i, current) })
				e.admission.release(nil)
				if err == nil {
					updated.State = TransferVerified
					updated.BytesDone = updated.Object.Size
				} else {
					updated.State = TransferFailed
				}
				saveErr := saveTransfer(i, updated)
				if err == nil {
					err = saveErr
				}
				if err != nil {
					errOnce.Do(func() { firstErr = err; cancel() })
					return
				}
			}
		}()
	}
	workers.Wait()
	task.Counters = progress.counters()
	if firstErr == nil && ctx.Err() != nil {
		firstErr = classify("download", task.ID, ctx.Err())
	}
	if firstErr != nil {
		return e.finishTask(&task, firstErr)
	}
	task.State = StateStaged
	if task.Kind == TaskVerifyOnly {
		task.State = StateDone
	}
	if err = e.store.Save(context.Background(), task); err != nil {
		return err
	}
	progress.emit("staged")
	return nil
}
func (e *Engine) finishTask(task *Task, err error) error {
	task.State = StateFailed
	var te *TaskError
	if errors.As(err, &te) {
		switch te.Kind {
		case ErrCancelled, ErrDiskFull, ErrPermission:
			task.State = StatePaused
		case ErrStaleTask:
			task.State = StateStale
		}
	}
	task.Err = fmt.Sprintf("download failed (%d)", task.State)
	if saveErr := e.store.Save(context.Background(), *task); saveErr != nil {
		return errors.Join(err, saveErr)
	}
	return err
}

// Recover never starts network work. It checks disk evidence with a new engine,
// invalidates malformed bitmaps, and marks interrupted work Paused.
func (e *Engine) Recover(ctx context.Context, currentListHash string) ([]Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.lease.writer.TryLock() {
		return nil, failure(ErrLocked, "active writer", "lock", nil)
	}
	defer e.lease.writer.Unlock()
	if e.closed {
		return nil, failure(ErrLocked, "closed engine", "", nil)
	}
	if currentListHash != "" && !validMD5(currentListHash) {
		return nil, failure(ErrStaleTask, "current manifest", "", nil)
	}
	tasks, err := e.store.List(ctx)
	if err != nil {
		return nil, err
	}
	var result []Task
	for _, task := range tasks {
		if currentListHash != "" && task.ListHash != currentListHash {
			task.State = StateStale
			if err = e.store.Save(ctx, task); err != nil {
				return nil, err
			}
			return nil, failure(ErrStaleTask, "recover manifest", task.ID, nil)
		}
		if task.State == StateDone || task.State == StateStaged || task.State == StateStale {
			continue
		}
		for i := range task.Transfers {
			tr := &task.Transfers[i]
			f, openErr := e.openBlob(ctx, tr.Object)
			if openErr == nil {
				f.Close()
				tr.State = TransferVerified
				tr.BytesDone = tr.Object.Size
				continue
			}
			if !errors.Is(openErr, os.ErrNotExist) {
				var te *TaskError
				if !errors.As(openErr, &te) || te.Kind != ErrChecksum {
					return nil, openErr
				}
			}
			if tr.ChunkSize == 0 {
				tr.ChunkSize = min(e.options.Parameters.ChunkSize, tr.Object.Size)
				if tr.Object.Size == 0 {
					tr.ChunkSize = e.options.Parameters.ChunkSize
				}
			}
			part := partPath(tr.Object)
			if err = mkdirSafe(e.root, objectDir(tr.Object)); err != nil {
				return nil, err
			}
			f, err = openSafe(e.root, part, os.O_CREATE|os.O_RDWR)
			if err != nil {
				return nil, err
			}
			bm, loadErr := loadBitmap(e.root, bitmapPath(tr.Object), f, tr.Object, tr.ChunkSize)
			f.Close()
			if loadErr != nil {
				return nil, loadErr
			}
			tr.BytesDone = bm.downloaded()
			tr.State = TransferPending
		}
		task.State = StatePaused
		if err = e.store.Save(ctx, task); err != nil {
			return nil, err
		}
		result = append(result, task)
	}
	return result, nil
}

type speedSample struct {
	tick  int64
	bytes int64
}
type progressState struct {
	mu                                                  sync.Mutex
	id                                                  string
	report                                              func(Progress)
	total, downloaded, network, written, cached, reused int64
	samples                                             [50]speedSample
	start                                               time.Time
	networkRequests                                     int
	networkStarted                                      time.Time
	networkDuration                                     time.Duration
}

func newProgress(id string, report func(Progress)) *progressState {
	return &progressState{id: id, report: report, start: time.Now()}
}
func (p *progressState) beginNetwork() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.networkRequests == 0 {
		p.networkStarted = time.Now()
	}
	p.networkRequests++
}
func (p *progressState) endNetwork() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.networkRequests--
	if p.networkRequests == 0 {
		p.networkDuration += time.Since(p.networkStarted)
	}
}
func (p *progressState) addCache(n int64) { p.cached += n }
func (p *progressState) addIO(n int, write bool) {
	p.mu.Lock()
	if write {
		p.written += int64(n)
	} else {
		p.network += int64(n)
		tick := int64(time.Since(p.start) / (100 * time.Millisecond))
		sample := &p.samples[tick%int64(len(p.samples))]
		if sample.tick != tick {
			*sample = speedSample{tick: tick}
		}
		sample.bytes += int64(n)
	}
	p.mu.Unlock()
}
func (p *progressState) addDownloaded(n int64) {
	p.mu.Lock()
	p.downloaded += n
	p.emitLocked("downloading")
	p.mu.Unlock()
}
func (p *progressState) emit(stage string) { p.mu.Lock(); p.emitLocked(stage); p.mu.Unlock() }
func (p *progressState) emitLocked(stage string) {
	if p.report == nil {
		return
	}
	now := time.Now()
	var bytes int64
	tick := int64(now.Sub(p.start) / (100 * time.Millisecond))
	for _, s := range p.samples {
		if tick-s.tick >= 0 && tick-s.tick < int64(len(p.samples)) {
			bytes += s.bytes
		}
	}
	duration := min(now.Sub(p.start), 5*time.Second).Seconds()
	var speed float64
	if duration > 0 {
		speed = float64(bytes) / duration
	}
	snapshot := Progress{TaskID: p.id, Stage: stage, Downloaded: p.downloaded, Total: p.total, NetworkBytes: p.network, WrittenBytes: p.written, CachedBytes: p.cached, ReusedBytes: p.reused, BytesPerSecond: speed}
	snapshot.NetworkActiveDuration = p.networkDuration
	if p.networkRequests > 0 {
		snapshot.NetworkActiveDuration += now.Sub(p.networkStarted)
	}
	if speed >= 16<<10 && p.total >= p.downloaded {
		snapshot.ETAKnown = true
		snapshot.ETA = time.Duration(float64(p.total-p.downloaded) / speed * float64(time.Second))
	}
	p.report(snapshot)
}
func (p *progressState) counters() Counters {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Counters{NetworkBytes: p.network, WrittenBytes: p.written, CachedBytes: p.cached, ReusedBytes: p.reused}
}
