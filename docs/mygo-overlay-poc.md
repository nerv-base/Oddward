# MyGo Native UI Overlay PoC

> 实验性验证文档。本文描述的 `cmd/overlay-poc`、`cmd/overlay-host` 与
> `internal/overlaywin` 不进入正式启动流程，不修改现有 WPF 登录实现。

## 结论

**CONDITIONAL GO**。MyGo v0.10.0 已具备把登录窗口与欢迎横幅做成纯 Native UI 的能力：
透明窗口、圆角卡片、输入框、文本选择与剪贴板、欢迎横幅淡入淡出、无焦点/鼠标穿透、
PID + 创建时间校验、WinEvent 监听、最小化隐藏与 HWND 重建恢复，都已在真实 Win32
窗口上通过自动化验证（owner 模式 33/33、native child 模式 19/19）。

native child（`WS_CHILD` + `SetParent`）路线最初表现为“窗口关系与几何正确、客户区
无像素”。经本轮根因定位，这不是 DirectComposition 不支持子窗口，而是 **MyGo
v0.10.0 的缺陷**：Native UI 窗口创建时被 DWM cloak，而 Windows 不允许对任何
`WS_CHILD` 窗口清除 `DWMWA_CLOAK`（`E_HANDLE`）；隐藏窗口被外部 `SetParent` 到可见
宿主后，`IsWindowVisible` 已为真，MyGo 的 `ShowInactive` 跳过 uncloak，首帧
`drawHidden` 又因“可见”提前返回，最终永远停在 cloak 状态、无法绘制。详见第 8.1 节。

该缺陷已有两条可行路径：

1. **Oddward 侧工作区（已验证）**：在仍为顶层窗口时先 `ShowInactive` + 轮询/调用
   uncloak，再 `SetParent`；同时移除子窗口残留的 `WS_CAPTION|WS_SYSMENU` 等顶层
   装饰。`cmd/overlay-poc -mode child` 使用此路径，19/19 自动化通过。
2. **MyGo 上游最小修复（已在独立实验分支验证）**：在 `WM_STYLECHANGING` 检测
   `WS_CHILD` 置位时提前 uncloak，补丁和回归测试见第 8.1 节；不依赖应用侧时序。

但中文 IME 端到端、150%/200% DPI、真实受限 Broker 权限模型、真实游戏全屏/无边框
场景仍未在本环境验证，见「未测试项」。在这些条件满足前，不应直接用 MyGo 替换
正式 WPF 登录流程。

## 1. 范围

本 PoC 验证以下问题，不实现真实登录 API、不读写账号、不启动游戏：

- 纯 MyGo Native UI 是否能还原现有 WPF 登录窗口与欢迎横幅的可见行为；
- MyGo 窗口的 HWND 是否能安全地绑定到独立进程的模拟游戏窗口；
- native child 与 owned popup 两种模式的实际差异；
- 输入、焦点、剪贴板、窗口生命周期、DPI 与资源清理是否达到验收条件；
- 移除 WPF payload 后 EXE / 压缩发布体积能降低多少。

明确不在范围：接入真实《异环》客户端、真实 Broker/IPC、真实登录协议、真实账号库。

## 2. 测试环境

| 项目 | 值 |
|---|---|
| 操作系统 | Windows 11 家庭版 中文版，10.0.26300，64 位 |
| CPU/GPU | NVIDIA GeForce RTX 5070 Ti Laptop GPU（Direct3D 11 / DirectComposition） |
| 显示 | 2560×1440，测试窗口所在显示器 DPI 96（100%） |
| Go | go1.27.3 windows/amd64（项目内 `.tools/go1.27.3`） |
| MyGo | v0.10.0（`github.com/egoist/mygo v0.10.0`） |
| .NET SDK（仅现有 WPF 构建） | 10.0.303 |
| 测试宿主 | `cmd/overlay-host.exe`，独立 Win32 进程，`OddwardOverlayHost-<pid>` 窗口类 |
| 测试入口 | `cmd/overlay-poc.exe`，独立 MyGo 进程 |

所有尺寸与像素结果来自本机真实窗口；原始报告保存在被忽略的
`artifacts/overlay-poc/report.json`，原始截图在 `artifacts/` 下，不提交仓库。

## 3. 架构

### 3.1 测试宿主 `cmd/overlay-host`

独立 Win32 进程，模拟游戏主窗口：

- `-mode window`：`WS_OVERLAPPEDWINDOW`，可移动、缩放；
- `-mode frameless`：`WS_POPUP|WS_THICKFRAME|WS_CLIPCHILDREN`；
- 支持最小化、恢复、销毁并重建 HWND（`recreate`）；
- 客户区填充 `0x00101820`，点击位置画黄色十字线；
- 记录 `WM_LBUTTONDOWN`、`WM_RBUTTONDOWN`、`WM_KEYDOWN`、`WM_CHAR`、`WM_SIZE`、
  `WM_SYSCOMMAND` 到 JSONL；
- 通过本机回环 TCP 控制通道接收 `status/minimize/restore/move/recreate/destroy/quit`，
  所有窗口操作都投递到 UI 线程执行；
- `-status <file>` 写出 `{hwnd,pid,created,mode,minimized,rect,client}`，供测试脚本读取。

### 3.2 Overlay `cmd/overlay-poc`

独立 MyGo 进程，使用纯 MyGo Native UI：

- `mygo.NewWindow(WindowOptions{Frameless: true, Transparent: true,
  SkipTaskbar: true, AlwaysOnTop: false, DisableResize: true, Hidden: true, Content: ui.View(...)})`；
- 主题根背景设为 `ui.RGBA(0,0,0,0)`，由 DirectComposition 输出逐像素透明；
- 登录页：半透明全屏遮罩（黑色 alpha 0.65）、420×520 圆角卡片（18 px 圆角、
  6 px 深色 bezel、12 px 内框）、标题栏、手机号/验证码输入、获取验证码按钮、
  “在此设备保存账号”复选框、登录/其他账号登录按钮；
- 欢迎横幅：距宿主客户区顶部 48 px 的圆角胶囊，`E3E3E3` 背景 alpha 0.92、
  `3A3B3F` 1 px 边框、15 px 加粗文字；淡入 220 ms、停留 2.4 s、淡出 380 ms；
  系统关闭客户端动画时跳过淡入淡出，只保留 2.4 s；
- 控制通道 `state` 输出 HWND、样式、矩形、输入值、焦点、选择区间、banner 状态、
  opacity、绑定状态、watcher 回调数、重建次数与最后错误，供自动化断言；
- `-mode owner`（默认）用 `SetWindowLongPtr(GWLP_HWNDPARENT)` 绑定；
  `-mode child` 用 `SetParent` + `WS_CHILD` 绑定。child 模式在绑定前先以
  opacity 0 `ShowInactive` 并等待 MyGo 清除 DWM cloak（见第 8.1 节），
  绑定后移除 `WS_CAPTION|WS_SYSMENU|WS_MINIMIZEBOX|WS_MAXIMIZEBOX`，
  避免 user32 在子窗口客户区上绘制顶层标题栏。

### 3.3 Win32 适配层 `internal/overlaywin`

独立的 `//go:build windows` 平台模块，不含 UI 与登录领域逻辑：

- `Identity{PID, CreationTime}`：`OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION|SYNCHRONIZE)`，
  `GetProcessTimes` 校验创建时间；`ParseCreationTime` 只接受规范十进制 uint64；
- `Watcher`：`SetWinEventHook(EVENT_OBJECT_LOCATIONCHANGE=0x800B,
  EVENT_SYSTEM_MOVESIZESTART/END=0x000A/0x000B, EVENT_OBJECT_DESTROY=0x8001)`，
  按进程过滤，`OUTOFCONTEXT` 不注入 DLL；
- `SetPos` 使用 `SWP_NOZORDER|SWP_NOACTIVATE|SWP_NOOWNERZORDER`；
- `SetOwner` 写 `GWLP_HWNDPARENT` 并回读校验；
- `SetChild` 复现 broker 的 `embed_overlay` 序列：清 `WS_CAPTION` 等顶层装饰、
  置 `WS_CHILD`、`SetParent`、`SetWindowPos`；`SetParent` 的返回值是原父窗口
  （顶层窗口为 NULL），因此用清空后的 `GetLastError` 判断失败，而不是把 0 当错误；
- `SameDPI` 使用 `AreDpiAwarenessContextsEqual` 比较完整 DPI awareness context；
  `IsCloaked`/`Uncloak` 用于验证并清除 `DWMWA_CLOAK`；
- `MakeBannerStyle` 添加 `WS_EX_TRANSPARENT|WS_EX_TOOLWINDOW|WS_EX_NOACTIVATE`；
- `WithThreadDPI` 仅在同步创建 HWND 时切换线程 DPI 上下文，结束后恢复；
- `FindMainWindow` 用 `EnumWindows` 选择客户区最大的候选窗口；
- `Candidate` 要求可见、无 owner、PID 匹配、非 tool window、未被 DWM cloaked。

### 3.4 安全边界

- PoC 只观察目标进程的 PID + 创建时间，只写自己的 HWND；不向游戏进程注入代码、
  不发送输入、不改变游戏窗口样式；
- 不调用真实 Broker、不降低现有 `bind_overlay` / `embed_overlay` 的进程身份校验；
- 测试宿主是 **同用户、非受限的模拟进程**，它的成功不等于真实游戏 + 受限 Broker
  场景成功；正式迁移必须复用现有 broker 调用链重新验证（见「未测试项」）。

## 4. 运行方式

```powershell
# 构建（Release 参数与产品构建一致）
pwsh -File scripts/go.ps1 build -trimpath -tags mygo_noinspector `
    -ldflags '-H=windowsgui -s -w' -o artifacts/overlay-poc/overlay-host.exe ./cmd/overlay-host
pwsh -File scripts/go.ps1 build -trimpath -tags mygo_noinspector `
    -ldflags '-H=windowsgui -s -w' -o artifacts/overlay-poc/overlay-poc.exe ./cmd/overlay-poc

# 全自动真实窗口测试（会构建并运行 host + overlay，owner 模式 33 项断言）
pwsh -File scripts/test-overlay-poc.ps1

# native child 模式自动化（PrintWindow 像素断言，19 项；不依赖桌面输入）
pwsh -File scripts/test-overlay-child.ps1

# 体积对比（会重新构建 WPF 内嵌版、无 payload 版、PoC，并做 ZIP 压缩）
pwsh -File scripts/size-overlay-poc.ps1
```

手工观察：

```powershell
# 终端 A：启动模拟游戏窗口，状态写到 artifacts/overlay-poc/host.json
.\artifacts\overlay-poc\overlay-host.exe -mode window -x 300 -y 300 -w 900 -h 640 `
    -status artifacts\overlay-poc\host.json -log artifacts\overlay-poc\host.jsonl -control 48001

# 终端 B：读取 host.json 的 hwnd 与 created，启动 owner 模式 overlay
$h = Get-Content artifacts\overlay-poc\host.json -Raw | ConvertFrom-Json
.\artifacts\overlay-poc\overlay-poc.exe -host $h.hwnd -host-created $h.created `
    -mode owner -control 48002

# 终端 C：查询/操作
#   TCP 48001: status / move 200 180 1000 700 / minimize / restore / recreate / quit
#   TCP 48002: state / banner 测试账号，欢迎登录 / quit
```

`-host-created` 是必填项：缺失或与目标进程的创建时间不一致时，`overlay-poc`
直接失败退出，不会创建或绑定窗口。`-mode child` 用于验证 native child 路线：
启动时会先在顶层状态解除 DWM cloak，再交给 `SetParent`；未打上游补丁的
MyGo v0.10.0 必须走这条应用侧路径（见第 8.1 节）。

## 5. 实测结果

### 5.1 自动化结果

`scripts/test-overlay-poc.ps1` 最后一次**解锁桌面会话**运行：**33/33 通过**（报告
`artifacts/overlay-poc/report.json`）。测试启动独立 `overlay-host` 进程与独立
`overlay-poc` 进程，使用真实 `SetCursorPos`/`mouse_event`/`keybd_event` 注入输入，
并读取两端控制通道与宿主日志断言。

> 2026-11-23 的 native child 根因实验期间测试桌面被系统锁定（`GetForegroundWindow()==0`，
> `LogonUI` 处于前台），屏幕 `GetPixel` 与合成输入不可用；此时重跑 owner 脚本会在
> 像素/输入步骤被环境阻断（5/7 后停止），不是产品回归。child 脚本改用
> `PrintWindow` 与窗口控制通道，因此在锁屏下仍能完整跑完（19/19）。解锁后应重跑
> owner 脚本恢复 33/33 证据。

| 分组 | 通过项 |
|---|---|
| 透明与窗口 | DirectComposition 无重定向位图（`ex & 0x200000`）；owner = 宿主 HWND；矩形 = 宿主客户区 |
| 像素 | 卡片主体像素 `(227,227,227)`；以覆盖前宿主客户区像素为基线，遮罩是 `RGBA(0,0,0,0.65)` 的 35% 混合（本次背景 `(32,24,16)` → 遮罩 `(11,8,6)`），非纯黑、无白边 |
| DPI | overlay 与宿主 `GetDpiForWindow` 都是 96（当前 100%） |
| 输入 | 获得键盘焦点；输入 11 位手机号；宿主日志无按键；退格；Ctrl+A 全选；Ctrl+V 替换选区；Shift+Home/方向键选择；验证码输入 |
| 焦点 | 关闭 overlay 后前台窗口回到宿主 |
| 窗口同步 | 移动/缩放（984×661）后矩形一致；最小化隐藏；恢复显示 |
| 生命周期 | 宿主 `recreate` 后 overlay `recreates=1`、新 GW_OWNER 正确、手机号/验证码/选择区间保留、watcher 有回调 |
| 安全边界 | 提供错误创建时间时 `overlay-poc` 以 exit=1 拒绝启动，不创建/绑定窗口 |
| 清理 | overlay 退出、host 退出、进程表无 `overlay-poc`/`overlay-host` |
| 欢迎横幅 | screen=banner、文本正确；`ex=0x82801a8` 含 `TRANSPARENT|TOOLWINDOW|NOACTIVATE`（测试脚本为压过桌面窗口额外加了 `WS_EX_TOPMOST`，PoC 自身为 `0x82801a0`）；淡入到 opacity 1；点击穿透到宿主；`WindowFromPoint` 根窗口为宿主；前台窗口保持宿主；淡出后自行退出 |

`scripts/test-overlay-child.ps1` 最后一次运行：**19/19 通过**（报告
`artifacts/overlay-poc/child-report.json`）。child 模式不依赖屏幕像素或合成输入，
使用宿主/overlay 控制通道 + `PrintWindow(PW_RENDERFULLCONTENT)`：

| 分组 | 通过项 |
|---|---|
| 安全边界 | 创建时间不匹配时 `overlay-poc` 以 exit=1 拒绝，不绑定 |
| 窗口关系 | `style=0x56000000`（`WS_CHILD`、无 `WS_CAPTION`）、parent=宿主、矩形=宿主客户区；外层 `MyGoWindow` 与子层 `MyGoSurface` 均 `cloak=0` |
| 命中测试 | `ChildWindowFromPointEx(host, 卡片中心)` 返回 overlay 子窗口，说明卡片可被鼠标命中 |
| 渲染 | 卡片像素 `(442,300)=(227,227,227)`；遮罩 `(4,4)=(11,8,6)` = 宿主 `(32,24,16)` 的 35% 黑色混合 |
| 窗口同步 | 宿主缩放到 984×661 后 overlay 矩形同步，标题栏像素 `(492,100)=(30,31,34)` 仍正确 |
| 生命周期 | 最小化隐藏、恢复显示；宿主 HWND 重建后 `recreates=1`、parent 更新、渲染恢复 |
| 清理 | overlay 与宿主均正常退出，精确 PID 无残留 |

### 5.2 渲染证据

- 透明窗口：隐藏/显示 overlay 后，卡片外桌面像素逐点相同（探针
  `probes/overlayprobe`，本地产物 `artifacts/probe/shown.png`、`hidden.png`）；
- 卡片主体像素精确为 `(227,227,227)`（`LoginVisuals.Body` 的 `0xE3E3E3`）；
- 覆盖前先采集宿主客户区背景 `(32,24,16)`，覆盖后遮罩像素为 `(11,8,6)`，正好是 `RGBA(0,0,0,0.65)` 的 35% 混合，说明遮罩是半透明叠加而非实心黑底；
- 截图 `artifacts/overlay-poc/login2.png` 显示圆角 bezel、标题栏、输入框、
  复选框与按钮的完整渲染。

### 5.3 未测试项（`NOT TESTED`）

以下项目在本环境无法可靠自动化，不能宣称通过：

| 项目 | 原因 | 迁移前要求 |
|---|---|---|
| Windows 中文 IME 端到端 | 需要切换输入法并人工组合候选词；MyGo 源码已实现 IMM32 消息（`surface.go` 的 `WM_IME_*`），但本 PoC 未驱动真实输入法 | 在目标机器上用微软拼音实际输入中文并验证候选窗、组合、删除 |
| 150% / 200% DPI | 当前显示器为 96 DPI；改系统缩放需要注销/重启会话 | 分别设置 150%/200% 后重跑 `scripts/test-overlay-poc.ps1`，检查坐标、字号与点击命中 |
| 真实游戏窗口 | 不运行《异环》，测试宿主不是游戏 | 用真实客户端重复 owner/child 绑定与最小化/恢复 |
| 受限 Broker 权限模型 | PoC 直连 Win32；真实 `embed_overlay`/`bind_overlay` 在受限宿主中执行 | 在真实 Broker 会话中验证同 SID、同会话、隐藏窗口、同 DPI、失败回滚 |
| 用户拖动窗口过程中的闪烁 | 自动化只能移动/缩放，不能可靠量化帧抖动 | 人工拖动/缩放并录屏观察 |
| 资源句柄计数 | 只验证进程退出与 hook 回调；未统计 GDI/USER 句柄增长 | 长时压测 `GetGuiResources` 与 `GetProcessHandleCount` |
| 子窗口的真实鼠标/键盘注入 | 自动化会话处于锁屏桌面，无法注入真实输入；已用 `ChildWindowFromPointEx` 验证命中测试，用 `PrintWindow` 验证渲染 | 解锁桌面上重跑 child 模式并注入点击/按键，验证输入不泄漏给宿主 |
| 子窗口 150%/200% DPI | 同 DPI 校验只在 96 DPI 下通过 | 在 150%/200% 下重跑 child 测试并检查坐标/像素 |

### 5.4 平台限制

- `cmd/overlay-host` 是 Win32 模拟宿主，不含真实游戏渲染与反作弊；
- MyGo window 是独立进程，尚未接入 `internal/cnlogin/loginui` 的 frame 协议与
  账号密码状态机；
- PoC 不实现 WPF 的通知页、已保存账号列表、重命名/删除、短信倒计时等业务页面。

## 6. WPF 与 MyGo 功能对照

| 功能 | 现有 WPF | MyGo PoC | 结果 |
|---|---|---|---|
| 无边框窗口 | `WindowStyle=None` + `AllowsTransparency` | `Frameless: true` | PASS |
| 透明背景 | `AllowsTransparency=true` | `Transparent: true` + 主题透明根 | PASS（像素证据） |
| 圆角卡片/bezel | 18 px 圆角、6 px 渐变 bezel | `Radius(18)` + 深色外框 | PASS（视觉） |
| 手机号输入 | `TextBox`，MaxLength 11 | `ui.TextInput` MaxLength 11 | PASS |
| 验证码输入 | `TextBox`，MaxLength 8 | `ui.TextInput` MaxLength 8 | PASS |
| 退格/删除 | WPF 原生 | TextInput 原生 | PASS |
| 全选/Shift 选择 | WPF 原生 | `TextSelection`/`SetTextSelection` | PASS |
| 剪贴板粘贴 | WPF 原生 | `Ctrl+V` + `ReadClipboard` | PASS |
| 中文 IME | WPF 在 phone/captcha 上显式 `SetIsInputMethodEnabled(false)` | MyGo 原生 InputMethod 已实现 | NOT TESTED |
| 按钮/复选框 | 自定义模板 | `ui.PrimaryButton`/`ui.Button`/`ui.Checkbox` | PASS |
| 输入不泄漏给游戏 | `ShowActivated=false` + 逻辑焦点 | 本 PoC 输入时宿主日志无按键 | PASS |
| 欢迎横幅 | 独立 WPF helper 进程 | 独立 MyGo 进程 | PASS |
| 横幅淡入/淡出 | 220/2400/380 ms | 220/2400/380 ms（读系统动画设置） | PASS |
| 横幅不抢焦点 | `WS_EX_NOACTIVATE` | 同样样式 + `SetIgnoreMouseEvents` | PASS |
| 横幅鼠标穿透 | `WS_EX_TRANSPARENT` | 同样样式 + 点击穿透断言 | PASS |
| 横幅自退出 | helper `Close()` | 淡出后 `App.Quit()` | PASS |
| PID+创建时间校验 | `GameOverlay` | `overlaywin.Identity` | PASS（模拟宿主） |
| owner 绑定 | `setOwner` | `SetWindowLongPtr(GWLP_HWNDPARENT)` | PASS |
| native child 绑定 | 首选 `embed_overlay` | `SetParent` + `WS_CHILD` + 绑定前 uncloak + 去标题栏 | PASS（19/19；依赖应用侧时序，上游补丁见 8.1） |
| WinEvent 监听 | 3 类事件 | 4 类事件，回调数 > 0 | PASS |
| 位置/尺寸同步 | 50 ms timer + hook | 50 ms ticker + hook | PASS |
| 最小化隐藏/恢复 | `IsIconic` | `Watcher.State` | PASS |
| 宿主 HWND 重建 | `recreateView` | `recreateOverlay`（recreates=1） | PASS |
| 表单/选择恢复 | `FormState` | `restoreFocus`/`restoreSel` | PASS |
| DPI 同步 | `SetThreadDpiAwarenessContext` | `WithThreadDPI` | PASS（96 DPI）；150/200 NOT TESTED |
| 关闭后焦点返回 | 未单独验证 | 关闭时 `SetForegroundWindow(宿主)` | PASS（模拟宿主） |
| 资源清理 | Dispose/unhook | 进程退出 + 无残留进程 | PASS（句柄计数未测） |
| 真实 Broker 安全模型 | `embed_overlay`/`bind_overlay` | 未接入 | NOT TESTED |

## 7. 体积对比

`scripts/size-overlay-poc.ps1 -SkipWPF`（复用已有 WPF 构建）实测：

| 产物 | EXE 字节 | EXE MiB | ZIP 字节 | ZIP MiB |
|---|---:|---:|---:|---:|
| 当前完整 WPF 内嵌 `oddward-wpf.exe` | 89,267,712 | 85.13 | 75,184,132 | 71.70 |
| 不含 WPF payload 的 `launcher-nopayload.exe` | 23,927,808 | 22.82 | 10,039,822 | 9.57 |
| PoC `overlay-poc.exe` | 11,212,288 | 10.69 | 4,745,235 | 4.53 |
| PoC 测试宿主 `overlay-host.exe` | 3,475,968 | 3.31 | 1,516,581 | 1.45 |

结论：

- WPF payload 约占 **62.31 MiB EXE / 62.13 MiB ZIP**（完整 85.13 − 无 payload 22.82）；
- 测试用 PoC 两个进程合计 **14.00 MiB EXE / 5.98 MiB ZIP**；其中
  `overlay-host.exe` 只是测试宿主，不进入产品；
- 若按迁移后产品 = 无 WPF payload 启动器 + `overlay-poc.exe` 估算：
  **33.51 MiB EXE / 14.10 MiB ZIP**，比当前完整 WPF 版少
  **51.62 MiB EXE / 57.60 MiB ZIP**；
- 若把测试宿主也算入，合计 36.82 MiB EXE / 15.55 MiB ZIP，比完整 WPF 版少
  48.31 MiB EXE / 56.15 MiB ZIP；
- ZIP 列只是 Deflate 压缩后的归档对比，不是 `build-release.ps1` 的安装器格式；
  正式安装包请用同一脚本按实际打包方式复测。

## 8. MyGo 缺陷与上游建议

### 8.1 native child 首次绑定不渲染（已定位：MYGO BUG）

**结论**：这是 **MyGo v0.10.0 的缺陷**，不是 DirectComposition 不支持子窗口，也不是
Oddward 的 `SetParent` 用法错误。DirectComposition 的 `CreateTargetForHwnd` 支持
`WS_CHILD` 窗口；真正的限制在 DWM：**任何 `WS_CHILD` 窗口都不能再设置或清除
`DWMWA_CLOAK`**，而 MyGo 在创建 Native UI 窗口时先 cloak，等首帧绘制后才 uncloak。
隐藏窗口被外部 `SetParent` 到可见宿主后，`IsWindowVisible` 已为真，`ShowInactive`
跳过 uncloak，首帧绘制又被“窗口已可见”判断挡掉，于是永远停在 cloak 状态。

**最小复现**

1. `cmd/overlay-host.exe -mode window -x 120 -y 120 -w 900 -h 640 -status host.json`；
2. 读取 `host.json` 的 `hwnd` 与 `created`；
3. `cmd/overlay-poc.exe -host <hwnd> -host-created <created> -mode child -control 48002`；
4. 绑定前 `overlay-poc` 处于 `Hidden:true`；`state` 返回 `bound=true`、`style=0x56CA0000`、
   parent=宿主、rect=宿主客户区、`visible=true`，但 `DWMWA_CLOAKED=1`，客户区没有
   overlay 像素（宿主背景 `(32,24,16)`）。

**实验矩阵**

| # | 实验 | 结果 |
|---|---|---|
| A | 修正 `SetParent` 错误判断后重跑 | PASS：`SetParent` 成功返回原父窗口（顶层为 NULL）；之前把 0 当失败是错误检查 bug，已修复 |
| B | 隐藏创建 → 重挂到可见宿主 → `ShowInactive` | FAIL：`cloak=1`，无像素；根因路径 |
| C | 先显示并绘制首帧 → 再重挂 | PASS：卡片像素 `(227,227,227)` 在重挂后保持 |
| D | `Transparent:true` / `false` | 两种路径在 B 下都不出帧（不是透明合成单一路径）；`Transparent:true` 在 C 下 PASS |
| E | 同进程 / 跨进程宿主 | `DwmSetWindowAttribute(DWMWA_CLOAK)` 对同进程和跨进程的 `WS_CHILD` 都返回 `0x80070006`；渲染问题跨进程复现，同进程修法一致 |
| F | 绑定前后 `DWMWA_CLOAKED` | 绑定前 `1`；C 路径 `0`；B 路径保持 `1` |
| G | `AreDpiAwarenessContextsEqual` 校验 | PASS（96 DPI）；150%/200% NOT TESTED |
| H | 重建 renderer / `SurfaceRenew` | 不需要：C 路径证明原 renderer 可直接绘制到子窗口；未做侵入式重建 |

**证据**

- `probes/cloakdiag`：顶层窗口 `DwmSetWindowAttribute(hwnd, DWMWA_CLOAK=13, ...)`
  → `hr=0x0`；同一窗口置为 `WS_CHILD` 后 → `hr=0x80070006`（`ERROR_INVALID_HANDLE`，
  “The handle is invalid.”）；跨进程同样如此。
- `probes/cloakorder`：在 `WM_STYLECHANGING`（0x007C）里 uncloak → `hr=0x0`、
  `cloak-after=0`；在 `WM_STYLECHANGED`（0x007D）里同样成功；在窗口已经是 child
  之后才 uncloak → `0x80070006`。
- `probes/cloakfix`：`-uncloak-first=false` 时 `cloak=1`、全黑；`-uncloak-first=true`
  时 `child bound (hidden) cloak=0`、`child shown cloak=0`，宿主中心像素稳定为
  `(227,227,227)`。
- MyGo 内部日志（实验副本）在 `reveal composed` 后打印
  `mygo-dbg: uncloak hwnd=0x3a072e hr=0x80070006 err=The handle is invalid.`。

**根因代码路径（MyGo v0.10.0，upstream main 同）**

- `internal/windows/surface.go:224` `newSurface` 创建时调用 `s.cloak()`；
  `:304` `dwmwaCloak = 13`；`:318` `uncloak()` 是唯一清除入口。
- `internal/windows/window.go:1132 Show` / `:1150 ShowInactive` 只在
  `w.surface != nil && w.surface.cloaked && w.surface.drawn && !w.IsVisible()`
  时 uncloak；外部 `SetParent` 到可见宿主后 `IsWindowVisible` 已为真，分支跳过。
- `surface.showing()`（`surface.go:330`，启动 `timerUncloak`）只由
  `window.go:634-638` 的 `WM_WINDOWPOSCHANGING`（`SWP_SHOWWINDOW`）触发；
  `ShowWindow(SW_SHOWNOACTIVATE)` 在这种情况下是 no-op，收不到该消息。
- `surface.go:278` `RequestFrame` 投递 `drawHidden`；`:288` `unpainted()` 为
  `Occluded() || !drawn && !IsVisible()`，对“可见但未绘制”的 child 为 false，
  `drawHidden` 在 `:418-424` 提前返回，`drawn` 永远为 false，`WM_PAINT` 也不到来，
  `reveal()`（`:348`）永不执行。

**Oddward 侧工作区（已验证，stock MyGo v0.10.0）**

`cmd/overlay-poc` 在 `-mode child` 绑定前调用 `uncloakBeforeBind`：先 `SetOpacity(0)`、
在仍为顶层窗口时 `ShowInactive()`，轮询 `IsCloaked` 至 0（最多 2 s），再
`SetChild`；绑定后 `place`、`ShowInactive`、`SetOpacity(1)`。同时 `SetChild` 去掉
`WS_CAPTION|WS_SYSMENU|WS_MINIMIZEBOX|WS_MAXIMIZEBOX`，否则 user32 会在子窗口
客户区上绘制经典标题栏。`scripts/test-overlay-child.ps1` 19/19 通过。

**上游最小修复（实验分支，未提交上游）**

在 `internal/windows/window.go` 的窗口过程增加：

```go
case wmStyleChanging: // 0x007C
    if w.surface != nil && w.surface.cloaked {
        ss := (*styleStruct)(native(lp))
        if ss.New&wsChild != 0 && ss.Old&wsChild == 0 {
            w.surface.uncloak()
        }
    }
    return 0, false
```

配套 `win32.go` 增加 `wmStyleChanging = 0x007C`、`wmStyleChanged = 0x007D` 和
`type styleStruct struct{ Old, New uint32 }`。回归测试
`internal/e2e/child_uncloak_windows_test.go`：无补丁时在 60 s 守卫内失败
（`the child window uncloaks and draws its first frame never happened`）；有补丁时
0.32–1.05 s 通过。完整补丁在 `docs/mygo-child-uncloak.patch`，实验仓库在
`artifacts/mygo-main` 的 `fix/child-uncloak` 分支，commit `e08752d`，未 push、未提交上游。

**建议上游修复位置**

- `internal/windows/window.go`：监听 `WM_STYLECHANGING`/`WM_STYLECHANGED`，在
  `WS_CHILD` 置位前清除 cloak；或提供公开的 `SetParent`/`Embed` API，由框架
  在关系变化时维护 cloak 与首帧状态。
- `internal/windows/surface.go`：让 `unpainted()` 把“已可见但尚未绘制且仍 cloak”
  视为未绘制，并在 `drawHidden` 成功后调用 `reveal()`；同时为 `ShowInactive`
  增加“已可见但 cloak 且已绘制”时的 uncloak 分支。
- 文档：在 `WindowOptions`/`NativeHandle` 文档中明确 `NativeHandle()` 是外层
  `MyGoWindow`，Renderer 是内部 `MyGoSurface` 子窗口；外部 `SetParent` 属于
  受支持场景前，应提供受控 API 而不是让应用直接操作 HWND。

### 8.2 输入区域轮询的固有延迟（非阻断）

`internal/windows/input_region.go` 用 10 ms `SetTimer` + `GetCursorPos` 轮询来决定
`WS_EX_TRANSPARENT`。`SetInputRegion` 用于局部可点击的桌面宠物类窗口时，快速点击
可能在 15.6 ms 系统 tick 边界落到“无窗口”区域。欢迎横幅使用
`SetIgnoreMouseEvents(true)`（全窗口穿透）不受此影响；正式登录卡片应保持整窗可输入。
建议上游提供真正的 per-pixel hit-test 或至少允许应用自行控制 polling 间隔，并在
文档中说明该延迟。

## 9. 迁移计划（若继续）

1. **先决条件**：采用上游 `WM_STYLECHANGING` 补丁（推荐），或在正式适配层保留
   `uncloakBeforeBind` + 去顶层装饰的应用侧处理，并在真实游戏窗口上复测 child 路线；
2. 把 `internal/overlaywin` 的 identity/watcher/HWND 操作收敛为正式适配层，
   只通过 `internal/cnlogin/broker` 的 `BindOverlay`/`EmbedOverlay` 使用，不直接
   暴露任意 Win32 写入；
3. 在 `internal/cnlogin/loginui` 之外新建 MyGo helper 进程，复用现有 frame 协议
   （Model/UiEvent/RequestID），保持 WPF helper 仍可回退；
4. 用真实游戏 + 受限 Broker 重跑本文档全部自动化项，并补充 IME、150%/200% DPI、
   全屏/无边框 z-order、长时间句柄增长测试；
5. 通过后才删除 `compat/loginui` 的 WPF payload；在任何阶段保留回退开关。

## 10. 复现产物

- 自动化报告：`artifacts/overlay-poc/report.json`（owner）、`artifacts/overlay-poc/child-report.json`（native child）
- 测试日志：`artifacts/overlay-poc/test-run.log`
- 截图：`artifacts/overlay-poc/login2.png`、`artifacts/probe/*.png`
- 体积报告：`artifacts/size/size-report.json`
- MyGo 回归实验：`docs/mygo-child-uncloak.patch`（可直接应用到 upstream `a0eaaaa`），实验仓库在 `artifacts/mygo-main` 的 `fix/child-uncloak` 分支（本地，不随 PR 提交）

这些文件均位于 `.gitignore` 的 `artifacts/` 下，不随 PR 提交；PR 以本文的表格和
可复现脚本作为测试证据。
