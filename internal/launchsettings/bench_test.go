// SPDX-License-Identifier: GPL-3.0-or-later

package launchsettings

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func benchmarkSettings(tb testing.TB, root string) Settings {
	tb.Helper()
	return Settings{
		GameRoot:  root,
		Network:   Network{Proxy: "system", Mirror: true, MirrorPrefix: "https://mirror.example/"},
		Downloads: Downloads{RateLimitKiB: 2048},
		UI:        DefaultUI(),
	}
}

// BenchmarkSettingsLoad reads the launch_settings row, the path every start and
// every panel that reviews the choices takes.
func BenchmarkSettingsLoad(b *testing.B) {
	ctx := context.Background()
	root := b.TempDir()
	store, err := NewStore(dbtest.Open(b), filepath.Join(root, "launcher-v1.json"))
	if err != nil {
		b.Fatal(err)
	}
	if err := store.Save(ctx, benchmarkSettings(b, root)); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Load(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSettingsSave rewrites the row together with its JSON mirror, the cost
// of a preference toggle.
func BenchmarkSettingsSave(b *testing.B) {
	ctx := context.Background()
	root := b.TempDir()
	store, err := NewStore(dbtest.Open(b), filepath.Join(root, "launcher-v1.json"))
	if err != nil {
		b.Fatal(err)
	}
	settings := benchmarkSettings(b, root)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.Save(ctx, settings); err != nil {
			b.Fatal(err)
		}
	}
}
