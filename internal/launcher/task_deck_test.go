// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func transferring(flow state.Flow, progress float64, x state.Transfer) state.Model {
	m := settled(flow)
	m.Progress, m.Transfer = progress, x
	return m
}

func TestProgressAcceptsOnlyCoherentTransferFigures(t *testing.T) {
	m, _ := state.Reduce(settled(state.NotInstalled), state.Event{Kind: state.Primary})
	if m.Flow != state.Downloading {
		t.Fatalf("expected download, got %v", m.Flow)
	}
	next, _ := state.Reduce(m, state.Event{Kind: state.Progressed, Token: m.Token, Progress: .5, Downloaded: 5 << 30, Total: 10 << 30, Rate: 20 << 20})
	if next.Transfer != (state.Transfer{Downloaded: 5 << 30, Total: 10 << 30, Rate: 20 << 20}) {
		t.Fatalf("transfer figures lost: %+v", next.Transfer)
	}
	for _, bad := range []state.Event{
		{Kind: state.Progressed, Token: next.Token, Progress: .6, Downloaded: 11 << 30, Total: 10 << 30, Rate: 1},
		{Kind: state.Progressed, Token: next.Token, Progress: .6, Downloaded: 6 << 30, Total: 10 << 30, Rate: -1},
		{Kind: state.Progressed, Token: next.Token + 1, Progress: .6, Downloaded: 6 << 30, Total: 10 << 30, Rate: 1},
	} {
		if got, _ := state.Reduce(next, bad); got.Transfer != next.Transfer {
			t.Fatalf("incoherent or stale figures accepted: %+v", got.Transfer)
		}
	}
	paused, _ := state.Reduce(next, state.Event{Kind: state.Primary})
	if paused.Flow != state.Paused || paused.Transfer.Rate != 0 || paused.Transfer.Downloaded != 5<<30 {
		t.Fatalf("pausing must keep bytes but drop the live rate: %+v", paused.Transfer)
	}
	fresh, _ := state.Reduce(settled(state.UpdateAvailable), state.Event{Kind: state.Primary})
	if fresh.Transfer != (state.Transfer{}) {
		t.Fatal("a new download must not inherit old figures")
	}
}

func TestTaskDeckGivesTheTransferItsOwnRoom(t *testing.T) {
	for _, size := range [][2]int{{1024, 631}, {1280, 720}, {1600, 900}} {
		m := transferring(state.Downloading, .42, state.Transfer{Downloaded: 6 << 30, Total: 14 << 30, Rate: 18 << 20})
		s := NewShell(m)
		tt := ui.NewTester(s.View, size[0], size[1])
		cta, _ := tt.Find("暂停下载")
		news, _ := tt.Find("官方公告")
		bar, ok := tt.AccessByRole(ui.RoleProgress, "任务进度")
		if !ok || bar.Now != .42 {
			t.Fatalf("native progress missing: %+v", bar)
		}
		right := float32(size[0]) - dockRight
		if bar.Bounds.Y+bar.Bounds.H > cta.Y-8 || bar.Bounds.X < cta.X || bar.Bounds.X+bar.Bounds.W > right {
			t.Fatalf("progress belongs to a deck stacked on the dock, not inside the pill: bar %+v cta %+v", bar.Bounds, cta)
		}
		if bar.Bounds.W < dockWidth-48 || bar.Bounds.H < 3 {
			t.Fatalf("progress must span the deck: %+v", bar.Bounds)
		}
		if bar.Bounds.X < news.X+news.W+24 {
			t.Fatalf("deck crowds the news card: %+v %+v", bar.Bounds, news)
		}
		for _, text := range []string{"42%", "18.0 MB/s", "剩余 07:35", "6.0 / 14.0 GB"} {
			if !tt.HasText(text) {
				t.Fatalf("deck at %v missing %q", size, text)
			}
		}
		s.Dispose()
	}
}

func TestTaskDeckNeverInventsFigures(t *testing.T) {
	s := NewShell(transferring(state.Downloading, .42, state.Transfer{}))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if tt.HasText("MB/s") || tt.HasText("剩余") || tt.HasText("GB") {
		t.Fatal("a transfer without reported figures must not show speed, time or size")
	}
	if !tt.HasText(state.Present(s.Model).Detail) {
		t.Fatal("without figures the deck explains the transfer honestly")
	}
	paused := NewShell(transferring(state.Paused, .42, state.Transfer{Downloaded: 6 << 30, Total: 14 << 30}))
	defer paused.Dispose()
	pt := ui.NewTester(paused.View, 1280, 720)
	if pt.HasText("MB/s") || pt.HasText("剩余") || !pt.HasText("6.0 / 14.0 GB") || !pt.HasText("已暂停") {
		t.Fatal("a paused transfer shows kept bytes, never a live rate")
	}
}

func TestPillStaysCleanWhileATaskRuns(t *testing.T) {
	for _, flow := range []state.Flow{state.Downloading, state.Paused, state.Verifying} {
		s := NewShell(transferring(flow, .5, state.Transfer{}))
		tt := ui.NewTester(s.View, 1280, 720)
		p := state.Present(s.Model)
		cta, _ := tt.Find(p.Primary)
		ready := NewShell(settled(state.Ready))
		rt := ui.NewTester(ready.View, 1280, 720)
		rcta, _ := rt.Find("启动游戏")
		if cta != rcta {
			t.Fatalf("flow %v pill moved: %+v vs %+v", flow, cta, rcta)
		}
		// The caption keeps its resting column: ink starts at X+62 below the status.
		img := tt.Image()
		left := int(cta.X + cta.W)
		for y := int(cta.Y + 30); y < int(cta.Y+cta.H-10); y++ {
			for x := int(cta.X + 56); x < int(cta.X+cta.W-20); x++ {
				if px := img.RGBAAt(x, y); px.R < 100 && px.G < 100 && px.B < 100 {
					left = min(left, x)
				}
			}
		}
		if math.Abs(float64(left)-float64(cta.X+62)) > 3 {
			t.Fatalf("flow %v caption drifted while a task runs: %d", flow, left)
		}
		s.Dispose()
		ready.Dispose()
	}
}
