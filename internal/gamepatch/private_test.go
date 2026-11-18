// SPDX-License-Identifier: GPL-3.0-or-later
package gamepatch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gameres"
)

// Private fixtures are deliberately not embedded or discovered automatically.
// The extra authorization flag ensures merely inheriting a directory variable
// cannot make the default suite read real game data without explicit opt-in.
type privateStep struct {
	OldPath   string         `json:"old_path"`
	PatchPath string         `json:"patch_path"`
	Old       gameres.Object `json:"old"`
	Patch     gameres.Object `json:"patch"`
	New       gameres.Object `json:"new"`
}
type privateCase struct {
	Name  string        `json:"name"`
	Steps []privateStep `json:"steps"`
}
type privateManifest struct {
	Cases []privateCase `json:"cases"`
}

func TestApplyPrivateFixtures(t *testing.T) {
	dir := os.Getenv("ODDWARD_PATCH_FIXTURES")
	if dir == "" || os.Getenv("ODDWARD_PATCH_FIXTURES_AUTHORIZED") != "1" {
		t.Skip("real compatibility not tested: requires explicit ODDWARD_PATCH_FIXTURES and ODDWARD_PATCH_FIXTURES_AUTHORIZED=1 authorization")
	}
	if testing.Short() {
		t.Skip("private compatibility fixtures disabled in short mode")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	manifest, err := root.Open("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer manifest.Close()
	decoder := json.NewDecoder(io.LimitReader(manifest, (1<<20)+1))
	decoder.DisallowUnknownFields()
	var m privateManifest
	if err := decoder.Decode(&m); err != nil {
		t.Fatal(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatal("manifest contains trailing data", err)
	}
	if len(m.Cases) == 0 || len(m.Cases) > 100 {
		t.Fatal("expected 1..100 explicitly listed cases")
	}
	for caseIndex, c := range m.Cases {
		t.Run(fmt.Sprintf("case-%d", caseIndex), func(t *testing.T) {
			// Names and absolute private paths are omitted from test logs.
			if len(c.Steps) == 0 || len(c.Steps) > 4 {
				t.Fatal("expected 1..4 steps")
			}
			steps := make([]Step, len(c.Steps))
			for i, s := range c.Steps {
				patch, err := privateInputPath(root, dir, s.PatchPath)
				if err != nil {
					t.Fatal("invalid private patch path")
				}
				old := ""
				if i == 0 {
					old, err = privateInputPath(root, dir, s.OldPath)
					if err != nil {
						t.Fatal("invalid private old path")
					}
				} else if s.OldPath != "" {
					t.Fatal("only first step may specify old_path")
				}
				steps[i] = Step{OldPath: old, PatchPath: patch, Old: s.Old, Patch: s.Patch, New: s.New}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
			defer cancel()
			started := time.Now()
			output, err := ApplyChain(ctx, steps, t.TempDir(), nil)
			if err != nil {
				t.Fatalf("private fixture failed: %v", err)
			}
			// Independent final identity verification after the chain has returned.
			f, err := os.Open(output)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := verifyFile(ctx, f, steps[len(steps)-1].New, make([]byte, bufferSize), ErrOutputMismatch); err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(started)
			t.Logf("synthetic=false steps=%d output_bytes=%d duration=%s output_MiB_per_second=%.2f", len(steps), steps[len(steps)-1].New.Size, elapsed, float64(steps[len(steps)-1].New.Size)/(1<<20)/elapsed.Seconds())
		})
	}
}

func privateInputPath(root *os.Root, dir, name string) (string, error) {
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("fixture path must be relative")
	}
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("fixture input must be regular")
	}
	return filepath.Join(dir, name), nil
}

func TestPrivateFixturePathsAreConfined(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, path := range []string{"", "../escape", `C:\private\file`, `\absolute`, "missing"} {
		if _, err := privateInputPath(root, root.Name(), path); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
}
