// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package launchsettings_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nerv-base/Oddward/internal/launchsettings"
	"golang.org/x/sys/windows"
)

func TestReplacementFailureRetainsOldFileAndCleansStage(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "launch.json")
	store, err := launchsettings.NewStore(file)
	if err != nil {
		t.Fatal(err)
	}
	old := launchsettings.Settings{GameRoot: filepath.Join(dir, "old"), RiskVersion: "v1"}
	if err := store.Save(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(file)
	if err != nil {
		t.Fatal(err)
	}
	// Allow ordinary reads/writes but deny deleting/replacing the old directory
	// entry. An unsafe in-place write succeeds and corrupts the old contents.
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := store.Save(context.Background(), launchsettings.Settings{}); err == nil {
		t.Error("Save succeeded despite replacement being denied; likely overwrote in place")
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed replacement changed old bytes: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed replacement leaked stage: %v, %v", entries, err)
	}
}

func TestReparseAncestorsAndHardlinkedTargetsAreRejected(t *testing.T) {
	base, target := t.TempDir(), t.TempDir()
	link := filepath.Join(base, "junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("create isolated synthetic junction: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	store, err := launchsettings.NewStore(filepath.Join(link, "launch.json"))
	if err == nil {
		if _, err := store.Load(context.Background()); err == nil {
			t.Error("Load followed linked parent")
		}
		if err := store.Save(context.Background(), launchsettings.Settings{}); err == nil {
			t.Error("Save followed linked parent")
		}
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Errorf("rejected parent created target data: %v, %v", entries, err)
	}

	file := filepath.Join(base, "launch.json")
	store, err = launchsettings.NewStore(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), launchsettings.Settings{GameRoot: link}); err == nil {
		t.Error("persisted junction game root")
	}
	// Remove only this known synthetic fixture if a regressed Save created it.
	if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	original := filepath.Join(base, "original.json")
	before := []byte(`{"version":1,"gameRoot":"","riskVersion":""}`)
	if err := os.WriteFile(original, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, file); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Error("Load accepted linked target")
	}
	if err := store.Save(context.Background(), launchsettings.Settings{}); err == nil {
		t.Error("Save accepted linked target")
	}
	after, err := os.ReadFile(original)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("linked original changed: %v", err)
	}
}

func TestDirectoryReparseRejectedWithLegacyGoMode(t *testing.T) {
	// Under this supported Go compatibility flag, junctions report ModeDir,
	// not ModeSymlink or ModeIrregular; Windows reparse attributes still apply.
	t.Setenv("GODEBUG", "winsymlink=0")
	base, target := t.TempDir(), t.TempDir()
	link := filepath.Join(base, "junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("create isolated legacy-mode junction: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	store, err := launchsettings.NewStore(filepath.Join(link, "launch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Error("Load accepted legacy-mode reparse directory")
	}
	if err := store.Save(context.Background(), launchsettings.Settings{}); err == nil {
		t.Error("Save followed legacy-mode reparse directory")
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Errorf("legacy-mode reparse refusal changed target: %v, %v", entries, err)
	}
}

// This context cancels when the required same-directory stage first exists,
// without exposing Store internals or relying on a particular Err call count.
type cancelOnStage struct {
	context.Context
	cancel context.CancelFunc
	dir    string
}

func (c cancelOnStage) Err() error {
	entries, _ := os.ReadDir(c.dir)
	for _, entry := range entries {
		if entry.Name() != "launch.json" {
			c.cancel()
			break
		}
	}
	return c.Context.Err()
}

func TestCancellationAfterStagingDoesNotCommit(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "launch.json")
	store, err := launchsettings.NewStore(file)
	if err != nil {
		t.Fatal(err)
	}
	old := launchsettings.Settings{GameRoot: filepath.Join(dir, "old"), RiskVersion: "v1"}
	if err := store.Save(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := cancelOnStage{Context: base, cancel: cancel, dir: dir}
	if err := store.Save(ctx, launchsettings.Settings{}); !errors.Is(err, context.Canceled) {
		t.Errorf("staged cancellation = %v; want context.Canceled", err)
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("canceled staged Save changed old bytes: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cancellation leaked stage: %v, %v", entries, err)
	}
}

func TestConcurrentSavesAndLoadsStayComplete(t *testing.T) {
	file := filepath.Join(t.TempDir(), "launch.json")
	store, err := launchsettings.NewStore(file)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var group sync.WaitGroup
	errorsFound := make(chan error, 64)
	for i := range 32 {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			if err := store.Save(context.Background(), launchsettings.Settings{GameRoot: filepath.Join(root, fmt.Sprint(i)), RiskVersion: "v1"}); err != nil {
				errorsFound <- err
			}
			if _, err := store.Load(context.Background()); err != nil {
				errorsFound <- err
			}
		}(i)
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	got, err := store.Load(context.Background())
	if err != nil || got.RiskVersion != "v1" {
		t.Fatalf("concurrent final state = %+v, %v", got, err)
	}
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil || len(entries) != 1 {
		t.Fatalf("concurrent writers leaked stage: %v, %v", entries, err)
	}
}
