# Oddward

Windows《异环》非官方启动器，使用 Go 与 MyGo Native UI 构建。

Oddward 提供国服官服登录、账号管理、官方公告和抽卡档案。主界面为原生窗口，不使用 WebView 或 Electron。

> 项目仍在开发中，尚无正式发行版。需要已有的完整游戏安装；目前不提供游戏本体的下载、更新或修复。自动签到页面仅为演示。

[参与贡献](CONTRIBUTING.md) · [架构](docs/architecture.md) · [构建与发布](docs/building.md) · [安全策略](SECURITY.md) · [许可证](LICENSE)

## 功能

- **国服登录**：手机号验证码或已保存账号登录；游戏确认身份后自动关闭登录窗口。
- **游戏内登录层**：黑色遮罩与居中卡片随游戏客户区缩放，采用原生子窗口随游戏一起移动；条件不满足时回退为从属窗口。
- **账号保护**：可选保存最多 16 个账号，使用 Windows DPAPI 加密。验证码不落盘；短信请求采用 60 秒冷却。
- **官方公告**：查看公告、活动、资讯及正文。
- **抽卡档案**：导入、合并和导出 Oddward / NTEUID 格式；支持用户主动关联塔吉多并同步记录。
- **原生界面**：可使用本地背景视频、静态图片、logo 和 slogan，支持高对比度及减少动态效果。
- **网络设置**：系统代理、直连、自定义代理，以及可选的 GitHub 文件镜像。

支持 Windows 10/11 x64，当前面向国服官方渠道。游戏与登录 SDK 更新可能影响兼容性；不支持的组合会被拒绝启动。

## 从源码运行

准备以下工具：

- PowerShell 7。
- Go **1.27.3 windows/amd64**。
- .NET SDK **10.0.303**，用于构建自包含 WPF 登录窗口。

```powershell
git clone https://github.com/nerv-base/Oddward.git
Set-Location Oddward
pwsh -File scripts/go.ps1 mod download
pwsh -File scripts/build-launcher.ps1
.\artifacts\native-launcher\oddward.exe
```

Go 可以安装在 PATH，或放在 `.tools/go1.27.3/go/bin/go.exe`；包装脚本核对版本并固定 Windows/amd64 目标，不修改系统 PATH。详细说明见[构建文档](docs/building.md)。

首次使用时选择游戏安装目录。也可以指定游戏根目录和独立数据目录：

```powershell
.\artifacts\native-launcher\oddward.exe `
  -game-root 'D:\Games\Neverness To Everness' `
  -data-dir (Join-Path $PWD 'oddward-data')
```

构建产物已内嵌 WPF 窗口及其运行库，使用者不需要另外安装 .NET。不带版本号的构建属于开发构建，不会自动检查更新。正式发行渠道为 [GitHub Releases](https://github.com/nerv-base/Oddward/releases)；安装器和签名更新包的准备方式见[构建与发布](docs/building.md#发行包)。

## 登录与权限

主界面、登录窗口、网络登录和账号处理均以普通权限运行。启动游戏时，Windows UAC 会为一个受限的游戏 / SDK 宿主请求管理员权限；它负责保留游戏需要的启动环境和 SDK 会话。

登录窗口仍是独立进程，其窗口嵌入游戏客户区，不单独占用任务栏，也不全局置顶。DPI 条件不满足时保守回退为从属窗口。

默认设置位于 `%APPDATA%\Oddward`，账号、抽卡档案和缓存位于 `%LOCALAPPDATA%\Oddward`。`-data-dir` 接受绝对路径，可将这些数据统一放入指定目录。已保存账号受 Windows DPAPI 保护，不应把账号文件复制到公开仓库或分享给其他人。遇到人机验证，请在官方客户端完成。

## 自定义外观

不设置素材也可以运行。所有素材由用户自行提供，版权归原权利人；源码和发行包均不附带官方媒体。

```powershell
.\artifacts\native-launcher\oddward.exe `
  -background 'D:\Artwork\background.mp4' `
  -background-still 'D:\Artwork\background.png' `
  -logo 'D:\Artwork\logo.png' `
  -slogan 'D:\Artwork\slogan.png'
```

也可使用 `ODDWARD_BACKGROUND`、`ODDWARD_BACKGROUND_STILL`、`ODDWARD_LOGO` 和 `ODDWARD_SLOGAN` 环境变量。`-demo` 打开内存演示模式。

## 抽卡记录

抽卡页只展示本地档案里的真实记录，未收录的物品显示原始 ID；图标不可用时使用占位图。支持：

- `oddward-gacha` v1：保留 Oddward 的完整档案信息。
- `nteuid-gacha` v1：用于互通，部分扩展字段不会随导出保留。

保存游戏账号后，可主动点击“更新记录”关联塔吉多。同步只在点击时发起，不进行后台定时拉取。解除关联会保留本地抽卡档案。

## 贡献与许可

欢迎提交问题和改进。开发流程、测试命令与提交要求见 [CONTRIBUTING.md](CONTRIBUTING.md)；安全问题请走 [SECURITY.md](SECURITY.md) 中的私密渠道。

原创代码使用 **GPL-3.0-or-later**。依赖及运行库声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。分发二进制时需同时保留许可证并提供对应源码。

Oddward 与游戏运营方没有关联，不分发官方游戏文件、SDK 或媒体，不绕过认证、实名、防沉迷或反作弊。
