<div align="center">

<img src="internal/branding/icon.svg" alt="Oddward 异象眼图标" width="112" height="112">

# Oddward

**Windows 上的《异环》非官方启动器**

![Windows 10 / 11 x64](https://img.shields.io/badge/Windows-10%20%2F%2011%20x64-0078D4?style=flat-square)
[![GPL-3.0-or-later](https://img.shields.io/badge/License-GPL--3.0--or--later-blue?style=flat-square)](LICENSE)

[开始使用](#开始使用) · [从源码构建](#从源码构建) · [发布页](https://github.com/nerv-base/Oddward/releases) · [问题反馈](https://github.com/nerv-base/Oddward/issues)

</div>

Oddward 面向《异环》国服官方渠道，将游戏启动、账号管理、官方公告和抽卡记录放在同一个原生界面中。你可以保存常用账号，整理不同账号的抽卡档案，也可以用自己的背景视频和图片装饰启动器。

主界面使用 Go 与 [MyGo Native UI](https://github.com/egoist/mygo) 构建，不依赖 WebView 或 Electron。

> [!NOTE]
> 项目仍在开发中，尚无正式发行版，目前请从源码构建。使用前需要已有的完整游戏安装；游戏本体的下载、更新和修复仍需通过官方启动器完成。自动签到默认关闭，只有你打开开关后才会在启动器运行期间执行。

## 功能

- **游戏登录与账号管理**：支持手机验证码登录和已保存账号登录，可选保存最多 16 个账号，方便切换。
- **随游戏显示的登录界面**：居中登录卡片与黑色遮罩随游戏窗口移动、缩放，登录成功后自动收起。
- **抽卡记录**：按账号保存、查看、合并和导出抽卡档案，支持 Oddward / NTEUID 格式，也可主动关联塔吉多同步记录。
- **签到与游戏信息**：关联塔吉多后可分别执行《异环》游戏签到与社区签到，查看签到结果与本日奖励，以及本性像素、都市活力、活跃度和周本剩余；自动签到需你逐个打开开关，且只在启动器运行期间生效。
- **官方公告**：在启动器中查看公告、活动、资讯及正文。
- **自定义外观**：支持本地背景视频、静态图片、logo 和 slogan，并适配高对比度、减少动态效果等系统偏好。
- **网络设置**：支持系统代理、直连、自定义代理，以及可选的 GitHub 更新文件镜像。

## 开始使用

需要 **Windows 10 / 11 x64**，以及已安装完整的《异环》国服官方渠道客户端。

1. 按[源码构建说明](#从源码构建)生成启动器并运行。
2. 首次使用时选择游戏安装目录。
3. 点击启动游戏，在登录窗口中使用手机验证码或已保存账号登录。

启动器构建产物已包含登录窗口及其运行库，运行时无需另外安装 .NET。后续版本可在 [GitHub Releases](https://github.com/nerv-base/Oddward/releases) 查看。

保存游戏账号后，可在抽卡页点击“更新记录”关联塔吉多。同步由你主动发起，不会后台定时拉取；解除关联会保留本地抽卡档案。签到页复用同一条塔吉多关联：游戏签到与社区签到各自独立，自动签到默认关闭，打开后在你点击“启动游戏”时随启动流程并行执行一次并按天去重，不会在退出后驻留。

<details>
<summary>自定义背景与数据目录</summary>

素材由你自行提供；不设置素材也可以使用启动器。以下路径为示例：

```powershell
.\artifacts\native-launcher\oddward.exe `
  -game-root 'D:\Games\Neverness To Everness' `
  -data-dir (Join-Path $PWD 'oddward-data') `
  -background 'D:\Artwork\background.mp4' `
  -background-still 'D:\Artwork\background.png' `
  -logo 'D:\Artwork\logo.png' `
  -slogan 'D:\Artwork\slogan.png'
```

`-data-dir` 需要绝对路径。素材也可通过 `ODDWARD_BACKGROUND`、`ODDWARD_BACKGROUND_STILL`、`ODDWARD_LOGO` 和 `ODDWARD_SLOGAN` 环境变量指定。

</details>

## 账号与权限

已保存账号使用 Windows DPAPI 加密保护，验证码不落盘。获取验证码有 60 秒冷却，切换页面或重开登录窗口不会重置。遇到人机验证时，请在官方客户端完成。

默认设置保存在 `%APPDATA%\Oddward`，账号、抽卡档案及缓存保存在 `%LOCALAPPDATA%\Oddward`。签到记录另存于 `%LOCALAPPDATA%\Oddward\checkin`，只包含账号 ID、角色 ID、日期、结果和一句说明，不含令牌或手机号。请勿公开分享账号数据文件。

启动游戏时会出现 Windows UAC 提示，用于启动受限的游戏与登录 SDK 宿主。启动器主界面、登录窗口和账号处理仍以普通权限运行。

登录窗口由独立进程呈现，优先嵌入游戏窗口，不单独占用任务栏；DPI 条件不满足时回退为从属窗口。游戏或登录 SDK 更新可能影响兼容性，不支持的版本组合会被拒绝启动。

## 从源码构建

准备 **PowerShell 7**、**Go 1.27.3 windows/amd64** 和 **.NET SDK 10.0.303**，然后执行：

```powershell
git clone https://github.com/nerv-base/Oddward.git
Set-Location Oddward
pwsh -File scripts/go.ps1 mod download
pwsh -File scripts/build-launcher.ps1
.\artifacts\native-launcher\oddward.exe
```

Go 可安装在 PATH，或放在 `.tools/go1.27.3/go/bin/go.exe`。构建脚本会嵌入自包含登录窗口，并附带许可证文件；分发时请保留整个输出目录中的许可声明。

不带版本号的产物属于开发构建，不会自动检查更新。工具链配置、开发命令及发行流程见[构建与发布](docs/building.md)。

## 反馈与贡献

欢迎通过 [Issues](https://github.com/nerv-base/Oddward/issues) 反馈问题或建议。反馈时请说明启动器版本、Windows 版本及复现步骤，不要附上手机号、验证码、账号文件或未经脱敏的日志。

参与开发前请阅读[贡献指南](CONTRIBUTING.md)与[架构说明](docs/architecture.md)。安全问题请按[安全策略](SECURITY.md)中的私密渠道报告。

## 致谢

感谢以下项目提供的实现与文档参考：

- [Starward](https://github.com/Scighost/Starward)：启动器项目灵感来源。
- [Oddity](https://git.miprota.cc/Oddity/Oddity)：《异环》启动器的实现参考。
- [Hi3Helper.Plugin.NTE](https://github.com/maxinglo/Hi3Helper.Plugin.NTE)：《异环》登录兼容实现参考。
- [MyGo](https://github.com/egoist/mygo)：原生 UI 框架。

第三方组件及其完整许可声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

## 许可与声明

Oddward 的原创代码采用 [GPL-3.0-or-later](LICENSE) 许可。分发二进制时，需保留相关许可证并提供对应源码。

本项目与《异环》的开发方、发行方及运营方没有隶属关系。游戏名称、商标及素材归原权利人所有；源码和发行包不附带官方游戏文件、登录 SDK 或媒体，也不绕过认证、实名、防沉迷或反作弊。
