// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestMediaBarStateIsIndependentAndGraceIsCancelable(t *testing.T) {
	now := time.Unix(1000, 0)
	m := settled(state.Paused)
	m.Progress = .38
	original := m
	m, fx := state.Reduce(m, state.Event{Kind: state.MediaBarActivity, Enabled: true, Now: now})
	if m.Bar != state.MediaBarVisible || len(fx) != 0 {
		t.Fatal("bottom edge must reveal media bar without effects")
	}
	if m.Flow != original.Flow || m.Token != original.Token || m.Progress != .38 || m.Motion != original.Motion {
		t.Fatal("hover must not touch task or playback")
	}
	m, _ = state.Reduce(m, state.Event{Kind: state.MediaBarActivity, Now: now})
	if m.Bar != state.MediaBarLeaving || m.BarDeadline.IsZero() {
		t.Fatal("pointer leave needs a grace period, not immediate disappearance")
	}
	m, _ = state.Reduce(m, state.Event{Kind: state.MediaBarActivity, Now: now.Add(300 * time.Millisecond)})
	if m.Bar != state.MediaBarLeaving {
		t.Fatal("bar vanished while pointer crosses toward it")
	}
	m, _ = state.Reduce(m, state.Event{Kind: state.MediaBarActivity, Focused: true, Now: now.Add(time.Second)})
	if m.Bar != state.MediaBarVisible || !m.BarDeadline.IsZero() {
		t.Fatal("keyboard focus must cancel hiding")
	}
	m, _ = state.Reduce(m, state.Event{Kind: state.MediaBarActivity, Now: now.Add(2 * time.Second)})
	m, _ = state.Reduce(m, state.Event{Kind: state.MediaBarActivity, Now: now.Add(3 * time.Second)})
	if m.Bar != state.MediaBarHidden {
		t.Fatal("bar must hide after pointer and focus leave")
	}
}

func TestMediaBarEscapeAndModalDoNotImmediatelyReopen(t *testing.T) {
	m, _ := state.Reduce(settled(state.Ready), state.Event{Kind: state.RevealMediaBar})
	if m.Bar != state.MediaBarVisible {
		t.Fatal("keyboard reveal missing")
	}
	m, _ = state.Reduce(m, state.Event{Kind: state.HideMediaBar})
	m, _ = state.Reduce(m, state.Event{Kind: state.MediaBarActivity, Enabled: true})
	if m.Bar != state.MediaBarDismissed {
		t.Fatal("Escape must win until pointer leaves")
	}
	m, _ = state.Reduce(m, state.Event{Kind: state.MediaBarActivity})
	m, _ = state.Reduce(m, state.Event{Kind: state.MediaBarActivity, Enabled: true})
	if m.Bar != state.MediaBarVisible {
		t.Fatal("next genuine pointer enter must work")
	}
	m, _ = state.Reduce(m, state.Event{Kind: state.Navigate, Page: state.LauncherSettings})
	m, _ = state.Reduce(m, state.Event{Kind: state.RevealMediaBar})
	if m.Bar != state.MediaBarHidden {
		t.Fatal("launcher settings must suppress the home media bar")
	}
}

func TestNativeMediaBarHoverFocusAndPlayback(t *testing.T) {
	s := NewShell(send(settled(state.Ready), state.MediaReady))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1024, 631)
	if _, ok := tt.AccessByRole(ui.RoleButton, "暂停动态背景"); ok {
		t.Fatal("background control must not stay on the campaign")
	}
	tt.Move(600, 629)
	if _, ok := tt.AccessByRole(ui.RoleButton, "暂停动态背景"); !ok {
		t.Fatal("bottom hover did not reveal playback")
	}
	tt.Advance(250 * time.Millisecond)
	r, _ := tt.Find("暂停动态背景")
	if r.Y < 571 || r.X < 350 || r.X+r.W > 720 {
		t.Fatalf("playback must stay in the centered bottom console group: %+v", r)
	}
	if clickFailed(tt, "暂停动态背景") || s.Model.Media != state.MediaPaused {
		t.Fatal("bar must pause through reducer")
	}
	tt.Move(500, 300)
	tt.Advance(2 * time.Second)
	// A clicked control owns focus: do not hide it from keyboard users.
	if s.Model.Bar != state.MediaBarVisible {
		t.Fatal("focused playback must stay reachable")
	}
	tt.Key(0, ui.KeyEscape)
	if _, ok := tt.AccessByRole(ui.RoleButton, "播放动态背景"); ok {
		t.Fatal("Escape must remove hidden controls from access tree")
	}
	tt.Key(0, ui.KeyF6)
	tt.Advance(250 * time.Millisecond)
	if _, ok := tt.AccessByRole(ui.RoleButton, "播放动态背景"); !ok {
		t.Fatal("F6 must expose media controls without mouse")
	}
	tt.Key(0, ui.KeyTab)
	tt.Key(0, ui.KeySpace)
	if s.Model.Media != state.MediaPlaying {
		t.Fatal("keyboard playback is inaccessible")
	}
}

func TestNativeMediaBarHidesAfterMouseLeavesAndYieldsToModal(t *testing.T) {
	s := NewShell(send(settled(state.Ready), state.MediaReady))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Move(600, 719)
	tt.Move(600, 400)
	tt.Advance(2 * time.Second)
	if _, ok := tt.AccessByRole(ui.RoleButton, "暂停动态背景"); ok {
		t.Fatal("idle bar still exposed")
	}
	tt.Move(600, 719)
	tt.Key(0, ui.KeyF6)
	if clickFailed(tt, "启动器设置") {
		t.Fatal("bar intercepts unrelated navigation")
	}
	tt.Key(0, ui.KeyF6)
	if _, ok := tt.AccessByRole(ui.RoleButton, "暂停动态背景"); ok {
		t.Fatal("F6 must not escape settings modal")
	}
}

func TestNativePosterIsVisibleBeforeVideoAndOnFailure(t *testing.T) {
	for _, media := range []state.Media{state.MediaLoading, state.MediaUnavailable} {
		s := NewShell(settled(state.Ready))
		t.Cleanup(s.Dispose)
		s.Model.Media = media
		img := image.NewRGBA(image.Rect(0, 0, 16, 9))
		for y := 0; y < 9; y++ {
			for x := 0; x < 16; x++ {
				img.SetRGBA(x, y, color.RGBA{R: 230, G: 30, B: 70, A: 255})
			}
		}
		s.Poster = ui.NewBitmap(img)
		tt := ui.NewTester(s.View, 1280, 720)
		r, g, _, _ := tt.Image().At(700, 320).RGBA()
		if r < 50000 || g > 12000 {
			t.Fatalf("poster must cover the stage during %v, got %d/%d", media, r, g)
		}
	}
	if !state.Initial().Motion {
		t.Fatal("video must be on by default")
	}
}

// The system decoder becomes Ready only when ui.Video advances it. Mount it
// during Loading, but keep the poster above it until readiness is projected.
func TestNativeLoadingKeepsVideoMountedUnderPoster(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	s.Model.Media = state.MediaLoading
	img := image.NewRGBA(image.Rect(0, 0, 16, 9))
	for y := 0; y < 9; y++ {
		for x := 0; x < 16; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 230, G: 30, B: 70, A: 255})
		}
	}
	s.Poster = ui.NewBitmap(img)
	videoImg := image.NewRGBA(image.Rect(0, 0, 16, 9))
	for y := 0; y < 9; y++ {
		for x := 0; x < 16; x++ {
			videoImg.SetRGBA(x, y, color.RGBA{B: 255, A: 255})
		}
	}
	p, err := ui.NewVideoPlayer(&posterProbeDecoder{frame: videoImg})
	if err != nil {
		t.Fatal(err)
	}
	s.Player = p
	// Build the stage before tick has projected the decoder's readiness.
	tt := ui.NewTester(s.stage, 1280, 720)
	if _, ok := tt.Find("背景视频"); !ok {
		t.Fatal("loading must keep the native video mounted so its decoder can advance")
	}
	r, g, _, _ := tt.Image().At(700, 320).RGBA()
	if r < 50000 || g > 12000 {
		t.Fatal("loading video must stay behind the poster")
	}
}

type posterProbeDecoder struct{ frame image.Image }

func (d *posterProbeDecoder) Next() (ui.VideoFrame, error) {
	return ui.VideoFrame{Image: d.frame, Duration: time.Second}, nil
}
func (d *posterProbeDecoder) Rewind() error { return nil }

func TestNativePrimaryReturnsToCapsuleShape(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1280, 720)
	r, _ := tt.Find("启动游戏")
	red, green, _, _ := tt.Image().At(int(r.X+9), int(r.Y+3)).RGBA()
	if red > 50000 && green > 40000 {
		t.Fatal("primary still has shallow rectangle corners instead of a capsule")
	}
}
