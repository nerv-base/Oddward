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
	e.mu.Lock()
	defer e.mu.Unlock()
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

func objectDir(obj gameres.Object) string  { return "cache/tmp/obj-" + obj.String() }
func partPath(obj gameres.Object) string   { return objectDir(obj) + "/part" }
func bitmapPath(obj gameres.Object) string { return objectDir(obj) + "/rank.bm" }
func blobPath(obj gameres.Object) string   { return "cache/blobs/" + obj.String() }

// OpenBlob revalidates both size and full MD5 on every open, including after
// restart or external modification. The returned handle is rewound and read-only.
func (e *Engine) OpenBlob(ctx context.Context, obj gameres.Object) (*os.File, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, failure(ErrLocked, "closed engine", "", nil)
	}
	return e.openBlob(ctx, obj)
}
func (e *Engine) openBlob(ctx context.Context, obj gameres.Object) (*os.File, error) {
	if !validObject(obj) {
		return nil, failure(ErrPathUnsafe, "blob identity", "", nil)
	}
	f, err := openSafe(e.root, blobPath(obj), os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	if err = verify(ctx, f, obj); err != nil {
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
	return e.publish(ctx, obj, relativePart)
}
func (e *Engine) publish(ctx context.Context, obj gameres.Object, part string) error {
	if !validObject(obj) || !safeRelative(part) || !strings.HasPrefix(part, "cache/tmp/") {
		return failure(ErrPathUnsafe, "publish source", part, nil)
	}
	f, err := openSafe(e.root, part, os.O_RDWR)
	if err != nil {
		return err
	}
	if err = f.Sync(); err == nil {
		err = verify(ctx, f, obj)
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
func verify(ctx context.Context, f *os.File, obj gameres.Object) error {
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
	digest, err := hashReader(ctx, f, obj.Size)
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
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
