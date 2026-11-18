// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// NetworkForm is the network panel's unsaved edit.
type NetworkForm struct {
	Proxy        uint8
	ProxyURL     string
	Mirror       bool
	MirrorPrefix string
}

func networkForm(n state.NetworkState) NetworkForm {
	return NetworkForm{Proxy: n.Proxy, ProxyURL: n.ProxyURL, Mirror: n.Mirror, MirrorPrefix: n.MirrorPrefix}
}

func (s *Shell) selectSettingsSection(section state.LauncherSettingsSection) {
	s.Dispatch(state.Event{Kind: state.SelectLauncherSettingsSection, Index: uint8(section)})
}

func (s *Shell) networkPanel(c *ui.Context) {
	n, form := s.Model.Network, &s.NetworkForm
	input := func(key, label, placeholder string, value *string, disabled bool) {
		ui.TextInput(c.Key(key), value).Label(label).FillWidth().Height(38).Padding(8, 10).Radius(10).Border(1, hairline).
			Background(ui.RGBA(255, 255, 255, .05)).TextColor(paper).FontSize(13).Placeholder(placeholder).Disabled(disabled || n.Saving)
	}
	ui.Column(c.Key("network-content")).FillWidth().Gap(12).Children(func() {
		ui.Text(c, "代理").FontSize(15).FontWeight(500)
		ui.Row(c.Key("proxy-mode")).FillWidth().Gap(4).Padding(4, 4).Radius(14).Background(ui.RGBA(255, 255, 255, .05)).Border(1, hairline).Role(ui.RoleTabList).Label("代理方式").Children(func() {
			for i, label := range []string{"跟随系统", "直连", "自定义"} {
				mode := uint8(i)
				b := ui.ButtonBase(c.Key("proxy-" + label)).Label(label).Role(ui.RoleTab).Selected(form.Proxy == mode).Grow(1).Height(32).Radius(10).Justify(ui.Center).TextColor(muted).Disabled(n.Saving)
				switch {
				case form.Proxy == mode:
					b.Background(s.tone()).TextColor(ink)
				case b.Hovered() || b.FocusVisible():
					b.Background(ui.RGBA(255, 255, 255, .08)).TextColor(paper)
				}
				b.Children(func() { ui.Text(c, label).FontSize(13).FontWeight(600) }).OnClick(func() { form.Proxy = mode })
			}
		})
		switch form.Proxy {
		case state.ProxySystem:
			ui.Text(c, "使用 Windows 手动代理设置。").FontSize(12).TextColor(muted).LineHeight(1.6)
		case state.ProxyDirect:
			ui.Text(c, "所有请求直接连接，忽略系统代理。").FontSize(12).TextColor(muted).LineHeight(1.6)
		case state.ProxyCustom:
			input("proxy-url", "代理地址", "http://127.0.0.1:7890 或 socks5://127.0.0.1:1080", &form.ProxyURL, false)
		}
		ui.Box(c).FillWidth().Height(1).Background(hairline).Margin(4, 0, 0, 0)
		ui.Row(c).FillWidth().Height(36).Children(func() {
			caption := ui.Text(c, "GitHub 加速").FontSize(15).FontWeight(500).Grow(1)
			s.formSwitch(c, "mirror-setting", caption, &form.Mirror, n.Saving)
		})
		ui.Text(c, "通过第三方镜像下载启动器更新。").FontSize(12).TextColor(muted).LineHeight(1.6)
		input("mirror-prefix", "镜像前缀", "https://gh-proxy.com/", &form.MirrorPrefix, !form.Mirror)
		switch {
		case n.Error != "":
			ui.Text(c, n.Error).FontSize(12).TextColor(warning).LineHeight(1.5)
		case n.Saved:
			ui.Text(c, "已保存").FontSize(12).TextColor(s.tone())
		}
		label := "保存"
		if n.Saving {
			label = "正在保存…"
		}
		b := s.accentButton(c, "network-save", label).FillWidth()
		if n.Saving {
			b.Disabled(true)
		}
		b.OnClick(func() {
			s.Dispatch(state.Event{Kind: state.NetworkApply, Index: form.Proxy, Path: form.ProxyURL, Enabled: form.Mirror, Message: form.MirrorPrefix})
		})
	})
}

// formSwitch edits a form field in place; nothing reaches the model until
// the form is saved.
func (s *Shell) formSwitch(c *ui.Context, key string, caption ui.Element, on *bool, disabled bool) {
	b := ui.SwitchBase(c.Key(key), on).LabelledBy(caption).Size(48, 28).Radius(14).Disabled(disabled)
	x, fill, knob := float32(3), ui.RGBA(255, 255, 255, .10), muted
	if *on {
		x, fill, knob = 23, s.tone(), ink
	}
	if b.Hovered() && !disabled && !*on {
		fill = ui.RGBA(255, 255, 255, .16)
	}
	s.controlFill(b, fill)
	b.Border(1, hairline).Children(func() {
		ui.Box(c).Absolute().Left(x).Top(3).Size(22, 22).Radius(11).Background(knob)
	})
}
