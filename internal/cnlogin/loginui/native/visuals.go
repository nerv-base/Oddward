// SPDX-License-Identifier: GPL-3.0-or-later

package native

import (
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/branding"
)

// Design dimensions and color tokens matched to the authored WPF LoginSurface
// and LoginVisuals specifications so every screen preserves Oddward's brand
// visual language with pure MyGo Native UI vectors and system fonts.
const (
	cardWidth       float32 = 420
	cardHeight      float32 = 520
	cardRadius      float32 = 18
	bezelWidth      float32 = 6
	frameRadius     float32 = 12
	headerHeight    float32 = 64
	headerSideWidth float32 = 104
	fieldWidth      float32 = 344
	fieldHeight     float32 = 52
	primaryHeight   float32 = 52
	secondaryHeight float32 = 44
	rememberHeight  float32 = 26
	footerHeight    float32 = 144
	footerInset     float32 = 18
	iconBtnSize     float32 = 44
	accountsMaxH    float32 = 164
	statusMaxH      float32 = 76
	bannerTopInset  float32 = 48
	bannerSideInset float32 = 24

	// Banner timing contract (docs/architecture.md & §7):
	// 220ms cubic-out fade in, 2400ms hold, 380ms cubic-in fade out.
	BannerFadeIn  = 220 * time.Millisecond
	BannerHold    = 2400 * time.Millisecond
	BannerFadeOut = 380 * time.Millisecond
	BannerTotal   = BannerFadeIn + BannerHold + BannerFadeOut

	loginFont    = "Microsoft YaHei UI"
	wordmarkFont = "Bahnschrift"
)

var (
	colHeader        = ui.Hex("#1E1F22")
	colBody          = ui.Hex("#E3E3E3")
	colStatus        = ui.Hex("#2B2C30")
	colInputBg       = ui.Hex("#FFFFFF")
	colInputBorder   = ui.Hex("#D0D1D3")
	colInputFocus    = ui.Hex("#55575C")
	colText          = ui.Hex("#2A2B2E")
	colMutedText     = ui.Hex("#8A8C90")
	colBezelTop      = ui.Hex("#4B4C51")
	colBezelBottom   = ui.Hex("#2A2B2F")
	colBezelBorder   = ui.RGBA(0x7A, 0x7B, 0x80, 0.40)
	colBannerBezel   = ui.Hex("#3A3B3F")
	colSeam          = ui.Hex("#B4B5B8")
	colWhite         = ui.Hex("#FFFFFF")
	colBlack         = ui.Hex("#000000")
	colTransparent   = ui.RGBA(0, 0, 0, 0)
	colPrimaryRingBg = ui.Hex("#F6F6F6")
	colPrimaryRingBd = ui.Hex("#D3D4D6")
	colPrimaryTop    = ui.Hex("#FDFDFD")
	colPrimaryBot    = ui.Hex("#EDEDED")
	colPrimaryBorder = ui.Hex("#8E9094")
	colPrimaryText   = ui.Hex("#1F2023")
	colPrimaryPress  = ui.Hex("#E2E2E2")
	colPrimaryDisBg  = ui.Hex("#D3D3D4")
	colPrimaryDisTxt = ui.Hex("#8B8C90")
	colSecondBg      = ui.Hex("#D4D4D5")
	colSecondHover   = ui.Hex("#CACACC")
	colSecondPress   = ui.Hex("#BEBFC1")
	colSecondText    = ui.Hex("#46484C")
	colLinkText      = ui.Hex("#3A3B3F")
	colLinkHoverBg   = ui.RGBA(0, 0, 0, 0.06)
	colLinkDisText   = ui.Hex("#75777B")
	colGhostHover    = ui.RGBA(255, 255, 255, 0.12)
	colGhostPress    = ui.RGBA(255, 255, 255, 0.20)
	colGhostFocus    = ui.RGBA(255, 255, 255, 0.80)
	colCheckRing     = ui.Hex("#9C9EA2")
	colCheckLabel    = ui.Hex("#5F6165")
	colAccBg         = ui.Hex("#26272B")
	colAccHover      = ui.Hex("#34353A")
	colAccSelText    = ui.Hex("#4A4C50")
	colAccAvatarBg   = ui.Hex("#3B3C41")
	colTagBorder     = ui.RGBA(255, 255, 255, 0.50)
	colTagText       = ui.RGBA(255, 255, 255, 0.80)
	colScrollbar     = ui.Hex("#A2A4A8")
	colSelection     = ui.Hex("#8E9094")
)

var (
	svgBrandMark = ui.MustParseSVG(branding.SymbolSVG)
	svgClose     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="#FFFFFF" d="M1,1 L12,8.6 L23,1 L15.4,12 L23,23 L12,15.4 L1,23 L8.6,12 Z"/></svg>`))
	svgBack      = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="none" stroke="#FFFFFF" stroke-width="3.6" stroke-linecap="round" stroke-linejoin="round" d="M15,3 L6,12 L15,21"/></svg>`))
	svgHelp      = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10" fill="none" stroke="#FFFFFF" stroke-width="2"/><path fill="none" stroke="#FFFFFF" stroke-width="2.1" stroke-linecap="round" d="M9.4,9.3 C9.7,7.7 10.9,6.8 12.2,6.8 C13.8,6.8 14.9,7.8 14.9,9.2 C14.9,10.5 13.9,11.2 12.9,11.9 C12.2,12.4 12,12.9 12,14"/><circle cx="12" cy="17.2" r="1.2" fill="#FFFFFF"/></svg>`))
	svgUser      = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="#FFFFFF" d="M12,12 A4.6,4.6 0 1 0 12,2.8 A4.6,4.6 0 1 0 12,12 Z M3.5,22 C3.5,17.2 7.3,14.2 12,14.2 C16.7,14.2 20.5,17.2 20.5,22 Z"/></svg>`))
	svgCheck     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 18 18"><path fill="none" stroke="#FFFFFF" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" d="M4.5,9.2 L7.6,12.2 L13.5,6.2"/></svg>`))
)

// OverlayMaskColor returns the backdrop mask color behind the login card.
// High contrast or disabled Windows transparency falls back to opaque black.
func OverlayMaskColor(highContrast, transparencyEnabled bool) ui.Color {
	if highContrast || !transparencyEnabled {
		return ui.Color{R: 0, G: 0, B: 0, A: 255}
	}
	return ui.Color{R: 0, G: 0, B: 0, A: 166}
}

// BannerColors returns the pill background, border and monochrome foreground
// for the welcome banner, respecting high contrast and reduced transparency.
func BannerColors(highContrast, transparencyEnabled bool) (bg, border, fg ui.Color) {
	if highContrast {
		return colBlack, colWhite, colWhite
	}
	alpha := uint8(235)
	if !transparencyEnabled {
		alpha = 255
	}
	return ui.Color{R: 0xE3, G: 0xE3, B: 0xE3, A: alpha}, colBannerBezel, colText
}

// OverlayCardScale computes the uniform zoom factor that fits the 420×520 card
// inside 90% of the client width and 80% of the client height, matching WPF's
// Viewbox layout.
func OverlayCardScale(clientW, clientH float32) float32 {
	if clientW <= 0 || clientH <= 0 {
		return 1
	}
	sx := (clientW * 0.9) / cardWidth
	sy := (clientH * 0.8) / cardHeight
	scale := sx
	if sy < scale {
		scale = sy
	}
	if scale < 0.25 {
		return 0.25
	}
	if scale > 5 {
		return 5
	}
	return scale
}

// BannerOpacity evaluates the welcome banner's opacity curve at elapsed time
// since first shown. Under reduceMotion, fade-in and fade-out are skipped and
// only the 2.4 s hold remains.
func BannerOpacity(elapsed time.Duration, reduceMotion bool) (alpha float32, done bool) {
	if elapsed < 0 {
		elapsed = 0
	}
	if reduceMotion {
		if elapsed >= BannerHold {
			return 0, true
		}
		return 1, false
	}
	switch {
	case elapsed < BannerFadeIn:
		t := float64(elapsed) / float64(BannerFadeIn)
		u := 1 - t
		return float32(1 - u*u*u), false
	case elapsed < BannerFadeIn+BannerHold:
		return 1, false
	case elapsed < BannerTotal:
		t := float64(elapsed-(BannerFadeIn+BannerHold)) / float64(BannerFadeOut)
		return float32(1 - t*t*t), false
	default:
		return 0, true
	}
}
