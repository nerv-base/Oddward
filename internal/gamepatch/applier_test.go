// SPDX-License-Identifier: GPL-3.0-or-later
package gamepatch

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyLiteralAndEmpty(t *testing.T) {
	for _, new := range [][]byte{[]byte("new bytes"), nil} {
		t.Run(string(new), func(t *testing.T) {
			old := []byte("old immutable")
			o := fixture(t, old, literalPatch(old, new), new)
			if err := Apply(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			unchanged(t, o.OutputPath, new)
			unchanged(t, o.OldPath, old)
		})
	}
}
func TestApplyCoversSignedOffsetsAllRLEKinds(t *testing.T) {
	old := []byte{10, 20, 30, 40, 50, 60}
	// gap='X'; cover [3:5], then backward from old end 5 to 0,
	// then cover [2:4]. RLE spans literal and cover boundaries.
	covers := []byte{3, 1, 2, 0x85, 0, 2, 0, 0, 2}
	controls := append(packed(0, 2, 3), packed(1, 2, 0)...)
	controls = append(controls, packed(0, 2, 1)...)
	controls = append(controls, packed(0, 2, 2)...)
	controls = append(controls, packed(1, 2, 3)...)
	patch := wire(7, 6, 3, [4][]byte{covers, controls, {99, 5, 250, 1}, []byte("X")})
	new := []byte{'X', 40, 50, 9, 25, 24, 41}
	o := fixture(t, old, patch, new)
	if err := Apply(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	unchanged(t, o.OutputPath, new)
	unchanged(t, o.OldPath, old)
}
func TestApplyRejectsMalformedWire(t *testing.T) {
	good := literalPatch([]byte("o"), []byte("n"))
	tests := []struct {
		name  string
		patch []byte
		want  error
	}{
		{"magic", []byte("HDIFFSF20"), ErrUnsupportedPatch},
		{"compression", append(append([]byte{}, good[:13]...), append([]byte{1}, good[14:]...)...), ErrUnsupportedPatch},
		{"short header", []byte("HDIFF13&\x00\x80"), ErrCorruptPatch},
		{"overflow", append([]byte("HDIFF13&\x00"), packed(math.MaxUint64, 0, 0)...), ErrCorruptPatch},
		{"overlong integer", append(append(append([]byte("HDIFF13&\x00"), bytes.Repeat([]byte{0x80}, 10)...), 1), good[10:]...), ErrCorruptPatch},
		{"section size", wire(1, 1, 0, [4][]byte{nil, {0}, nil, {'n', 'x'}}), ErrCorruptPatch},
		{"tail", append(append([]byte{}, good...), 0), ErrCorruptPatch},
		{"cover source escape", wire(1, 1, 1, [4][]byte{{2, 0, 1}, {0}, nil, nil}), ErrCorruptPatch},
		{"cover negative", wire(1, 1, 1, [4][]byte{{0x81, 0, 1}, {0}, nil, nil}), ErrCorruptPatch},
		{"cover destination escape", wire(1, 1, 1, [4][]byte{{0, 1, 1}, {1}, nil, {'x'}}), ErrCorruptPatch},
		{"cover count", wire(1, 1, 2, [4][]byte{{0, 0, 1}, {0}, nil, nil}), ErrCorruptPatch},
		{"rle underflow", wire(1, 1, 0, [4][]byte{nil, nil, nil, {'n'}}), ErrCorruptPatch},
		{"rle leftover", wire(1, 1, 0, [4][]byte{nil, {1}, nil, {'n'}}), ErrCorruptPatch},
		{"rle code underflow", wire(1, 1, 0, [4][]byte{nil, {0xc0}, nil, {'n'}}), ErrCorruptPatch},
		{"rle code leftover", wire(1, 1, 0, [4][]byte{nil, {0}, {5}, {'n'}}), ErrCorruptPatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := fixture(t, []byte("o"), tt.patch, []byte("n"))
			err := Apply(context.Background(), o)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			absent(t, o.OutputPath)
			unchanged(t, o.OldPath, []byte("o"))
		})
	}
}
func TestApplyValidatesAllIdentities(t *testing.T) {
	for _, which := range []string{"old md5", "old size", "patch md5", "patch size", "output md5", "output size", "old header", "invalid identity"} {
		t.Run(which, func(t *testing.T) {
			old, new := []byte("old"), []byte("new")
			patch := literalPatch(old, new)
			o := fixture(t, old, patch, new)
			want := ErrOutputMismatch
			switch which {
			case "old md5":
				o.OldExpect.MD5 = strings.Repeat("0", 32)
				want = ErrSourceMismatch
			case "old size":
				o.OldExpect.Size++
				want = ErrSourceMismatch
			case "patch md5":
				o.PatchExpect.MD5 = strings.Repeat("0", 32)
				want = ErrPatchMismatch
			case "patch size":
				o.PatchExpect.Size++
				want = ErrPatchMismatch
			case "output md5":
				o.Expect.MD5 = strings.Repeat("0", 32)
			case "output size":
				o.Expect.Size++
			case "old header":
				patch = literalPatch([]byte("long old"), new)
				os.WriteFile(o.PatchPath, patch, 0600)
				o.PatchExpect = object(patch)
				want = ErrSourceMismatch
			case "invalid identity":
				o.OldExpect.MD5 = ""
				want = ErrSourceMismatch
			}
			if err := Apply(context.Background(), o); !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			absent(t, o.OutputPath)
			unchanged(t, o.OldPath, old)
		})
	}
}
func TestApplyExclusiveOutputAndAliases(t *testing.T) {
	for _, kind := range []string{"existing", "old", "patch", "hardlink", "symlink", "case alias", "dot alias"} {
		t.Run(kind, func(t *testing.T) {
			old, new := []byte("old"), []byte("new")
			o := fixture(t, old, literalPatch(old, new), new)
			switch kind {
			case "existing":
				if err := os.WriteFile(o.OutputPath, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "old":
				o.OutputPath = o.OldPath
			case "patch":
				o.OutputPath = o.PatchPath
			case "hardlink":
				if err := os.Link(o.OldPath, o.OutputPath); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(o.OldPath, o.OutputPath); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "case alias":
				o.OutputPath = filepath.Join(filepath.Dir(o.OldPath), "OLD")
			case "dot alias":
				o.OutputPath = filepath.Dir(o.OldPath) + string(os.PathSeparator) + "." + string(os.PathSeparator) + "old"
			}
			if err := Apply(context.Background(), o); err == nil {
				t.Fatal("accepted existing/aliased destination")
			}
			unchanged(t, o.OldPath, old)
		})
	}
}
func TestApplyStreamingCancellationAndAtomicPublication(t *testing.T) {
	old := []byte("old")
	new := bytes.Repeat([]byte("N"), 3<<20)
	o := fixture(t, old, literalPatch(old, new), new)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	o.Progress = func(written, total int64) {
		calls++
		if total != int64(len(new)) || written <= 0 || written > 1<<20 {
			t.Errorf("unexpected progress %d/%d", written, total)
		}
		absent(t, o.OutputPath)
		cancel()
	}
	if err := Apply(ctx, o); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if calls != 1 {
		t.Fatalf("progress calls=%d", calls)
	}
	absent(t, o.OutputPath)
	unchanged(t, o.OldPath, old)
	files, err := filepath.Glob(filepath.Join(filepath.Dir(o.OutputPath), ".gamepatch-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary files: %v %v", files, err)
	}
	o.Progress = nil
	if err := Apply(context.Background(), o); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	unchanged(t, o.OutputPath, new)
}
func TestApplyPublicationNeverOverwritesConcurrentOutput(t *testing.T) {
	old, new := []byte("old"), []byte("new")
	o := fixture(t, old, literalPatch(old, new), new)
	o.Progress = func(written, total int64) {
		if err := os.WriteFile(o.OutputPath, []byte("concurrent owner"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Apply(context.Background(), o); !errors.Is(err, os.ErrExist) {
		t.Fatalf("got %v, expected exclusive publication failure", err)
	}
	unchanged(t, o.OutputPath, []byte("concurrent owner"))
	unchanged(t, o.OldPath, old)
	files, err := filepath.Glob(filepath.Join(filepath.Dir(o.OutputPath), ".gamepatch-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("partial leaked: %v %v", files, err)
	}
}

func TestApplyPrecancelled(t *testing.T) {
	o := fixture(t, []byte("o"), literalPatch([]byte("o"), []byte("n")), []byte("n"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Apply(ctx, o); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	absent(t, o.OutputPath)
}
