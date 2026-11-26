# 参与贡献

开发前请阅读 [AGENTS.md](AGENTS.md) 和[架构说明](docs/architecture.md)。环境与构建方式见[构建文档](docs/building.md)。

## 项目边界

- 保持 Go + MyGo Native UI；主 UI 不提权，不改用 WebView 或 Electron。
- 不绕过认证、实名、防沉迷或反作弊，不向游戏注入，不明文保存凭据。
- 不提交官方二进制或媒体、账号数据、原始用户日志、内部调研和临时实验材料。
- 新依赖固定版本，确认许可证兼容，并更新第三方声明。
- 不对日常使用的游戏安装做自动化破坏性操作。真实游戏测试需使用明确的隔离方案并得到维护者授权。

## 开发流程

1. 较大的改动先开 issue 说明问题，小修正可直接提 PR。
2. 行为改动先补回归测试，再实现；测试使用合成数据和临时目录。
3. 界面改动检查实际布局与无障碍行为，截图只使用自制占位素材，不含账号信息或官方媒体。
4. 行为、构建方式或目录结构发生变化时，同步更新相关文档。

## 提交前

```powershell
pwsh -File scripts/go.ps1 test -count=1 ./...
pwsh -File scripts/go.ps1 vet ./...
pwsh -File scripts/go.ps1 mod tidy -diff
gofmt -l cmd internal tools
```

使用项目内工具链时，可将最后一条替换为 `.tools/go1.27.3/go/bin/gofmt.exe -l cmd internal tools`。格式和 tidy 检查应无输出。

登录窗口与欢迎横幅使用纯 Go + MyGo Native UI（`internal/cnlogin/loginui/native`），由 `go test` 直接覆盖。删除代码或调整包结构时，补充架构文档中的[静态检查](docs/architecture.md#静态检查)。[Windows CI](.github/workflows/windows.yml)负责测试、静态检查、启动器及安装布局构建。

## 提交与 PR

- 一个 PR 聚焦一项改动，说明问题、实现和测试范围。
- 不把编译成功或合成测试等同于真实游戏结果。
- 新增原创 Go、PowerShell 文件添加 `SPDX-License-Identifier: GPL-3.0-or-later`。
- 更新签名公钥和发布流程由维护者管理；不提交任何签名私钥。

提交贡献即表示同意以 [GPL-3.0-or-later](LICENSE) 发布，并确认有权提交相关内容。第三方代码必须保留原声明，记录到 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
