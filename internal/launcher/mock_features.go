// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// featurePage intentionally has no clock, account, filesystem or HTTP service.
// Every temporary state remains visible until its explicit mock outcome button.
func (s *Shell) featurePage(c *ui.Context) {
	page := state.CheckIn
	title, start, icon, kicker, numeral := "自动签到", "模拟签到", "\uE787", "03 / DAILY CHECK-IN", "03"
	current := s.Model.CheckIn
	tone := s.tone()
	// The page sits inside the same rounded canvas as the home art, dimmed to
	// a dark print so the content reads, with one tone bloom for colour.
	ui.Box(c.Key("feature-surface")).Absolute().Left(railWidth).Right(bezelWidth).Top(bezelWidth).Bottom(bezelWidth).
		Radius(canvasRadius).Clip().LinearGradient(ui.LinearGradient{Angle: 90, Stops: []ui.GradientStop{
		{Color: ui.RGBA(12, 13, 17, .94), At: 0}, {Color: ui.RGBA(12, 13, 17, .86), At: .55}, {Color: ui.RGBA(12, 13, 17, .64), At: 1},
	}}).Children(func() {
		ui.Box(c).Absolute().Right(-200).Bottom(-260).Size(720, 720).PassThrough().RadialGradient(ui.RadialGradient{Radius: 360, Stops: []ui.GradientStop{
			{Color: tone.Alpha(.16), At: 0}, {Color: tone.Alpha(.05), At: .55}, {Color: ui.Transparent, At: 1},
		}})
		// A ghost index numeral, filled softly in the tone, anchors the right field.
		ui.Text(c, numeral).Font(monoFont).FontSize(240).FontWeight(700).TextColor(tone.Alpha(.07)).
			Absolute().Right(48).Bottom(28).PassThrough()
		ui.Column(c).Absolute().Left(homeLeft - railWidth).Right(48).Top(64).Gap(10).Children(func() {
			ui.Row(c).Height(24).Padding(0, 10).Gap(7).Radius(12).AlignSelf(ui.Start).Background(ui.RGBA(255, 255, 255, .06)).Border(1, hairline).Children(func() {
				ui.Box(c).Size(6, 6).Radius(3).Background(tone)
				mono(c, kicker, 10).TextColor(muted)
			})
			ui.Text(c, title).FontSize(34).FontWeight(700).Margin(6, 0, 0, 0)
			ui.Row(c).Gap(8).Children(func() {
				ui.Box(c).Size(6, 6).Radius(3).Background(warning)
				ui.Text(c, "MOCK · 仅内存演示").FontSize(13).TextColor(muted)
			})
		})
		card := ui.Column(c.Key("feature-state")).Absolute().Left(homeLeft-railWidth).Top(214).Width(640).Padding(26, 28, 26, 28).Gap(16).
			Radius(20).Clip().Border(1, ui.RGBA(255, 255, 255, .10))
		card.Material(frostedSurface{Tint: glassInk})
		card.Children(func() {
			heading, description := "尚无示例结果", "手动预览签到流程；不会登录账号、设置定时任务或向任何服务提交签到。"
			switch current {
			case state.FeatureLoading:
				heading, description = "模拟处理中", "没有后台请求或倒计时。请选择模拟成功或模拟失败，手动检查下一种界面状态。"
			case state.FeatureReady:
				heading, description = "示例签到结果", "签到演示已完成。下方奖励仅为 MOCK 示例，没有执行或提交真实签到。"
			case state.FeatureFailed:
				heading, description = "模拟失败，未连接任何服务", "这是手动选择的失败示例，没有账号或文件受到影响。可以重试模拟，或重置为初始空状态。"
			}
			ui.Row(c).Height(40).Gap(14).Children(func() {
				iconTint, iconInk := tone.Alpha(.16), tone
				if current == state.FeatureFailed {
					iconTint, iconInk = ui.RGBA(229, 72, 77, .16), ui.Hex("#FF7A7E")
				}
				ui.Row(c).Size(40, 40).Radius(12).Background(iconTint).Justify(ui.Center).TextColor(iconInk).Children(func() {
					glyph(c, icon, 20)
				})
				ui.Text(c, heading).FontSize(20).FontWeight(600).TextColor(paper)
			})
			ui.Text(c, description).FontSize(14).TextColor(muted).LineHeight(1.7)
			if current == state.FeatureReady {
				ui.Row(c.Key("feature-example-results")).Gap(12).Children(func() {
					featureExample(c, "示例奖励", "+10 模拟积分")
					featureExample(c, "示例状态", "已模拟签到")
				})
			}
			ui.Box(c).FillWidth().Height(1).Background(hairline)
			ui.Row(c.Key("feature-actions")).Gap(10).Height(44).Children(func() {
				switch current {
				case state.FeatureEmpty:
					s.accentButton(c, "feature-start", start).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.StartFeature, Page: page})
					})
				case state.FeatureLoading:
					s.accentButton(c, "feature-complete", "模拟成功").OnClick(func() {
						s.Dispatch(state.Event{Kind: state.CompleteFeature, Page: page})
					})
					s.quietButton(c, "feature-fail", "模拟失败", false).Height(44).Radius(22).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.FailFeature, Page: page})
					})
				case state.FeatureFailed:
					s.accentButton(c, "feature-retry", "重试模拟").OnClick(func() {
						s.Dispatch(state.Event{Kind: state.StartFeature, Page: page})
					})
				case state.FeatureReady:
					ui.Row(c).Height(44).Gap(8).Children(func() {
						ui.Box(c).Size(6, 6).Radius(3).Background(tone)
						ui.Text(c, "示例已就绪 · 重置后可再次预览").FontSize(13).TextColor(muted)
					})
				}
				ui.Box(c).Grow(1)
				s.quietButton(c, "feature-reset", "重置预览", false).Height(44).Radius(22).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.ResetFeature, Page: page})
				})
				s.quietButton(c, "return-home", "返回游戏", false).Height(44).Radius(22).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.Navigate, Page: state.Home})
				})
			})
		})
	})
}
func featureExample(c *ui.Context, label, value string) {
	ui.Column(c).MinWidth(150).Padding(14, 18).Gap(6).Radius(14).Background(ui.RGBA(255, 255, 255, .05)).Border(1, hairline).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(muted)
		ui.Text(c, value).Font(monoFont).FontSize(26).FontWeight(600).TextColor(paper)
	})
}
