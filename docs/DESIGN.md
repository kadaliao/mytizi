# MyTizi 设计说明

一个极简的 macOS TUN 代理客户端：粘贴一个订阅链接，点一个按钮，其余（规则、DNS、IPv6、国内 CDN）都不让用户面对。

## 1. 目标与非目标

**目标**

- 只需要一个远端订阅链接；图形界面只有「连接 / 断开」「线路」「订阅信息」「遇到问题」。
- 以 TUN 方式接管系统流量；内置 GeoIP/GeoSite 兜底；连接期间关闭 IPv6；国内常用平台及其 CDN 固定直连。
- 出问题时一键复制「运行情况 + 日志位置 + 自动结论」交给 AI 排查。

**非目标（v1）**

- 多订阅、多配置切换；自定义规则 UI（只有一个文本文件入口，面向高级用户）。
- 使用订阅里自带的规则/策略组（只取节点）。
- Network Extension / 系统扩展（见 §2）。
- 自动更新 App、开机自启、公证（没有 Developer ID）。

## 2. 关键决策

| 决策 | 选择 | 理由 |
| --- | --- | --- |
| 如何做 TUN | 内置 **mihomo** + 一次性安装的 **root LaunchDaemon helper** | 开发者账号只有 Apple Development，没有 Developer ID，Network Extension 无法分发给别人；"内置核心 + 特权 helper"是同类工具里已经被验证稳定的路线。 |
| 提权方式 | 首次连接弹**一次**管理员授权，安装 helper；之后零弹窗 | 旧方案靠 `sudo` 凭据缓存，GUI 进程没有终端，需要伪终端 + askpass 绕一圈；helper 常驻后启停都不再需要密码。 |
| 订阅格式 | **交给 mihomo 自己解析**（`file` provider） | Clash YAML、base64 链接列表、ss/vmess/vless/trojan/hysteria2… 全部支持，App 不实现任何解析；`CoreProbe` 起一个无监听端口的临时实例读出节点，和真正连接时用同一个解析器。 |
| 订阅由谁下载 | **App 下载**，mihomo 只读本地文件 | 能给出"链接失效/拿到网页/服务器拒绝"这类人话错误；能离线用缓存；避免连接时"先有鸡还是先有蛋"。 |
| DNS | `fake-ip` + TUN `dns-hijack` + **系统 DNS 指向 198.18.0.2** | 路由器 DNS 在同一网段（on-link），包不会进 TUN，必须改系统 DNS 才能保证域名规则生效。`.2` 是 mihomo fake-ip 池保留地址，不会被分配。 |
| IPv6 | 核心 `ipv6:false`、DNS 不返回 AAAA、**helper 在系统层关闭**（带恢复） | TUN 只接管 IPv4，应用拿到 IPv6 地址会直接绕过规则（真实案例：twimg.com 解析到 IPv6 地址后绕过 TUN，连接被重置）。 |
| 地理库 | `geoip.metadb` + `geosite.dat` **随 App 内置**，`geo-auto-update:false` | 核心首次启动联网下载会卡住/失败；用户在国内最需要它的时候恰恰下不到。 |
| 配置格式 | 自己的小型 YAML 输出器，所有字符串加引号 | 配置只写不读，不引入 YAML 库；避免 `no`/`on`/`+.lan` 被当成别的类型。 |
| 工程形态 | SwiftPM（无 xcodeproj）+ 脚本拼装 `.app` | 不需要维护 xcodeproj，命令行即可构建/测试/打包。 |

## 3. 架构

```
MyTizi.app（SwiftUI，普通用户）
 ├─ AppModel          状态机：订阅 / 连接 / 轮询 / 诊断
 ├─ MytiziCore        订阅抓取、CoreProbe、ConfigBuilder、规则目录、诊断、HelperClient/Installer
 └─ Contents/
     ├─ Helpers/mytizi-helper          ┐
     └─ Resources/core/{mihomo,        │ 首次连接时由管理员授权安装到系统目录
        geoip.metadb,geosite.dat,      │
        components.json}               ┘
                │  Unix socket /var/run/mytizi.sock（0600，属于安装者；每个连接再校验 uid）
                ▼
com.mytizi.helper（root LaunchDaemon，KeepAlive）
 ├─ CoreSupervisor    写配置 → mihomo -t 校验 → 启动 → 等控制接口 → 等 TUN 网卡 → 接管网络
 ├─ NetworkGuard      DNS / IPv6 的接管与恢复（恢复点先落盘再修改）
 └─ mihomo（root，TUN）  控制接口只监听 127.0.0.1，密钥每次连接随机
```

系统位置：

| 内容 | 路径 |
| --- | --- |
| helper / plist | `/Library/PrivilegedHelperTools/com.mytizi.helper`、`/Library/LaunchDaemons/com.mytizi.helper.plist` |
| 核心、地理库、工作目录 | `/Library/Application Support/MyTizi/{bin/mihomo, core-home/}` |
| 组件清单 | `/Library/Application Support/MyTizi/components.json` |
| 网络恢复点 | `/Library/Application Support/MyTizi/network-state.json` |
| 日志（用户可读） | `/Library/Logs/MyTizi/{helper.log, mihomo.log, helper.stderr.log}`、`~/Library/Logs/MyTizi/app.log` |
| 用户设置 | `~/Library/Application Support/MyTizi/{settings.json, subscription.txt, custom-rules.txt, last-config.redacted.yaml}` |

组件是否需要更新由 `components.json`（各二进制的 SHA-256）与包内清单对比决定，而不是版本号：改了任何一个文件都会触发"更新组件"，且只弹一次授权。

## 4. 规则阶梯

先匹配先生效（实现：`ConfigBuilder.rules`，顺序由测试固定）：

1. **自定义规则**（`custom-rules.txt`，高级用户的覆盖口，带行号校验）
2. **订阅主机 DIRECT** —— 节点全挂时仍能刷新订阅
3. **内网/局域网 DIRECT**（含 Apple 推送 TCP 5223）
4. **国内平台与 CDN 域名 DIRECT**（`RuleCatalog.directGroups`，纯域名，不依赖 DNS）
5. **海外固定域名 → PROXY**（`RuleCatalog.proxyGroups`）—— 先于 GeoSite，防止数据库过期或 DNS 污染
6. `GEOSITE,apple@cn / microsoft@cn / cn` DIRECT → **UDP 443 REJECT**（迫使 QUIC 回退 TCP）→ `GEOSITE,geolocation-!cn` PROXY
7. **`GEOIP,CN` DIRECT** —— 兜底：域名没命中任何列表时按解析出的 IP 归属判断
8. `MATCH` → PROXY

为什么国内 CDN 不能只靠 `GEOIP,CN`（继承自同类工具的踩坑记录）：

- 国内平台的图片/视频 CDN 通过 HTTPDNS 和多 CDN 调度，GEOIP 判定会在 DIRECT 与代理之间漂移，表现为"接口正常、图片/视频反复失败"。
- 微信图片依赖自家 HTTPDNS 取 CDN 地址后**按 IP 直连**，SNI/Host 只是占位：嗅探器的目标改写会把请求送到别的 CDN 集群，所以 `weixin.qq.com` 在 `sniffer.skip-domain` 里。
- 飞书 API 与它的图片 CDN（`feishucdn.com`、`byteimg.com`…）必须同一出口。
- YouTube/Gemini 媒体/Claude 在国内 DNS 污染最严重，解析结果一旦被判成 CN 就直连失败，所以用域名固定走代理。

`RuleCatalog` 的测试保证：格式合法、无重复、无被父域名覆盖的冗余项、直连与代理清单互不遮蔽、上述关键域名一定在列。

## 5. 网络接管与崩溃安全

连接成功的判据是**内核层面的证据**：控制接口应答 + `getifaddrs` 里真的出现 `198.18.0.1` 的 utun 网卡，而不是"进程起来了"。

`NetworkGuard` 的顺序约束：

1. 先对所有已启用、挂在 `enN/bridgeN` 上的网络服务做**快照**（DNS 与 IPv6 设置，手动 IPv6 连地址/前缀/路由器一起记）；
2. **恢复点写盘并 fsync**；
3. 才修改系统；中途任何一步失败，整体回滚，不留半改状态；
4. 恢复全部成功才删除恢复点，否则保留，下次 helper 启动继续重试。

触发恢复的时机：用户断开、核心**非预期退出**（立即）、helper 收到 SIGTERM（关机/卸载）、helper 启动时发现遗留恢复点（崩溃/断电后）。停止时先还原 DNS 再杀核心，避免应用解析到已无人应答的地址。连接期间每 20 秒补接管新插入的网卡（幂等，已记录的服务不会被二次快照覆盖）。

## 6. 安全模型

- helper 只服务安装它的那个用户：socket 文件 `0600` 且属于该用户，每个连接再用 `getpeereid` 校验 uid；helper 拒绝 `--owner-uid 0` 启动。
- helper 只接受固定形状的请求；写入文件路径必须匹配 `^providers/[A-Za-z0-9._-]{1,64}$`；配置先过 `mihomo -t` 才启动。
- 核心、地理库、helper 都复制到 root 拥有的系统目录后才执行，不从用户可写位置执行。
- 安装脚本以内联方式通过 `osascript … with administrator privileges` 执行（不落临时文件，避免 TOCTOU）；安装前先校验包内文件与清单哈希一致。
- **已知的信任边界**：能以该用户身份读 App 内存/设置的进程，等价于能控制 helper 与 mihomo 控制接口（与 Clash Verge 等同类产品相同）。控制密钥每次连接随机，只存在于内存与 root 目录，不写入用户目录。
- 订阅链接（含 token）以 `0600` 存于 `settings.json`；诊断报告一律脱敏（见 §7）。

## 7. 「遇到问题 → 复制诊断信息」

目标：拿到这段文本的 AI **不需要任何背景**就能开始排查。入口：主界面底部「遇到问题」、失败卡片上的按钮、菜单栏、`⌘⇧C`；命令行 `MyTizi --diagnose [--full]`（给没有图形界面的 AI/同事）。

报告结构（`DiagnosticsReport`）：

1. **给 AI 的说明**：架构要点（TUN 地址、DNS 指向、规则顺序、日志格式 `match X using Y`）+「现象 → 可能原因」对照表；
2. **自动识别的结论**（`DiagnosticsAnalyzer`，纯函数）：组件未装/过期、核心崩溃、找不到 TUN 网卡、**另一个 TUN 抢同一地址段**、系统 DNS 未指向 TUN、**断开后 DNS 仍指向 198.18.0.2（会断网）并给出修复命令**、IPv6 仍开、系统代理指向死端口、**失效的 `/etc/resolver/*`**、其他代理/VPN 进程、订阅过期/流量用尽/超过 7 天未更新、所有线路超时、核心日志高频错误签名；
3. 当前状态、订阅（仅主机）、线路延迟；
4. **实机自检**：经真实网络栈访问国内站、国内 CDN（腾讯/阿里）、海外站，并对照核心日志确认**实际命中的规则与出口**，与预期不符会标出；
5. 系统网络事实（默认路由、scutil DNS、各服务 DNS/IPv6、系统代理、utun、`/etc/resolver`、冲突进程）；
6. **所有文件与日志的绝对路径 + 可直接执行的命令**（能读文件的 AI 可以自己继续查）；
7. 日志尾部：App、helper、核心的警告与错误。「完整版」另含最近 200 行连接日志（含访问过的域名，发送前请自行确认）。

脱敏（`Redactor`）：订阅链接及其 token、控制密钥按原文精确替换；`token=`/`Authorization`/`password`/协议链接/UUID 按模式替换；家目录替换为 `~`。保留域名与 IP（排查所需），摘要版不含连接日志。

## 8. 代码结构

```
Sources/
  MytiziKit/        共享：AppInfo、SystemPaths、IPC 协议、Unix socket、Shell、RotatingLog
  MytiziCore/       YAML、RuleCatalog、ConfigBuilder、SubscriptionService、CoreProbe、MihomoAPI、
                    HelperClient/Installer、SystemFacts、SelfTest、Diagnostics*、Redactor
  MytiziHelperKit/  NetworkSetup(解析)、NetworkGuard、CoreSupervisor、HelperServer
  MytiziHelper/     helper 入口（LaunchDaemon）
  MyTizi/           SwiftUI：AppModel、Views、App、命令行诊断、DEBUG 快照渲染
scripts/            fetch-core.sh（拉取并校验核心/地理库）、build-app.sh、make-icon.swift
core.lock           固定的 mihomo 版本与 SHA-256
```

## 9. 验证状态（诚实版）

**已自动化验证**（`swift test`，83 项，约 4 秒）

- 配置：生成的配置通过真实 mihomo v1.19.32 的 `-t`；规则阶梯顺序、IPv6 全关、TUN/DNS 默认值。
- 订阅：本地 HTTP 服务器上的真实 `URLSession` 抓取（头部、HTTP 错误、网页、不可达）；**Clash YAML 与 base64 的 ss/trojan/vmess 列表都由真实 mihomo 解析出节点**；垃圾内容得到 0 节点。
- helper 生命周期（真实 mihomo，不开 TUN）：启动/停止/重启、**核心被 `kill -9` 后立即还原 DNS/IPv6**、先还原 DNS 再停核心、配置无效不碰网络、socket 访问控制（外来 uid 被拒）、路径穿越被拒。
- 网络接管：在有状态的 networksetup 模拟器上验证快照、回滚、崩溃后由新实例恢复、二次接管不覆盖原始值、恢复失败保留恢复点。
- 安装脚本：plist 经 `plutil -lint`；脚本语法；**把系统路径替换到临时目录后真实执行**，校验文件布局、权限与"清单最后写、最后才启动"。
- 诊断：解析器用真实机器输出做样本；脱敏不泄漏；每条自动结论都有对应测试。
- 打包：通用二进制（arm64+x86_64）、ad-hoc 签名通过 `codesign --verify`、清单哈希与包内文件逐一吻合、dmg 校验通过、App 能启动。

**真机验证**

维护者已在真机上试用，基本流程（添加订阅、连接、使用、断开）正常。以下场景还没有系统化的回归记录，改动相关代码后请按 [TESTING.md](TESTING.md) 手动验证：

- 系统层 `networksetup -setv6off/-setv6automatic` 与 DNS 设置/恢复的边界情况（手动 IPv6、多网卡、崩溃恢复）；
- 睡眠唤醒、Wi-Fi/有线切换；与其他 TUN 共存（必须先退出其他会开 TUN 的代理软件）；
- Intel 机器，以及全新机器上 Gatekeeper 的「仍要打开」流程。

## 10. 后续

- 联调后根据真实日志调整 DNS/嗅探细节；
- 自启动与开机自动连接（`SMAppService`）、App 内更新；
- 订阅里的规则可选启用；多订阅；
- 「线路」自动故障转移的更多反馈（目前由 `url-test` 负责）。
