# MyTizi Windows 版

与 macOS 版功能一致的 Windows 客户端：粘贴订阅链接，一键连接。Go 编写，界面用 [Wails v3](https://wails.io)（WebView2），在 macOS / Linux 上就能交叉编译出安装包。

## 组成

```
MyTizi.exe（普通用户，界面 + 托盘 + 命令行诊断）
  internal/appcore   状态机：订阅、连接、轮询、诊断、更新提示（macOS 版 AppModel 的移植）
  cmd/mytizi         Wails 界面、托盘、平台胶水（剪贴板、开机启动、资源管理器）
        │  命名管道 \\.\pipe\MyTizi.Helper（SYSTEM/管理员/本机登录用户可连，客户端会核对管道属主）
        ▼
mytizi-service.exe（Windows 服务 MyTiziHelper，LocalSystem，开机自动启动，没连接时空闲）
  internal/service   看管 mihomo：校验参数 → 生成配置 → mihomo -t → 启动（作业对象）→ 等控制接口 → 等 TUN 网卡
        │
        ▼
core\mihomo.exe（自带 Wintun）→ 虚拟网卡 "MyTizi" 198.18.0.1，strict-route 拦截其他网卡上的 DNS 与 IPv6
```

共享包：`config`（配置生成，规则阶梯与 macOS 版相同）、`rules`（域名清单，测试会逐项对比 `Sources/MytiziCore/RuleCatalog.swift`）、`subscription`、`probe`（用内置核心解析订阅）、`mihomo`（控制接口客户端）、`diag`（诊断报告与实机自检）、`update`、`redact`、`ipc`、`svcctl`。

与 macOS 版的关键差异和理由见 [docs/DESIGN.md](../docs/DESIGN.md) 的「Windows 版」一节。

## 文件位置

| 内容 | 路径 |
| --- | --- |
| 程序、服务、核心、地理库、许可证 | `C:\Program Files\MyTizi\` |
| 核心工作目录（含控制密钥，仅 SYSTEM/管理员） | `C:\ProgramData\MyTizi\core-home\` |
| 服务与核心日志（用户可读） | `C:\ProgramData\MyTizi\logs\{helper.log, mihomo.log}` |
| 设置、订阅原文、自定义规则 | `%APPDATA%\MyTizi\` |
| App 日志、WebView2 数据 | `%LOCALAPPDATA%\MyTizi\` |

## 开发

需要 Go 1.26+。下面的命令都在 `windows/` 目录下运行。

```sh
go run ./tools/fetchcore          # 按 ../core.lock 下载并校验 Windows 版 mihomo、地理库（放到 ../Vendor/core）
go test ./...                     # 单元测试；配置校验、订阅解析、服务的进程管理都用真实 mihomo
GOOS=windows go vet -composites=false ./...
```

在 macOS 上预览界面（不开 TUN，不需要 root；用户数据放在 `~/Library/Application Support/MyTizi Windows Dev`，不会碰 macOS 版）：

```sh
mkdir -p /tmp/mt-core && ln -sf "$PWD/../Vendor/core/mihomo-arm64" /tmp/mt-core/mihomo
ln -sf "$PWD/../Vendor/core/geoip.metadb" "$PWD/../Vendor/core/geosite.dat" /tmp/mt-core/
MYTIZI_CORE_DIR=/tmp/mt-core MYTIZI_DEV_NO_TUN=1 go run ./cmd/mytizi-service run &
MYTIZI_CORE=/tmp/mt-core/mihomo go run ./cmd/mytizi
```

前端是 `cmd/mytizi/frontend/` 里的纯 HTML/CSS/JS（无构建步骤）。直接用浏览器打开 `index.html?state=connected` 等可以看各个状态的假数据预览。图标由 `scripts/make-icons.sh` 生成。

## 打包

```sh
VERSION=0.4.0 scripts/build.sh            # 两个架构；需要 makensis（brew install makensis / apt install nsis）
ARCHES=amd64 NSIS=0 scripts/build.sh      # 只编译并拼装目录，不生成安装包
```

产物在 `dist/`：`MyTizi-<版本>-windows-{x64,arm64}-setup.exe` 与 `SHA256SUMS-windows.txt`。安装包没有代码签名，首次运行会被 SmartScreen 拦一下（「更多信息 → 仍要运行」）。

安装程序（`build/installer.nsi`）：关掉旧 App、停服务 → 复制文件 → 缺 WebView2 运行时就静默补装 → `mytizi-service.exe install` 注册并启动服务 → 快捷方式与卸载项。卸载时 `mytizi-service.exe uninstall` 断开连接、删除服务、清理 `ProgramData\MyTizi`，用户的订阅与设置保留。

## CI

`.github/workflows/windows.yml`：

1. **单元测试**在 Windows 虚拟机上跑（包含 Windows 专属代码：目录权限、作业对象、命名管道）。
2. **构建**在 Linux 上交叉编译两个架构的安装包。
3. **端到端测试**在全新的 Windows 虚拟机上静默安装，然后 `e2e/e2e_test.go` 用本机另一个 mihomo 当"节点"，验证：服务与目录权限、TUN 网卡、系统 DNS 返回 fake-ip、直发 8.8.8.8:53 被劫持、国内直连与海外走节点（对照核心日志）、`--diagnose` 自检、核心被杀后网络自动恢复、服务被强杀时核心随之结束并自动重启、断开后复原；`e2e/gui-smoke.ps1` 启动真实界面截图（引导页、已连接）；最后卸载并确认服务、文件、网卡都清理干净。截图与日志作为 artifact 上传。
4. 推送 `v*` 标签时把安装包附加到同名 Release（Release 需要先建好）。

## 发布

1. 确认 `main` 上 Windows 工作流是绿的。
2. 和 macOS 版一样建好 Release（tag `vX.Y.Z`）。推送 tag 后工作流会把 `MyTizi-X.Y.Z-windows-*-setup.exe` 与校验文件附加上去；App 的"检查更新"会在 Release 页面里找对应架构的安装包。
