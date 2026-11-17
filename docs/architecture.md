# 架构

本文描述当前产品代码。开发边界见 [AGENTS.md](../AGENTS.md)，命令与发行流程见[构建文档](building.md)。

## 入口与依赖

产品入口为 `cmd/launcher`。安装版由 `cmd/stub` 构建的 `Oddward.exe` 选择版本目录，再运行其中的 `oddward-launcher.exe`。

```text
cmd/launcher
  ├─ internal/launcher/system → 领域包
  ├─ internal/launcher        → UI、输入、副作用调度
  └─ internal/launcher/state  → 纯状态规则
```

依赖单向向下。`state` 不访问文件、网络、时钟、goroutine 或 UI 框架；`launcher` 通过最小接口使用系统能力；真实实现由 `system` 提供并在入口注入。

## 目录职责

| 目录 | 职责 |
|---|---|
| `cmd/launcher` | 命令行、素材、依赖注入及主窗口 |
| `cmd/stub` | 安装版按版本启动与回退 |
| `internal/branding` | 原创图标的 SVG、PNG、ICO 源资源及 Go 嵌入数据 |
| `internal/launcher/state` | Model、Event、Reduce、Present，以及抽卡页规则 |
| `internal/launcher` | MyGo 视图、Shell 和副作用调度 |
| `internal/launcher/system` | 登录、设置、网络、更新、档案等系统适配 |
| `internal/cnlogin` | 国服登录、加密账号库、游戏启动及 SDK 会话 |
| `compat/loginui` | 自包含 WPF 登录窗口和窗口呈现 |
| `internal/game`、`gamelocate` | 只读安装检查与用户所选目录规范化 |
| `internal/process` | PID＋创建时间的进程身份及退出观察 |
| `internal/launchsettings`、`netcfg` | 非秘密设置、代理及 GitHub 文件镜像 |
| `internal/news` | 官方公告读取与解析 |
| `internal/gacha` | 抽卡档案、统计、合并、导入导出 |
| `internal/gamemedia`、`imagecheck` | 图片来源限制、解码边界和缓存 |
| `internal/tajiduo` | 用户主动发起的账号关联与抽卡同步 |
| `internal/selfupdate`、`buildinfo` | 签名更新、版本布局、构建版本及公钥 |

新增业务规则放在 `state`，新增副作用定义为 `Effect`，需要真实系统时在 `launcher` 定义接口、在 `system` 实现。领域能力放在对应的 `internal` 包中，不把文件、进程或网络操作放入视图。

## 主界面数据流

输入经 `Shell.Dispatch` 进入 reducer，产生新模型和副作用。后台工作携带 context 与任务 token，将结果写入有界 channel；下一帧在 UI 线程再次分派事件。过期结果由 reducer 丢弃，后台不直接修改界面。

颜色和尺寸集中在 `internal/launcher/view.go`，强调色使用 `s.tone()`。共享控件放在 `controls.go`。素材以 contain 方式呈现，不假设宽高比；无障碍标签、高对比度及减少动态效果应覆盖每个可交互页面。

应用图标统一来自 `internal/branding`：主窗口通过 MyGo `SetIcon` 设置任务栏图标，导航栏使用单色 SVG，WPF 通过项目资源引用同源图像。两个 Go 入口链接已提交的 Windows/amd64 COFF 图标资源，安装器直接使用同一 ICO；修改 ICO 后用 `scripts/update-icons.ps1` 再生成 COFF。

## 国服登录

`internal/cnlogin/session` 编排配置加载、HTTP 登录、准入、身份投递和窗口生命周期。相关子包各自负责一项能力：

- `launch`：安装和 SDK 兼容性检查、单实例约束、启动环境、游戏进程身份。
- `sdkhost`：SDK 管道、宿主窗口和原生回调。
- `elevatedipc`、`broker`：受限宿主启动、私有 IPC、身份校验及请求转发。
- `laohu`、`bootcfg`、`cnproto`：登录客户端、配置解析和 SDK 消息编码。
- `accounts`：DPAPI、私有目录权限及账号保存事务。
- `loginui`：与 WPF 助手的有界帧通信。

主 UI、WPF、账号库和 HTTP 登录均为普通权限。受限宿主只持有游戏与 SDK 会话，不接受任意命令、账号文件或任意进程操作。窗口嵌入仅作用于本次会话的登录窗口，核对进程身份、窗口、隐藏状态与 DPI 后才完成绑定；WPF 等待请求确认及原生关系检查后显示。

登录层优先使用原生子窗口，移动由 Windows 随父窗口处理，缩放由窗口事件同步。DPI 不匹配时回退为从属窗口。游戏主 HWND 销毁或切换时重建自己的隐藏窗口，未提交表单只在内存中保留。

短信冷却以客户端为唯一状态源：有效请求开始即消费 60 秒，错误不返还，切页或重开不重置。界面刷新保留输入和选择状态。

身份投递获得原生 ACK，且账号保存或回滚结束后关闭登录窗口；SDK 会话仍可回答后续查询。管道断开代表观察丢失，不能当作游戏已退出；只有实际进程退出才结束宿主生命周期。

## 数据与网络

非秘密设置与账号数据分开保存。账号及塔吉多续期凭据使用 DPAPI；验证码不持久化。删除账号会一并删除其关联凭据，解除塔吉多关联不删除本地抽卡档案。

抽卡页只展示实际记录，名称不明时保留原始 ID，图标不可用时使用占位。读取、导入和导出经系统适配完成，视图只使用档案类型与纯函数。

HTTP 使用统一网络设置。媒体下载限制来源、重定向、大小及像素数。遇到人机验证停止自动流程，由用户在官方客户端完成。

## 自更新

正式安装按版本目录存放文件。更新先验签、逐文件校验，再完成可恢复的版本切换；不会在未完成校验时覆盖旧版本。启动桩可在新版本启动失败后回退。

开发构建或未配置更新公钥时禁用更新。镜像仅影响下载地址，不跳过签名、哈希或路径检查。签名私钥不进入源码和构建输出。

## 依赖与许可

Go 模块固定在 `go.mod` / `go.sum`，.NET SDK 固定在 `compat/loginui/global.json`，自包含运行库固定在项目文件中。当前主要依赖为 MyGo、purego、go-text/typesetting 和 golang.org/x 系列包。

版本、来源和完整声明统一维护在 [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md) 与 `licenses/`。新增依赖前需核对许可证；发行构建会携带这些声明。官方游戏 SDK 与媒体不纳入源码或发行包。

## 静态检查

常规测试命令见 [CONTRIBUTING.md](../CONTRIBUTING.md)。结构调整或删除代码时，使用固定工具版本补充检查：

```powershell
pwsh -File scripts/go.ps1 run honnef.co/go/tools/cmd/staticcheck@v0.8.2 -checks=U1000,SA*,S1* ./...
pwsh -File scripts/go.ps1 run golang.org/x/tools/cmd/deadcode@v0.52.0 -test ./...
pwsh -File scripts/go.ps1 mod tidy
```

分析结果需要结合入口与测试用途判断；不要仅凭工具报告删除被其他入口使用的代码。
