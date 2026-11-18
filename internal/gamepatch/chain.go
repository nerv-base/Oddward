// SPDX-License-Identifier: GPL-3.0-or-later
package gamepatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nerv-base/Oddward/internal/gameres"
)

var ErrInvalidChain = errors.New("invalid patch chain")

// Step links three verified identities. OldPath is required on the first step
// and must be empty thereafter: later input comes only from the preceding
// verified output. PatchPath is required on every step.
type Step struct {
	OldPath, PatchPath string
	Old, Patch, New    gameres.Object
}

// ApplyChain serially rebuilds a chain of one to four steps under workdir,
// which must already exist in the caller's rebuild workspace. Identity links
// are checked before creating files. Each invocation exclusively owns a fresh
// child directory, so interrupted invocations are never mistaken for results.
//
// report receives a zero-based step index and per-step byte counts; each step
// begins with (step,0,newSize). Only a fully verified final path is returned.
// Intermediates are removed as soon as the next step succeeds. Any error removes
// this invocation's directory and returns an empty path; unrelated files and
// abandoned directories are left for the caller's recovery policy. After
// success the caller owns the returned file and its enclosing directory.
func ApplyChain(ctx context.Context, steps []Step, workdir string, report func(step int, written, total int64)) (string, error) {
	return applyChain(ctx, steps, workdir, report, nil)
}

func applyChain(ctx context.Context, steps []Step, workdir string, report func(step int, written, total int64), stats *IOStats) (output string, result error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(steps) < 1 || len(steps) > 4 {
		return "", fmt.Errorf("%w: steps=%d (expected 1..4)", ErrInvalidChain, len(steps))
	}
	// Snapshot the descriptors: callbacks cannot redirect subsequent steps.
	steps = append([]Step(nil), steps...)
	for i, s := range steps {
		if !validIdentity(s.Old) || !validIdentity(s.Patch) || !validIdentity(s.New) || s.PatchPath == "" {
			return "", fmt.Errorf("%w: step=%d identity/path", ErrInvalidChain, i)
		}
		if i == 0 && s.OldPath == "" || i > 0 && (s.OldPath != "" || s.Old != steps[i-1].New) {
			return "", fmt.Errorf("%w: step=%d source link", ErrInvalidChain, i)
		}
	}
	absolute, err := filepath.Abs(workdir)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return "", err
	}
	defer root.Close()
	name, err := privateName()
	if err != nil {
		return "", err
	}
	if err = root.Mkdir(name, 0700); err != nil {
		return "", err
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, root.RemoveAll(name))
			output = ""
		}
	}()
	owned, err := root.OpenRoot(name)
	if err != nil {
		return "", err
	}
	defer owned.Close()
	previous := ""
	for i, s := range steps {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		oldPath := s.OldPath
		if i > 0 {
			oldPath = filepath.Join(absolute, name, previous)
		}
		current := fmt.Sprintf("step-%03d.new", i)
		path := filepath.Join(absolute, name, current)
		if report != nil {
			report(i, 0, s.New.Size)
		}
		var progress func(int64, int64)
		if report != nil {
			progress = func(written, total int64) { report(i, written, total) }
		}
		if err = apply(ctx, Options{OldPath: oldPath, PatchPath: s.PatchPath, OutputPath: path, OldExpect: s.Old, PatchExpect: s.Patch, Expect: s.New, Progress: progress}, stats); err != nil {
			return "", fmt.Errorf("patch chain step=%d: %w", i, err)
		}
		if previous != "" {
			if err = owned.Remove(previous); err != nil {
				return "", err
			}
		}
		previous = current
		output = path
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	return output, nil
}
