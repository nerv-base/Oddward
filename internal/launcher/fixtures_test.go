// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"time"

	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/news"
)

type wishImageFixture struct {
	data    []byte
	err     error
	calls   chan string
	release <-chan struct{}
}

func (f wishImageFixture) Image(ctx context.Context, id string) ([]byte, error) {
	if f.calls != nil {
		select {
		case f.calls <- id:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.data, f.err
}

type wishBackendFixture struct {
	archive      gacha.Archive
	catalog      gacha.Catalog
	catalogError error
	loadError    error
	importReport WishImportReport
	exported     chan state.WishFormat
}

func (b wishBackendFixture) List(ctx context.Context) ([]string, error) {
	return []string{b.archive.RoleID}, ctx.Err()
}
func (b wishBackendFixture) Load(ctx context.Context, _ string) (gacha.Archive, error) {
	if b.loadError != nil {
		return gacha.Archive{}, b.loadError
	}
	return b.archive, ctx.Err()
}
func (b wishBackendFixture) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (b wishBackendFixture) Import(ctx context.Context, _, _ string, _ bool) (WishImportReport, error) {
	return b.importReport, ctx.Err()
}
func (b wishBackendFixture) Export(ctx context.Context, _, _ string, format state.WishFormat) error {
	if b.exported != nil {
		select {
		case b.exported <- format:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}
func (b wishBackendFixture) Catalog(ctx context.Context) (gacha.Catalog, error) {
	return b.catalog, b.catalogError
}

func wishArchiveFixture() gacha.Archive {
	at := time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)
	return gacha.Archive{
		RoleID: "100012345",
		Records: []gacha.Record{
			{Pool: gacha.Limited, ItemID: "11001", At: at, Pity: 75, Luck: 1},
			{Pool: gacha.Limited, ItemID: "11001", At: at.Add(-24 * time.Hour), Pity: 90, Luck: 2},
			{Pool: gacha.Permanent, ItemID: "11002", At: at.Add(-48 * time.Hour), Pity: 22, Luck: 1},
			{Pool: gacha.Weapon, ItemID: "21001", At: at.Add(-72 * time.Hour), Pity: 60, Luck: 2},
			{Pool: gacha.Weapon, ItemID: "21002", At: at.Add(-73 * time.Hour), Pity: 61, Luck: 1},
			{Pool: gacha.Limited, ItemID: "11003", At: at.AddDate(0, -8, 0), Pity: 32, Luck: 1},
		},
		Snapshots: []gacha.SnapshotMeta{{FetchedAt: at, LuckType: 2, LuckTitle: "欧气满满", Pools: []gacha.PoolMeta{
			{Pool: gacha.Limited, M: 90, DrawCount: 197, RareCount: 3, Average: "65.67", PlayerOver: "80.2%"},
			{Pool: gacha.Permanent, M: 90, DrawCount: 22, RareCount: 1, Average: "22.00", PlayerOver: "91.0%"},
			{Pool: gacha.Weapon, M: 80, DrawCount: 121, RareCount: 2, Average: "60.50", PlayerOver: "40.0%", RuleMismatch: true},
		}}},
	}
}

// wishFeaturedArchiveFixture adds one featured arc so previews cover the
// Starward-style up! marker and the 50-pull red bar.
func wishFeaturedArchiveFixture() gacha.Archive {
	a := wishArchiveFixture()
	at := time.Date(2026, 11, 15, 2, 0, 0, 0, time.UTC)
	a.Records = append([]gacha.Record{{Pool: gacha.Weapon, ItemID: "fork_featured", At: at, Pity: 50, Luck: 1}}, a.Records...)
	return a
}

func send(m state.Model, kind state.EventKind) state.Model {
	n, _ := state.Reduce(m, state.Event{Kind: kind})
	return n
}

func settled(flow state.Flow) state.Model { m := state.Initial(); m.Flow = flow; return m }

func normalFixture(phase state.ManagedPhase) state.Model {
	m := state.Initial()
	m.NormalMode = true
	m.Flow = state.Ready
	m.Managed = state.ManagedState{Phase: phase, Request: 8, Root: `C:\fixture\nte`}
	return m
}

func projectedBannerFixture() state.BannerRow {
	return state.BannerRow{Count: 2, Titles: [8]string{"启动器推荐一", "启动器推荐二"}, URLs: [8]string{"https://yh.wanmei.com/news/gamebroad/20261028/264788.html", "https://yh.wanmei.com/news/gameevent/20261027/264764.html"}, Images: [8]string{"https://yhvmg.wmupd.com/one.jpg", "https://yhvmg.wmupd.com/two.jpg"}, Source: news.SourceLauncher}
}
