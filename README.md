<div align="center">

<img src="internal/branding/icon.svg" alt="Oddward 异象眼图标" width="112" height="112">

# Oddward

**面向《异环》国服官方渠道的 Windows 非官方启动器**

[下载安装](https://github.com/nerv-base/Oddward/releases/latest) · [界面截图](#界面截图) · [开始使用](#开始使用) · [从源码构建](#从源码构建) · [问题反馈](https://github.com/nerv-base/Oddward/issues)

</div>

Oddward 可以启动、校验和更新《异环》，在本机保存常用游戏账号，并管理各角色的抽卡档案。关联塔吉多后，可以同步抽卡记录、签到和查看游戏信息。主界面使用 Go 和 [MyGo Native UI](https://github.com/egoist/mygo)。

## 开始使用

Oddward 支持 **Windows 10 / 11 x64**。在[最新发行页](https://github.com/nerv-base/Oddward/releases/latest)的 **Assets** 中下载 `oddward-<版本>-setup.exe` 并运行安装。安装包已包含登录窗口及其 .NET 运行库。[v0.1.0 发布说明](https://github.com/nerv-base/Oddward/releases/tag/v0.1.0)

1. **已有游戏时**，点击主页操作栏下方的「定位游戏」并选择现有安装。定位后可重新校验文件或检查游戏更新。
2. **首次安装时**，点击主页的「下载游戏」，选择空目录，等安装方案显示后确认下载。Oddward 会连同游戏资源一起下载并安装登录与启动所需的运行组件（约 30 MB），完成后主按钮直接变为「启动游戏」，不需要另外安装官方启动器。
3. **准备好游戏后**，点击「启动游戏」，按提示完成首次启动确认和 Windows UAC 授权，再在随游戏显示的登录窗口中用手机验证码或已保存的账号登录。勾选「在此设备保存账号」后，下次可以直接选择该账号，本机最多保存 16 个。

如果游戏文件被外部修改或损坏，Oddward 会提示重新校验；当前游戏版本暂不支持原生登录时，可以临时改用官方启动器启动。

## 界面截图

[![启动器主页](docs/screenshots/home.png)](docs/screenshots/home.png)

## 主页与设置

- 主页右下角的胶囊就是游戏主操作：安装、更新、启动、暂停和进度都在这里；下载中悬停可展开摘要。运行中会显示本次游戏时长。
- 胶囊上方是一条账号行，显示下次登录会使用的已保存账号，可以切换、重命名或删除；添加账号仍然通过启动游戏后的登录窗口完成。
- 胶囊右侧的齿轮打开游戏面板（安装目录、版本、重新校验、预下载管理）。左下角的齿轮是启动器自己的设置，分为常规、外观、下载与网络、文件、关于。
- 关闭窗口默认最小化到托盘，可在「常规」里改为直接询问是否退出；托盘图标可以重新打开主界面或退出 Oddward。

## 游戏下载与更新

- **正式更新**会复用已校验的本地文件和缓存。条件满足时可以应用官方差分补丁，差分不可用或重建结果校验失败时改为下载对应的完整文件。空间不足时会停止更新并提示所需空间。
- **断点与恢复**支持暂停和继续下载。首装可另选本地卷暂存下载。安装提交中断后，可通过「完成上次更新」恢复，再启动游戏。
- **预下载**从「检查预下载」进入。国服官方开放且清单通过校验时才能开始。下载完成的文件先暂存，正式版本发布后再核对并应用。
- 「下载与网络」设置可调整全局限速，并选择系统代理、直连、自定义代理或可选的 GitHub 更新文件镜像。自动恢复和差分更新始终开启，不再作为选项。

## 抽卡、签到与游戏数据

- 在「抽卡分析」中按角色查看和合并本地记录，导入或导出 Oddward / NTEUID JSON。保存游戏账号并勾选「在此设备保存账号」后，可点击「关联塔吉多」：可以复用已保存的游戏登录凭据直接关联，无需短信；凭据不可用时改用官方短信登录，再选择角色。关联会顶掉手机上已登录的塔吉多。之后点击「更新记录」，可合并塔吉多提供的最近约 6 个月的 S 级记录。同步由你手动触发，解除关联会保留本地档案。
- 官方名称没有收录的弧盘，会用 B 站 BWIKI 的公开数据补齐名称、稀有度和图标；只缓存这三项显示字段，缓存 7 天，刷新失败时保留旧缓存并继续显示原始 id。官方来源始终优先。
- 「每日签到」页复用同一条塔吉多关联，可以手动执行按角色计算的《异环》游戏签到，以及按账号计算的塔吉多社区签到。两个自动开关默认关闭，可分别开启，开启后只在点击「启动游戏」时尝试签到，每项每天至多一次；不启动游戏不会执行。结果待核实的签到不会自动重试。
- 游戏信息面板显示塔吉多返回的角色信息、本性像素、都市活力、今日活跃度和周本剩余。可在面板手动刷新。
- 主页右上角的「本性像素」卡片使用游戏内官方图标，按塔吉多快照和 6 分钟/点计算下一点与回满时间，卡片右上角可手动刷新；角色头像按塔吉多返回的头像 id 从官方社区 CDN 读取。塔吉多没有提供备用体力，备用池不做推算。
- 「游戏数据」页显示角色概览、角色收集、区域探索、房产与载具概览。只显示服务器返回的字段，进入页面时读取一次，手动刷新才会再次读取。

塔吉多关联遇到人机验证时，请在官方客户端完成验证。

## 账号数据与权限

启动游戏时，UAC 授权用于启动专用的游戏与登录 SDK 宿主。Oddward 主界面、登录窗口和账号处理均以普通权限运行。已保存的账号凭据和塔吉多续期凭据使用 Windows DPAPI 加密，短信验证码仅在内存中使用。

| 默认目录 | 内容 |
|---|---|
| `%APPDATA%\Oddward` | 启动器设置 |
| `%LOCALAPPDATA%\Oddward` | 账号、抽卡档案和缓存 |
| `%LOCALAPPDATA%\Oddward\checkin` | 签到结果和每日签到开关 |

## 公告与外观

主页展示国服官方公告、活动、资讯及正文。可以提供自己的背景视频、静态图、标志或标语。未指定本地标语时，Oddward 会尝试读取国服官网当前标语，离线时使用上次校验成功的缓存。

<details>
<summary>启动参数示例</summary>

```powershell
.\artifacts\native-launcher\oddward.exe `
  -data-dir (Join-Path $PWD 'oddward-data') `
  -background 'D:\Artwork\background.mp4' `
  -background-still 'D:\Artwork\background.png'
```

`-data-dir` 接受绝对路径，可把设置和本地数据放在指定目录。`-logo` 和 `-slogan` 可指定本地图像。素材参数也可通过 `ODDWARD_BACKGROUND`、`ODDWARD_BACKGROUND_STILL`、`ODDWARD_LOGO` 和 `ODDWARD_SLOGAN` 设置。

</details>

## 从源码构建

准备 **PowerShell 7**、**Go 1.27.3 windows/amd64** 和 **.NET SDK 10.0.303**。Go 可以装在 `PATH`，也可以使用项目内工具链。

```powershell
git clone https://github.com/nerv-base/Oddward.git
Set-Location Oddward
pwsh -File scripts/go.ps1 mod download
pwsh -File scripts/build-launcher.ps1
.\artifacts\native-launcher\oddward.exe
```

这组命令生成开发版，启动器自更新处于关闭状态。分发构建产物时请保留随附的许可证并提供对应源码。工具链位置、自更新条件和打包流程见[构建与发布](docs/building.md)。

## 反馈与贡献

反馈时请附上 Oddward 版本、Windows 版本和复现步骤，并隐去手机号、验证码和其他账号信息。账号文件和原始日志请留在本机。[问题反馈](https://github.com/nerv-base/Oddward/issues) · [安全问题报告](SECURITY.md)

开发约定、测试命令和代码结构见[贡献指南](CONTRIBUTING.md)与[架构说明](docs/architecture.md)。

## 许可与致谢

Oddward 的原创代码采用 [GPL-3.0-or-later](LICENSE)。第三方组件及许可见[第三方声明](THIRD_PARTY_NOTICES.md)。

Oddward 独立于《异环》的开发、发行和运营方。游戏名称、商标及素材归各自权利人所有。游戏文件和官方登录 SDK 由官方渠道提供。

感谢 [Starward](https://github.com/Scighost/Starward) 提供项目灵感，[Oddity](https://git.miprota.cc/Oddity/Oddity) 和 [Hi3Helper.Plugin.NTE](https://github.com/maxinglo/Hi3Helper.Plugin.NTE) 提供实现参考，以及 [MyGo](https://github.com/egoist/mygo) 提供原生 UI 框架。
