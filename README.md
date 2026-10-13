# MyTizi

极简 macOS TUN 代理客户端：**粘贴一个订阅链接，点一个按钮**。规则、DNS、IPv6、国内 CDN 都不用管。

- 订阅支持 Clash YAML 与 base64 链接列表（ss / vmess / vless / trojan / hysteria2 …），由内置 mihomo 解析。
- 内置 GeoIP / GeoSite 数据库，国内平台及其 CDN 固定直连，其余按 GeoSite → GeoIP 兜底，最终走代理。
- 连接期间自动关闭 IPv6、接管系统 DNS，断开或崩溃时自动恢复。
- 遇到问题：**「遇到问题 → 复制诊断信息」**，粘贴给 AI 即可排查。

设计与取舍见 [docs/DESIGN.md](docs/DESIGN.md)。

## 使用

1. 把 `MyTizi.app` 拖进「应用程序」并打开（应用未经 Apple 公证，首次需在「系统设置 → 隐私与安全性」点一次「仍要打开」）。
2. 粘贴订阅链接 → 「添加并检测」。
3. 点中间的大按钮连接。第一次会请求一次管理员授权来安装后台组件，之后不再询问。

不要同时运行其他会开 TUN 的代理软件（Clash Verge 等），它们会抢路由和 DNS。

## 遇到问题

- 窗口底部「遇到问题 → 复制诊断信息（给 AI 排查）」，或菜单栏、`⌘⇧C`。
- 失败时错误卡片上直接有「复制诊断信息」按钮。
- 「复制完整诊断」额外包含最近的连接日志（含访问过的域名，发送前请自行确认）。
- 没有图形界面时：`MyTizi.app/Contents/MacOS/MyTizi --diagnose [--full] [--no-selftest]` 直接打印报告。
- 日志位置：`/Library/Logs/MyTizi/mihomo.log`（每条连接命中的规则）、`/Library/Logs/MyTizi/helper.log`、`~/Library/Logs/MyTizi/app.log`。

报告已脱敏：订阅链接、控制密钥、节点密码与 UUID 不会出现。

自定义规则（高级）：「⋯ → 高级设置 → 编辑自定义规则」，每行 `类型,值,目标`，目标可写 `PROXY`/`DIRECT`/`REJECT`（或 代理/直连/拒绝），优先于所有内置规则，重新连接生效。

## 开发

需要 macOS 13+、Xcode（Swift 5.9+）。

```sh
scripts/fetch-core.sh          # 下载并校验内置 mihomo 与 GeoIP/GeoSite 到 Vendor/core/
swift build                    # 编译
swift test                     # 全部测试（需要先运行 fetch-core.sh，否则依赖核心的测试会跳过）
ARCHS=arm64 scripts/build-app.sh   # 打包本机架构 → dist/MyTizi.app
DMG=1 scripts/build-app.sh         # 通用二进制 + dmg
```

升级核心：`scripts/fetch-core.sh --update-core` 改写 `core.lock`，重新跑测试后提交。`--refresh-geo` 强制刷新地理库（每次打包都会取当天最新）。

调试界面（仅 DEBUG 构建，不需要录屏权限）：

```sh
swift build && .build/debug/MyTizi --snapshot /tmp/shots   # 各状态、亮/暗色渲染为 PNG
```

> 截图里黄色 🚫 方块是 `ImageRenderer` 对 AppKit 控件（菜单、输入框）的占位符，不是真实界面。

改名：`AppInfo.displayName` / `bundleID` / `helperLabel`（`Sources/MytiziKit/AppInfo.swift`）与 `scripts/build-app.sh` 顶部的 `APP_NAME`、`BUNDLE_ID`。

## 联调清单（需要 root、网络干净；首次真机验证时按顺序做）

> ⚠️ 先退出其他代理软件（Clash Verge 等）。两个 TUN 同时存在会互相抢路由与 DNS，联调结果没有意义。

1. 打开 `dist/MyTizi.app`，添加订阅，点连接并完成授权。
2. 确认接管生效：
   ```sh
   ifconfig | grep -B1 "198.18.0.1"          # 出现 utunN
   scutil --dns | sed -n 1,12p               # 第一个 resolver 的 nameserver 是 198.18.0.2
   networksetup -getinfo Wi-Fi | grep IPv6   # IPv6: Off
   tail -n 20 /Library/Logs/MyTizi/mihomo.log
   ```
3. 用「遇到问题 → 复制诊断信息」，看「实机自检」表：国内站/国内 CDN 应为直连，海外站应走代理，DNS 应为 fake-ip。
4. 实际使用：浏览器、微信（含图片）、飞书、YouTube、终端 `curl`。
5. 断开，确认恢复：
   ```sh
   networksetup -getdnsservers Wi-Fi         # 回到原值（通常是 "There aren't any DNS Servers set"）
   networksetup -getinfo Wi-Fi | grep IPv6   # 回到 Automatic
   ls "/Library/Application Support/MyTizi/network-state.json"   # 应不存在
   ```
6. 崩溃恢复：连接后 `sudo pkill -9 mihomo`，几秒内网络应自动恢复，App 显示「连接意外中断」。
7. 睡眠/唤醒、Wi-Fi 与有线切换后网页应能继续打开。
8. 卸载：「⋯ → 高级设置 → 卸载后台组件」。

## 第三方组件

- [mihomo](https://github.com/MetaCubeX/mihomo)（GPL-3.0）：构建时由 `scripts/fetch-core.sh` 按 `core.lock` 下载并校验 SHA-256，**不包含在本仓库里**；分发打包后的 `.app`/`.dmg` 时需遵守它的许可证。
- [meta-rules-dat](https://github.com/MetaCubeX/meta-rules-dat)：`geoip.metadb`、`geosite.dat`，同样在构建时下载。

## 已知限制

- 未经 Apple 公证（没有 Developer ID），首次打开需手动放行。
- 只支持一个订阅；订阅里自带的规则不会被使用。
- 暂无开机自启和 App 内更新。
- 上面的联调清单尚未在真机上跑过，详见 [docs/DESIGN.md §9](docs/DESIGN.md)。
