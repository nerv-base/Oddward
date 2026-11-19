// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// generalSection holds choices about running Oddward itself.
func (s *Shell) generalSection(c *ui.Context) {
	s.setting(c, "close-to-tray", "关闭窗口时最小化到托盘",
		"关闭后 Oddward 继续在后台运行；从托盘图标重新打开，或在托盘菜单里退出。", s.Model.CloseToTray, false, state.ToggleCloseToTray)
	s.setting(c, "check-updates", "启动时检查 Oddward 更新",
		"发现有新版本时在标题栏提示；关闭后可以在关于页手动检查。", s.Model.CheckUpdates, false, state.ToggleCheckUpdates)
}

// appearanceSection groups every visual preference. Artwork overrides stay
// unavailable here until choosing a local file is wired end to end.
func (s *Shell) appearanceSection(c *ui.Context) {
	s.setting(c, "motion-setting", "动态背景", "关闭后显示静态背景。", s.Model.Motion, s.Model.Media == state.MediaUnavailable, state.ToggleMotion)
	s.setting(c, "reduced-motion", "减少动态效果", "暂停背景播放和界面动画。", s.Model.ReducedMotion, false, state.ToggleReducedMotion)
	s.setting(c, "accent-follow", "强调色跟随版本主视觉", "关闭后使用 Oddward 的默认黄色。", s.Model.AccentFollow, false, state.ToggleAccentFollow)
	ui.Text(c, "自定义背景用启动参数 -background 指定视频、-background-still 指定静态图；也可以用 ODDWARD_BACKGROUND 环境变量。改动在下次启动时生效。").FontSize(12).TextColor(muted).LineHeight(1.6)
	if s.Model.MediaPreview {
		ui.Text(c, "MOCK · 媒体状态预览").FontSize(12).TextColor(muted)
	}
}

// filesSection exposes the folders Oddward writes to. Paths are read-only here:
// the settings dialog must never move user data behind the user's back.
func (s *Shell) filesSection(c *ui.Context) {
	folder := func(key, title, detail, path string) {
		ui.Column(c.Key(key)).FillWidth().Gap(6).Children(func() {
			ui.Text(c, title).FontSize(15).FontWeight(500)
			ui.Text(c, path).Font("Bahnschrift").FontSize(12).TextColor(muted).SingleLine()
			ui.Text(c, detail).FontSize(12).TextColor(muted).LineHeight(1.6)
			button := s.quietButton(c, key+"-open", "打开文件夹", s.OpenFolder == nil || path == "").Height(36)
			button.OnClick(func() {
				if s.OpenFolder != nil && path != "" {
					s.OpenFolder(path)
				}
			})
		})
		ui.Box(c).FillWidth().Height(1).Background(hairline).Margin(8, 0, 0, 0)
	}
	folder("data-folder", "设置与账号", "启动器设置、已保存账号和抽卡档案。", s.DataDir)
	game := s.Model.Managed.Root
	detail := "已定位的游戏安装目录。"
	if game == "" {
		detail = "还没有定位游戏；先在主页选择安装或定位。"
	}
	folder("game-folder", "游戏安装", detail, game)
}
