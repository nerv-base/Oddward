// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package launchsettings_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nerv-base/Oddward/internal/launchsettings"
	"golang.org/x/sys/windows"
)

func TestMirrorFailureRefusesTheSaveAndCleansStage(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "launcher-v1.json")
	store, db := newStoreFor(t, file)
	previous := launchsettings.Settings{GameRoot: filepath.Join(dir, "old"), RiskVersion: "v1"}
	if err := store.Save(context.Background(), previous); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, file)
	name, err := windows.UTF16PtrFromString(file)
	if err != nil {
		t.Fatal(err)
	}
	// Allow ordinary reads and writes but deny deleting or replacing the directory
	// entry, so the mirror can only be replaced by writing over it in place.
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	want := launchsettings.Settings{GameRoot: filepath.Join(dir, "new"), RiskVersion: "v2"}
	err = store.Save(context.Background(), want)
	if err == nil || !strings.Contains(err.Error(), "write settings mirror") {
		t.Errorf("Save with a blocked mirror = %v; want the mirror write reported", err)
	}
	// The mirror is written before the rows, so a refused mirror write leaves the
	// previous choices and the previous file in place instead of accepting a value
	// that a rolled-back build would then revert.
	requireSettings(t, previous, mustLoad(t, store))
	requireFile(t, file, before)
	if mirrorStale(t, db) {
		t.Error("a refused mirror write must not report a stale mirror")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed mirror write leaked a temporary file: %v, %v", entries, err)
	}
}

func TestReparseAncestorsAndHardlinkedTargetsAreRejected(t *testing.T) {
	base, target := t.TempDir(), t.TempDir()
	link := filepath.Join(base, "junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("create isolated synthetic junction: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	store, db := newStoreFor(t, filepath.Join(link, "launcher-v1.json"))
	if err := store.Save(context.Background(), launchsettings.Settings{}); err == nil {
		t.Error("Save followed a linked mirror parent")
	}
	if _, err := db.RunImport(context.Background(), launchsettings.Domain, launchsettings.ImportVersion, store.ImportLegacy); err == nil {
		t.Error("the import followed a linked mirror parent")
	}
	requireSettings(t, freshProfile(), mustLoad(t, store))
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Errorf("a rejected mirror parent wrote into its target: %v, %v", entries, err)
	}

	// The game root is checked with the same guard, so a junction there is refused
	// before it can be persisted.
	rootStore, _ := newStoreFor(t, filepath.Join(base, "settings.json"))
	if err := rootStore.Save(context.Background(), launchsettings.Settings{GameRoot: link}); err == nil {
		t.Error("persisted a junction game root")
	}

	// A hardlinked legacy file may be a system file this build must not replace.
	original := filepath.Join(base, "original.json")
	before := []byte(`{"version":1,"gameRoot":"","riskVersion":""}`)
	writeLegacy(t, original, before)
	linked := filepath.Join(base, "linked.json")
	if err := os.Link(original, linked); err != nil {
		t.Fatal(err)
	}
	linkedStore, linkedDB := newStoreFor(t, linked)
	if _, err := linkedDB.RunImport(context.Background(), launchsettings.Domain, launchsettings.ImportVersion, linkedStore.ImportLegacy); err == nil {
		t.Error("the import accepted a hardlinked legacy file")
	}
	if err := linkedStore.Save(context.Background(), launchsettings.Settings{}); err == nil {
		t.Error("Save accepted a hardlinked mirror")
	}
	requireFile(t, original, before)
}

func TestDirectoryReparseRejectedWithLegacyGoMode(t *testing.T) {
	// Under this supported Go compatibility flag, junctions report ModeDir, not
	// ModeSymlink or ModeIrregular; the Windows reparse attribute still applies.
	t.Setenv("GODEBUG", "winsymlink=0")
	base, target := t.TempDir(), t.TempDir()
	link := filepath.Join(base, "junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("create isolated legacy-mode junction: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	store, db := newStoreFor(t, filepath.Join(link, "launcher-v1.json"))
	if err := store.Save(context.Background(), launchsettings.Settings{}); err == nil {
		t.Error("Save followed a legacy-mode reparse directory")
	}
	if _, err := db.RunImport(context.Background(), launchsettings.Domain, launchsettings.ImportVersion, store.ImportLegacy); err == nil {
		t.Error("the import accepted a legacy-mode reparse directory")
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Errorf("a rejected legacy-mode reparse directory changed its target: %v, %v", entries, err)
	}
}

func TestConcurrentSavesAndLoadsStayComplete(t *testing.T) {
	store, db, file := newStore(t)
	root := t.TempDir()
	var group sync.WaitGroup
	failures := make(chan error, 64)
	for i := range 32 {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			if err := store.Save(context.Background(), launchsettings.Settings{GameRoot: filepath.Join(root, fmt.Sprint(i)), RiskVersion: "v1"}); err != nil {
				failures <- err
			}
			if _, err := store.Load(context.Background()); err != nil {
				failures <- err
			}
		}(i)
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	got := mustLoad(t, store)
	if got.RiskVersion != "v1" {
		t.Fatalf("concurrent final state = %+v", got)
	}
	if count := settingsRows(t, db); count != 1 {
		t.Fatalf("concurrent writers left %d rows", count)
	}
	// Every writer replaced the mirror through a temporary file; a leak would show
	// up next to it.
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil || len(entries) != 1 {
		t.Fatalf("concurrent writers leaked a temporary file: %v, %v", entries, err)
	}
}
