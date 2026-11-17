// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"testing"

	"github.com/egoist/mygo"
)

// Inspect the options without creating a native window or starting the app.
func TestLauncherWindowOptionsFixedSize(t *testing.T) {
	o := launcherWindowOptions()
	if o.Width != 1280 || o.Height != 720 {
		t.Errorf("initial window = %dx%d, want 1280x720 DIPs", o.Width, o.Height)
	}
	if o.MinWidth != 1280 || o.MinHeight != 720 || o.MaxWidth != 1280 || o.MaxHeight != 720 {
		t.Errorf("window bounds = min %dx%d, max %dx%d; want both 1280x720 DIPs", o.MinWidth, o.MinHeight, o.MaxWidth, o.MaxHeight)
	}
	if !o.DisableResize {
		t.Error("DisableResize must remove native resizing edges, not merely constrain min/max size")
	}
	if !o.DisableMaximize {
		t.Error("DisableMaximize must disallow native maximize and title-bar double-click maximize")
	}
	if !o.DisableFullScreen || o.FullScreen || o.Maximized {
		t.Errorf("fixed window must not enter full screen or start maximized: DisableFullScreen=%v, FullScreen=%v, Maximized=%v", o.DisableFullScreen, o.FullScreen, o.Maximized)
	}
	if o.UseContentSize || o.FitContent || o.StateKey != "" {
		t.Errorf("fixed outer size must not be replaced by content or restored bounds: UseContentSize=%v, FitContent=%v, StateKey=%q", o.UseContentSize, o.FitContent, o.StateKey)
	}
	if o.DisableMove || o.DisableMinimize || o.DisableClose {
		t.Error("fixed size must preserve normal moving, minimizing and closing")
	}
	if o.Title != "Oddward · 异环" || o.TitleBarStyle != mygo.TitleBarHidden || !o.HideWindowButtons || o.BackgroundColor != "#0D1218" {
		t.Errorf("native launcher chrome changed unexpectedly: Title=%q, TitleBarStyle=%q, HideWindowButtons=%v, BackgroundColor=%q", o.Title, o.TitleBarStyle, o.HideWindowButtons, o.BackgroundColor)
	}
	if o.Content != nil {
		t.Error("Content must remain unset for main to attach the native shell")
	}
}
