// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package gamelocate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func makeFixtureJunction(t *testing.T, link, target string) {
	t.Helper()
	if !filepath.IsAbs(link) || !filepath.IsAbs(target) {
		t.Fatal("junction fixture paths must be explicit absolute local temp paths")
	}
	out, err := exec.Command("cmd", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("synthetic local mklink /J failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func TestInspectRejectsSyntheticJunctionSelection(t *testing.T) {
	root := locateFixture(t)
	link := filepath.Join(t.TempDir(), "linked-root")
	makeFixtureJunction(t, link, root)
	for _, selected := range selections(link) {
		if got, err := Inspect(context.Background(), selected.path); !errors.Is(err, ErrNotInstallation) || got != (Installation{}) {
			t.Fatalf("reparse root or ancestor must not bypass the reused local-path guard: %+v %v", got, err)
		}
	}
}

func TestInspectRejectsEscapingSyntheticExecutableJunction(t *testing.T) {
	root := locateFixture(t)
	other := locateFixture(t)
	win64 := filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Binaries", "Win64")
	otherWin64 := filepath.Join(other, "Client", "WindowsNoEditor", "HT", "Binaries", "Win64")
	// Both source and destination are explicitly constructed inside our fixture;
	// preserve the original tiny fixture instead of deleting or touching installs.
	if !strings.HasPrefix(win64, root+string(filepath.Separator)) {
		t.Fatal("invalid fixture move target")
	}
	if err := os.Rename(win64, win64+"-original"); err != nil {
		t.Fatal(err)
	}
	makeFixtureJunction(t, win64, otherWin64)
	for _, selected := range selections(root) {
		if got, err := Inspect(context.Background(), selected.path); !errors.Is(err, ErrNotInstallation) || got != (Installation{}) {
			t.Fatalf("root-scoped executable read must not escape to another fixture: %+v %v", got, err)
		}
	}
}
