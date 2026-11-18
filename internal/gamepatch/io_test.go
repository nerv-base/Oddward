// SPDX-License-Identifier: GPL-3.0-or-later
package gamepatch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyChainMeasuredCountsConcreteIO(t *testing.T) {
	old, next := []byte("old source"), []byte("rebuilt bytes")
	patch := literalPatch(old, next)
	o := fixture(t, old, patch, next)
	path, stats, err := ApplyChainMeasured(context.Background(), []Step{{OldPath: o.OldPath, PatchPath: o.PatchPath, Old: o.OldExpect, Patch: o.PatchExpect, New: o.Expect}}, filepath.Dir(o.OutputPath), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(path))
	// Small header buffering reads the entire patch once, in addition to its
	// whole-file hash. Decoder reads one RLE control byte and all literals.
	wantRead := int64(len(old) + 2*len(patch) + 1 + len(next) + len(next))
	if stats.ReadBytes != wantRead || stats.WrittenBytes != int64(len(next)) || stats.SourceVerifiedBytes != int64(len(old)) {
		t.Fatalf("actual I/O = %+v; want read=%d written=%d verified-source=%d", stats, wantRead, len(next), len(old))
	}
}

func TestApplyChainMeasuredCancellationRetainsActualCounters(t *testing.T) {
	old, next := []byte("old source"), bytes.Repeat([]byte("n"), 3*bufferSize)
	patch := literalPatch(old, next)
	o := fixture(t, old, patch, next)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path, stats, err := ApplyChainMeasured(ctx, []Step{{OldPath: o.OldPath, PatchPath: o.PatchPath, Old: o.OldExpect, Patch: o.PatchExpect, New: o.Expect}}, filepath.Dir(o.OutputPath), func(_ int, written, _ int64) {
		if written > 0 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || path != "" {
		t.Fatalf("path=%q err=%v", path, err)
	}
	if stats.WrittenBytes != bufferSize || stats.ReadBytes <= int64(len(patch)+len(old)) {
		t.Fatalf("partial I/O=%+v", stats)
	}
	unchanged(t, o.OldPath, old)
}
