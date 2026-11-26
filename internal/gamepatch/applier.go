// SPDX-License-Identifier: GPL-3.0-or-later
// Package gamepatch rebuilds verified new objects from uncompressed HDIFF13
// patches. It does not download, modify installed inputs, or commit updates.
package gamepatch

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nerv-base/Oddward/internal/gameres"
)

var (
	ErrUnsupportedPatch = errors.New("unsupported patch format")
	ErrCorruptPatch     = errors.New("corrupt patch")
	ErrSourceMismatch   = errors.New("patch source identity mismatch")
	ErrPatchMismatch    = errors.New("patch blob identity mismatch")
	ErrOutputMismatch   = errors.New("rebuilt output identity mismatch")
)

// Options describes a single rebuild. All three identities are mandatory.
// OutputPath must name a nonexistent file in the caller's rebuild workspace.
// Progress reports bytes actually written, before final validation/publication.
// Progress must not mutate inputs; cancellation from a callback is supported.
// Callers must prevent concurrent modification of the input files.
type Options struct {
	OldPath, PatchPath, OutputPath string
	OldExpect, PatchExpect, Expect gameres.Object
	OldVerified, PatchVerified     bool
	Progress                       func(written, total int64)
}

const bufferSize = 1 << 20

// Apply verifies patch, source, and rebuilt output using whole-file MD5 and
// size. Inputs are opened read-only. Only success publishes OutputPath, using
// an exclusive hard link from a verified private partial file (NTFS supports
// this); unsupported filesystems return their underlying error. Existing output
// paths, including aliases, are never overwritten or removed. Failed calls
// remove only their own private partial file. No transaction is performed.
func Apply(ctx context.Context, o Options) error {
	return apply(ctx, o, nil)
}

func apply(ctx context.Context, o Options, stats *IOStats) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, v := range []struct {
		id   gameres.Object
		kind error
	}{{o.OldExpect, ErrSourceMismatch}, {o.PatchExpect, ErrPatchMismatch}, {o.Expect, ErrOutputMismatch}} {
		if !validIdentity(v.id) {
			return fmt.Errorf("%w: invalid expected identity", v.kind)
		}
	}
	root, err := os.OpenRoot(filepath.Dir(o.OutputPath))
	if err != nil {
		return err
	}
	defer root.Close()
	output := filepath.Base(o.OutputPath)
	if _, err = root.Lstat(output); err == nil {
		return fmt.Errorf("output already exists: %w", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Verify the patch before opening/reading the old source unless the caller
	// already proved its identity under the active work lease.
	patch, err := openReadOnly(o.PatchPath)
	if err != nil {
		return err
	}
	defer patch.Close()
	buffer := make([]byte, bufferSize)
	mpatch := measuredFile{patch, stats}
	if o.PatchVerified {
		if err = checkFileSize(mpatch, o.PatchExpect, ErrPatchMismatch); err != nil {
			return err
		}
	} else if err = verifyFile(ctx, mpatch, o.PatchExpect, buffer, ErrPatchMismatch); err != nil {
		return err
	}
	header, err := readHeader(mpatch, o.PatchExpect.Size)
	if err != nil {
		if o.PatchVerified {
			if verr := verifyFile(ctx, mpatch, o.PatchExpect, buffer, ErrPatchMismatch); verr != nil {
				return verr
			}
		}
		return err
	}
	if header.newSize != o.Expect.Size {
		if o.PatchVerified {
			if verr := verifyFile(ctx, mpatch, o.PatchExpect, buffer, ErrPatchMismatch); verr != nil {
				return verr
			}
		}
		return fmt.Errorf("%w: header newSize=%d expected=%d", ErrOutputMismatch, header.newSize, o.Expect.Size)
	}
	if header.oldSize != o.OldExpect.Size {
		if o.PatchVerified {
			if verr := verifyFile(ctx, mpatch, o.PatchExpect, buffer, ErrPatchMismatch); verr != nil {
				return verr
			}
		}
		return fmt.Errorf("%w: header oldSize=%d expected=%d", ErrSourceMismatch, header.oldSize, o.OldExpect.Size)
	}
	old, err := openReadOnly(o.OldPath)
	if err != nil {
		return err
	}
	defer old.Close()
	mold := measuredFile{old, stats}
	if o.OldVerified {
		if err = checkFileSize(mold, o.OldExpect, ErrSourceMismatch); err != nil {
			return err
		}
	} else if err = verifyFile(ctx, mold, o.OldExpect, buffer, ErrSourceMismatch); err != nil {
		return err
	}
	if stats != nil && !o.OldVerified {
		stats.SourceVerifiedBytes += o.OldExpect.Size
	}
	temp, err := privateName()
	if err != nil {
		return err
	}
	out, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if out != nil {
			result = errors.Join(result, out.Close())
		}
		if err := root.Remove(temp); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	gotMD5, err := decode(ctx, mold, mpatch, measuredFile{out, stats}, header, buffer, o.Progress)
	if err != nil {
		if ctx.Err() == nil {
			if o.PatchVerified {
				if verr := verifyFile(ctx, mpatch, o.PatchExpect, buffer, ErrPatchMismatch); verr != nil {
					return verr
				}
			}
			if o.OldVerified {
				if verr := verifyFile(ctx, mold, o.OldExpect, buffer, ErrSourceMismatch); verr != nil {
					return verr
				}
			}
		}
		return fmt.Errorf("HDIFF13 oldSize=%d newSize=%d covers=%d: %w", header.oldSize, header.newSize, header.coverCount, err)
	}
	if gotMD5 != o.Expect.MD5 {
		if o.PatchVerified {
			if verr := verifyFile(ctx, mpatch, o.PatchExpect, buffer, ErrPatchMismatch); verr != nil {
				return verr
			}
		}
		if o.OldVerified {
			if verr := verifyFile(ctx, mold, o.OldExpect, buffer, ErrSourceMismatch); verr != nil {
				return verr
			}
		}
		return fmt.Errorf("%w: whole-file MD5", ErrOutputMismatch)
	}
	if err = checkFileSize(out, o.Expect, ErrOutputMismatch); err != nil {
		return err
	}
	if stats != nil && o.OldVerified {
		stats.SourceVerifiedBytes += o.OldExpect.Size
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		out = nil
		return err
	}
	out = nil
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = root.Link(temp, output); err != nil {
		return err
	}
	// If private-name removal fails, undo our publication rather than report a
	// failed call while leaving a requested output behind.
	if err = root.Remove(temp); err != nil {
		return errors.Join(err, root.Remove(output))
	}
	return nil
}

func privateName() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return ".gamepatch-" + hex.EncodeToString(b[:]) + ".partial", nil
}

func validIdentity(o gameres.Object) bool {
	if o.Size < 0 || len(o.MD5) != 32 {
		return false
	}
	for _, c := range o.MD5 {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func openReadOnly(path string) (*os.File, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("input is not a regular file")
	}
	return f, nil
}

func checkFileSize(f interface{ Stat() (os.FileInfo, error) }, want gameres.Object, kind error) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != want.Size {
		return fmt.Errorf("%w: size=%d expected=%d", kind, info.Size(), want.Size)
	}
	return nil
}

func verifyFile(ctx context.Context, f readableFile, want gameres.Object, buffer []byte, kind error) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != want.Size {
		return fmt.Errorf("%w: size=%d expected=%d", kind, info.Size(), want.Size)
	}
	hash := md5.New()
	for offset := int64(0); offset < want.Size; {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := int64(len(buffer))
		if n > want.Size-offset {
			n = want.Size - offset
		}
		if _, err := f.ReadAt(buffer[:n], offset); err != nil {
			return err
		}
		hash.Write(buffer[:n])
		offset += n
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != want.MD5 {
		return fmt.Errorf("%w: whole-file MD5", kind)
	}
	// Detect size changes during validation, in addition to requiring caller
	// serialization against concurrent input writers.
	info, err = f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != want.Size {
		return fmt.Errorf("%w: size changed", kind)
	}
	return nil
}

func writeFull(w io.Writer, p []byte) error {
	n, err := w.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return err
}
