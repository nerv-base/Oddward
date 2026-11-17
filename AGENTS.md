# Oddward 开发约定

Windows《异环》非官方启动器。Go 1.27.3 windows/amd64 + MyGo Native UI，原创代码 GPL-3.0-or-later。唯一产品入口是 `cmd/launcher`；安装版由 `cmd/stub`（`Oddward.exe`）按版本目录启动它，见 [docs/architecture.md](docs/architecture.md#自更新)。

## 动手之前

- 先读 [docs/architecture.md](docs/architecture.md)：分层、依赖方向、新代码该放在哪里、依赖许可。
- 项目方向由用户定。仓库里没有长期计划文档；需求不清楚、或改动会碰到下面的硬性边界时，先问用户。

## 硬性边界（不可协商）

以下边界只能由用户修改。

**技术栈**
- 只用 Go 和 MyGo Native UI，不换成 WebView/Electron。主 UI 进程不提权。
- 依赖固定到版本或提交并核对许可证（记录在 [docs/architecture.md](docs/architecture.md#依赖与许可)）。实验性 MyGo API 先做本地集成探针再用。框架改动单独提交，不做侵入式 fork。

**安全与账号**
- 不绕过认证或反作弊，不向游戏注入，不明文保存凭据。只读取必需的非秘密配置。
- 塔吉多（抽卡记录）关联只在用户点击后发起，走官方短信登录；遇到人机验证就停下让用户在官方客户端完成，不重试、不猜验证码。续期凭据用 DPAPI 加密后随账号档案保存。协议固定参数只写在 `internal/tajiduo/identity.go`，不进文档、日志、错误信息或 `artifacts/`。

**用户的游戏安装**
- 不对用户日常使用的安装执行、更新、重命名、删除或注入故障。更新测试只在独立可写的副本上做，副本之间不共享硬链接。
- 真实启动或登录测试必须先有明确的隔离方案，并得到用户授权。
- 同一安装不允许出现不受控的并发写入。
- 更新时，新文件验证通过并完成可恢复提交之前，旧安装必须保持完好。空间不足时拒绝更新，不做不安全的原地修补。多文件依次重命名本身不是原子事务。

**内容与发布**
- 保留 `analysis/`。它是私有的历史研究，既不能发布，也不能当作当前兼容性的证据。
- 不提交、不发布官方二进制、受版权保护的媒体（背景视频、logo、slogan、公告图）、凭据、用户日志或原始机器证据。机器路径、截图和原始日志只放在被忽略的 `artifacts/` 下。脱敏后的汇总结果和公开协议元数据可以写进文档。
- 第三方条款归原作者，保留其声明。发行包里要带上内嵌组件的许可证（见下文「构建」）。
- 不做破坏性 Git 操作（`reset --hard`、`clean`、强推、删分支等）。未经授权不公开 push，不推送发版标签，也不发布 release（发版工作流只建草稿，发布由人完成）。
- 启动器自更新只接受用 `buildinfo.UpdatePublicKey` 验签通过的清单，逐文件校验后才切换版本。不得加入跳过签名的开关；更新签名私钥只存在仓库 secret 和维护者的离线备份里，不进 Git、不写进 `artifacts/`。

## 架构规则

依赖只能单向向下：`cmd/launcher` → `internal/launcher/system` → 领域包；`internal/launcher`（UI）→ `internal/launcher/state`（纯规则）。

| 要加的东西 | 放在哪里 |
|---|---|
| 可见行为、状态流转、按钮文案 | `internal/launcher/state`：只做纯数据处理，不碰 I/O、时钟、goroutine 和 MyGo。先在这里写测试 |
| 后台副作用 | 在 `state` 定义 `Effect`，由 `internal/launcher` 的 `Shell.apply` 分派。结果经有界 channel 送回，在 UI 线程上再 `Dispatch` 成事件 |
| 需要真实系统的能力 | 先在 `internal/launcher` 定义最小接口，在 `internal/launcher/system` 实现，由 `cmd/launcher` 注入。UI 测试用 fake |
| 新领域能力（下载、更新、校验……） | 新建 `internal/<领域>` 包，自带测试，只允许 `system` 引用 |
| 本地实验 | 放在被忽略的本地目录，不进入产品代码或公开源码 |

- 后台操作一律带 `context`。UI 只在 UI 线程修改。
- 进程身份由 PID 和创建时间共同确定。界面上只显示后端真实上报的数据（进度、速度、剩余时间、时长），不推算、不编造。
- 不写空壳抽象：只为真实存在的第二种实现或测试替身才定义接口。
- 删掉不再使用的代码，不要注释掉留着。删除前确认没有引用（用下文的 `deadcode` 和 `staticcheck`）。

## UI 约定

- 背景视频、slogan 和 logo 每个版本都会换，界面必须对任意素材都成立：素材用 contain 方式放置，不假设比例，也不依赖当前活动的配色。
- 强调色一律用 `s.tone()`，它由 `PosterAccent` 从版本主视觉取色，失败时回退到默认黄。不要写死某个颜色的高亮。
- 颜色、尺寸、间距常量集中放在 `internal/launcher/view.go` 顶部。共享控件用 `controls.go` 里的 `quietButton`、`accentButton`、`preferenceSwitch`。
- 每个可交互元素都要有无障碍标签和角色。减少透明度和高对比度模式下要有不透明回退。动效要尊重减少动态效果的设置。
- 自动签到目前是内存演示，界面上必须明确标注为演示。
- 抽卡分析只显示本地档案里的真实记录（`internal/gacha`），不编造记录，不推算「已垫」；未收录的物品名显示原始 id 并标「未收录」，图标取不到时一律用占位。
- 塔吉多只在用户点击「更新记录」时拉取，不做后台定时同步；解除关联会保留本地档案。没有已保存的游戏账号时不提供关联入口。

## 开发流程

1. **测试先行**：先写会失败的生产行为测试，再写实现。分清编译或环境失败与行为失败，只有后者才算「红」。
2. 默认用小型合成夹具，不在单元测试里反复下载完整游戏数据。共享夹具放在各包的 `fixtures_test.go`。
3. UI 测试用 `ui.NewTester` 渲染，断言几何位置、像素颜色和无障碍标签。测试文件按主题命名（如 `home_layout_test.go`、`primary_test.go`），不按「第几轮改版」命名。
4. 改界面后渲染预览图，亲眼检查后再交付（命令见下）。预览输出只放在 `artifacts/`。

## 常用命令

全部经过 `scripts/go.ps1`，它会固定使用项目内的工具链 `.tools/go1.27.3` 和本地缓存（CI 中没有 `.tools` 时改用 PATH 上同版本的 Go）。

```powershell
pwsh -File scripts/go.ps1 test -count=1 ./...               # 全量，约 40 s
pwsh -File scripts/go.ps1 test ./internal/launcher/state/   # 只测规则，最快
pwsh -File scripts/go.ps1 vet ./...
pwsh -File scripts/go.ps1 mod tidy -diff                    # CI 会检查，输出必须为空
.tools/go1.27.3/go/bin/gofmt.exe -l cmd internal tools      # 输出必须为空

# 设计预览（默认跳过；环境变量要和命令写在同一次 pwsh 调用里）
$env:ODDWARD_STATES_OUT="$PWD\artifacts\redesign\states"     # 或 ODDWARD_PREVIEW_OUT 只渲染主页帧
$env:ODDWARD_BACKGROUND_STILL='<bg_0.png>'; $env:ODDWARD_LOGO='<logo.png>'; $env:ODDWARD_SLOGAN='<slogan.png>'
pwsh -File scripts/go.ps1 test ./internal/launcher/ -run 'TestRenderMockStateFrames|TestRenderPreviewFrames' -count=1
```

静态检查工具的固定版本和调用方式见 [docs/architecture.md](docs/architecture.md#静态检查)（staticcheck v0.8.2、deadcode v0.52.0；跑完后执行 `go mod tidy` 还原 go.mod）。

## 构建

- **发行构建**：`pwsh -File scripts/build-launcher.ps1 [-Version X.Y.Z]` 自包含发布并嵌入 `compat/loginui`，使用 `mygo_noinspector loginui_embed` 标签，同时输出完整许可证。需要 .NET SDK 10.0.303；不带版本号的开发构建不检查更新。
- **完整发布包**：`pwsh -File scripts/build-release.ps1 -Version X.Y.Z` 产出安装布局、签名更新包和安装器；`-Unsigned -NoInstaller` 用于本地打包。正式流程由 `vX.Y.Z` 标签触发，只创建草稿 Release。私钥通过 `ODDWARD_UPDATE_KEY` 提供。
- 修改 WPF 后运行 `pwsh -File compat/loginui/Test.ps1`。不带嵌入标签的开发构建读取 exe 旁的 `login-compat\Oddward.LoginUi.exe`，或使用 `-login-ui` 指定。
- `e2e` 标签仅供测试使用。临时 Go 实验应放入被忽略的独立模块，避免进入产品的 `./...`。
- 具体命令与输出布局见 [构建文档](docs/building.md)。测试结果应说明覆盖范围，不把合成测试等同于真实游戏结果。

## 协作

- 并行修改时，各自的写入范围不能重叠。由 lead 审查 diff 并重新跑测试。
- 文档只描述当前代码。行为、构建方式或目录结构变了，要在同一次改动里同步更新 `docs/architecture.md` 和 README。不再成立的计划、调研和验收记录直接删除，历史从 Git 里查，不要另写新的计划文档。
