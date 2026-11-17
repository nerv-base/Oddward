# 构建与发布

## 环境

- Windows 10/11 x64、PowerShell 7。
- Go **1.27.3 windows/amd64**。
- .NET SDK **10.0.303**，由 `compat/loginui/global.json` 固定。
- 创建安装器时还需要 Inno Setup 6。本机请自行安装，或通过 `ODDWARD_ISCC` 指定 `ISCC.exe`；`scripts/install-inno.ps1` 会查找已有安装，仅在 CI 环境允许自动安装。

从 [Go 官方下载页](https://go.dev/dl/) 获取工具链并核对官方校验值。可以安装到 PATH，也可以将压缩包解压到项目的 `.tools/go1.27.3`，使 Go 位于 `.tools/go1.27.3/go/bin/go.exe`。

`scripts/go.ps1` 优先使用项目内工具链，否则使用 PATH 中的同版本 Go。脚本固定 `windows/amd64`、关闭 cgo，并禁止隐式切换工具链；环境设置只对当前调用生效。

.NET SDK 从 [Microsoft 官方页面](https://dotnet.microsoft.com/download/dotnet/10.0) 安装。登录窗口的运行库版本由项目文件固定，恢复包只使用项目配置中的官方 NuGet 源。

## 开发构建

在项目根目录执行：

```powershell
pwsh -File scripts/go.ps1 mod download
pwsh -File scripts/build-launcher.ps1
.\artifacts\native-launcher\oddward.exe
```

构建脚本会：

1. 自包含发布 WPF 登录窗口。
2. 将窗口程序及其运行库嵌入 Go 启动器。
3. 使用 `mygo_noinspector loginui_embed` 标签构建 Windows GUI 程序。
4. 将许可证与第三方声明一同放入输出目录。

可以通过 `-Output` 指定目标文件。分发时保留输出目录中的许可证文件，不要只拿走可执行文件。

```powershell
pwsh -File scripts/build-launcher.ps1 -Output artifacts/local/Oddward.exe
```

不传 `-Version` 的产物为开发构建，不检查更新。

## 单独开发登录窗口

```powershell
pwsh -File compat/loginui/Build.ps1
pwsh -File compat/loginui/Test.ps1
```

不带 `loginui_embed` 标签的 Go 开发构建会从可执行文件旁的 `login-compat` 目录读取助手，也可以通过 `-login-ui` 指定自包含发布后的程序。Go 负责登录流程与账号存储，WPF 仅负责界面和窗口呈现。

## 应用图标

原创品牌资源位于 `internal/branding`。PNG 用于原生窗口，单色 SVG/PNG 用于界面，ICO 包含 16–256 像素的九个尺寸，供 EXE、登录助手和安装器使用。

修改 ICO 后运行：

```powershell
pwsh -File scripts/update-icons.ps1
```

脚本固定使用 MIT 许可的 [`github.com/akavel/rsrc` `v0.10.2`](https://github.com/akavel/rsrc/tree/v0.10.2)，更新两个入口中的 `icon_windows_amd64.syso`。将源图与生成资源一同提交；普通 Go 构建会自动链接这些资源，无需额外安装生成器。此工具仅在图标更新时运行，不是运行时依赖。

## 常规检查

```powershell
pwsh -File scripts/go.ps1 test -count=1 ./...
pwsh -File scripts/go.ps1 vet ./...
pwsh -File scripts/go.ps1 mod tidy -diff
```

修改 WPF 时还应运行 `compat/loginui/Test.ps1`。窗口回归需要可交互的 Windows 桌面；CI 的无游戏构建不能替代本机窗口检查。

## 发行包

安装后的布局由启动桩和版本目录组成：

```text
Oddward/
  Oddward.exe
  app-X.Y.Z/
    .ready
    oddward-launcher.exe
    licenses/
```

安装器完成安装时写入 `.ready`，启动桩只接受带该标记的版本目录。

只准备安装器输入的暂存目录：

```powershell
pwsh -File scripts/build-release.ps1 -Version 0.1.0 -Unsigned -NoInstaller
```

此命令生成 `stage/`，不签名、不编译安装器，`publish/` 为空。暂存目录没有 `.ready`，其中的启动桩不能直接启动该版本；本地运行请使用前面的独立开发构建。

正式更新包要求 `internal/buildinfo.UpdatePublicKey` 已配置公钥，签名私钥通过 `ODDWARD_UPDATE_KEY` 提供。私钥不放入源码、构建输出或日志。密钥初始化脚本为 `scripts/new-update-key.ps1`，应仅由维护者在受控环境中运行并保留离线备份。

```powershell
pwsh -File scripts/build-release.ps1 -Version X.Y.Z
```

输出在 `artifacts/release/X.Y.Z/`。发行内容包含 GPL 许可证和第三方声明；发布二进制时还需提供对应源码。

[Release 工作流](../.github/workflows/release.yml)由 `vX.Y.Z` 标签触发，从源码构建并创建草稿 Release。维护者检查后再手动发布。不要把开发构建、未签名包或自动化构建当作正式版本。

## 自更新

只有安装布局中的正式版本，且编译时配置了更新公钥，才启用自更新。更新先验证签名和逐文件哈希，再切换版本目录；启动失败时可回退。开发构建或缺少公钥时不会接受更新。

当前更新源为 [nerv-base/Oddward Releases](https://github.com/nerv-base/Oddward/releases)。GitHub 镜像只改变传输地址，不跳过签名或哈希校验。
