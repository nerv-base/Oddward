// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package game

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// Only our own local temp trees are used. Do not make a remote junction just to
// test rejection: a regressed resolver must never send an SMB authentication attempt.
func TestInspectRejectsReparseRootAndAncestor(t *testing.T) {
	root := fixture(t)
	junction := filepath.Join(t.TempDir(), "linked-client")
	makeJunction(t, junction, filepath.Dir(root))
	for _, selected := range []string{junction, filepath.Join(junction, "WindowsNoEditor")} {
		if _, err := Inspect(context.Background(), selected); !errors.Is(err, ErrNotInstallation) {
			t.Fatalf("linked selection %q accepted: %v", selected, err)
		}
	}
}
func TestInspectRejectsReparseCandidate(t *testing.T) {
	root := fixture(t)
	parent := t.TempDir()
	makeJunction(t, filepath.Join(parent, "WindowsNoEditor"), root)
	if _, err := Inspect(context.Background(), parent); !errors.Is(err, ErrNotInstallation) {
		t.Fatalf("linked candidate accepted: %v", err)
	}
}
