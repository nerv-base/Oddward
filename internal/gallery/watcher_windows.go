// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package gallery

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	watchNotifyMask = windows.FILE_NOTIFY_CHANGE_FILE_NAME |
		windows.FILE_NOTIFY_CHANGE_DIR_NAME |
		windows.FILE_NOTIFY_CHANGE_SIZE |
		windows.FILE_NOTIFY_CHANGE_LAST_WRITE |
		windows.FILE_NOTIFY_CHANGE_CREATION

	watchBufferBytes   = 64 * 1024
	defaultDebounce    = 350 * time.Millisecond
	sleepCheckInterval = 30 * time.Second
	sleepJumpThreshold = 60 * time.Second
)

// WatchEventKind distinguishes file-level events from full reconcile triggers.
type WatchEventKind uint8

const (
	WatchFileChanged WatchEventKind = iota
	WatchFileRemoved
	WatchReconcileOverflow
	WatchReconcileWake
)

// WatchEvent is a debounced, coalesced notification delivered by Watcher.
type WatchEvent struct {
	Kind    WatchEventKind
	Root    string
	RelPath string
}

// WatcherOptions configures ReadDirectoryChangesW watching.
type WatcherOptions struct {
	Debounce           time.Duration
	SleepCheckInterval time.Duration
	// Now overrides time.Now for deterministic sleep/resume wake testing.
	Now func() time.Time
}

// Watcher monitors one or more local directories via overlapped
// ReadDirectoryChangesW, coalesces rapid write bursts and cleanly closes all
// Windows handles on cancellation.
type Watcher struct {
	opts   WatcherOptions
	events chan WatchEvent
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// WatchRoot specifies one directory to watch and whether to include subtrees.
type WatchRoot struct {
	Path    string
	Subtree bool
}

// StartWatcher begins event-driven Windows directory watching on roots.
// Nonexistent or unsafe roots are skipped without failing the remaining roots.
func StartWatcher(parent context.Context, roots []WatchRoot, opts WatcherOptions) (*Watcher, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if opts.Debounce <= 0 {
		opts.Debounce = defaultDebounce
	}
	if opts.SleepCheckInterval <= 0 {
		opts.SleepCheckInterval = sleepCheckInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	ctx, cancel := context.WithCancel(parent)
	w := &Watcher{
		opts:   opts,
		events: make(chan WatchEvent, 128),
		cancel: cancel,
	}

	rawCh := make(chan WatchEvent, 256)
	var watchWG sync.WaitGroup
	var armedWG sync.WaitGroup
	seen := make(map[string]bool)

	for _, r := range roots {
		clean, err := checkLocalPath(ctx, r.Path, true, false)
		if err != nil {
			continue
		}
		key := strings.ToLower(clean)
		if seen[key] {
			continue
		}
		seen[key] = true

		dirHandle, err := openWatchDirectory(clean)
		if err != nil {
			continue
		}
		armedWG.Add(1)
		watchWG.Add(1)
		go func(root string, subtree bool, h windows.Handle) {
			defer watchWG.Done()
			runDirectoryWatch(ctx, root, subtree, h, rawCh, &armedWG)
		}(clean, r.Subtree, dirHandle)
	}
	armedWG.Wait()

	// Lightweight sleep/resume detector: wakes once per SleepCheckInterval
	// (30 s default), costing effectively 0% CPU while detecting system suspend.
	watchWG.Add(1)
	go func() {
		defer watchWG.Done()
		runWakeDetector(ctx, opts.SleepCheckInterval, opts.Now, rawCh)
	}()

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer close(w.events)
		runDebouncer(ctx, opts.Debounce, rawCh, w.events)
		watchWG.Wait()
	}()

	return w, nil
}

// Events returns the channel of coalesced watch notifications. It closes once
// the watcher stops and all OS handles are released.
func (w *Watcher) Events() <-chan WatchEvent { return w.events }

// Close stops watching, cancels all pending Windows I/O and waits for all
// handles to close.
func (w *Watcher) Close() {
	if w == nil {
		return
	}
	w.cancel()
	w.wg.Wait()
}

func openWatchDirectory(dir string) (windows.Handle, error) {
	wide, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return windows.InvalidHandle, err
	}
	h, err := windows.CreateFile(
		wide,
		windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return h, nil
}

func runDirectoryWatch(ctx context.Context, root string, subtree bool, dirHandle windows.Handle, out chan<- WatchEvent, armed *sync.WaitGroup) {
	defer windows.CloseHandle(dirHandle)
	var armedOnce sync.Once
	markArmed := func() {
		if armed != nil {
			armedOnce.Do(armed.Done)
		}
	}
	defer markArmed()

	ioEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return
	}
	defer windows.CloseHandle(ioEvent)

	stopEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return
	}
	defer windows.CloseHandle(stopEvent)

	stopDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = windows.SetEvent(stopEvent)
			_ = windows.CancelIoEx(dirHandle, nil)
		case <-stopDone:
		}
	}()
	defer close(stopDone)

	buf := make([]byte, watchBufferBytes)
	handles := []windows.Handle{ioEvent, stopEvent}

	for {
		if ctx.Err() != nil {
			return
		}
		if err := windows.ResetEvent(ioEvent); err != nil {
			return
		}
		var ov windows.Overlapped
		ov.HEvent = ioEvent

		err := windows.ReadDirectoryChanges(
			dirHandle,
			&buf[0],
			uint32(len(buf)),
			subtree,
			watchNotifyMask,
			nil,
			&ov,
			0,
		)
		markArmed()
		if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
			if errors.Is(err, windows.ERROR_NOTIFY_ENUM_DIR) {
				emitRaw(ctx, out, WatchEvent{Kind: WatchReconcileOverflow, Root: root})
				continue
			}
			return
		}

		waitRes, werr := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
		if werr != nil || waitRes == windows.WAIT_OBJECT_0+1 {
			// Stop requested: cancel pending I/O and wait for kernel completion
			// before freeing buf and ov on stack/heap.
			_ = windows.CancelIoEx(dirHandle, &ov)
			var ignored uint32
			_ = windows.GetOverlappedResult(dirHandle, &ov, &ignored, true)
			return
		}
		if waitRes != windows.WAIT_OBJECT_0 {
			return
		}

		var n uint32
		err = windows.GetOverlappedResult(dirHandle, &ov, &n, false)
		if err != nil {
			if errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
				return
			}
			if errors.Is(err, windows.ERROR_NOTIFY_ENUM_DIR) {
				emitRaw(ctx, out, WatchEvent{Kind: WatchReconcileOverflow, Root: root})
				continue
			}
			return
		}
		if n == 0 {
			// Zero bytes returned on ReadDirectoryChangesW indicates internal
			// buffer overflow: trigger incremental reconcile.
			emitRaw(ctx, out, WatchEvent{Kind: WatchReconcileOverflow, Root: root})
			continue
		}
		parseNotifyBuffer(ctx, root, buf[:n], out)
	}
}

func parseNotifyBuffer(ctx context.Context, root string, data []byte, out chan<- WatchEvent) {
	const nameOffset = unsafe.Offsetof(windows.FileNotifyInformation{}.FileName)
	offset := uint32(0)
	for int(offset)+int(nameOffset) <= len(data) {
		raw := (*windows.FileNotifyInformation)(unsafe.Pointer(&data[offset]))
		nameBytes := raw.FileNameLength
		nameStart := offset + uint32(nameOffset)
		if int(nameStart+nameBytes) > len(data) {
			break
		}
		u16Len := nameBytes / 2
		u16 := unsafe.Slice(&raw.FileName, u16Len)
		rel := windows.UTF16ToString(u16)
		normRel, err := NormalizeRelativePath(rel)
		if err == nil {
			kind := WatchFileChanged
			switch raw.Action {
			case windows.FILE_ACTION_REMOVED, windows.FILE_ACTION_RENAMED_OLD_NAME:
				kind = WatchFileRemoved
			case windows.FILE_ACTION_ADDED, windows.FILE_ACTION_MODIFIED, windows.FILE_ACTION_RENAMED_NEW_NAME:
				kind = WatchFileChanged
			}
			emitRaw(ctx, out, WatchEvent{Kind: kind, Root: root, RelPath: normRel})
		}
		if raw.NextEntryOffset == 0 {
			break
		}
		if raw.NextEntryOffset > uint32(len(data))-offset {
			break
		}
		offset += raw.NextEntryOffset
	}
}

func emitRaw(ctx context.Context, out chan<- WatchEvent, ev WatchEvent) {
	select {
	case out <- ev:
	case <-ctx.Done():
	}
}

func runWakeDetector(ctx context.Context, interval time.Duration, now func() time.Time, out chan<- WatchEvent) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastWall := now().UnixNano()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			curWall := now().UnixNano()
			wallDelta := time.Duration(curWall - lastWall)
			lastWall = curWall
			// A wall-clock advance significantly larger than the ticker interval
			// indicates system sleep/hibernation resume (or clock forward jump).
			if wallDelta > interval+sleepJumpThreshold {
				emitRaw(ctx, out, WatchEvent{Kind: WatchReconcileWake})
			}
		}
	}
}

// runDebouncer merges bursts of rapid events for the same (root, relPath) key
// so a multi-write PNG flush only triggers a single scan after writes settle.
func runDebouncer(ctx context.Context, delay time.Duration, in <-chan WatchEvent, out chan<- WatchEvent) {
	type key struct {
		root, rel string
	}
	type pendingItem struct {
		ev       WatchEvent
		deadline time.Time
	}
	pending := make(map[key]pendingItem)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	defer timer.Stop()

	resetNext := func() {
		if len(pending) == 0 {
			return
		}
		var earliest time.Time
		for _, item := range pending {
			if earliest.IsZero() || item.deadline.Before(earliest) {
				earliest = item.deadline
			}
		}
		wait := time.Until(earliest)
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
	}

	send := func(ev WatchEvent) {
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-in:
			if ev.Kind == WatchReconcileOverflow || ev.Kind == WatchReconcileWake {
				send(ev)
				continue
			}
			k := key{
				root: strings.ToLower(filepath.Clean(ev.Root)),
				rel:  strings.ToLower(ev.RelPath),
			}
			pending[k] = pendingItem{ev: ev, deadline: time.Now().Add(delay)}
			resetNext()
		case <-timer.C:
			now := time.Now()
			for k, item := range pending {
				if !now.Before(item.deadline) {
					delete(pending, k)
					send(item.ev)
				}
			}
			resetNext()
		}
	}
}
