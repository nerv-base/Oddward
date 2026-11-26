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
	// Output MD5 is streamed during decode without reading back the written file.
	wantRead := int64(len(old) + 2*len(patch) + 1 + len(next))
	if stats.ReadBytes != wantRead || stats.WrittenBytes != int64(len(next)) || stats.SourceVerifiedBytes != int64(len(old)) {
		t.Fatalf("actual I/O = %+v; want read=%d written=%d verified-source=%d", stats, wantRead, len(next), len(old))
	}
}

func TestApplyVerifiedInputsAvoidRedundantPreHashAndClassifyCorruption(t *testing.T) {
	old := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB
	next := append([]byte(nil), old...)
	// Single cover copying entire old with zero delta.
	covers := append(append(packed(0, 1, 0), packed(0, 0, 0)...), packed(uint64(len(old)), 0, 0)...)
	controls := packed(uint64(len(old)-1), 2, 0)
	patch := wire(uint64(len(next)), uint64(len(old)), 1, [4][]byte{covers, controls, nil, nil})

	t.Run("skips-prehash", func(t *testing.T) {
		o := fixture(t, old, patch, next)
		path, stats, err := ApplyChainMeasured(context.Background(), []Step{{
			OldPath: o.OldPath, PatchPath: o.PatchPath,
			Old: o.OldExpect, Patch: o.PatchExpect, New: o.Expect,
			OldVerified: true, PatchVerified: true,
		}}, filepath.Dir(o.OutputPath), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(filepath.Dir(path))
		unchanged(t, path, next)
		// Skipping pre-hash reads old once during decode, never twice.
		if stats.ReadBytes >= int64(2*len(old)) {
			t.Fatalf("verified inputs still pre-hashed old: read=%d old=%d", stats.ReadBytes, len(old))
		}
		if stats.WrittenBytes != int64(len(next)) || stats.SourceVerifiedBytes != int64(len(old)) {
			t.Fatalf("unexpected stats: %+v", stats)
		}
	})

	t.Run("corrupt-old-detected", func(t *testing.T) {
		o := fixture(t, old, patch, next)
		corrupt := append([]byte(nil), old...)
		corrupt[1024] ^= 0xff
		if err := os.WriteFile(o.OldPath, corrupt, 0600); err != nil {
			t.Fatal(err)
		}
		o.OldVerified, o.PatchVerified = true, true
		if err := Apply(context.Background(), o); !errors.Is(err, ErrSourceMismatch) {
			t.Fatalf("corrupt verified old got %v, want ErrSourceMismatch", err)
		}
		absent(t, o.OutputPath)
	})

	t.Run("corrupt-patch-detected", func(t *testing.T) {
		o := fixture(t, old, patch, next)
		corrupt := append([]byte(nil), patch...)
		corrupt[0] ^= 0xff
		if err := os.WriteFile(o.PatchPath, corrupt, 0600); err != nil {
			t.Fatal(err)
		}
		o.OldVerified, o.PatchVerified = true, true
		if err := Apply(context.Background(), o); !errors.Is(err, ErrPatchMismatch) {
			t.Fatalf("corrupt verified patch got %v, want ErrPatchMismatch", err)
		}
		absent(t, o.OutputPath)
	})
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

func TestApplyBufferedOldCoalescesSmallCoversAndHandlesBackwardSeeks(t *testing.T) {
	const count = 512
	const chunk = 64
	old := make([]byte, count*chunk)
	for i := range old {
		old[i] = byte((i*31 + 7) & 0xff)
	}
	// Interleave forward small covers and a final backward seek back to offset 0.
	var covers []byte
	var next []byte
	for i := 0; i < count; i++ {
		covers = append(covers, packed(0, 1, 0)...)
		covers = append(covers, packed(0, 0, 0)...)
		covers = append(covers, packed(chunk, 0, 0)...)
		next = append(next, old[i*chunk:(i+1)*chunk]...)
	}
	// Backward jump by len(old) back to offset 0 for one extra chunk.
	covers = append(covers, packed(uint64(len(old)), 1, 1)...)
	covers = append(covers, packed(0, 0, 0)...)
	covers = append(covers, packed(chunk, 0, 0)...)
	next = append(next, old[:chunk]...)
	controls := packed(uint64(len(next)-1), 2, 0)
	patch := wire(uint64(len(next)), uint64(len(old)), count+1, [4][]byte{covers, controls, nil, nil})

	o := fixture(t, old, patch, next)
	path, stats, err := ApplyChainMeasured(context.Background(), []Step{{
		OldPath: o.OldPath, PatchPath: o.PatchPath,
		Old: o.OldExpect, Patch: o.PatchExpect, New: o.Expect,
		OldVerified: true, PatchVerified: true,
	}}, filepath.Dir(o.OutputPath), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(path))
	unchanged(t, path, next)
	// First read loads all 32 KiB of old into the 1 MiB window; all 512 forward
	// covers AND the backward jump to offset 0 hit the cached window without a
	// second read of old.
	if stats.ReadBytes > int64(len(old)+2*len(patch)) {
		t.Fatalf("expected old window hit, read=%d old=%d patch=%d", stats.ReadBytes, len(old), len(patch))
	}
	if stats.WrittenBytes != int64(len(next)) {
		t.Fatalf("written=%d want %d", stats.WrittenBytes, len(next))
	}
}
