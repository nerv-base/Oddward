// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFreshDownloadLocationDoesNotWriteAndRejectsUsedDirectory(t *testing.T) {
	parent := t.TempDir()
	selected := filepath.Join(parent, "新安装")
	if got, err := freshDownloadRoot(selected); err != nil || got != selected {
		t.Fatalf("fresh = %q, %v", got, err)
	}
	if _, err := os.Stat(selected); !os.IsNotExist(err) {
		t.Fatalf("picker validation created root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parent, "user-file"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := freshDownloadRoot(parent); err == nil {
		t.Fatal("accepted non-empty directory")
	}
	if _, err := freshDownloadRoot(filepath.VolumeName(parent) + `\`); err == nil {
		t.Fatal("accepted drive root")
	}
	if _, err := freshDownloadRoot(filepath.Join(parent, "existing.")); err == nil {
		t.Fatal("accepted trailing dot alias")
	}
}

func TestDeliveryPathCanonicalizesOfficialRelativeShape(t *testing.T) {
	for _, path := range []string{"HT/Content/Paks/a.pak", "Client/WindowsNoEditor/HT/Content/Paks/a.pak"} {
		got, err := downloadTargetPath(path)
		if got != "Client/WindowsNoEditor/HT/Content/Paks/a.pak" || err != nil {
			t.Fatalf("path = %q, %v", got, err)
		}
	}
	for _, path := range []string{"Client/Elsewhere/a.pak", "../escape", "PatcherConfig/Patch_Windows.json", "Client/WindowsNoEditor/PatcherConfig/Patch_Windows.source.json"} {
		if _, err := downloadTargetPath(path); err == nil {
			t.Errorf("accepted protected or invalid %q", path)
		}
	}
}
