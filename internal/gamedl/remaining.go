// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"context"
	"errors"
	"os"
)

// Remaining conservatively counts bytes that are not in verified cache blobs or
// CRC-checked durable partial chunks. It does not repair state or use HTTP.
func (e *Engine) Remaining(ctx context.Context, transfers []Transfer) (int64, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return 0, failure(ErrLocked, "closed engine", "", nil)
	}
	var remaining int64
	for _, tr := range transfers {
		if err := ctx.Err(); err != nil {
			return 0, classify("remaining", "", err)
		}
		if err := validateTransfer(tr); err != nil {
			return 0, err
		}
		blob, err := e.openBlob(ctx, tr.Object)
		if err == nil {
			blob.Close()
			continue
		}
		var te *TaskError
		if !errors.Is(err, os.ErrNotExist) && (!errors.As(err, &te) || te.Kind != ErrChecksum) {
			return 0, err
		}
		remaining += tr.Object.Size
		chunk := tr.ChunkSize
		if chunk == 0 {
			chunk = e.options.Parameters.ChunkSize
		}
		part, err := openSafe(e.root, partPath(tr.Object), os.O_RDONLY)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		info, err := part.Stat()
		part.Close()
		if err != nil {
			return 0, err
		}
		if info.Size() != tr.Object.Size {
			continue
		}
		data, err := readBounded(e.root, bitmapPath(tr.Object), 1<<20)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			if errors.As(err, &te) && te.Kind == ErrRecoveryRequired {
				continue
			}
			return 0, err
		}
		bm := newBitmap(tr.Object, chunk)
		if bm.restore(data) {
			remaining -= bm.downloaded()
		}
	}
	return remaining, nil
}
