// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// "Frame": the version's key art hangs in a rounded canvas inside a dark
// bezel, like a print in a passe-partout. Chrome is neutral glass; the only
// saturated colour is the launch tone, borrowed from the art itself, so the
// launcher follows each version's slogan and background instead of fighting
// them.
var (
	ink     = ui.Hex("#0C0D10")
	paper   = ui.Hex("#F5F6F7")
	muted   = ui.Hex("#A4A8B1")
	warning = ui.Hex("#FFC857")
	// The farther candidate stays distinguishable from any artwork tone;
	// their RGB separation also bounds the worst-case midpoint distance.
	wishWarningWarm = ui.Hex("#FF9966")
	wishWarningCool = ui.Hex("#66DDFF")
	// Starward's pity bar stops: green while the pull is early, red once it
	// crosses the pool's colour threshold. The orange marks featured drops.
	wishPityGreen = ui.Hex("#00E079")
	wishPityRed   = ui.Hex("#C83C23")
	wishUpAccent  = ui.Hex("#FF8A3D")
	hairline      = ui.RGBA(255, 255, 255, .12)
	bezel         = ui.Hex("#0E0F12")
	glassInk      = ui.RGBA(14, 15, 19, .58)
	homeDockTint  = ui.RGBA(14, 15, 19, .80)
)

const (
	downloadInputHeight       float32 = 34
	downloadGap               float32 = 10
	downloadTitleSize         float32 = 20
	downloadBodySize          float32 = 14
	downloadCaptionSize       float32 = 12
	downloadButtonHeight      float32 = 36
	downloadMeterHeight       float32 = 4
	downloadStatusHeight      float32 = 26
	homeDownloadGap           float32 = 8
	homeDockHeight            float32 = 48
	homeDockPrimaryWidth      float32 = 144
	homeDockWidth                     = homeDockPrimaryWidth + homeDockHeight
	homePreloadChipWidth      float32 = 120
	homePreloadChipGap        float32 = 8
	homeDockBadgeSize         float32 = 34
	homeDockPopupInset        float32 = 12
	homeDockPopupRadius       float32 = 8
	locateActionGap           float32 = 4
	locateActionTextSize      float32 = 12
	brandMarkSize             float32 = 40
	bezelWidth                float32 = 8
	canvasRadius              float32 = 16
	homeLeft                          = railWidth + 40
	newsWidth                 float32 = 600
	newsHeight                float32 = 152
	coverWidth                float32 = 270
	coverHeight                       = newsHeight
	homeBottom                        = bezelWidth + 72
	sheetInset                        = 12
	sheetEnterDistance        float32 = 12
	sheetRadius                       = 20
	sheetGap                  float32 = 14
	sheetWidth                float32 = 408
	locateGameDialogWidth     float32 = 544
	locateGameDialogHeight    float32 = 520
	gameSettingsWidthRatio    float32 = 2.0 / 3.0
	gameSettingsHeightRatio   float32 = .72
	gameSettingsNavWidth      float32 = 148
	gameSettingsTabHeight     float32 = 40
	gameSettingsTabRadius     float32 = 8
	gameSettingsTabGap        float32 = 6
	gameSettingsTabInset      float32 = 12
	gameSettingsTabMark       float32 = 3
	gameSettingsPaneGap       float32 = 18
	gameSettingsHeadingSize   float32 = 17
	launcherSettingsTop       float32 = 68
	launcherSettingsInset     float32 = 28
	launcherSettingsTitleSize float32 = 25
	launcherSettingsNavWidth  float32 = 218
	launcherSettingsTabHeight float32 = 40
	launcherSettingsGap       float32 = 16
	launcherSettingsBodyWidth float32 = 620
	launcherSettingsColumnGap float32 = 32
	launcherSettingsMarkSize  float32 = 56
	dockRight                         = bezelWidth + 40
	actionWidth               float32 = 248
	actionHeight              float32 = 60
	menuGap                   float32 = 10
	gameSettingsIconSize      float32 = 16
	gameSettingsLabelSize     float32 = 11
	gameSettingsLabelGap      float32 = 3
	dockWidth                         = actionWidth + menuGap + actionHeight
	deckHeight                        = 96
	deckGap                           = 12
	captionSize               float32 = 32
	sloganTop                 float32 = 140
	sloganWidth               float32 = 440
	sloganHeight              float32 = 200
	wishInset                 float32 = 32
	wishHeaderTop             float32 = 76
	wishCardsTop              float32 = 192
	wishImportReportHeight    float32 = 28
	wishLinkNoteHeight        float32 = 28
	wishCardMinWidth          float32 = 300
	wishCardGap               float32 = 16
	wishCardRadius            float32 = 8
	wishCardPaddingX          float32 = 12
	wishCardPaddingY          float32 = 8
	wishRowHeight             float32 = 28
	wishRowGap                float32 = 2
	wishIconSize              float32 = 28
	wishNarrowCardH           float32 = 440
	wishBottomInset           float32 = 54
	wishRecordBarInsetTop     float32 = 2
	wishRecordBarInsetBottom  float32 = 2
	wishRecordBarRadius       float32 = 4
	wishRecordBarOpacity      float32 = .32
	wishOpaqueBarMix          float32 = .72
	wishRecordPityWidth       float32 = 60
	wishPoolHeaderHeight      float32 = 24
	wishPoolMetaHeight        float32 = 15
	wishPoolStatRowHeight     float32 = 18
	wishPoolSectionHeight     float32 = 24
	wishMonospace                     = "Consolas"
	wishIconCacheSize                 = 128
	wishIconDecodeEdge                = 96
	wishTextFootnote          float32 = 11
	wishTextIndex             float32 = 11
	wishTextCaption           float32 = 12
	wishTextBody              float32 = 13
	wishTextNumber            float32 = 13
	wishTextHelp              float32 = 14
	wishTextPool              float32 = 16
	wishTextIcon              float32 = 14
	wishTextDialog            float32 = 22
	wishTextTitle             float32 = 30
	wishSpaceXXS              float32 = 4
	wishSpaceXS               float32 = 5
	wishSpaceSM               float32 = 6
	wishSpaceMD               float32 = 8
	wishSpaceLG               float32 = 10
	wishSpaceXL               float32 = 12
	wishSpaceXXL              float32 = 16
	wishSpaceHero             float32 = 18
	wishHairlineWidth         float32 = 1
	wishHeaderHeight          float32 = 44
	wishActionsHeight         float32 = 42
	wishNarrowHeaderExtra     float32 = 40
	wishSourceDotSize         float32 = 6
	wishEmptyPadding          float32 = 28
	wishRecordsMinHeight      float32 = 80
	wishIconRadius            float32 = 4
	wishIconInitialLeft       float32 = 9
	wishIconInitialTop        float32 = 4
	wishIconCornerRight       float32 = 3
	wishIconCornerBottom      float32 = 2
	wishConfirmWidth          float32 = 440
	wishConfirmOffsetY        float32 = 130
	wishConfirmPadding        float32 = 26
	wishBloomSize             float32 = 720
	wishBloomRight            float32 = -200
	wishBloomBottom           float32 = -260
	wishHighlightOpacity      float32 = .12
	wishBackdropOpacity       float32 = .72
	wishOpaqueHighlightMix    float32 = .82
	wishBodyLineHeight        float32 = 1.6
	// The pool card keeps Starward's compact 12/8 padding and 28px record
	// rows; records get the vertical space instead of a separate chart.
	wishSnapshotRadius float32 = 10
	// Check-in page: one sheet, an account row, two independent task cards and
	// the game status panel. Sizes stay here so every page shares one scale.
	checkinInset         float32 = 32
	checkinBottomInset   float32 = 54 // clears the footer note, same as the wish columns
	checkinHeaderTop     float32 = 64
	checkinBodyTop       float32 = 154
	checkinBodyGap       float32 = 12
	checkinCardPadding   float32 = 20
	checkinCardRadius    float32 = 16
	checkinCardGap       float32 = 16
	checkinStatusGap     float32 = 6
	checkinTaskHeight    float32 = 154
	checkinStatusMin     float32 = 230
	checkinRowHeight     float32 = 26
	checkinChipHeight    float32 = 24
	checkinChipRadius    float32 = 12
	checkinChipPadding   float32 = 10
	checkinMeterHeight   float32 = 6
	checkinMeterRadius   float32 = 3
	checkinSourceDotSize float32 = 6
	checkinStatGap       float32 = 24
	checkinTextIndex     float32 = 12
	checkinTextCaption   float32 = 12
	checkinTextBody      float32 = 13
	checkinTextHelp      float32 = 14
	checkinTextValue     float32 = 22
	checkinTextTitle     float32 = 30
	checkinSpaceXXS      float32 = 4
	checkinSpaceXS       float32 = 6
	checkinSpaceSM       float32 = 8
	checkinSpaceMD       float32 = 12
	checkinSpaceLG       float32 = 18
	checkinSpaceXL       float32 = 24
	checkinHairlineWidth float32 = 1
	checkinStatusOpacity float32 = .18
	// Hethereau Gallery page layout constants.
	galleryInset                float32 = 28
	galleryHeaderTop            float32 = 58
	galleryToolbarHeight        float32 = 40
	galleryTabsHeight           float32 = 34
	galleryGridTop              float32 = 154
	galleryGridTopNotice        float32 = 182
	galleryStatusBarHeight      float32 = 32
	galleryBottomInset          float32 = 54
	galleryDateHeaderHeight     float32 = 34
	galleryTileMinWidth         float32 = 232
	galleryTileGap              float32 = 12
	galleryTileFooterHeight     float32 = 34
	galleryTileRadius           float32 = 10
	galleryControlHeight        float32 = 34
	galleryViewerBarHeight      float32 = 52
	galleryViewerFootHeight     float32 = 64
	gallerySettingsDialogWidth  float32 = 760
	gallerySettingsDialogHeight float32 = 520
	gallerySettingsNavWidth     float32 = 184
	gallerySettingsCardRadius   float32 = 14
	monoFont                            = "Bahnschrift"
)

// The first-screen prefetch estimate is kept out of the layout block above so
// adding it never realigns every constant; see wishFirstScreenRows.
const (
	wishIconFirstScreenMaxRows         = 24
	wishIconFootnoteLine       float32 = 20
)

func launcherTheme() *ui.Theme {
	t := *ui.DarkTheme()
	t.Background, t.Text, t.TextMuted = ink, paper, muted
	t.Accent, t.AccentText, t.Focus = defaultAccent, ink, defaultAccent
	t.InactiveAccent = defaultAccent
	t.Surface, t.SurfaceHover = ui.Hex("#17191E"), ui.Hex("#20232A")
	t.Border, t.Radius, t.FontSize = hairline, 8, 14
	t.Font = "Microsoft YaHei UI"
	return &t
}

// tone is the launch colour: the key art's own hue when one was derived,
// the default signal yellow otherwise.
func (s *Shell) tone() ui.Color {
	if s.Accent.A == 0 || !s.Model.AccentFollow {
		return defaultAccent
	}
	return s.Accent
}

func (s *Shell) motion() bool { return !s.Model.ReducedMotion && !s.Model.SystemReducedMotion }

// mono is the Latin index type: Bahnschrift's DIN-like figures, tracked out.
func mono(c *ui.Context, s string, size float32) ui.Element {
	return ui.Text(c, s).Font(monoFont).FontSize(size).LetterSpacing(size * .1).SingleLine()
}

// canvas is the rounded window onto the key art.
func canvas(c *ui.Context, key string) ui.Element {
	return ui.Box(c.Key(key)).Absolute().Left(railWidth).Top(bezelWidth).Right(bezelWidth).Bottom(bezelWidth).Radius(canvasRadius).Clip()
}

func (s *Shell) stage(c *ui.Context) {
	canvas(c, "stage").PassThrough().Children(func() {
		ui.Box(c).Fill().Gradient(ui.Hex("#22252D"), ui.Hex("#121318"), 150)
		if s.Poster != nil {
			ui.Image(c.Key("background-still"), s.Poster).Absolute().Left(0).Right(0).Top(0).Bottom(0).Fit(ui.Cover)
		} else {
			// Native, asset-free fallback; official artwork is never bundled.
			tone := s.tone()
			ui.Box(c).Absolute().Right(-160).Top(-200).Size(720, 720).Radius(360).Border(1, tone.Alpha(.10))
			ui.Box(c).Absolute().Right(-40).Top(-80).Size(480, 480).Radius(240).Border(1, tone.Alpha(.07))
		}
		// Mount while loading: construction advances the native decoder. Hiding
		// an unready/paused surface must not stop its initialization.
		if s.Player != nil && !s.Model.MediaPreview && s.Model.Media != state.MediaUnavailable {
			v := ui.Video(c, s.Player).Label("背景视频").Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover)
			if s.Model.Page == state.LauncherSettings || s.Model.Media == state.MediaLoading || !s.Model.AnimateBackground() {
				v.Opacity(0)
			}
		}
		// A replacement decodes behind the visible frame. It must be mounted to
		// start, but stays invisible until tick swaps it in on Ready.
		if s.nextPlayer != nil && !s.Model.MediaPreview {
			ui.Video(c, s.nextPlayer).Label("背景视频（预载）").Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover).Opacity(0)
		}
		// Feathered legibility only, so any version's art keeps its colour:
		// a soft left shade for the hero column and a floor for the dock.
		ui.Box(c).Absolute().Left(0).Top(0).Bottom(0).Width(520).LinearGradient(ui.LinearGradient{Angle: 90, Stops: []ui.GradientStop{
			{Color: ui.RGBA(8, 9, 12, .32), At: 0}, {Color: ui.RGBA(8, 9, 12, .10), At: .6}, {Color: ui.Transparent, At: 1},
		}})
		ui.Box(c).Absolute().Left(0).Top(0).Right(0).Height(110).Gradient(ui.RGBA(8, 9, 12, .26), ui.Transparent, 180)
		ui.Box(c).Absolute().Left(0).Bottom(0).Right(0).Height(280).LinearGradient(ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
			{Color: ui.Transparent, At: 0}, {Color: ui.RGBA(8, 9, 12, .20), At: .5}, {Color: ui.RGBA(8, 9, 12, .58), At: 1},
		}})
		// A one-pixel inner rim lifts the print off the bezel.
		ui.Box(c).Fill().Radius(canvasRadius).Border(1, ui.RGBA(255, 255, 255, .08)).PassThrough()
	})
}

// ButtonBase retains native focus, keyboard activation and accessible names.
func iconButton(c *ui.Context, key, label, icon string, size float32, fn func()) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Label(label).Tooltip(label).Size(size, size).Radius(size / 2).Justify(ui.Center).TextColor(muted)
	if b.Hovered() {
		b.Background(ui.RGBA(255, 255, 255, .10)).TextColor(paper)
	}
	if b.Pressed() {
		b.Background(ui.RGBA(255, 255, 255, .18))
	}
	b.Children(func() { glyph(c, icon, 16) }).OnClick(fn)
	return b
}

func (s *Shell) topbar(c *ui.Context) {
	// The bezel's top edge and the canvas header both move the window.
	ui.Box(c.Key("drag-bezel")).Absolute().Left(0).Top(0).Right(0).Height(bezelWidth).DragWindow()
	ui.Box(c.Key("topbar")).Absolute().Left(railWidth).Top(bezelWidth).Right(bezelWidth).Height(captionSize + 32).DragWindow()
	ui.Row(c.Key("caption")).Absolute().Right(bezelWidth + 16).Top(bezelWidth + 16).Height(captionSize).Gap(8).Children(func() {
		s.updatePill(c)
		for _, item := range []struct {
			key, label, icon string
			fn               func()
		}{
			{"minimize", "最小化", "\uE921", s.Minimize},
			{"maximize", "最大化或还原", "\uE922", s.Maximize},
			{"close", "关闭窗口", "\uE8BB", func() { s.Dispatch(state.Event{Kind: state.RequestQuit}) }},
		} {
			item := item
			if item.key == "maximize" && s.FixedWindow {
				continue
			}
			b := ui.ButtonBase(c.Key(item.key)).Label(item.label).Tooltip(item.label).Size(captionSize, captionSize).Radius(captionSize/2).
				Justify(ui.Center).TextColor(paper).Border(1, hairline)
			switch {
			case b.Hovered() && item.key == "close":
				b.Background(ui.Hex("#E5484D")).Border(1, ui.Transparent)
			case b.Hovered():
				b.Material(frostedSurface{Tint: ui.RGBA(14, 15, 19, .72)})
			default:
				b.Material(frostedSurface{Tint: ui.RGBA(14, 15, 19, .46)})
			}
			if b.Pressed() {
				b.Opacity(.82)
			}
			b.Children(func() { glyph(c, item.icon, 13) }).OnClick(func() {
				if item.fn != nil {
					item.fn()
				}
			})
		}
	})
}

// Artwork uses its complete source ratio inside a version-neutral safe area.
// No offsets refer to the transparent margins of one particular campaign.
func containedArtwork(c *ui.Context, key, label string, bitmap *ui.Bitmap, area ui.Rect) {
	w, h := ui.ImageSize(bitmap)
	if w <= 0 || h <= 0 || area.W <= 0 || area.H <= 0 {
		return
	}
	scale := min(area.W/w, area.H/h)
	ui.Image(c.Key(key), bitmap).Absolute().Left(area.X).Top(area.Y).Size(w*scale, h*scale).Fit(ui.Contain).Label(label).PassThrough()
}

func dockTop(height float32) float32 { return height - homeBottom - newsHeight }

func (s *Shell) hero(c *ui.Context) {
	_, height := c.Size()
	if s.Logo != nil {
		containedArtwork(c, "official-logo", "异环官方标志", s.Logo, ui.Rect{X: homeLeft, Y: 40, W: 120, H: 84})
	} else {
		ui.Text(c, "异环").Absolute().Left(homeLeft).Top(44).FontSize(22).TextColor(paper).PassThrough()
	}
	if s.Slogan != nil {
		available := min(sloganHeight, dockTop(height)-32-sloganTop)
		containedArtwork(c, "campaign", "异环官网活动标语", s.Slogan, ui.Rect{X: homeLeft, Y: sloganTop, W: sloganWidth, H: available})
	}
}

func (s *Shell) action(c *ui.Context, p state.Presentation) {
	s.homeStamina(c)
	s.newsCard(c)
	if s.Model.NormalMode {
		s.managedActionNote(c, p)
		// One capsule starts, updates or pauses; the preload chip only exists
		// while there is a preload to report.
		s.homeDock(c, p)
	} else {
		s.taskDeck(c, p)
		ui.Box(c.Key("action-area")).Absolute().Right(dockRight+actionHeight+menuGap).Bottom(homeBottom).Size(actionWidth, actionHeight).Children(func() {
			s.primary(c, p)
		})
		s.gameSettingsButton(c)
	}
	s.footer(c)
}

// locateActionBounds is the first-run link's box: bottom-right, just under
// the download capsule, and touching the strip the media lane reveals over.
// updateMediaBar reads the same box so the lane never opens under the link.
func locateActionBounds(width, height float32) ui.Rect {
	return ui.Rect{
		X: width - dockRight - homeDockHeight - homeDockPrimaryWidth,
		Y: height - homeBottom + locateActionGap,
		W: homeDockPrimaryWidth,
		H: downloadStatusHeight,
	}
}

func (s *Shell) locateExistingAction(c *ui.Context) {
	width, height := c.Size()
	r := locateActionBounds(width, height)
	ui.Row(c.Key("locate-existing-row")).Absolute().Left(r.X).Top(r.Y).Size(r.W, r.H).
		Justify(ui.End).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "已下载游戏？点击").FontSize(locateActionTextSize).TextColor(paper).SingleLine().PassThrough()
		const label = "定位游戏"
		b := ui.ButtonBase(c.Key("locate-existing")).Label(label).Height(downloadStatusHeight).Justify(ui.Center)
		tone := s.tone()
		if b.Hovered() || b.FocusVisible() {
			b.Draw(func(p *ui.Painter, r ui.Rect) {
				y := r.Y + r.H/2 + locateActionTextSize/2 + 2
				p.Line(r.X, y, r.X+r.W, y, 1, tone)
			})
		}
		b.Children(func() {
			ui.Text(c, label).FontSize(locateActionTextSize).TextColor(tone).SingleLine().PassThrough()
		}).OnClick(func() {
			s.ManagedPathInput = ""
			s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.LocateGame})
		})
	})
}

func (s *Shell) dispatchWidth(c *ui.Context) float32 {
	width, _ := c.Size()
	return max(min(newsWidth, width-homeLeft-dockRight-dockWidth-48), 360)
}

// One glass card over the official feed the shell read from the public
// site: the carousel cover on the left, categories and rows on the right.
// Nothing here fetches, and nothing opens a browser.
func (s *Shell) newsCard(c *ui.Context) {
	tab := s.Model.NewsTab
	if tab > 2 {
		tab = 0
	}
	width := s.dispatchWidth(c)
	tone := s.tone()
	module := ui.Box(c.Key("news-module")).Label("官方公告").Absolute().Left(homeLeft).Bottom(homeBottom).Size(width, newsHeight).
		Radius(14).Clip().Material(frostedSurface{Tint: glassInk}).Bind(&s.bannerRotation.module)
	s.updateBannerRotation(c, module)
	headline, meta := s.newsHeadline()
	module.Children(func() {
		cover := ui.ButtonBase(c.Key("official-news")).Label("查看官方公告").Tooltip(s.newsTooltip()).Absolute().Left(0).Top(0).Bottom(0).Width(coverWidth).TextColor(paper).Clip()
		cover.Children(func() {
			s.bannerArtwork(c)
			ui.Box(c).Absolute().Left(0).Right(0).Top(0).Bottom(0).PassThrough().LinearGradient(ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
				{Color: ui.Transparent, At: 0}, {Color: ui.RGBA(8, 9, 12, .30), At: .4}, {Color: ui.RGBA(8, 9, 12, .92), At: .85}, {Color: ui.RGBA(8, 9, 12, .95), At: 1},
			}})
			if cover.Hovered() {
				ui.Box(c).Fill().Background(ui.RGBA(255, 255, 255, .06))
			}
			ui.Column(c).Absolute().Left(16).Bottom(14).Right(16).Gap(3).PassThrough().Children(func() {
				h := ui.Text(c, headline).FontSize(15).FontWeight(600).SingleLine()
				if cover.Hovered() {
					h.TextColor(tone)
				}
				if meta != "" {
					ui.Text(c, meta).FontSize(11).TextColor(muted).SingleLine()
				}
			})
		}).HandleInput(func(ev ui.InputEvent) bool {
			if ev.Kind != ui.InputScroll {
				return false
			}
			return s.stepNewsBanner(ev.DY)
		}).OnClick(func() {
			if s.Model.News == state.NewsPresent && s.Model.NewsBanners.Count > 0 {
				s.Dispatch(state.Event{Kind: state.OpenNewsBanner})
				return
			}
			if s.Model.News != state.NewsPresent || s.Model.NewsRows.Empty() {
				s.Dispatch(state.Event{Kind: state.NewsRequested})
			}
		})
		s.bannerSelector(c)
		s.newsReading(c, tab)
		ui.Box(c).Fill().Radius(14).Border(1, hairline).PassThrough()
	})
}

func (s *Shell) newsReading(c *ui.Context, tab uint8) {
	tone := s.tone()
	ui.Column(c.Key("news-reading")).Label("公告分类").Absolute().Left(coverWidth).Right(0).Top(0).Bottom(0).Padding(10, 18, 10, 14).Gap(2).Children(func() {
		ui.Row(c).Height(34).Gap(2).Children(func() {
			for index, label := range state.NewsCategories {
				index, label := uint8(index), label
				selected := s.Model.NewsTab == index
				b := ui.ButtonBase(c.Key(label)).Label(label).Tooltip("切换公告分类").Height(34).Width(48).Justify(ui.Center).TextColor(muted).Role(ui.RoleTab).Selected(selected).Background(ui.Transparent)
				if selected || b.Hovered() {
					b.TextColor(paper)
				}
				b.Children(func() {
					ui.Text(c, label).FontSize(14).FontWeight(600)
					if selected {
						ui.Box(c).Absolute().Left(16).Right(16).Bottom(3).Height(3).Radius(1.5).Background(tone)
					}
				}).OnClick(func() { s.Dispatch(state.Event{Kind: state.SelectNewsTab, Index: index}) })
			}
			ui.Box(c).Grow(1)
			if s.Model.NewsFromCache && !s.Model.NewsRefreshing {
				b := ui.ButtonBase(c.Key("news-cache-retry")).Label("重新获取公告").Tooltip("当前显示离线缓存，点击重新获取公告").Height(30).Padding(0, 8).Radius(6).TextColor(muted)
				if b.Hovered() {
					b.Background(ui.RGBA(255, 255, 255, .07)).TextColor(paper)
				}
				b.Children(func() { ui.Text(c, "离线缓存").FontSize(11).SingleLine() }).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.NewsRequested})
				})
			} else if s.Model.NewsFromCache && s.Model.NewsRefreshing {
				ui.Text(c, "刷新中…").FontSize(11).TextColor(muted)
			}
		})
		ui.Box(c).Height(4)
		switch s.Model.News {
		case state.NewsUnavailable:
			b := ui.ButtonBase(c.Key("news-retry")).Label("重新获取公告").Tooltip("再次读取官网公开公告").FillWidth().Height(30).Radius(8).Padding(0, 10).Gap(10).TextColor(paper)
			if b.Hovered() {
				b.Background(ui.RGBA(255, 255, 255, .07))
			}
			b.Children(func() {
				ui.Box(c).TextColor(warning).Children(func() { glyph(c, "\uE72C", 13) })
				ui.Text(c, "重新获取公告").Grow(1).FontSize(12)
			}).OnClick(func() { s.Dispatch(state.Event{Kind: state.NewsRequested}) })
		case state.NewsPending:
			ui.Row(c).Height(30).Gap(10).Padding(0, 10).Children(func() {
				ui.Box(c).Size(6, 6).Radius(3).Background(tone)
				ui.Text(c, state.NewsLoadingDetail).FontSize(12).TextColor(muted).SingleLine()
			})
		default:
			rows := 0
			for item := uint8(0); item < 3; item++ {
				title, date, _, ok := s.Model.NewsRows.At(tab, item)
				if !ok {
					continue
				}
				rows++
				item := item
				b := ui.ButtonBase(c.Key(fmt.Sprintf("news-%d", item))).Label(title).Tooltip("打开官方公告详情").FillWidth().Height(30).Radius(8).Padding(0, 10).Gap(10).TextColor(ui.RGBA(245, 246, 247, .86))
				if b.Hovered() {
					b.TextColor(paper).Background(ui.RGBA(255, 255, 255, .07))
				}
				b.Children(func() {
					ui.Text(c, title).Grow(1).FontSize(13).SingleLine()
					mono(c, date, 11).TextColor(muted)
				}).OnClick(func() { s.Dispatch(state.Event{Kind: state.OpenOfficial, Index: item}) })
			}
			if rows == 0 {
				ui.Text(c, "暂无内容").FontSize(12).TextColor(ui.RGBA(164, 168, 177, .7)).Padding(0, 10).Height(30).SingleLine()
			}
		}
	})
}

// newsHeadline is the cover's one story, or why there is none to show.
func (s *Shell) newsHeadline() (headline, meta string) {
	switch s.Model.News {
	case state.NewsUnavailable:
		return state.NewsFailedHeadline, state.NewsFailedDetail
	case state.NewsPending:
		return state.NewsLoadingHeadline, state.NewsLoadingDetail
	}
	title, _, _, ok := s.Model.NewsBanners.At(s.Model.NewsIndex)
	if !ok {
		if s.Model.NewsRows.Empty() {
			return state.NewsEmptyHeadline, state.NewsEmptyDetail
		}
		return "官方公告", ""
	}
	return title, ""
}

func (s *Shell) newsTooltip() string {
	switch s.Model.News {
	case state.NewsUnavailable:
		return "获取失败，点击重新获取官方公告"
	case state.NewsPending:
		return "正在读取官网公开公告"
	}
	return "查看官方公告"
}

func flowGlyph(f state.Flow) string {
	switch f {
	case state.Downloading:
		return "\uE769"
	case state.NotInstalled, state.UpdateAvailable:
		return "\uE896"
	case state.Failed:
		return "\uE72C"
	case state.Checking, state.Verifying, state.Launching:
		return "\uE895"
	case state.Running:
		return "\uE73E"
	}
	return "\uE768"
}

// primary is the launch pill, filled with the art's own tone. An ink disc
// holds the state glyph; a slow sheen crosses it while it waits, and it
// lifts with a tinted glow under the pointer.
func (s *Shell) primary(c *ui.Context, p state.Presentation) {
	tone := s.tone()
	b := ui.ButtonBase(c.Key("primary")).Label(p.Primary).Size(actionWidth, actionHeight).
		Radius(actionHeight / 2).TextColor(ink).Disabled(!p.Enabled).DimDisabled(false).Clip().ZIndex(1)
	if p.Detail != "" {
		b.Tooltip(p.Detail)
	} else if p.Status != "" {
		b.Tooltip(p.Status)
	}
	fill, glow := tone, float32(.45)
	switch {
	case !p.Enabled:
		fill, glow = tone.Mix(ui.Hex("#6B6E76"), .55), 0
	case b.Pressed():
		fill, glow = tone.Mix(ink, .10), .2
	case b.Hovered() || b.FocusVisible():
		fill, glow = tone.Mix(ui.Hex("#FFFFFF"), .14), 1
	}
	if s.motion() {
		fill = b.AnimateColor("fill", fill, ui.Animation{Duration: 140 * time.Millisecond})
		glow = b.AnimateTo("glow", glow, ui.Animation{Duration: 200 * time.Millisecond})
	}
	b.Background(fill)
	if glow > .01 {
		b.Shadow(0, 8+4*glow, 18+16*glow, 0, tone.Alpha(.5*glow))
	}
	b.Children(func() {
		if s.motion() && p.Enabled && glow < .5 {
			phase := b.Loop("sheen", 6*time.Second, ui.Linear)
			ui.Box(c.Key("sheen")).Fill().PassThrough().Draw(func(pt *ui.Painter, r ui.Rect) {
				t := min(phase/.22, 1)
				if t >= 1 {
					return
				}
				x := r.X - 90 + (r.W+180)*t
				band := (&ui.Path{}).MoveTo(x, r.Y).LineTo(x+46, r.Y).LineTo(x+16, r.Y+r.H).LineTo(x-30, r.Y+r.H).Close()
				pt.FillPath(band, ui.RGBA(255, 255, 255, .26))
			})
		}
		ui.Row(c).Absolute().Left(10).Top(10).Size(40, 40).Radius(20).Background(ink).Justify(ui.Center).TextColor(tone).PassThrough().Children(func() {
			glyph(c, flowGlyph(s.Model.Flow), 17)
		})
		status := ui.RGBA(12, 13, 16, .62)
		if s.Model.Flow == state.Failed {
			status = ui.Hex("#9E1030")
		}
		ui.Column(c).Absolute().Left(62).Right(24).Top(0).Bottom(0).Justify(ui.Center).PassThrough().Children(func() {
			if p.Status != "" {
				ui.Text(c, p.Status).FontSize(11).FontWeight(600).TextColor(status).SingleLine()
			}
			ui.Text(c, p.Primary).FontSize(18).FontWeight(700).LetterSpacing(1).SingleLine()
		})
	}).OnClick(func() { s.Dispatch(state.Event{Kind: state.Primary}) })
}

// gameSettingsButton is the companion of the launch pill.
func (s *Shell) gameSettingsButton(c *ui.Context) {
	const label = "游戏设置"
	b := ui.ButtonBase(c.Key("game-settings-button")).Label(label).Tooltip(label).Absolute().Right(dockRight).Bottom(homeBottom).
		Size(actionHeight, actionHeight).Radius(actionHeight/2).Justify(ui.Center).TextColor(paper).Border(1, hairline).Disabled(!s.Model.NormalMode)
	if b.Hovered() || b.FocusVisible() {
		b.Material(frostedSurface{Tint: ui.RGBA(14, 15, 19, .78)}).Border(1, s.tone().Alpha(.6))
	} else {
		b.Material(frostedSurface{Tint: glassInk})
	}
	if b.Pressed() {
		b.Opacity(.82)
	}
	b.Children(func() {
		ui.Column(c).Gap(gameSettingsLabelGap).AlignItems(ui.Center).PassThrough().Children(func() {
			glyph(c, "\uE713", gameSettingsIconSize)
			ui.Text(c, label).FontSize(gameSettingsLabelSize).SingleLine()
		})
	}).OnClick(func() { s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.GameSettings}) })
}

func (s *Shell) demoNote(c *ui.Context) {
	if s.ManagedBackend != nil {
		if s.quietButton(c, "back-to-normal", "返回正常启动", false).Height(24).Clicked() {
			s.Dispatch(state.Event{Kind: state.ManagedResume})
		}
	}
	ui.Row(c).Gap(8).Children(func() {
		ui.Box(c).Size(6, 6).Radius(3).Background(warning)
		ui.Text(c, "MOCK · 交互演示").FontSize(10).TextColor(muted)
		ui.Box(c).Size(1, 8).Background(hairline)
		ui.Text(c, "不操作游戏文件").FontSize(10).TextColor(muted)
	})
}

func (s *Shell) footer(c *ui.Context) {
	ui.Row(c.Key("home-note")).Absolute().Left(homeLeft).Bottom(bezelWidth + 22).Height(14).Children(func() {
		if !s.Model.NormalMode && s.Model.Page != state.WishAnalysis {
			s.demoNote(c)
		}
	})
	ui.Row(c.Key("signature")).Absolute().Right(dockRight + 4).Bottom(bezelWidth + 22).Height(14).Gap(8).PassThrough().Children(func() {
		ui.Box(c).Size(6, 6).Radius(3).Background(s.tone())
		mono(c, "ODDWARD", 10).TextColor(ui.RGBA(245, 246, 247, .64))
	})
}
