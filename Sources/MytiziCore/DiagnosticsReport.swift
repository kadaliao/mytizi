import Foundation
import MytiziKit

/// 把诊断输入渲染成一份可以整段粘贴给 AI 的 Markdown。
///
/// 设计目标：拿到这份文本的 AI 不需要任何背景就能开始排查——
/// 所以报告自带"架构说明 + 规则顺序 + 常见现象对照 + 可执行的命令"，
/// 并且把每个文件/日志的绝对路径写出来，能读文件的 AI 可以自己继续往下查。
public enum DiagnosticsReport {
    public static func render(_ input: DiagnosticsInput, redactor: Redactor, includeFullLogs: Bool) -> String {
        let findings = DiagnosticsAnalyzer.findings(input)
        var out: [String] = []

        out.append("# \(AppInfo.displayName) 诊断报告")
        let arch = "\(input.facts.arch)\(input.facts.model.isEmpty ? "" : "，\(input.facts.model)")"
        let generatedAt = "生成时间 \(DiagnosticsAnalyzer.format(input.now))　|　\(AppInfo.displayName) \(input.appVersion)"
        out.append(generatedAt + "　|　macOS \(input.facts.macOS)（\(arch)）")
        out.append("")
        out.append(aiBriefing(includeFullLogs: includeFullLogs))

        out.append("## 1. 自动识别的结论")
        if findings.isEmpty {
            out.append("没有发现明显异常。请结合用户描述的具体现象，再看下面的自检和日志。")
        } else {
            for finding in findings { out.append("- **\(finding.marker)　\(finding.title)**：\(finding.detail)") }
        }
        out.append("")

        out.append("## 2. 当前状态")
        out += statusSection(input)

        out.append("## 3. 订阅（已脱敏）")
        out += subscriptionSection(input)

        if !input.nodes.isEmpty {
            out.append("## 4. 线路")
            out.append("| 名称 | 类型 | 最近延迟 |")
            out.append("| --- | --- | --- |")
            for node in input.nodes.prefix(30) {
                out.append("| \(node.name) | \(node.type) | \(node.delay.map { "\($0) ms" } ?? "不可用/未测") |")
            }
            if input.nodes.count > 30 { out.append("（共 \(input.nodes.count) 个，仅列前 30 个）") }
            out.append("")
        }

        out.append("## 5. 实机自检")
        if input.selfTest.isEmpty {
            out.append("本次没有运行自检（未连接时跳过）。")
        } else {
            out.append("| 项目 | 期望 | 结果 | 实际命中 | 耗时 |")
            out.append("| --- | --- | --- | --- | --- |")
            for test in input.selfTest {
                let result = test.passed ? "✅ 正常" : "❌ " + (test.concern != nil ? "异常" : (test.reachable ? "走向不符" : "不通：\(test.error ?? "无响应")"))
                out.append("| \(test.name) | \(test.expected?.rawValue ?? "-") | \(result) | \(test.observed ?? "-") | \(test.milliseconds.map { "\($0) ms" } ?? "-") |")
            }
            out.append("")
        }

        out.append("## 6. 系统网络")
        out += networkSection(input)

        out.append("## 7. 规则")
        out += rulesSection(input)

        out.append("## 8. 文件位置（能读文件的 AI 可以直接查看）")
        out += filesSection(input)

        out.append("## 9. 日志")
        out += logsSection(input, full: includeFullLogs)

        return redactor.redact(out.joined(separator: "\n"))
    }

    // MARK: 各段

    static func aiBriefing(includeFullLogs: Bool) -> String {
        """
        > ## 给 AI 的说明
        > \(AppInfo.displayName) 是 macOS 上的极简 TUN 代理客户端，内置 mihomo 核心。请基于下面的信息帮我排查问题：先给结论，再给我可以逐条执行的步骤；需要更多信息时告诉我执行哪条命令。
        >
        > **架构要点**
        > 1. 一个以 root 运行的后台组件（LaunchDaemon `\(AppInfo.helperLabel)`）启动 mihomo。TUN 网卡地址 `\(AppInfo.tunGateway)`，fake-ip 地址段 `198.18.0.0/16`。
        > 2. 后台组件把各网络服务的 DNS 改为 `\(AppInfo.tunDNS)`（让解析进入 TUN），并在系统层关闭 IPv6；断开或核心崩溃时从恢复点 `\(SystemPaths.networkState)` 还原。
        > 3. 规则顺序（先匹配先生效）：自定义 → 订阅主机直连 → 内网直连 → 国内平台与 CDN 域名直连 → 海外固定域名走代理 → GeoSite 国内直连 → UDP 443 拒绝（迫使 QUIC 回退到 TCP）→ GeoSite 海外走代理 → `GEOIP,CN` 直连（兜底）→ 其余走代理。
        > 4. 订阅由 App 抓取后交给 mihomo 作为 file provider 解析；订阅自带的规则不会被使用。GeoIP/GeoSite 数据库随 App 内置，核心不会联网更新。
        > 5. 核心日志里 `match X using Y` 表示该连接命中的规则和出口：`DIRECT` 是直连，`PROXY[…]` 是走代理，`REJECT` 是拒绝。
        > 6. 核心的出站网卡固定在物理默认网卡上（`interface-name`，后台组件随网络变化更新）。Tailscale 等 VPN 可以共存：它们的地址段（100.64.0.0/10）不进本 App 的 TUN；但它们开启 exit node 会接管默认路由，开启「Override DNS servers」会让解析绕过 mihomo，这两种情况诊断会单独指出。
        >
        > **常见现象 → 可能原因**
        > - 国内网站 / 微信图片 / 飞书打不开或很慢 → 命中了 `PROXY`（在日志里搜该域名），需要补直连域名；或 DNS 没被接管。
        > - 海外网站打不开 → 命中了 `DIRECT`，或所有线路延迟都失败（节点不可用 / 订阅过期）。
        > - 日志里大量 `dial PROXY … context deadline exceeded`，而直连自检正常 → **节点服务器连不上**（不是规则问题）：先查节点地址/端口、DDNS、IPv6。
        > - 日志里 `dial PROXY` 伴随 `tls:` / `EOF` / 认证错误 → 节点能连上但协议或密码/UUID 不对。
        > - 全部打不开 → 后台组件没运行、系统 DNS 没指向 `\(AppInfo.tunDNS)`、另一个代理/TUN 冲突、残留的 `/etc/resolver/*` 指向已关闭的本地端口。
        > - 断开后断网 → 网络恢复点没有执行：检查网络服务的 DNS 是否还是 `\(AppInfo.tunDNS)`。
        >
        > **隐私**：订阅链接、控制密钥、节点密码与 UUID 已移除。\(includeFullLogs ? "这是**完整版**，日志里有用户访问过的域名，发给别人之前请自行确认。" : "这是摘要版，不含连接日志。")
        """ + "\n"
    }

    static func statusSection(_ input: DiagnosticsInput) -> [String] {
        var lines: [String] = []
        lines.append("- App 阶段：\(input.appPhase)\(input.lastError.map { "；最近错误：\($0)" } ?? "")")
        if let status = input.helperStatus {
            var parts = ["阶段 \(status.phase.rawValue)"]
            if let pid = status.corePID { parts.append("核心 pid \(pid)") }
            if let started = status.startedAt { parts.append("已运行 \(duration(from: started, to: input.now))") }
            if let tun = status.tunInterface { parts.append("TUN \(tun)") }
            parts.append("出口 \(status.egressInterface ?? "自动检测")")
            if let port = status.controllerPort { parts.append("控制端口 127.0.0.1:\(port)") }
            lines.append("- 后台组件：" + parts.joined(separator: "，"))
            if !status.changes.isEmpty {
                lines.append("- 当前由后台组件接管的网络服务：DNS \(status.changes.dnsServices.joined(separator: ", "))；关闭 IPv6 \(status.changes.ipv6Services.joined(separator: ", "))")
            }
            if let exit = status.lastExit {
                lines.append("- 核心上次退出：\(DiagnosticsAnalyzer.format(exit.at))，code \(exit.code)\(exit.bySignal ? "（信号）" : "")，\(exit.expected ? "预期内" : "非预期")")
            }
        } else {
            lines.append("- 后台组件：无法连接\(input.helperError.map { "（\($0)）" } ?? "")")
        }
        if let hello = input.helperHello {
            lines.append("- 后台组件协议 \(hello.ipcVersion)，pid \(hello.helperPID)，owner uid \(hello.ownerUID)，核心已安装 \(hello.coreInstalled)")
        }
        if let version = input.coreVersion { lines.append("- 核心版本：\(version)") }
        switch input.installState {
        case .upToDate: lines.append("- 组件安装：最新")
        case .notInstalled: lines.append("- 组件安装：未安装")
        case let .outdated(reason): lines.append("- 组件安装：需要更新（\(reason)）")
        }
        if let manifest = input.installedManifest {
            lines.append("- 已安装清单：core \(manifest.core)，地理库 \(manifest.geoPublishedAt)，App \(manifest.appVersion)")
        }
        lines.append("- 设置：接管 DNS \(input.settings.takeOverDNS)，关闭 IPv6 \(input.settings.disableIPv6)，测速地址 \(input.settings.healthCheckURL)")
        lines.append("")
        return lines
    }

    static func subscriptionSection(_ input: DiagnosticsInput) -> [String] {
        guard let subscription = input.settings.subscription else { return ["尚未添加订阅。", ""] }
        var lines = ["- 来源主机：\(subscription.host)（完整链接已隐藏）", "- 名称：\(subscription.name)"]
        lines.append("- 线路数：\(subscription.nodes.count)；协议分布：\(typeBreakdown(subscription.nodes))")
        lines.append("- 添加于 \(DiagnosticsAnalyzer.format(subscription.addedAt))；上次更新 \(subscription.updatedAt.map(DiagnosticsAnalyzer.format) ?? "从未")")
        if let usage = subscription.usage {
            let total = usage.total > 0 ? DiagnosticsAnalyzer.bytes(usage.total) : "不限"
            let expire = usage.expire.map(DiagnosticsAnalyzer.format) ?? "不过期"
            lines.append("- 流量：已用 \(DiagnosticsAnalyzer.bytes(usage.used)) / \(total)；到期 \(expire)")
        }
        lines.append("")
        return lines
    }

    static func networkSection(_ input: DiagnosticsInput) -> [String] {
        let facts = input.facts
        var lines: [String] = []
        lines.append("- 默认路由：\(facts.defaultRoute.isEmpty ? "未知" : facts.defaultRoute)")
        if !facts.defaultRoutes.isEmpty {
            lines.append("- 全部默认路由（按优先级）：" + facts.defaultRoutes.map { "\($0.interface) via \($0.gateway) [\($0.flags)]" }.joined(separator: "；"))
        }
        let primary = facts.dnsResolvers.filter { !$0.scoped }.prefix(6)
        lines.append("- 系统 DNS（scutil，非 scoped）：" + (primary.isEmpty ? "无" : primary.map {
            "#\($0.index) \($0.nameservers.isEmpty ? "-" : $0.nameservers.joined(separator: ","))\($0.domain.map { " domain=\($0)" } ?? "")\($0.port.map { " port=\($0)" } ?? "")"
        }.joined(separator: "；")))
        if !facts.services.isEmpty {
            lines.append("- 各网络服务：")
            for service in facts.services {
                lines.append("  - \(service.name)（\(service.device)）：DNS \(service.dnsAutomatic ? "自动" : service.dnsServers.joined(separator: ", "))；IPv6 \(service.ipv6)")
            }
        }
        lines.append("- 系统代理：" + (facts.systemProxy.isAnyEnabled
            ? "HTTP \(facts.systemProxy.http ?? "-")，HTTPS \(facts.systemProxy.https ?? "-")，SOCKS \(facts.systemProxy.socks ?? "-")，PAC \(facts.systemProxy.pac ?? "-")" : "未开启"))
        lines.append("- 带 IPv4 的 utun 网卡：" + (facts.tunInterfaces.isEmpty ? "无" : facts.tunInterfaces.map { "\($0.name) \($0.addresses.joined(separator: ","))" }.joined(separator: "；")))
        lines.append("- 有全局 IPv6 地址的网卡：" + (facts.globalIPv6Interfaces.isEmpty ? "无" : facts.globalIPv6Interfaces.joined(separator: ", ")))
        if facts.resolverFiles.isEmpty {
            lines.append("- /etc/resolver：空")
        } else {
            lines.append("- /etc/resolver 文件（\(facts.resolverFiles.count) 个）：")
            for file in facts.resolverFiles.prefix(20) {
                let dead = facts.deadLoopbackResolvers.contains(file) ? " ⚠️ 无进程监听" : ""
                lines.append("  - \(file.name) → \(file.nameservers.joined(separator: ","))\(file.port.map { ":\($0)" } ?? "")\(dead)")
            }
        }
        lines.append("- 其他代理/VPN 进程：" + (facts.otherProxyProcesses.isEmpty ? "未发现" : facts.otherProxyProcesses.prefix(8).joined(separator: "；")))
        lines.append("")
        return lines
    }

    static func rulesSection(_ input: DiagnosticsInput) -> [String] {
        let probe = ConfigOptions(controllerPort: 0, controllerSecret: "-")
        let rules = ConfigBuilder.rules(probe)
        return [
            "- 共 \(rules.count) 条内置规则（国内平台直连域名 \(RuleCatalog.directDomains.count) 个，海外固定代理域名 \(RuleCatalog.proxyDomains.count) 个），另有自定义规则 \(input.customRuleCount) 条。",
            "- 最近一次生成的配置（已脱敏）：`\(input.userPaths.lastConfigRedacted.path)`",
            "- 兜底：`GEOIP,CN → DIRECT`，其余 `MATCH → PROXY`；DNS 为 fake-ip，国内 DNS \(RuleCatalog.domesticDNS.joined(separator: " / "))。",
            "",
        ]
    }

    static func filesSection(_ input: DiagnosticsInput) -> [String] {
        let paths = input.userPaths
        return [
            "| 内容 | 路径 |",
            "| --- | --- |",
            "| 核心日志（含每条连接命中的规则） | `\(SystemPaths.coreLog)` |",
            "| 后台组件日志 | `\(SystemPaths.helperLog)`（stderr：`\(SystemPaths.logDir)/helper.stderr.log`） |",
            "| App 日志 | `\(paths.appLog.path)` |",
            "| 设置（含订阅链接，勿外发） | `\(paths.settings.path)` |",
            "| 自定义规则 | `\(paths.customRules.path)` |",
            "| 最近配置（脱敏） | `\(paths.lastConfigRedacted.path)` |",
            "| 网络恢复点 | `\(SystemPaths.networkState)`（存在表示有未还原的改动） |",
            "| 已安装组件清单 | `\(SystemPaths.componentsManifest)` |",
            "",
            "常用排查命令：",
            "```sh",
            "tail -n 200 \(SystemPaths.coreLog)",
            "tail -n 100 \(SystemPaths.helperLog)",
            "scutil --dns | head -60",
            "netstat -rn -f inet | head -30",
            "ls -la /etc/resolver; networksetup -getdnsservers Wi-Fi",
            "sudo launchctl print system/\(AppInfo.helperLabel)",
            "```",
            "",
        ]
    }

    static func logsSection(_ input: DiagnosticsInput, full: Bool) -> [String] {
        var lines: [String] = []
        func block(_ title: String, _ text: String) {
            lines.append("### \(title)")
            lines.append("```")
            lines.append(text.isEmpty ? "（空）" : text)
            lines.append("```")
            lines.append("")
        }
        block("App 日志（末尾 30 行）", tail(input.appLog, 30))
        block("后台组件日志（末尾 40 行）", tail(input.helperLog, 40))
        let problems = input.coreLog.split(separator: "\n").filter {
            $0.contains("level=error") || $0.contains("level=warning") || $0.contains("level=fatal")
        }
        block("核心日志：警告与错误（末尾 40 条）", problems.suffix(40).joined(separator: "\n"))
        if full {
            block("核心日志：最近 200 行（含访问过的域名）", tail(input.coreLog, 200))
        }
        return lines
    }

    // MARK: 工具

    static func tail(_ text: String, _ count: Int) -> String {
        text.split(separator: "\n", omittingEmptySubsequences: true).suffix(count).joined(separator: "\n")
    }

    static func typeBreakdown(_ nodes: [ProbedNode]) -> String {
        let counts = Dictionary(grouping: nodes, by: \.type).mapValues(\.count)
        return counts.sorted { $0.value > $1.value || ($0.value == $1.value && $0.key < $1.key) }
            .map { "\($0.key)×\($0.value)" }.joined(separator: " ")
    }

    static func duration(from start: Date, to end: Date) -> String {
        let seconds = max(0, Int(end.timeIntervalSince(start)))
        if seconds >= 3600 { return "\(seconds / 3600) 小时 \(seconds % 3600 / 60) 分" }
        if seconds >= 60 { return "\(seconds / 60) 分 \(seconds % 60) 秒" }
        return "\(seconds) 秒"
    }
}
