// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
)

func TestFreshCacheKeepsTwoIndependentSpaceBudgets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "new-game")
	cache := t.TempDir()
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	b := &GameDownloadBackend{downloadVolume: func(p string) (string, error) {
		if p == cache {
			return "cache", nil
		}
		return "install", nil
	}}
	selected, work, err := b.externalDownloadCache(root, cache, key)
	if err != nil || selected != cache || filepath.Dir(work) != cache {
		t.Fatalf("cache selection = %q, %q, %v", selected, work, err)
	}
	if _, err := os.Lstat(work); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selection created work directory: %v", err)
	}
	p := &downloadPlan{root: root, workRoot: work, externalCache: cache, margin: 10, plan: gameres.Plan{DownloadBytes: 20, Targets: []gameres.Target{{Size: 15}, {Size: 15}}}}
	b.free = func(p string) (int64, error) {
		if p == cache {
			return 29, nil
		}
		return 40, nil
	}
	if err := b.budgetExternalDownload(p); err != nil || p.need != 30 || p.free != 29 {
		t.Fatalf("cache budget = %d/%d, %v", p.need, p.free, err)
	}
	b.free = func(p string) (int64, error) {
		if p == cache {
			return 30, nil
		}
		return 39, nil
	}
	if err := b.budgetExternalDownload(p); err != nil || p.need != 40 || p.free != 39 {
		t.Fatalf("destination budget = %d/%d, %v", p.need, p.free, err)
	}
	if err := b.checkDownloadSpace(p, 25); !errors.Is(err, gameres.ErrInsufficientSpace) {
		t.Fatalf("rechecked destination space: %v", err)
	}
	if _, _, err := b.externalDownloadCache(root, filepath.Dir(root), key); !errors.Is(err, gameres.ErrPathUnsafe) {
		t.Fatalf("overlap accepted: %v", err)
	}
}
