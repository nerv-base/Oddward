// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image/color"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gacha"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestWishWarningBarsRemainDistinctFromArbitraryTone(t *testing.T) {
	for _, tone := range []ui.Color{defaultAccent, ui.Hex("#FFC857"), ui.Hex("#FF9967"), ui.Hex("#66DDFE")} {
		s := NewShell(settled(state.Ready))
		s.Accent = tone
		a := wishArchiveFixture()
		tt := ui.NewTester(func(c *ui.Context) {
			c.SetTheme(s.theme)
			s.wishPage(c, wishPageData{Archive: a, Stats: gacha.Statistics(a)})
		}, 1280, 720)
		boosted, ok := tt.AccessByRole(ui.RoleButton, "11001，75 抽，2026-11-15 04:00")
		if !ok {
			t.Fatal("boosted fixture record missing")
		}
		regular, ok := tt.AccessByRole(ui.RoleButton, "11003，32 抽，2026-03-15 04:00")
		if !ok {
			t.Fatal("regular fixture record missing")
		}
		img := tt.Image()
		b := img.RGBAAt(int(boosted.Bounds.X+4), int(boosted.Bounds.Y+boosted.Bounds.H/2))
		r := img.RGBAAt(int(regular.Bounds.X+4), int(regular.Bounds.Y+regular.Bounds.H/2))
		dr, dg, db := int(b.R)-int(r.R), int(b.G)-int(r.G), int(b.B)-int(r.B)
		// The row bar is a translucent wash, so its on-screen difference is
		// smaller than a fully opaque swatch; it still needs to read as a
		// different hue, not just a slightly darker shade.
		if distance := dr*dr + dg*dg + db*db; distance <= 60*60 {
			t.Fatalf("warning bar indistinguishable from tone: tone=%+v boosted=%+v regular=%+v squared distance=%d", tone, b, r, distance)
		}
		s.Dispose()
	}
}

func TestWishRecordBarsFollowStarwardPityColors(t *testing.T) {
	at := time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)
	archive := gacha.Archive{RoleID: "100012345", Records: []gacha.Record{
		{Pool: gacha.Limited, ItemID: "1055", At: at, Pity: 59},
		{Pool: gacha.Limited, ItemID: "1054", At: at.Add(-time.Hour), Pity: 60},
		{Pool: gacha.Weapon, ItemID: "fork_butterfly", At: at.Add(-2 * time.Hour), Pity: 49},
		{Pool: gacha.Weapon, ItemID: "fork_featured", At: at.Add(-3 * time.Hour), Pity: 50},
	}}
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(s.theme)
		s.wishPage(c, wishPageData{Archive: archive, Stats: gacha.Statistics(archive)})
	}, 1280, 720)
	img := tt.Image()
	for _, tc := range []struct {
		label string
		red   bool
	}{
		{"1055，59 抽，2026-11-15 04:00", false},
		{"1054，60 抽，2026-11-15 03:00", true},
		{"fork_butterfly，49 抽，2026-11-15 02:00", false},
		{"fork_featured，50 抽，2026-11-15 01:00，当期限定", true},
	} {
		record, ok := tt.AccessByRole(ui.RoleButton, tc.label)
		if !ok {
			t.Fatalf("missing record %q", tc.label)
		}
		pixel := img.RGBAAt(int(record.Bounds.X+4), int(record.Bounds.Y+record.Bounds.H/2))
		if tc.red && pixel.R <= pixel.G {
			t.Fatalf("%s must use the red Starward bar: %+v", tc.label, pixel)
		}
		if !tc.red && pixel.G <= pixel.R {
			t.Fatalf("%s must use the green Starward bar: %+v", tc.label, pixel)
		}
	}
	if !tt.HasText("up!") {
		t.Fatal("featured arc must render up!")
	}
	standard := gacha.Archive{RoleID: "100012345", Records: []gacha.Record{{Pool: gacha.Weapon, ItemID: "fork_butterfly", At: at, Pity: 50}}}
	s2 := NewShell(settled(state.Ready))
	defer s2.Dispose()
	tt2 := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(s2.theme)
		s2.wishPage(c, wishPageData{Archive: standard, Stats: gacha.Statistics(standard)})
	}, 1280, 720)
	if tt2.HasText("up!") {
		t.Fatal("a standard-pool arc must not be marked as featured")
	}
}

func TestWishUnknownIconUsesNeutralGlyph(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	a := wishArchiveFixture()
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(s.theme)
		s.wishPage(c, wishPageData{Archive: a, Stats: gacha.Statistics(a)})
	}, 1280, 720)
	if !tt.HasText("?") {
		t.Fatal("unknown IDs must use a neutral placeholder glyph")
	}
	if _, ok := tt.AccessByRole(ui.RoleImage, "11001，图片不可用"); !ok {
		t.Fatal("neutral glyph must preserve the item accessibility label")
	}
}

func TestWishSourceDoesNotInventAnImportReport(t *testing.T) {
	m := settled(state.Ready)
	m.Page = state.WishAnalysis
	s := NewShell(m)
	defer s.Dispose()
	a := wishArchiveFixture()
	s.wish.archive = a
	s.wish.stats = gacha.Statistics(a)
	s.Model.Wish = state.WishState{RoleID: a.RoleID, Phase: state.WishReady}
	tt := ui.NewTester(s.View, 1280, 720)
	if tt.HasText("新增 0 条") || tt.HasText("新增 6 条") {
		t.Fatal("source must not claim an import before a successful import result")
	}
	s.Dispatch(state.Event{Kind: state.WishImported, Token: s.Model.Wish.Token, WishRoleID: a.RoleID})
	tt.Frame()
	if !tt.HasText("新增 0 条") {
		t.Fatal("a reported zero-addition import must remain visible")
	}
}

func TestWishThreePoolsGeometryAndHonestArchiveLabels(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	archive := wishArchiveFixture()
	data := wishPageData{Archive: archive, RoleID: archive.RoleID, Roles: []string{archive.RoleID}, Stats: gacha.Statistics(archive), Location: time.FixedZone("fixture", 8*60*60), Now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC), NewCount: 6, ImportReported: true}
	tt := ui.NewTester(func(c *ui.Context) { c.SetTheme(s.theme); s.wishPage(c, data) }, 1280, 720)
	var prev ui.Rect
	for i, name := range []string{"限定卡池", "常驻卡池", "弧盘池"} {
		card, ok := tt.AccessByRole(ui.RoleGroup, name)
		if !ok {
			t.Fatalf("missing pool group %s", name)
		}
		if card.Bounds.Y+card.Bounds.H > 720-bezelWidth-wishBottomInset+1 {
			t.Fatalf("pool card clipped by outer viewport: %+v", card.Bounds)
		}
		if card.Bounds.W < 300 || card.Bounds.H < 340 {
			t.Fatalf("pool card too small: %+v", card)
		}
		if i > 0 && (!near(card.Bounds.Y, prev.Y, 1) || card.Bounds.X < prev.X+prev.W+wishCardGap-1) {
			t.Fatalf("cards must share a row with gaps: %+v / %+v", prev, card.Bounds)
		}
		prev = card.Bounds
	}
	for _, text := range []string{"未收录", "塔吉多统计 ·", "规则表可能过期", "更早的记录来自本地档案", "更新于 2026-11-15 12:00", "塔吉多 · 角色 100012345"} {
		if !tt.HasText(text) {
			t.Fatalf("missing truthful archive label %q", text)
		}
	}
	for _, text := range []string{"常规", "概率提升", "保底", "■ 保底", "! 异常"} {
		if tt.HasText(text) {
			t.Fatalf("record band label %q must not be displayed", text)
		}
	}
	if tt.HasText("当前已垫") || tt.HasText("80 抽保底") {
		t.Fatal("page exposed unsupported pity inference")
	}
	if _, ok := tt.AccessByRole(ui.RoleImage, "11001，图片不可用"); !ok {
		t.Fatal("unknown image lacks accessible fallback label")
	}
}

func TestWishPoolCardsUseCompactStarwardRows(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	a := wishArchiveFixture()
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(s.theme)
		s.wishPage(c, wishPageData{Archive: a, Stats: gacha.Statistics(a)})
	}, 1280, 720)
	record, ok := tt.AccessByRole(ui.RoleButton, "11001，75 抽，2026-11-15 04:00")
	if !ok {
		t.Fatal("fixture record missing")
	}
	if record.Bounds.H > 30 {
		t.Fatalf("record rows must stay compact like Starward: %+v", record.Bounds)
	}
	for _, text := range []string{"S 平均", "S 统计", "累计", "最欧", "最非"} {
		if !tt.HasText(text) {
			t.Fatalf("pool statistics line %q missing", text)
		}
	}
}

func TestWishPoolsStateRuleAndSnapshotSummary(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Accent = ui.Hex("#65C9C2")
	a := wishArchiveFixture()
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(s.theme)
		s.wishPage(c, wishPageData{Archive: a, Stats: gacha.Statistics(a)})
	}, 1280, 720)
	tt.SetPreferences(ui.Preferences{HighContrast: true})
	for _, text := range []string{
		"累计",
		"塔吉多统计 · 197 抽 · 平均 65.67 · 超过 80.2%",
		"塔吉多统计 · 121 抽 · 平均 60.50 · 超过 40.0% · 规则表可能过期",
		"欧气满满",
	} {
		if !tt.HasText(text) {
			t.Fatalf("pool caption %q missing", text)
		}
	}
}

func TestWishKeyboardFocusHighlightsMatchingRecords(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Accent = ui.Hex("#65C9C2")
	a := wishArchiveFixture()
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(s.theme)
		s.wishPage(c, wishPageData{Archive: a, Stats: gacha.Statistics(a)})
	}, 1280, 720)
	tt.SetPreferences(ui.Preferences{HighContrast: true})
	firstLabel := "11001，75 抽，2026-11-15 04:00"
	var first ui.AccessInfo
	for range 20 {
		tt.Key(0, ui.KeyTab)
		first, _ = tt.AccessByRole(ui.RoleButton, firstLabel)
		if first.Focused {
			break
		}
	}
	if !first.Focused {
		t.Fatal("record must be reachable with keyboard Tab")
	}
	tt.Frame()
	tt.Frame()
	second, ok := tt.AccessByRole(ui.RoleButton, "11001，90 抽，2026-11-14 04:00")
	if !ok {
		t.Fatal("matching record missing")
	}
	tone := s.tone().Mix(ink, wishOpaqueHighlightMix)
	want := color.RGBA{tone.R, tone.G, tone.B, 255}
	img := tt.Image()
	for _, record := range []ui.AccessInfo{first, second} {
		// Sample just above the ratio bar, inside the row highlight but away
		// from the rounded corner introduced by the compact Starward row.
		got := img.RGBAAt(int(record.Bounds.X+10), int(record.Bounds.Y+1))
		if !sameColor(got, want, 3) {
			t.Fatalf("same item did not receive keyboard highlight: got %+v want %+v", got, want)
		}
	}
}

func TestWishNarrowPageUsesOneColumn(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	a := wishArchiveFixture()
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(s.theme)
		s.wishPage(c, wishPageData{Archive: a, Stats: gacha.Statistics(a)})
	}, 900, 800)
	var prev ui.Rect
	for i, name := range []string{"限定卡池", "常驻卡池", "弧盘池"} {
		card, ok := tt.AccessByRole(ui.RoleGroup, name)
		if !ok {
			t.Fatalf("missing narrow pool %s", name)
		}
		if card.Bounds.X < railWidth || card.Bounds.X+card.Bounds.W > 900-bezelWidth {
			t.Fatalf("card escaped viewport: %+v", card)
		}
		if i > 0 && (!near(card.Bounds.X, prev.X, 1) || card.Bounds.Y < prev.Y+prev.H+wishCardGap-1) {
			t.Fatalf("narrow cards must stack: %+v / %+v", prev, card.Bounds)
		}
		prev = card.Bounds
	}
}

func TestWishToneAndOpaqueAccessibilityFallback(t *testing.T) {
	for _, prefs := range []ui.Preferences{{HighContrast: true}, {ReduceTransparency: true}} {
		s := NewShell(settled(state.Ready))
		s.Accent = ui.Hex("#65C9C2")
		a := wishArchiveFixture()
		tt := ui.NewTester(func(c *ui.Context) {
			c.SetTheme(s.theme)
			s.wishPage(c, wishPageData{Archive: a, Stats: gacha.Statistics(a)})
		}, 1280, 720)
		tt.SetPreferences(prefs)
		card, _ := tt.AccessByRole(ui.RoleGroup, "常驻卡池")
		img := tt.Image()
		got := img.RGBAAt(int(card.Bounds.X+6), int(card.Bounds.Y+70))
		want := color.RGBA{s.theme.Surface.R, s.theme.Surface.G, s.theme.Surface.B, 255}
		if !sameColor(got, want, 2) {
			t.Fatalf("opaque card fallback differs from theme surface: got %+v want %+v", got, want)
		}
		// The regular permanent record uses Starward's green pity bar; the
		// opaque fallback darkens the colour so white text stays readable.
		record, _ := tt.AccessByRole(ui.RoleButton, "11002，22 抽，2026-11-13 04:00")
		bar := wishPityGreen.Mix(ink, wishOpaqueBarMix)
		wantBar := color.RGBA{bar.R, bar.G, bar.B, 255}
		longest := 0
		for y := int(record.Bounds.Y); y < int(record.Bounds.Y+record.Bounds.H); y++ {
			run := 0
			for x := int(record.Bounds.X); x < int(record.Bounds.X+record.Bounds.W); x++ {
				if sameColor(img.RGBAAt(x, y), wantBar, 3) {
					run++
					longest = max(longest, run)
				} else {
					run = 0
				}
			}
		}
		if longest < 40 {
			t.Fatalf("regular record must have a ratio bar in s.tone(): longest run=%d record %+v", longest, record.Bounds)
		}
		s.Dispose()
	}
}

func TestWishConfirmationDefaultsToCancelAndIsKeyboardEquivalent(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	accepted, cancelled := 0, 0
	data := wishPageData{Confirm: &wishPageConfirmation{Title: "删除本地记录", Body: "将删除角色 100012345 的 6 条本地记录。", Accept: "删除记录", OnAccept: func() { accepted++ }, OnCancel: func() { cancelled++ }}}
	tt := ui.NewTester(func(c *ui.Context) { c.SetTheme(s.theme); s.wishPage(c, data) }, 1280, 720)
	if _, ok := tt.AccessByRole(ui.RoleDialog, "删除本地记录"); !ok {
		t.Fatal("delete confirmation lacks dialog role")
	}
	tt.Key(0, ui.KeyEnter)
	if cancelled != 1 || accepted != 0 {
		t.Fatalf("default focus must cancel: cancelled=%d accepted=%d", cancelled, accepted)
	}
	tt.Key(0, ui.KeyEscape)
	if cancelled != 2 || accepted != 0 {
		t.Fatal("Escape must provide cancellation equivalent")
	}
}

func TestWishFailureAndCorruptArchiveStayDistinct(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	for _, tc := range []struct {
		data wishPageData
		text string
	}{{wishPageData{Failed: true}, "无法读取本地档案"}, {wishPageData{Corrupt: true}, "本地档案已损坏"}} {
		tt := ui.NewTester(func(c *ui.Context) { c.SetTheme(s.theme); s.wishPage(c, tc.data) }, 1280, 720)
		if !tt.HasText(tc.text) {
			t.Fatalf("missing archive failure state %s", tc.text)
		}
	}
}

func TestWishUpdateControlFollowsTheLinkState(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	for _, tc := range []struct {
		name  string
		data  wishTajiduo
		label string
		role  ui.Role
		menu  bool
		hint  string
	}{
		// Q15: no saved account means no link entry point at all.
		{name: "no saved account", data: wishTajiduo{}, hint: "先在登录窗口勾选「在此设备保存账号」"},
		{name: "linkable", data: wishTajiduo{Linkable: true}, label: "关联塔吉多", role: ui.RoleButton, hint: "会顶掉手机上已登录的塔吉多"},
		{name: "linked", data: wishTajiduo{Linkable: true, Linked: true}, label: "更新记录", role: ui.RoleButton, menu: true},
		{name: "needs relink", data: wishTajiduo{Linkable: true, NeedsRelink: true, Status: "塔吉多关联已失效，请重新关联", StatusWarn: true}, label: "重新关联", role: ui.RoleButton, menu: true},
		{name: "needs relink without a session message", data: wishTajiduo{Linkable: true, NeedsRelink: true, StatusWarn: true}, label: "重新关联", role: ui.RoleButton, menu: true, hint: "塔吉多关联已失效，本地档案仍可查看"},
		// Q15 still wins if the account disappeared since the status was read.
		{name: "needs relink without a saved account", data: wishTajiduo{NeedsRelink: true, StatusWarn: true}, hint: "先在登录窗口勾选「在此设备保存账号」"},
		{name: "fetching", data: wishTajiduo{Linkable: true, Linked: true, Fetching: true}, label: "正在更新…", role: ui.RoleButton},
		{name: "cooldown", data: wishTajiduo{Linkable: true, Linked: true, Cooldown: 42}, label: "42 秒后可更新", role: ui.RoleButton},
		{name: "another role", data: wishTajiduo{Linkable: true, Linked: true, RoleID: "999", RoleMismatch: true, Status: "当前档案不是塔吉多绑定的角色，请先选择绑定角色", StatusWarn: true}, label: "更新记录", role: ui.RoleButton, menu: true, hint: "当前档案不是塔吉多绑定的角色"},
	} {
		tt := ui.NewTester(func(c *ui.Context) { c.SetTheme(s.theme); s.wishPage(c, wishPageData{Tajiduo: tc.data}) }, 1280, 720)
		if tc.label == "" {
			for _, name := range []string{"关联塔吉多", "更新记录", "重新关联"} {
				if _, ok := tt.AccessByRole(ui.RoleButton, name); ok {
					t.Fatalf("%s must not offer the %q button", tc.name, name)
				}
			}
		} else if _, ok := tt.AccessByRole(tc.role, tc.label); !ok {
			t.Fatalf("%s: missing accessible %q; nodes=%+v", tc.name, tc.label, tt.AccessAll())
		}
		if tc.menu {
			if _, ok := tt.AccessByRole(ui.RoleMenuButton, "塔吉多关联"); !ok {
				t.Fatalf("%s: the split control needs an accessible menu half", tc.name)
			}
		}
		if tc.hint != "" && !tt.HasText(tc.hint) {
			t.Fatalf("%s: missing the honest hint %q", tc.name, tc.hint)
		}
	}
}

func TestWishUpdateControlSendsOneRequestPerEnabledClick(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	for _, tc := range []struct {
		name   string
		data   wishTajiduo
		clicks int
		text   string
	}{
		{name: "linked", data: wishTajiduo{Linkable: true, Linked: true}, clicks: 1, text: "更新记录"},
		{name: "cooldown blocks", data: wishTajiduo{Linkable: true, Linked: true, Cooldown: 42}, clicks: 0, text: "42 秒后可更新"},
		{name: "fetching blocks", data: wishTajiduo{Linkable: true, Linked: true, Fetching: true}, clicks: 0, text: "正在更新…"},
		{name: "another role blocks", data: wishTajiduo{Linkable: true, Linked: true, RoleID: "999", RoleMismatch: true}, clicks: 0, text: "更新记录"},
	} {
		calls := 0
		tc.data.OnUpdate = func() { calls++ }
		tt := ui.NewTester(func(c *ui.Context) { c.SetTheme(s.theme); s.wishPage(c, wishPageData{Tajiduo: tc.data}) }, 1280, 720)
		if err := tt.Click(tc.text); err != nil {
			t.Fatalf("%s: cannot click %q: %v", tc.name, tc.text, err)
		}
		if calls != tc.clicks {
			t.Fatalf("%s: %q sent %d requests, want %d", tc.name, tc.text, calls, tc.clicks)
		}
	}
}

func TestWishUpdateSplitControlReadsAsOneKey(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	data := wishPageData{Tajiduo: wishTajiduo{Linkable: true, Linked: true}}
	tt := ui.NewTester(func(c *ui.Context) { c.SetTheme(s.theme); s.wishPage(c, data) }, 1280, 720)
	update, ok := tt.AccessByRole(ui.RoleButton, "更新记录")
	if !ok {
		t.Fatal("missing update button")
	}
	more, ok := tt.AccessByRole(ui.RoleMenuButton, "塔吉多关联")
	if !ok {
		t.Fatal("missing menu half")
	}
	if gap := more.Bounds.X - (update.Bounds.X + update.Bounds.W); gap < 0 || gap > 2 {
		t.Fatalf("split halves must touch: gap=%v update=%+v more=%+v", gap, update.Bounds, more.Bounds)
	}
	if update.Bounds.Y != more.Bounds.Y || update.Bounds.H != more.Bounds.H {
		t.Fatalf("split halves must share one row height: update=%+v more=%+v", update.Bounds, more.Bounds)
	}
}

func TestWishEmptyArchiveExplainsImportAndHasAccessibleActions(t *testing.T) {
	m := settled(state.Ready)
	m.Page = state.WishAnalysis
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if !tt.HasText("NTEUID") || !tt.HasText("最近约 6 个月") {
		t.Fatal("empty wish page must explain supported local JSON formats")
	}
	if tt.HasText("MOCK · 仅内存演示") || tt.HasText("载入示例记录") {
		t.Fatal("wish analysis must no longer claim to be a demo")
	}
	if tt.HasText("不推算当前已垫") {
		t.Fatal("wish analysis footer must not carry a pity disclaimer")
	}
	if _, ok := tt.AccessByRole(ui.RoleMenuButton, "角色档案"); !ok {
		t.Fatalf("missing archive menu; nodes=%+v", tt.AccessAll())
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "导入抽卡记录"); !ok {
		t.Fatalf("missing empty-state import; nodes=%+v", tt.AccessAll())
	}
	if _, ok := tt.AccessByRole(ui.RoleMenuButton, "导出"); ok {
		t.Fatal("empty archive must not offer export")
	}
}
