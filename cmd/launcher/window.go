// SPDX-License-Identifier: GPL-3.0-or-later

package main

import "github.com/egoist/mygo"

// launcherWindowOptions leaves Content for main to bind to the native shell.
func launcherWindowOptions() mygo.WindowOptions {
	return mygo.WindowOptions{
		Title: "Oddward", Width: 1280, Height: 720,
		MinWidth: 1280, MinHeight: 720, MaxWidth: 1280, MaxHeight: 720,
		// These flags remove native resize/maximize affordances; equal bounds
		// alone would leave resizing edges and title-bar maximize behavior.
		DisableResize: true, DisableMaximize: true, DisableFullScreen: true,
		TitleBarStyle: mygo.TitleBarHidden, HideWindowButtons: true, BackgroundColor: "#0D1218",
	}
}
