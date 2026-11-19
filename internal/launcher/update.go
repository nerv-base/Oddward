// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// UpdateOffer is what a check found.
type UpdateOffer struct {
	Version, Notes string
	Size           int64
	Newer          bool
}

// UpdateBackend checks for and stages the launcher's own updates. Stage
// works on the offer the last Check returned. Calls run off the UI thread
// except Restart, which only starts the stub.
type UpdateBackend interface {
	Check(ctx context.Context) (UpdateOffer, error)
	Stage(ctx context.Context, progress func(done, total int64)) error
	Restart() error
	SaveSkipped(ctx context.Context, version string) error
}

// NetworkBackend validates, applies and persists the network preference.
type NetworkBackend interface {
	Apply(ctx context.Context, n state.NetworkState) error
}

// startWork runs fn off the UI thread and hands its event back through out,
// which only the UI thread drains.
func (s *Shell) startWork(out chan state.Event, fn func(ctx context.Context) state.Event) {
	ctx, wake := s.ctx, s.RequestFrame
	go func() {
		ev := fn(ctx)
		select {
		case out <- ev:
			if wake != nil && ctx.Err() == nil {
				wake()
			}
		case <-ctx.Done():
		}
	}()
}

func (s *Shell) applyUpdateEffect(f state.Effect) bool {
	switch f.Kind {
	case state.CheckForUpdate:
		if b := s.UpdateBackend; b != nil {
			s.startWork(s.workOut, func(ctx context.Context) state.Event {
				o, err := b.Check(ctx)
				if err != nil {
					return state.Event{Kind: state.UpdateCheckFailed, Token: f.Token, Message: state.BriefError(err)}
				}
				return state.Event{Kind: state.UpdateChecked, Token: f.Token, Message: o.Version, Body: o.Notes, Total: o.Size, Enabled: o.Newer}
			})
		}
	case state.StageUpdate:
		if b := s.UpdateBackend; b != nil {
			progress, wake := s.updateProgress, s.RequestFrame
			s.startWork(s.workOut, func(ctx context.Context) state.Event {
				err := b.Stage(ctx, func(done, total int64) {
					ev := state.Event{Kind: state.UpdateProgress, Token: f.Token, Downloaded: done, Total: total}
					// Keep only the newest report; the UI reads it next frame.
					select {
					case <-progress:
					default:
					}
					select {
					case progress <- ev:
						if wake != nil {
							wake()
						}
					default:
					}
				})
				if err != nil {
					return state.Event{Kind: state.UpdateStageFailed, Token: f.Token, Message: state.BriefError(err)}
				}
				return state.Event{Kind: state.UpdateStaged, Token: f.Token}
			})
		}
	case state.SaveSkippedVersion:
		if b := s.UpdateBackend; b != nil {
			ctx, version := s.ctx, f.Path
			go b.SaveSkipped(ctx, version) // best effort: a lost skip only means one more prompt
		}
	case state.RestartForUpdate:
		if b := s.UpdateBackend; b != nil {
			if err := b.Restart(); err != nil {
				s.Dispatch(state.Event{Kind: state.UpdateRestarted, Message: "无法启动新版本：" + state.BriefError(err)})
			} else {
				s.Dispatch(state.Event{Kind: state.UpdateRestarted})
			}
		}
	case state.SaveNetwork:
		if b := s.NetworkBackend; b != nil {
			n := s.Model.Network
			s.startWork(s.workOut, func(ctx context.Context) state.Event {
				if err := b.Apply(ctx, n); err != nil {
					return state.Event{Kind: state.NetworkSaveFailed, Token: f.Token, Message: err.Error()}
				}
				return state.Event{Kind: state.NetworkSaved, Token: f.Token}
			})
		} else {
			s.Dispatch(state.Event{Kind: state.NetworkSaved, Token: f.Token})
		}
	default:
		return false
	}
	return true
}

// syncUpdate runs on the UI thread from tick.
func (s *Shell) syncUpdate() {
	for {
		// Progress reports are drained ahead of completion events: the staging
		// worker sends its last byte count and its "staged" result back to back,
		// and the ready transition must not win the race and drop that count.
		for {
			select {
			case ev := <-s.updateProgress:
				s.Dispatch(ev)
				continue
			default:
			}
			break
		}
		select {
		case ev := <-s.workOut:
			s.Dispatch(ev)
			continue
		default:
		}
		break
	}
	if s.UpdateBackend != nil && s.Model.CheckUpdates && !s.updateAutoChecked && s.Model.Update.Phase == state.SelfUpdateIdle {
		s.updateAutoChecked = true
		s.Dispatch(state.Event{Kind: state.UpdateCheck})
	}
}

func megabytes(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }

func updatePercent(u state.UpdateState) int {
	if u.Size <= 0 {
		return 0
	}
	return int(u.Downloaded * 100 / u.Size)
}

// updatePill sits left of the caption buttons while a release waits.
func (s *Shell) updatePill(c *ui.Context) {
	u := s.Model.Update
	if !state.UpdateOffered(u.Phase) {
		return
	}
	label, icon := "Oddward 新版本 v"+u.Version, "\uE896"
	switch u.Phase {
	case state.SelfUpdateDownloading:
		label = fmt.Sprintf("正在下载 %d%%", updatePercent(u))
	case state.SelfUpdateReady:
		label, icon = "重启以更新", "\uE72C"
	}
	b := ui.ButtonBase(c.Key("update-pill")).Label(label).Tooltip("查看 Oddward 更新").Height(captionSize).Radius(captionSize/2).Padding(0, 12).Gap(6).
		Background(s.tone()).TextColor(ink).Shadow(0, 4, 14, 0, s.tone().Alpha(.35))
	if b.Hovered() || b.FocusVisible() {
		b.Background(s.tone().Mix(paper, .18)).Shadow(0, 4, 18, 0, s.tone().Alpha(.6))
	}
	if b.Pressed() {
		b.Opacity(.86)
	}
	b.Children(func() {
		glyph(c, icon, 12)
		ui.Text(c, label).FontSize(12).FontWeight(600).SingleLine()
	}).OnClick(func() { s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.UpdateDetails}) })
}

func (s *Shell) updatePanel(c *ui.Context) {
	u := s.Model.Update
	ui.Column(c.Key("update-content")).Grow(1).MinHeight(0).FillWidth().Gap(12).Children(func() {
		ui.Row(c).Gap(10).Children(func() {
			ui.Text(c, "v"+u.Version).Font("Bahnschrift").FontSize(30).FontWeight(600).TextColor(s.tone())
			ui.Text(c, "当前 v"+u.Current+" · "+megabytes(u.Size)).FontSize(12).TextColor(muted).Margin(12, 0, 0, 0)
		})
		ui.Text(c, "更新说明").FontSize(13).TextColor(muted)
		notes := u.Notes
		if notes == "" {
			notes = "这个版本没有附带说明。"
		}
		ui.Scroll(c.Key("update-notes")).Grow(1).MinHeight(60).Scrollbars(true).Children(func() {
			ui.Text(c, notes).FontSize(14).LineHeight(1.7)
		})
		ui.Box(c).FillWidth().Height(1).Background(hairline)
		switch u.Phase {
		case state.SelfUpdateDownloading:
			ui.Row(c).FillWidth().Children(func() {
				ui.Text(c, "正在下载并校验").FontSize(13).Grow(1)
				mono(c, fmt.Sprintf("%d%%", updatePercent(u)), 13)
			})
			ui.Progress(c.Key("update-progress"), float64(updatePercent(u))/100).Label("更新下载进度").FillWidth().Height(4).MinHeight(4)
			ui.Text(c, megabytes(u.Downloaded)+" / "+megabytes(u.Size)+" · 当前版本不受影响").FontSize(12).TextColor(muted)
		case state.SelfUpdateReady:
			ui.Text(c, "新版本已就绪，重启后生效。旧版本会保留一份，以便出错时回退。").FontSize(13).TextColor(muted).LineHeight(1.6)
		}
		if u.Error != "" {
			ui.Text(c, u.Error).FontSize(12).TextColor(warning).LineHeight(1.5)
		}
		blocked := state.UpdateBlocked(s.Model)
		if u.Phase == state.SelfUpdateReady && blocked {
			ui.Text(c, "游戏或登录组件仍在运行，结束后再重启更新。").FontSize(12).TextColor(warning).LineHeight(1.5)
		}
		switch u.Phase {
		case state.SelfUpdateAvailable:
			s.accentButton(c, "update-now", "立即更新").Height(46).Radius(23).FillWidth().AutoFocus().OnClick(func() {
				s.Dispatch(state.Event{Kind: state.UpdateDownload})
			})
		case state.SelfUpdateReady:
			if blocked {
				// A dimmed tone pill reads as broken; a quiet disabled pill
				// keeps the label legible next to the warning above.
				s.quietButton(c, "update-restart", "重启并更新", true).Height(46).Radius(23).FillWidth()
				break
			}
			s.accentButton(c, "update-restart", "重启并更新").Height(46).Radius(23).FillWidth().OnClick(func() {
				s.Dispatch(state.Event{Kind: state.UpdateRestart})
			})
		}
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			s.quietButton(c, "update-later", "稍后", false).Grow(1).Height(40).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.Dismiss})
			})
			if u.Phase == state.SelfUpdateAvailable {
				s.quietButton(c, "update-skip", "跳过此版本", false).Grow(1).Height(40).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.UpdateSkip})
				})
			}
		})
	})
}

// versionSetting is the settings row with the version and a manual check.
func (s *Shell) versionSetting(c *ui.Context) {
	u := s.Model.Update
	version, status := "开发版本", ""
	if u.Current != "" {
		version = "v" + u.Current
	}
	switch u.Phase {
	case state.SelfUpdateIdle:
		status = "尚未检查更新"
	case state.SelfUpdateChecking:
		status = "正在检查更新…"
	case state.SelfUpdateCurrent:
		status = "已是最新版本"
	case state.SelfUpdateFailed:
		status = u.Error
	case state.SelfUpdateAvailable, state.SelfUpdateDownloading, state.SelfUpdateReady:
		status = "发现新版本 v" + u.Version
	}
	ui.Row(c.Key("version-setting")).FillWidth().Gap(10).Children(func() {
		ui.Column(c).Grow(1).Gap(3).Children(func() {
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, "当前版本").FontSize(14)
				ui.Text(c, version).FontSize(13).TextColor(muted)
			})
			if status != "" {
				ui.Text(c, status).FontSize(12).TextColor(muted).LineHeight(1.5)
			}
		})
		if state.UpdateOffered(u.Phase) {
			s.quietButton(c, "open-update", "查看更新", false).Height(32).Padding(0, 12).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.UpdateDetails})
			})
		} else {
			busy := u.Phase == state.SelfUpdateUnsupported || u.Phase == state.SelfUpdateChecking
			s.quietButton(c, "check-update", "检查更新", busy).Height(32).Padding(0, 12).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.UpdateCheck, Enabled: true})
			})
		}
	})
}
