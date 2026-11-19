// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"

	"github.com/egoist/mygo"

	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// installTray keeps Oddward reachable while the window is hidden. The icon is
// drawn here instead of shipped as artwork: the launcher must not depend on
// campaign media, and a generated ring stays legible on light and dark trays.
func installTray(w *mygo.Window, s *launcher.Shell) {
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "显示主界面", Click: func(*mygo.MenuItem, *mygo.Window) { showLauncher(w) }},
		mygo.Separator(),
		{Label: "退出 Oddward", Click: func(*mygo.MenuItem, *mygo.Window) { quitFromTray(s) }},
	})
	tray, err := mygo.NewTray(mygo.TrayOptions{ID: "oddward", Icon: trayIconPNG(), ToolTip: "Oddward", Menu: menu})
	if err != nil {
		log.Printf("tray unavailable: %v", err)
		return
	}
	tray.OnClick(func() { showLauncher(w) })
}

func showLauncher(w *mygo.Window) {
	w.Show()
	w.Focus()
}

// quitFromTray goes through the same reducer as the in-window exit so an
// in-flight download is stopped the same way.
func quitFromTray(s *launcher.Shell) {
	s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.ConfirmQuit})
	s.Dispatch(state.Event{Kind: state.ConfirmExit})
}

// trayIconPNG draws a 32 px ring in the launcher's default accent.
func trayIconPNG() []byte {
	const size = 32
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	accent := color.RGBA{R: 0xFF, G: 0xC8, B: 0x57, A: 0xFF}
	const center = size/2 - 0.5
	const outer, inner = 12.0, 8.0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// 3x3 supersampling keeps the ring smooth at tray sizes.
			var cover float64
			for sy := 0; sy < 3; sy++ {
				for sx := 0; sx < 3; sx++ {
					dx := float64(x) + (float64(sx)+0.5)/3 - center
					dy := float64(y) + (float64(sy)+0.5)/3 - center
					d := math.Hypot(dx, dy)
					if d >= inner && d <= outer {
						cover++
					}
				}
			}
			if cover == 0 {
				continue
			}
			c := accent
			c.A = uint8(math.Round(255 * cover / 9))
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}
