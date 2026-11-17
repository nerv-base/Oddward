// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestAboutExplainsNativeLoginAndLocalRecords(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Model.Overlay = state.About
	tt := ui.NewTester(s.View, 1024, 631)
	tt.Frame()
	if !tt.HasText("抽卡页物品图标运行时取自塔吉多官方 CDN，版权归原权利人；Oddward 不分发这些图片") {
		t.Fatal("About must credit runtime wish icons without implying bundled media")
	}
	if !tt.HasText("公告使用官方公开数据；抽卡分析使用本地档案，可导入和导出 JSON。启动器内置国服原生登录流程和登录窗口，首次启动需确认风险；签到仍为演示。") {
		t.Fatal("About must describe native login and local records")
	}
}
