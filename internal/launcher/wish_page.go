// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// wishPageData is a UI-thread projection. Archives and maps deliberately stay
// outside the comparable state.Model; callbacks only dispatch scalar events.
type wishPageData struct {
	Roles                          []string
	RoleID                         string
	Archive                        gacha.Archive
	Stats                          gacha.Stats
	Catalog                        gacha.Catalog
	Location                       *time.Location
	Now                            time.Time
	Loading, Busy, Failed, Corrupt bool
	ImportReported                 bool
	FailureTitle, FailureBody      string
	NewCount, Outside, Conflicts   int
	OnRole                         func(string)
	OnImport                       func()
	OnExport                       func(nteuid bool)
	OnDelete                       func()
	OnRetry                        func()
	Image                          func(string) *ui.Bitmap
	Confirm                        *wishPageConfirmation
	// Tajiduo drives the 更新记录 split button. Its zero value means no saved
	// account exists, so the button explains how to save one (decision Q15).
	Tajiduo wishTajiduo
}

// wishTajiduo is the Tajiduo link projection the page renders. It carries no
// credential: only flags, a status line and callbacks that dispatch events.
type wishTajiduo struct {
	// Linkable: a saved account exists, so the link flow may start.
	Linkable    bool
	Linked      bool
	NeedsRelink bool
	Fetching    bool
	// Cooldown is the whole seconds left before another fetch may be sent.
	Cooldown int
	// RoleID is the role bound to the saved Tajiduo session, if known.
	RoleID string
	// RoleMismatch: the selected archive does not belong to the bound role,
	// so fetching would update another archive.
	RoleMismatch bool
	// Status is a one-line note under the header (last fetch result or the
	// failure reason); StatusWarn draws it in the warning colour.
	Status     string
	StatusWarn bool
	// OnUpdate starts the link flow when not linked or NeedsRelink, and
	// otherwise sends one fetch. OnUnlink asks to remove the link.
	OnUpdate func()
	OnUnlink func()
}

type wishPageConfirmation struct {
	Title, Body, Accept string
	OnAccept, OnCancel  func()
}

func wishInvoke(fn func()) {
	if fn != nil {
		fn()
	}
}

func (s *Shell) wishPage(c *ui.Context, data wishPageData) {
	tone := s.tone()
	prefs := c.Preferences()
	opaque := prefs.HighContrast || prefs.ReduceTransparency
	width, height := c.Size()
	available := width - railWidth - bezelWidth - 2*wishInset
	narrow := available < 3*wishCardMinWidth+2*wishCardGap
	cardsTop := wishCardsTop
	if data.ImportReported && (data.Outside > 0 || data.Conflicts > 0) {
		cardsTop += wishImportReportHeight
	}
	if wishLinkNote(data.Tajiduo) != "" {
		cardsTop += wishLinkNoteHeight
	}
	if narrow {
		cardsTop += wishNarrowHeaderExtra
	}
	surface := canvas(c, "feature-surface")
	// The whole analysis pane is one frosted sheet over the key art. A tinted
	// material keeps text readable without turning the page into an opaque
	// overlay, so the artwork still reads through the glass.
	surface.Material(frostedSurface{Tint: ui.RGBA(10, 11, 14, .55)})
	surface.Children(func() {
		if !opaque {
			ui.Box(c).Absolute().Right(wishBloomRight).Bottom(wishBloomBottom).Size(wishBloomSize, wishBloomSize).PassThrough().RadialGradient(ui.RadialGradient{Radius: wishBloomSize / 2, Stops: []ui.GradientStop{
				{Color: tone.Alpha(wishHighlightOpacity), At: 0}, {Color: ui.Transparent, At: 1},
			}})
		}
		ui.Column(c.Key("wish-header")).Absolute().Left(wishInset).Right(wishInset).Top(wishHeaderTop).Gap(wishSpaceXL).Children(func() {
			ui.Row(c).Height(wishHeaderHeight).Gap(wishSpaceXL).Children(func() {
				ui.Text(c, "抽卡分析").FontSize(wishTextTitle).FontWeight(700).Grow(1)
				if !narrow {
					s.wishActions(c, data)
				}
			})
			if narrow {
				ui.Row(c).Height(wishActionsHeight).Gap(wishSpaceMD).Children(func() { s.wishActions(c, data) })
			}
			ui.Row(c.Key("wish-source")).Gap(wishSpaceMD).Children(func() {
				ui.Box(c).Size(wishSourceDotSize, wishSourceDotSize).Radius(wishSourceDotSize / 2).Background(tone)
				source := "本地档案 · 尚无角色档案 · 尚无快照"
				role := data.RoleID
				if role == "" {
					role = data.Archive.RoleID
				}
				if role != "" {
					origin, freshness := "本地档案", "尚无快照"
					if data.Stats.Tajiduo != nil {
						origin = "塔吉多"
						freshness = "更新于 " + gacha.FormatTime(data.Stats.Tajiduo.FetchedAt, data.Location)
					}
					source = fmt.Sprintf("%s · 角色 %s · %s", origin, role, freshness)
					if data.ImportReported {
						source += fmt.Sprintf(" · 新增 %d 条", data.NewCount)
					}
				}
				ui.Text(c, source).FontSize(wishTextBody).TextColor(muted).SingleLine().Shrink(1).Tooltip(source)
			})
			if data.ImportReported && (data.Outside > 0 || data.Conflicts > 0) {
				ui.Text(c, fmt.Sprintf("上次导入：新增 %d 条 · 窗口外保留 %d 条 · 冲突保留本地 %d 条", data.NewCount, data.Outside, data.Conflicts)).FontSize(wishTextCaption).TextColor(muted)
			}
			if note := wishLinkNote(data.Tajiduo); note != "" {
				color := muted
				if data.Tajiduo.StatusWarn {
					color = s.wishWarningColor()
				}
				ui.Text(c, note).FontSize(wishTextCaption).TextColor(color).SingleLine().Shrink(1).Tooltip(note)
			}
		})
		if data.Loading || data.Failed || data.Corrupt || data.Archive.RoleID == "" {
			s.wishEmpty(c, data, cardsTop)
			return
		}
		highlighted := ui.Local(surface, "wish-highlight", func() string { return "" })
		nextHighlight := ""
		ui.Scroll(c.Key("wish-pools-scroll")).Absolute().Left(wishInset).Right(wishInset).Top(cardsTop).Bottom(wishBottomInset).Scrollbars(true).Children(func() {
			var container ui.Element
			if narrow {
				container = ui.Column(c.Key("wish-pools")).FillWidth().Gap(wishCardGap)
			} else {
				container = ui.Row(c.Key("wish-pools")).FillWidth().Gap(wishCardGap).AlignItems(ui.Stretch)
			}
			container.Children(func() {
				cardW := (available - 2*wishCardGap) / 3
				// The wide row always ends exactly at the viewport edge: a floor here
				// would clip the cards as soon as the header grows a line.
				cardH := height - cardsTop - wishBottomInset - 2*bezelWidth
				for _, pool := range []gacha.Pool{gacha.Limited, gacha.Permanent, gacha.Weapon} {
					w, h := cardW, cardH
					if narrow {
						w, h = available, wishNarrowCardH
					}
					s.wishPool(c, data, pool, w, h, *highlighted, &nextHighlight)
				}
			})
		})
		if nextHighlight != *highlighted {
			*highlighted = nextHighlight
			c.Invalidate()
		}
	})
	if s.wishIconsEnabled() && !s.wishIcons.active && len(s.wishIcons.wanted) > 0 {
		c.Invalidate()
	}
	if data.Confirm != nil {
		s.wishConfirmation(c, *data.Confirm)
	}
}

func (s *Shell) wishQuietButton(c *ui.Context, key, label string, disabled bool) ui.Element {
	b := s.quietButton(c, key, label, disabled)
	if prefs := c.Preferences(); prefs.HighContrast || prefs.ReduceTransparency {
		b.Background(s.theme.Surface).Border(wishHairlineWidth, muted)
	}
	return b
}

// wishSplitHalf is one half of the split key: the outer row owns the glass
// fill, the hairline and the rounding, so the two halves read as one control
// with a single divider instead of two touching pills.
func (s *Shell) wishSplitHalf(c *ui.Context, key, label string, disabled bool) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Label(label).Height(wishActionsHeight).Padding(0, wishSpaceXL).Justify(ui.Center).TextColor(paper).Disabled(disabled).DimDisabled(false)
	fill := ui.Transparent
	if disabled {
		b.TextColor(ui.Hex("#6E727B"))
	} else if b.Pressed() {
		fill = ui.RGBA(255, 255, 255, .16)
	} else if b.Hovered() || b.FocusVisible() {
		fill = ui.RGBA(255, 255, 255, .10)
	}
	s.controlFill(b, fill)
	b.Children(func() { ui.Text(c, label).FontSize(14).SingleLine() })
	return b
}

func (s *Shell) wishActions(c *ui.Context, data wishPageData) {
	s.wishQuietButton(c, "wish-role", "角色档案 ▾", data.Loading || data.Busy).Label("角色档案").Menu(func(m *ui.Menu) {
		if len(data.Roles) == 0 {
			m.Item("尚无角色档案").Disabled(true)
		}
		for _, role := range data.Roles {
			if m.Item("角色 "+role).Radio(role == data.RoleID || role == data.Archive.RoleID).Chosen() && data.OnRole != nil {
				data.OnRole(role)
			}
		}
		if data.RoleID != "" || data.Archive.RoleID != "" {
			m.Separator()
			if m.Item("删除本地记录").Chosen() {
				wishInvoke(data.OnDelete)
			}
		}
	})
	s.wishUpdateControl(c, data)
	// An empty archive has one import entry, in the empty state. Repeating it
	// in the header only splits attention; export stays out until there is
	// something to export.
	if data.Archive.RoleID != "" || len(data.Roles) > 0 {
		s.wishQuietButton(c, "wish-import", "导入", data.Loading || data.Busy).OnClick(func() { wishInvoke(data.OnImport) })
		s.wishQuietButton(c, "wish-export", "导出 ▾", data.Loading || data.Busy || data.Failed || data.Corrupt || data.Archive.RoleID == "").Label("导出").Menu(func(m *ui.Menu) {
			if m.Item("Oddward 完整格式").Chosen() && data.OnExport != nil {
				data.OnExport(false)
			}
			if m.Item("NTEUID 互通格式（不含 luck）").Chosen() && data.OnExport != nil {
				data.OnExport(true)
			}
		})
	}
}

// wishUpdateControl is the Tajiduo split key. Its main half links or updates,
// its menu half unlinks; Q15 keeps the whole control away from archives that
// belong to no saved account.
func (s *Shell) wishUpdateControl(c *ui.Context, data wishPageData) {
	t := data.Tajiduo
	if !t.Linkable {
		return
	}
	switch {
	case t.Linked || t.NeedsRelink:
		label, disabled := "更新记录", data.Loading || t.Fetching || t.Cooldown > 0 || t.RoleMismatch
		switch {
		case t.NeedsRelink:
			// A dead grant keeps the archives readable; only the entry point
			// changes, and nothing re-sends a code on its own (spec Q13).
			label, disabled = "重新关联", data.Loading
		case t.Fetching:
			label = "正在更新…"
		case t.Cooldown > 0:
			label = fmt.Sprintf("%d 秒后可更新", t.Cooldown)
		}
		var main, more ui.Element
		outer := ui.Row(c).Height(wishActionsHeight).Radius(12).Clip().Border(wishHairlineWidth, hairline).Background(ui.RGBA(255, 255, 255, .05)).AlignItems(ui.Stretch)
		outer.Children(func() {
			text := label
			if !t.NeedsRelink {
				text = "↻ " + label
			}
			main = s.wishSplitHalf(c, "wish-update", text, disabled).Label(label)
			main.OnClick(func() { wishInvoke(t.OnUpdate) })
			ui.Box(c).Width(wishHairlineWidth).Height(wishActionsHeight).Background(hairline)
			more = s.wishSplitHalf(c, "wish-update-more", "▾", data.Loading).Label("塔吉多关联")
			more.Menu(func(m *ui.Menu) {
				if m.Item("解除关联").Chosen() {
					wishInvoke(t.OnUnlink)
				}
			})
		})
		if main.Hovered() || main.FocusVisible() || more.Hovered() || more.FocusVisible() {
			outer.Border(wishHairlineWidth, s.tone().Alpha(.55))
		}
	case t.Linkable:
		s.wishQuietButton(c, "wish-update", "关联塔吉多", data.Loading || t.Fetching).OnClick(func() { wishInvoke(t.OnUpdate) })
	}
}

// wishLinkHint states the entry points honestly: what linking costs, how to get
// a saved account when there is none (spec §5.2.7, Q15), or that a stale grant
// only affects fetching, not the local archive already on screen.
func wishLinkHint(t wishTajiduo) string {
	if !t.Linkable {
		return "先在登录窗口勾选「在此设备保存账号」才能关联塔吉多"
	}
	switch {
	case t.NeedsRelink:
		return "塔吉多关联已失效，本地档案仍可查看；需要更新时重新关联"
	case t.Linked || t.Fetching:
		return ""
	}
	return "关联塔吉多会顶掉手机上已登录的塔吉多；使用已保存的游戏账号无需短信，短信登录时手机号未注册会自动创建账号"
}

// wishLinkNote is the one line under the header: the last link result when
// there is one, otherwise the hint. It never invents a freshness value.
func wishLinkNote(t wishTajiduo) string {
	if t.Status != "" {
		return t.Status
	}
	return wishLinkHint(t)
}

func (s *Shell) wishEmpty(c *ui.Context, data wishPageData, top float32) {
	ui.Column(c.Key("wish-empty")).Absolute().Left(wishInset).Right(wishInset).Top(top).Padding(wishEmptyPadding).Gap(wishSpaceXXL).Radius(wishCardRadius).Border(wishHairlineWidth, hairline).Material(frostedSurface{Tint: ui.RGBA(12, 14, 18, .22)}).Children(func() {
		title, body := "开始记录你的抽卡数据", "支持 Oddward 或 NTEUID JSON；关联账号可同步最近约 6 个月的记录。"
		switch {
		case data.Loading:
			title, body = "正在读取本地档案", "读取完成后会显示角色档案与出货记录。"
		case data.Corrupt:
			title, body = "本地档案已损坏", "原文件已保留，不会覆盖。可从角色档案菜单删除损坏的本地记录，再导入备份。"
		case data.Failed:
			title, body = "无法读取本地档案", "请检查档案文件及访问权限，然后重试。"
			if data.FailureTitle != "" {
				title, body = data.FailureTitle, data.FailureBody
			}
		case len(data.Roles) > 0:
			title, body = "选择角色档案", "从角色档案菜单选择要查看的本地记录。"
		}
		ui.Text(c, title).FontSize(wishTextDialog).FontWeight(600)
		ui.Text(c, body).FontSize(wishTextHelp).TextColor(muted).LineHeight(wishBodyLineHeight)
		if data.Failed && !data.Corrupt {
			s.accentButton(c, "wish-retry", "重试读取").AlignSelf(ui.Start).Disabled(data.Busy).OnClick(func() { wishInvoke(data.OnRetry) })
		} else if !data.Loading {
			s.accentButton(c, "wish-empty-import", "导入抽卡记录").Disabled(data.Busy).AlignSelf(ui.Start).OnClick(func() { wishInvoke(data.OnImport) })
			if !data.Tajiduo.Linkable && s.Model.NormalMode {
				s.quietButton(c, "wish-empty-account", "启动游戏并保存账号", false).AlignSelf(ui.Start).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.Navigate, Page: state.Home})
					s.Dispatch(state.Event{Kind: state.Primary})
				})
			}
		}
	})
}

func wishPoolName(pool gacha.Pool) string {
	switch pool {
	case gacha.Limited:
		return "限定卡池"
	case gacha.Permanent:
		return "常驻卡池"
	default:
		return "弧盘池"
	}
}

func (s *Shell) wishWarningColor() ui.Color {
	tone := s.tone()
	distance := func(candidate ui.Color) int {
		r, g, b := int(candidate.R)-int(tone.R), int(candidate.G)-int(tone.G), int(candidate.B)-int(tone.B)
		return r*r + g*g + b*b
	}
	if distance(wishWarningWarm) >= distance(wishWarningCool) {
		return wishWarningWarm
	}
	return wishWarningCool
}

// wishPoolStatValue shows a statistic only when a valid record backs it.
func wishPoolStatValue(stats gacha.PoolStatistics, value string) string {
	if stats.SCount <= stats.Anomalous {
		return "—"
	}
	return value
}

func (s *Shell) wishPool(c *ui.Context, data wishPageData, pool gacha.Pool, width, height float32, highlight string, nextHighlight *string) {
	stats := data.Stats.Pools[pool]
	name := wishPoolName(pool)
	ui.Column(c.Key("wish-pool-"+string(pool))).Label(name).Role(ui.RoleGroup).Width(width).Height(height).Shrink(0).Radius(wishCardRadius).Clip().Border(wishHairlineWidth, hairline).Material(frostedSurface{Tint: ui.RGBA(12, 14, 18, .22)}).Children(func() {
		ui.Column(c).FillWidth().Grow(1).MinHeight(0).Padding(wishCardPaddingX, wishCardPaddingY).Gap(wishSpaceXXS).Children(func() {
			ui.Row(c).FillWidth().Height(wishPoolHeaderHeight).AlignItems(ui.Center).Children(func() {
				ui.Text(c, name).FontSize(wishTextPool).TextColor(muted).SingleLine().Grow(1).Shrink(1)
				ui.Text(c, fmt.Sprint(stats.SCount)).Font(monoFont).FontSize(wishTextPool).TextColor(muted).SingleLine().Tooltip("已出 S 记录")
			})
			ui.Row(c).FillWidth().Height(wishPoolMetaHeight).AlignItems(ui.Center).Children(func() {
				if !stats.Earliest.IsZero() {
					span := gacha.FormatTime(stats.Earliest, data.Location) + " - " + gacha.FormatTime(stats.Latest, data.Location)
					ui.Text(c, span).FontSize(wishTextFootnote).TextColor(muted).SingleLine().Tooltip(span)
				}
			})
			ui.Box(c).FillWidth().Height(wishHairlineWidth).Background(hairline)
			s.wishPoolStatLine(c, "S 平均", wishPoolStatValue(stats, fmt.Sprintf("%.1f 抽", stats.AveragePity)), wishPityGreen)
			s.wishPoolStatLine(c, "S 统计", wishPoolStatValue(stats, fmt.Sprintf("%d 条", stats.SCount)), wishUpAccent)
			// SumPity covers only valid S records, so label it as a
			// cumulative count instead of presenting it as total draws.
			s.wishPoolStatLine(c, "累计", wishPoolStatValue(stats, fmt.Sprintf("%d 抽", stats.SumPity)), muted)
			s.wishPoolStatLine(c, "最欧 / 最非", wishPoolSpread(stats), muted)
			ui.Box(c).FillWidth().Height(wishHairlineWidth).Background(hairline)
			ui.Row(c).FillWidth().Height(wishPoolSectionHeight).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "出货记录").FontSize(wishTextBody).TextColor(paper).SingleLine()
				ui.Box(c).Grow(1)
				ui.Text(c, fmt.Sprintf("%d 条", stats.SCount)).FontSize(wishTextFootnote).TextColor(muted).SingleLine()
			})
			ui.Scroll(c.Key("wish-records-" + string(pool))).FillWidth().Grow(1).MinHeight(wishRecordsMinHeight).Scrollbars(true).Children(func() {
				ui.Column(c).FillWidth().Gap(wishRowGap).Children(func() {
					empty := true
					for i, r := range data.Archive.Records {
						if r.Pool == pool {
							empty = false
							s.wishRecord(c, data, r, i, highlight, nextHighlight)
						}
					}
					if empty {
						ui.Text(c, "尚无 S 级出货记录").FontSize(wishTextBody).TextColor(muted)
					}
				})
			})
			now := data.Now
			if now.IsZero() {
				now = s.now
			}
			if !now.IsZero() && !stats.Earliest.IsZero() && stats.Earliest.Before(now.AddDate(0, -6, 0)) {
				ui.Text(c, "更早的记录来自本地档案").FontSize(wishTextFootnote).TextColor(muted)
			}
			if stats.Anomalous > 0 {
				ui.Text(c, fmt.Sprintf("! %d 条异常抽数未计入平均", stats.Anomalous)).FontSize(wishTextFootnote).TextColor(s.wishWarningColor())
			}
			if data.Stats.Tajiduo != nil {
				for _, meta := range data.Stats.Tajiduo.Pools {
					if meta.Pool == pool {
						s.wishSnapshot(c, meta, data.Stats.Tajiduo.LuckTitle)
					}
				}
			}
		})
	})
}

func (s *Shell) wishPoolStatLine(c *ui.Context, label, value string, color ui.Color) {
	ui.Row(c).FillWidth().Height(wishPoolStatRowHeight).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(wishTextBody).TextColor(color).SingleLine()
		ui.Box(c).Grow(1)
		ui.Text(c, value).Font(monoFont).FontSize(wishTextBody).TextColor(color).SingleLine()
	})
}

func wishPoolSpread(stats gacha.PoolStatistics) string {
	if stats.SCount <= stats.Anomalous {
		return "—"
	}
	return fmt.Sprintf("%d / %d 抽", stats.MinPity, stats.MaxPity)
}

// wishPityBarColor follows Starward's two-stop pity bar: the character pools
// turn red from 60 pulls, the arc pool from 50. Everything below stays green.
// It describes a recorded pity value only; it never estimates current pity.
func wishPityBarColor(pool gacha.Pool, pity int) ui.Color {
	threshold := 60
	if pool == gacha.Weapon {
		threshold = 50
	}
	if pity >= threshold {
		return wishPityRed
	}
	return wishPityGreen
}

func wishItemName(catalog gacha.Catalog, id string) (string, bool) {
	if item, ok := catalog.Lookup(id); ok {
		return strings.TrimSuffix(item.Name, "角色卡"), true
	}
	return id, false
}

func (s *Shell) wishRecord(c *ui.Context, data wishPageData, r gacha.Record, index int, highlight string, nextHighlight *string) {
	name, known := wishItemName(data.Catalog, r.ItemID)
	band := gacha.Band(r.Pool, r.Pity)
	at := gacha.FormatTime(r.At, data.Location)
	featured := r.Pool == gacha.Weapon && gacha.IsUp(r.Pool, r.ItemID)
	label := fmt.Sprintf("%s，%d 抽，%s", name, r.Pity, at)
	if featured {
		label += "，当期限定"
	}
	tone := s.tone()
	prefs := c.Preferences()
	opaque := prefs.HighContrast || prefs.ReduceTransparency
	bandColor := tone
	if band == gacha.Boosted || band == gacha.Abnormal {
		bandColor = s.wishWarningColor()
	}
	row := ui.Row(c.Key(fmt.Sprintf("wish-record-row-%d", index))).Height(wishRowHeight).FillWidth().AlignItems(ui.Center).Radius(wishSpaceMD).TextColor(paper)
	var record ui.Element
	row.Children(func() {
		var image *ui.Bitmap
		bounds := row.Bounds()
		_, viewportHeight := c.Size()
		if data.Image != nil && (bounds.H == 0 || (bounds.Y+bounds.H > 0 && bounds.Y < viewportHeight)) {
			image = data.Image(r.ItemID)
		}
		iconKey := c.Key(fmt.Sprintf("wish-icon-%d", index))
		if image != nil {
			ui.Image(iconKey, image).Size(wishIconSize, wishIconSize).Shrink(0).Radius(wishIconRadius).Fit(ui.Contain).Label(name)
		} else {
			ui.Box(iconKey).Size(wishIconSize, wishIconSize).Shrink(0).Radius(wishIconRadius).Background(s.theme.SurfaceHover).Role(ui.RoleImage).Label(name + "，图片不可用").Children(func() {
				first := '?'
				if known {
					first, _ = utf8.DecodeRuneInString(name)
				}
				ui.Text(c, string(first)).FontSize(wishTextIcon).TextColor(muted).Absolute().Left(wishIconInitialLeft).Top(wishIconInitialTop).PassThrough()
				if r.Pool == gacha.Weapon || gacha.Kind(r.ItemID) == gacha.Arc {
					ui.Text(c, "◇").FontSize(wishTextIndex).TextColor(muted).Absolute().Right(wishIconCornerRight).Bottom(wishIconCornerBottom).PassThrough()
				}
			})
		}
		record = ui.ButtonBase(c.Key(fmt.Sprintf("wish-record-%d", index))).Label(label).Tooltip(label).Grow(1).MinWidth(0).Height(wishRowHeight).TextColor(paper)
		record.OnClick(func() { record.Focus() }).Children(func() {
			ui.Row(c).FillWidth().Height(wishRowHeight).AlignItems(ui.Center).Gap(wishSpaceMD).Children(func() {
				// The name sits on a bar whose length follows the same pity
				// ratio as the strip above, matching Starward's compact rows
				// while keeping the number honest (it is the recorded pity,
				// never a current-pity guess).
				ui.Box(c).Grow(1).MinWidth(0).Height(wishRowHeight).Children(func() {
					fill := wishPityBarColor(r.Pool, r.Pity)
					bar := ui.Box(c.Key(fmt.Sprintf("wish-bar-%d", index))).Absolute().Left(0).Top(wishRecordBarInsetTop).Bottom(wishRecordBarInsetBottom).WidthPercent(max(float32(gacha.Ratio(r.Pool, r.Pity))*100, 12)).Radius(wishRecordBarRadius).PassThrough()
					if opaque {
						// Keep the hue, but darken it enough that the white
						// record name stays readable in high contrast.
						bar.Background(fill.Mix(ink, wishOpaqueBarMix))
					} else {
						// Match Starward's fading wash: strongest at the left
						// edge, transparent where the bar ends.
						bar.LinearGradient(ui.LinearGradient{Angle: 90, Stops: []ui.GradientStop{
							{Color: fill.Alpha(wishRecordBarOpacity), At: 0}, {Color: fill.Alpha(0), At: 1},
						}})
					}
					ui.Row(c).FillWidth().Height(wishRowHeight).Padding(0, wishSpaceSM).Gap(wishSpaceXXS).AlignItems(ui.Center).Children(func() {
						title := ui.Text(c, name).FontSize(wishTextBody).FontWeight(600).SingleLine().Grow(1).Shrink(1)
						if !known {
							title.Font(wishMonospace)
							ui.Text(c, "未收录").FontSize(wishTextFootnote).TextColor(muted).SingleLine()
						}
					})
				})
				ui.Column(c).Width(wishRecordPityWidth).Shrink(0).AlignItems(ui.End).Children(func() {
					ui.Row(c).FillWidth().Justify(ui.End).AlignItems(ui.Center).Gap(2).Children(func() {
						if featured {
							ui.Text(c, "up!").FontSize(wishTextFootnote).FontWeight(700).TextColor(wishUpAccent).SingleLine()
						}
						ui.Text(c, fmt.Sprintf("%d 抽", r.Pity)).Font(monoFont).FontSize(wishTextNumber).FontWeight(600).TextColor(bandColor).SingleLine()
					})
				})
			})
		})
	})
	if row.Hovered() || record.Focused() {
		*nextHighlight = r.ItemID
	}
	fill := ui.Transparent
	if highlight == r.ItemID {
		fill = tone.Alpha(wishHighlightOpacity)
		if opaque {
			fill = tone.Mix(ink, wishOpaqueHighlightMix)
		}
	}
	row.Background(fill)
}

func (s *Shell) wishSnapshot(c *ui.Context, meta gacha.PoolMeta, luckTitle string) {
	summary := fmt.Sprintf("塔吉多统计 · %d 抽 · 平均 %s · 超过 %s", meta.DrawCount, meta.Average, meta.PlayerOver)
	summaryColor := muted
	if meta.RuleMismatch {
		summary += " · 规则表可能过期"
		summaryColor = s.wishWarningColor()
	}
	ui.Row(c.Key("wish-snapshot-"+string(meta.Pool))).FillWidth().Padding(wishSpaceXS, wishSpaceMD).Radius(wishSnapshotRadius).Background(s.theme.Surface).Border(wishHairlineWidth, hairline).AlignItems(ui.Center).Gap(wishSpaceMD).Children(func() {
		ui.Text(c, summary).Font(monoFont).FontSize(wishTextIndex).TextColor(summaryColor).SingleLine().Grow(1).Shrink(1).Tooltip(summary)
		if luckTitle != "" {
			ui.Text(c, luckTitle).FontSize(wishTextIndex).TextColor(s.tone()).SingleLine()
		}
	})
}

func (s *Shell) wishConfirmation(c *ui.Context, confirmation wishPageConfirmation) {
	ui.Overlay(c, func() {
		backdrop := ink.Alpha(wishBackdropOpacity)
		if prefs := c.Preferences(); prefs.HighContrast || prefs.ReduceTransparency {
			backdrop = ink
		}
		back := ui.Box(c.Key("wish-confirm-backdrop")).Fill().Background(backdrop).Modal()
		back.Children(func() {
			ui.Column(c.Key("wish-confirm")).Absolute().LeftPercent(50).TopPercent(50).Translate(-wishConfirmWidth/2, -wishConfirmOffsetY).Width(wishConfirmWidth).Padding(wishConfirmPadding).Gap(wishSpaceHero).Radius(sheetRadius).Background(s.theme.Surface).Border(wishHairlineWidth, hairline).Role(ui.RoleDialog).Label(confirmation.Title).Children(func() {
				ui.Text(c, confirmation.Title).FontSize(wishTextDialog).FontWeight(700)
				ui.Text(c, confirmation.Body).FontSize(wishTextHelp).TextColor(muted).LineHeight(wishBodyLineHeight)
				ui.Row(c).FillWidth().Gap(wishSpaceXL).Children(func() {
					s.wishQuietButton(c, "wish-cancel", "取消", false).Grow(1).AutoFocus().OnClick(func() { wishInvoke(confirmation.OnCancel) })
					s.wishQuietButton(c, "wish-accept", confirmation.Accept, false).Grow(1).OnClick(func() { wishInvoke(confirmation.OnAccept) })
				})
			})
		})
		if back.OverlayShortcut(0, ui.KeyEscape) {
			wishInvoke(confirmation.OnCancel)
		}
	})
}
