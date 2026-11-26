// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/nerv-base/Oddward/internal/gameres"
	"golang.org/x/sys/windows"
)

// BlobSource returns a freshly verified work-root-relative cache path.
func (e *Engine) BlobSource(ctx context.Context, obj gameres.Object) (string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return "", failure(ErrLocked, "closed engine", "", nil)
	}
	f, err := e.openBlob(ctx, obj)
	if err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	return blobPath(obj), nil
}

// BlobPath checks blob path safety, regular file type, exact size and link
// count without rehashing the content. Callers that rely on full content
// identity must verify the file downstream (for example during patch decode or
// transaction preparation).
func (e *Engine) BlobPath(obj gameres.Object) (string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return "", failure(ErrLocked, "closed engine", "", nil)
	}
	if !validObject(obj) {
		return "", failure(ErrPathUnsafe, "blob identity", "", nil)
	}
	path := blobPath(obj)
	f, err := openSafe(e.root, path, os.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() != obj.Size {
		return "", failure(ErrChecksum, "blob size", obj.String(), nil)
	}
	var handleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &handleInfo); err != nil {
		return "", err
	}
	if handleInfo.NumberOfLinks != 1 {
		return "", failure(ErrPathUnsafe, "hardlinked blob", path, nil)
	}
	return path, nil
}

func objectDir(obj gameres.Object) string  { return "cache/tmp/obj-" + obj.String() }
func partPath(obj gameres.Object) string   { return objectDir(obj) + "/part" }
func bitmapPath(obj gameres.Object) string { return objectDir(obj) + "/rank.bm" }
func blobPath(obj gameres.Object) string   { return "cache/blobs/" + obj.String() }

// VerifyBlob validates the blob's size and full MD5, calling onRead after each
// hashed chunk so callers can measure real verification throughput.
func (e *Engine) VerifyBlob(ctx context.Context, obj gameres.Object, onRead func(int64)) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return failure(ErrLocked, "closed engine", "", nil)
	}
	f, err := e.openBlobProgress(ctx, obj, onRead)
	if err != nil {
		return err
	}
	return f.Close()
}

// OpenBlob revalidates both size and full MD5 on every open, including after
// restart or external modification. The returned handle is rewound and read-only.
func (e *Engine) OpenBlob(ctx context.Context, obj gameres.Object) (*os.File, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return nil, failure(ErrLocked, "closed engine", "", nil)
	}
	return e.openBlob(ctx, obj)
}
func (e *Engine) openBlob(ctx context.Context, obj gameres.Object) (*os.File, error) {
	return e.openBlobProgress(ctx, obj, nil)
}
func (e *Engine) openBlobProgress(ctx context.Context, obj gameres.Object, onRead func(int64)) (*os.File, error) {
	if !validObject(obj) {
		return nil, failure(ErrPathUnsafe, "blob identity", "", nil)
	}
	f, err := openSafe(e.root, blobPath(obj), os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	if err = verifyProgress(ctx, f, obj, onRead); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Publish validates an existing root-relative temporary file before a same-root
// rename. Existing valid blobs are retained. Cache files never share hardlinks.
func (e *Engine) Publish(ctx context.Context, obj gameres.Object, relativePart string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.lease.writer.TryLock() {
		return failure(ErrLocked, "active writer", "lock", nil)
	}
	defer e.lease.writer.Unlock()
	if e.closed {
		return failure(ErrLocked, "closed engine", "", nil)
	}
	return e.publish(ctx, obj, relativePart, true)
}

// PublishVerified publishes a root-relative temporary file whose exact full MD5
// was just verified by the caller (for example via streaming hash during patch
// reconstruction). It still enforces path safety, fsync, regular file type,
// exact size, and single-link ownership.
func (e *Engine) PublishVerified(ctx context.Context, obj gameres.Object, relativePart string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.lease.writer.TryLock() {
		return failure(ErrLocked, "active writer", "lock", nil)
	}
	defer e.lease.writer.Unlock()
	if e.closed {
		return failure(ErrLocked, "closed engine", "", nil)
	}
	return e.publish(ctx, obj, relativePart, false)
}
func (e *Engine) publish(ctx context.Context, obj gameres.Object, part string, verifyHash bool) error {
	if !validObject(obj) || !safeRelative(part) || !strings.HasPrefix(part, "cache/tmp/") {
		return failure(ErrPathUnsafe, "publish source", part, nil)
	}
	f, err := openSafe(e.root, part, os.O_RDWR)
	if err != nil {
		return err
	}
	if err = f.Sync(); err == nil {
		if verifyHash {
			err = verify(ctx, f, obj)
		} else {
			err = verifySize(f, obj)
		}
	}
	var info windows.ByHandleFileInformation
	if err == nil {
		err = windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info)
		if err == nil && info.NumberOfLinks != 1 {
			err = failure(ErrPathUnsafe, "hardlinked blob", part, nil)
		}
	}
	f.Close()
	if err != nil {
		return classify("publish verify", part, err)
	}
	existing, err := e.openBlob(ctx, obj)
	if err == nil {
		existing.Close()
		return removeSafe(e.root, part)
	}
	if !errors.Is(err, os.ErrNotExist) {
		var te *TaskError
		if !errors.As(err, &te) || te.Kind != ErrChecksum {
			return err
		}
		if err = removeSafe(e.root, blobPath(obj)); err != nil {
			return err
		}
	}
	if err = checkPath(e.root, part); err != nil {
		return err
	}
	if err = checkPath(e.root, blobPath(obj)); err != nil {
		return err
	}
	return classify("publish rename", blobPath(obj), e.root.Rename(part, blobPath(obj)))
}
func verifySize(f *os.File, obj gameres.Object) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != obj.Size {
		return failure(ErrChecksum, "blob size", obj.String(), nil)
	}
	return nil
}
func verify(ctx context.Context, f *os.File, obj gameres.Object) error {
	return verifyProgress(ctx, f, obj, nil)
}

func blocksContiguous(blocks []gameres.Block, size int64) bool {
	if len(blocks) == 0 {
		return false
	}
	var pos int64
	for _, b := range blocks {
		if b.Start != pos || b.Size <= 0 {
			return false
		}
		pos += b.Size
	}
	return pos == size
}

// verifyFileAndBlocks validates both contiguous manifest block MD5s and the
// whole-file MD5 in a single sequential pass over f.
func verifyFileAndBlocks(ctx context.Context, f *os.File, obj gameres.Object, blocks []gameres.Block) error {
	if !blocksContiguous(blocks, obj.Size) {
		for _, block := range blocks {
			actual, err := hashReader(ctx, io.NewSectionReader(f, block.Start, block.Size), block.Size)
			if err != nil {
				return err
			}
			if actual != block.MD5 {
				return failure(ErrChecksum, "manifest block MD5", obj.String(), nil)
			}
		}
		return verify(ctx, f, obj)
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != obj.Size {
		return failure(ErrChecksum, "blob size", obj.String(), nil)
	}
	if _, err = f.Seek(0, 0); err != nil {
		return err
	}
	ptr := buffers.Get().(*[]byte)
	defer buffers.Put(ptr)
	buf := *ptr
	whole := md5.New()
	blkIdx := 0
	blkRem := blocks[0].Size
	blkHash := md5.New()
	remaining := obj.Size
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return classify("blob hash", obj.String(), err)
		}
		want := min(int64(len(buf)), remaining)
		n, err := io.ReadFull(f, buf[:want])
		if n > 0 {
			chunk := buf[:n]
			whole.Write(chunk)
			remaining -= int64(n)
			for len(chunk) > 0 {
				take := int(min(int64(len(chunk)), blkRem))
				blkHash.Write(chunk[:take])
				chunk = chunk[take:]
				blkRem -= int64(take)
				if blkRem == 0 {
					if hex.EncodeToString(blkHash.Sum(nil)) != blocks[blkIdx].MD5 {
						return failure(ErrChecksum, "manifest block MD5", obj.String(), nil)
					}
					blkIdx++
					if blkIdx < len(blocks) {
						blkRem = blocks[blkIdx].Size
						blkHash.Reset()
					}
				}
			}
		}
		if err != nil {
			return classify("blob hash", obj.String(), err)
		}
	}
	if hex.EncodeToString(whole.Sum(nil)) != obj.MD5 {
		return failure(ErrChecksum, "blob MD5", obj.String(), nil)
	}
	_, err = f.Seek(0, 0)
	return err
}
func verifyProgress(ctx context.Context, f *os.File, obj gameres.Object, onRead func(int64)) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != obj.Size {
		return failure(ErrChecksum, "blob size", obj.String(), nil)
	}
	if _, err = f.Seek(0, 0); err != nil {
		return err
	}
	digest, err := hashReaderProgress(ctx, f, obj.Size, onRead)
	if err != nil {
		return classify("blob hash", obj.String(), err)
	}
	if digest != obj.MD5 {
		return failure(ErrChecksum, "blob MD5", obj.String(), nil)
	}
	_, err = f.Seek(0, 0)
	return err
}
func hashReader(ctx context.Context, r io.Reader, size int64) (string, error) {
	return hashReaderProgress(ctx, r, size, nil)
}
func hashReaderProgress(ctx context.Context, r io.Reader, size int64, onRead func(int64)) (string, error) {
	h := md5.New()
	ptr := buffers.Get().(*[]byte)
	defer buffers.Put(ptr)
	buffer := *ptr
	for size > 0 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		want := min(int64(len(buffer)), size)
		n, err := io.ReadFull(r, buffer[:want])
		if n > 0 {
			h.Write(buffer[:n])
			size -= int64(n)
			if onRead != nil {
				onRead(int64(n))
			}
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
