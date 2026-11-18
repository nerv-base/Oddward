// SPDX-License-Identifier: GPL-3.0-or-later
package gamepatch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func chainFixture(t *testing.T) ([]Step, string, []byte) {
	t.Helper()
	old, mid, new := []byte("old"), []byte("middle"), []byte("new final")
	first := fixture(t, old, literalPatch(old, mid), mid)
	second := fixture(t, mid, literalPatch(mid, new), new)
	return []Step{{OldPath: first.OldPath, PatchPath: first.PatchPath, Old: first.OldExpect, Patch: first.PatchExpect, New: first.Expect}, {PatchPath: second.PatchPath, Old: second.OldExpect, Patch: second.PatchExpect, New: second.Expect}}, t.TempDir(), new
}
func TestApplyChainTwoStepsCleansIntermediates(t *testing.T) {
	steps, work, new := chainFixture(t)
	var completed []int
	path, err := ApplyChain(context.Background(), steps, work, func(step int, written, total int64) {
		if written == total {
			completed = append(completed, step)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	unchanged(t, path, new)
	if len(completed) != 2 || completed[0] != 0 || completed[1] != 1 {
		t.Fatalf("completion reports %v", completed)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("intermediates not cleaned: %v %v", entries, err)
	}
	for _, step := range steps {
		if _, err := os.Stat(step.PatchPath); err != nil {
			t.Fatal("patch removed", err)
		}
	}
	unchanged(t, steps[0].OldPath, []byte("old"))
	second, err := ApplyChain(context.Background(), steps, work, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second == path {
		t.Fatal("reused owned output directory")
	}
	unchanged(t, second, new)
}
func TestApplyChainSingleStep(t *testing.T) {
	steps, work, _ := chainFixture(t)
	path, err := ApplyChain(context.Background(), steps[:1], work, nil)
	if err != nil {
		t.Fatal(err)
	}
	unchanged(t, path, []byte("middle"))
}
func TestApplyChainFailureCleansOnlyOwnedFiles(t *testing.T) {
	for _, kind := range []string{"second corrupt", "cancel", "unlink intermediate", "bad link", "later source", "empty", "too long"} {
		t.Run(kind, func(t *testing.T) {
			steps, work, _ := chainFixture(t)
			keep := filepath.Join(work, "unrelated")
			if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var report func(int, int64, int64)
			switch kind {
			case "second corrupt":
				bad := []byte("HDIFFSF20")
				if err := os.WriteFile(steps[1].PatchPath, bad, 0600); err != nil {
					t.Fatal(err)
				}
				steps[1].Patch = object(bad)
			case "cancel":
				report = func(step int, written, total int64) {
					if step == 0 {
						cancel()
					}
				}
			case "unlink intermediate":
				report = func(step int, written, total int64) {
					if step == 1 && written == 0 {
						dirs, err := os.ReadDir(work)
						if err != nil {
							t.Fatal(err)
						}
						for _, d := range dirs {
							if d.IsDir() {
								if err := os.Remove(filepath.Join(work, d.Name(), "step-000.new")); err != nil {
									t.Fatal(err)
								}
							}
						}
					}
				}
			case "bad link":
				steps[1].Old = object([]byte("unlinked"))
			case "later source":
				steps[1].OldPath = steps[0].OldPath
			case "empty":
				steps = nil
			case "too long":
				steps = append(steps, steps[1], steps[1], steps[1])
			}
			path, err := ApplyChain(ctx, steps, work, report)
			if err == nil || path != "" {
				t.Fatalf("expected failure without output, got %q %v", path, err)
			}
			if kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(work)
			if err != nil || len(entries) != 1 || entries[0].Name() != "unrelated" {
				t.Fatalf("owned rebuild leaked: %v %v", entries, err)
			}
			unchanged(t, keep, []byte("keep"))
			if len(steps) > 0 {
				unchanged(t, steps[0].OldPath, []byte("old"))
			}
		})
	}
}
func TestApplyCrashRecovery(t *testing.T) {
	const crashEnv = "ODDWARD_GAMEPATCH_SYNTHETIC_CRASH_DIR"
	if dir := os.Getenv(crashEnv); dir != "" {
		old, new := []byte("old immutable"), bytes.Repeat([]byte("N"), 3<<20)
		patch := literalPatch(old, new)
		o := Options{OldPath: filepath.Join(dir, "old"), PatchPath: filepath.Join(dir, "patch"), OutputPath: filepath.Join(dir, "new"), OldExpect: object(old), PatchExpect: object(patch), Expect: object(new), Progress: func(int64, int64) { os.Exit(73) }}
		if err := Apply(context.Background(), o); err != nil {
			t.Fatal(err)
		}
		t.Fatal("crash hook was not reached")
	}
	t.Run("process exits during stream", func(t *testing.T) {
		old, new := []byte("old immutable"), bytes.Repeat([]byte("N"), 3<<20)
		o := fixture(t, old, literalPatch(old, new), new)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestApplyCrashRecovery$")
		command.Env = append(os.Environ(), crashEnv+"="+filepath.Dir(o.OutputPath))
		err := command.Run()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 73 {
			t.Fatalf("expected injected exit 73, got %v", err)
		}
		absent(t, o.OutputPath)
		unchanged(t, o.OldPath, old)
		orphaned, err := filepath.Glob(filepath.Join(filepath.Dir(o.OutputPath), ".gamepatch-*.partial"))
		if err != nil || len(orphaned) != 1 {
			t.Fatalf("expected one private orphan: %v %v", orphaned, err)
		}
		if err := Apply(context.Background(), o); err != nil {
			t.Fatal(err)
		}
		unchanged(t, o.OutputPath, new)
		unchanged(t, o.OldPath, old)
	})
	steps, work, new := chainFixture(t)
	// An interrupted invocation's private directory is untrusted. New calls
	// allocate their own directory, never resume or publish orphan bytes.
	orphan := filepath.Join(work, ".gamepatch-crashed")
	if err := os.Mkdir(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(orphan, "step-000.new")
	if err := os.WriteFile(partial, []byte("unverified partial"), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := ApplyChain(context.Background(), steps, work, nil)
	if err != nil {
		t.Fatal(err)
	}
	unchanged(t, path, new)
	unchanged(t, partial, []byte("unverified partial"))
}
