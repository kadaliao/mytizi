import Foundation
import MytiziKit

public struct Finding: Equatable {
    public enum Severity: Int, Comparable {
        case info = 0, warning = 1, problem = 2
        public static func < (lhs: Self, rhs: Self) -> Bool { lhs.rawValue < rhs.rawValue }
    }

    public var severity: Severity
    public var title: String
    public var detail: String

    public var marker: String {
        switch severity {
        case .problem: return "🔴 问题"
        case .warning: return "🟡 注意"
        case .info: return "ℹ️ 提示"
        }
    }
}

public struct NodeDelay: Equatable {
    public var name: String
    public var type: String
    public var delay: Int?

    public init(name: String, type: String, delay: Int?) {
        self.name = name
        self.type = type
        self.delay = delay
    }
}

public struct DiagnosticsInput {
    public var appVersion = "dev"
    public var appPhase = ""
    public var lastError: String?
    public var settings = AppSettings()
    public var installState: InstallState = .notInstalled
    public var installedManifest: ComponentsManifest?
    public var helperHello: HelloInfo?
    public var helperStatus: HelperStatus?
    public var helperError: String?
    public var coreVersion: String?
    public var facts = SystemFacts()
    public var selfTest: [SelfTestResult] = []
    public var nodes: [NodeDelay] = []
    public var customRulesError: String?
    public var customRuleCount = 0
    public var appLog = ""
    public var helperLog = ""
    public var coreLog = ""
    public var userPaths = UserPaths()
    public var now = Date()

    public init() {}

    public var isRunning: Bool { helperStatus?.phase == .running }
}

/// 把一堆原始事实归纳成"哪里出了问题"。这一层是纯函数：给定输入就有确定的结论，方便测试，
/// 也是整个诊断功能价值最高的部分——AI（和用户）先看结论，再看细节。
public enum DiagnosticsAnalyzer {
    public static func findings(_ input: DiagnosticsInput) -> [Finding] {
        var result: [Finding] = []
        result += helperFindings(input)
        result += subscriptionFindings(input)
        result += coreStateFindings(input)
        result += input.isRunning ? runningFindings(input) : notRunningFindings(input)
        result += conflictFindings(input)
        result += selfTestFindings(input)
        result += logSignatureFindings(input)
        result += geoFindings(input)
        return result.sorted { $0.severity > $1.severity }
    }

    // MARK: 1. 后台组件

    static func helperFindings(_ input: DiagnosticsInput) -> [Finding] {
        var result: [Finding] = []
        switch input.installState {
        case .notInstalled:
            result.append(Finding(
                severity: .problem, title: "后台组件没有安装",
                detail: "没有找到 \(SystemPaths.componentsManifest)。点击连接时会请求一次管理员授权来安装。"))
        case let .outdated(reason):
            result.append(Finding(severity: .warning, title: "后台组件需要更新", detail: reason))
        case .upToDate:
            break
        }
        // 没安装时 socket 本来就不存在，已经由上一条说明了，不重复报。
        if let error = input.helperError, input.installState != .notInstalled {
            let message = error.trimmingCharacters(in: CharacterSet(charactersIn: "。"))
            result.append(Finding(
                severity: .problem, title: "无法连接后台组件",
                detail: "\(message)。检查 `launchctl print system/\(AppInfo.helperLabel)` 与 "
                    + "\(SystemPaths.logDir)/helper.stderr.log。"))
        }
        return result
    }

    // MARK: 2. 订阅

    static func subscriptionFindings(_ input: DiagnosticsInput) -> [Finding] {
        var result: [Finding] = []
        if let subscription = input.settings.subscription {
            if subscription.nodes.isEmpty {
                result.append(Finding(
                    severity: .problem, title: "订阅里没有线路",
                    detail: "订阅 \(subscription.host) 解析出 0 个节点。"))
            }
            if let updated = subscription.updatedAt, input.now.timeIntervalSince(updated) > 7 * 86400 {
                result.append(Finding(
                    severity: .warning, title: "订阅超过 7 天没有更新",
                    detail: "上次更新 \(Self.format(updated))。节点信息可能已经过期。"))
            }
            if let usage = subscription.usage {
                if let expire = usage.expire, expire < input.now {
                    result.append(Finding(
                        severity: .problem, title: "订阅已过期",
                        detail: "到期时间 \(Self.format(expire))。"))
                }
                if usage.total > 0, usage.used >= usage.total {
                    result.append(Finding(
                        severity: .problem, title: "订阅流量已用完",
                        detail: "已用 \(Self.bytes(usage.used)) / 总量 \(Self.bytes(usage.total))。"))
                }
            }
        } else {
            result.append(Finding(
                severity: .problem, title: "还没有添加订阅",
                detail: "settings.json 里没有订阅链接。"))
        }
        if let error = input.customRulesError {
            result.append(Finding(severity: .warning, title: "自定义规则有错误（已被忽略）", detail: error))
        }
        return result
    }

    // MARK: 3. 核心状态

    static func coreStateFindings(_ input: DiagnosticsInput) -> [Finding] {
        guard input.helperStatus?.phase == .crashed else { return [] }
        let exit = input.helperStatus?.lastExit
        return [Finding(
            severity: .problem, title: "核心意外退出",
            detail: "退出码 \(exit?.code ?? -1)\(exit?.bySignal == true ? "（被信号终止）" : "")，"
                + "时间 \(exit.map { Self.format($0.at) } ?? "?")。网络设置已由后台组件还原。详见核心日志末尾。")]
    }

    // MARK: 4. 连接中

    static func runningFindings(_ input: DiagnosticsInput) -> [Finding] {
        var result: [Finding] = []
        let facts = input.facts
        if !facts.tunInterfaces.contains(where: { $0.addresses.contains(AppInfo.tunGateway) }) {
            result.append(Finding(
                severity: .problem, title: "找不到 TUN 网卡",
                detail: "后台组件报告运行中，但系统里没有地址为 \(AppInfo.tunGateway) 的 utun 网卡。"))
        }
        let rivals = facts.tunInterfaces.filter {
            $0.addresses.contains { $0.hasPrefix("198.18.") && $0 != AppInfo.tunGateway }
        }
        if !rivals.isEmpty {
            let list = rivals.map { "\($0.name) \($0.addresses.joined(separator: ","))" }.joined(separator: "；")
            result.append(Finding(
                severity: .problem, title: "检测到另一个 TUN 在使用同一地址段",
                detail: "\(list)。两个代理同时开 TUN 会互相抢路由和 DNS，请关闭另一个。"))
        }
        result += egressFindings(input)
        result += dnsFindings(input)
        if input.settings.disableIPv6, !facts.globalIPv6Interfaces.isEmpty {
            result.append(Finding(
                severity: .warning, title: "IPv6 仍然开启",
                detail: "网卡 \(facts.globalIPv6Interfaces.joined(separator: ", ")) 仍有全局 IPv6 地址，"
                    + "IPv6 流量会绕过 TUN。"))
        }
        if input.facts.systemProxy.isAnyEnabled {
            let proxy = facts.systemProxy
            result.append(Finding(
                severity: .warning, title: "系统代理仍然开启",
                detail: "HTTP \(proxy.http ?? "-") HTTPS \(proxy.https ?? "-") SOCKS \(proxy.socks ?? "-") "
                    + "PAC \(proxy.pac ?? "-")。使用 TUN 时通常不需要系统代理；"
                    + "如果它指向一个已经关闭的端口，浏览器会全部打不开。"))
        }
        result += nodeFindings(input)
        return result
    }

    /// 另一个 VPN 把自己的 utun 排到了默认路由第一位（典型：Tailscale 开了 exit node）。
    /// 本 App 的 TUN 路由比默认路由更具体，应用流量仍会进本 App；真正的风险是核心的出站被它接走，
    /// 这由出口固定（interface-name）挡住——所以固定成功时只是提醒，没固定成功才是问题。
    static func egressFindings(_ input: DiagnosticsInput) -> [Finding] {
        let facts = input.facts
        guard let first = facts.defaultRoutes.first, first.interface.hasPrefix("utun"),
              first.interface != input.helperStatus?.tunInterface
        else { return [] }
        let addresses = facts.tunInterfaces.first { $0.name == first.interface }?.addresses ?? []
        let isTailscale = addresses.contains(where: isCGNAT)
        let pinned = input.helperStatus?.egressInterface
        let who = isTailscale ? "Tailscale 的出口节点（exit node）" : "另一个 VPN（\(first.interface)）"
        let effect = pinned.map { "本 App 已把核心出口固定在 \($0)，代理流量不会被它接走" }
            ?? "本 App 没能确定物理出口，核心的出站（包括连节点）可能全部经它转发"
        let advice = isTailscale ? "建议在 Tailscale 菜单里关闭 exit node，两者各管各的流量。" : "建议关闭该 VPN 的全局接管模式。"
        return [Finding(
            severity: pinned == nil ? .problem : .warning,
            title: "\(who)正在接管默认路由",
            detail: "默认路由第一位是 \(first.interface)\(addresses.isEmpty ? "" : "（\(addresses.joined(separator: ", "))）")。\(effect)；\(advice)")]
    }

    /// 100.64.0.0/10（CGNAT）：Tailscale 给每台设备分配的地址段。
    static func isCGNAT(_ address: String) -> Bool {
        let parts = address.split(separator: ".").compactMap { Int($0) }
        return parts.count == 4 && parts[0] == 100 && (64...127).contains(parts[1])
    }

    static let tailscaleDNS = "100.100.100.100"

    static func dnsFindings(_ input: DiagnosticsInput) -> [Finding] {
        guard input.settings.takeOverDNS else { return [] }
        let facts = input.facts
        let primary = facts.dnsResolvers.first { !$0.scoped && $0.domain == nil }
            ?? facts.dnsResolvers.first { !$0.scoped }
        guard let primary, primary.nameservers.first != AppInfo.tunDNS else { return [] }
        if primary.nameservers.first == tailscaleDNS {
            return [Finding(
                severity: .problem, title: "Tailscale 覆盖了系统 DNS",
                detail: "当前主 DNS 是 \(tailscaleDNS)（Tailscale），它的优先级高于本 App 设置的 \(AppInfo.tunDNS)，"
                    + "DNS 查询不经过 mihomo，fake-ip 和域名规则会失效。请在 Tailscale 管理后台的 DNS 页关闭「Override DNS servers」"
                    + "（MagicDNS 可以保留，它只接管 ts.net 域名），或在 Tailscale 菜单里取消「Use Tailscale DNS settings」。")]
        }
        return [Finding(
            severity: .problem, title: "系统 DNS 没有指向 TUN",
            detail: "当前主 DNS 是 \(primary.nameservers.joined(separator: ", "))，应为 \(AppInfo.tunDNS)。"
                + "DNS 查询不经过 mihomo，fake-ip 和域名规则会失效。")]
    }

    /// 线路全挂时把“卡在哪一步”说清楚：连不上节点服务器、端口没有服务，还是握手/认证失败。
    static func nodeFindings(_ input: DiagnosticsInput) -> [Finding] {
        guard !input.nodes.isEmpty, input.nodes.allSatisfy({ $0.delay == nil }) else { return [] }
        let logLines = input.coreLog.split(separator: "\n")
        let proxyTests = input.selfTest.filter { $0.expected == .proxy }
        let proxyAllFailed = !proxyTests.isEmpty && proxyTests.allSatisfy { !$0.reachable }
        let directWorks = input.selfTest.contains { $0.expected == .direct && $0.passed }
        let dialTimeouts = logLines.filter {
            $0.contains("dial PROXY") && $0.contains("context deadline exceeded")
        }.count
        let dialRefused = logLines.filter {
            $0.contains("dial PROXY") && $0.contains("connection refused")
        }.count
        let handshakeProblems = logLines.filter {
            $0.contains("dial PROXY") && ($0.contains("tls:") || $0.contains("EOF") || $0.contains("authentication"))
        }.count

        if directWorks, proxyAllFailed, dialTimeouts >= 5 {
            let detail = "直连自检全部正常，但所有走代理的连接都卡在“连接节点服务器”这一步"
                + "（核心日志里 `dial PROXY … context deadline exceeded` \(dialTimeouts) 次），代理握手根本没有开始。"
                + "这不是规则或 DNS 问题。按顺序检查：1) 节点服务器（自建 FRP/frps、VPS）是否在线、端口映射是否还在；"
                + "2) 节点域名是否还解析到当前公网 IP（DDNS 过期会一直超时）；"
                + "3) 节点是否只暴露了 IPv6（App 连接期间会关闭 IPv6）；"
                + "4) 换一个网络（手机热点）再试，排除运营商拦截端口。"
            return [Finding(severity: .problem, title: "节点服务器连不上（直连正常）", detail: detail)]
        }
        if directWorks, proxyAllFailed, dialRefused >= 3 {
            let detail = "直连正常，但连节点服务器时被拒绝（`connection refused` \(dialRefused) 次）："
                + "地址能到，但那个端口没有程序在监听。检查节点端口是否正确、服务端是否启动、FRP 映射是否还在。"
            return [Finding(severity: .problem, title: "节点端口没有服务（直连正常）", detail: detail)]
        }
        if directWorks, proxyAllFailed, handshakeProblems >= 3 {
            let detail = "直连正常、节点服务器也能连上，但代理协议握手失败"
                + "（TLS/EOF/认证错误 \(handshakeProblems) 次）。检查节点类型、密码/UUID、SNI 与端口是否和订阅一致。"
            return [Finding(severity: .problem, title: "节点可达但握手/认证失败（直连正常）", detail: detail)]
        }
        return [Finding(
            severity: .problem, title: "所有线路测速都失败",
            detail: "\(input.nodes.count) 个线路都没有可用的延迟记录。可能是节点全部不可用，"
                + "或者这个网络环境挡住了代理协议。")]
    }

    static func notRunningFindings(_ input: DiagnosticsInput) -> [Finding] {
        guard input.helperStatus?.phase != .starting else { return [] }
        var result: [Finding] = []
        let facts = input.facts
        // 未连接却还指向 TUN DNS ⇒ 用户会断网
        let stranded = facts.services.filter { $0.dnsServers.contains(AppInfo.tunDNS) }
        if !stranded.isEmpty {
            result.append(Finding(
                severity: .problem, title: "DNS 还指向 \(AppInfo.tunDNS) 但没有在连接",
                detail: "\(stranded.map(\.name).joined(separator: ", "))：这会导致断网。重新连接再断开即可还原；"
                    + "或执行 `networksetup -setdnsservers \"\(stranded[0].name)\" Empty`。"))
        }
        if let changes = input.helperStatus?.changes, !changes.isEmpty {
            result.append(Finding(
                severity: .warning, title: "后台组件还保留着未恢复的网络改动",
                detail: "DNS：\(changes.dnsServices.joined(separator: ", "))；"
                    + "IPv6：\(changes.ipv6Services.joined(separator: ", "))。"))
        }
        return result
    }

    // MARK: 5. 与其他工具的冲突

    static func conflictFindings(_ input: DiagnosticsInput) -> [Finding] {
        var result: [Finding] = []
        let facts = input.facts
        if !facts.deadLoopbackResolvers.isEmpty {
            let list = facts.deadLoopbackResolvers.map {
                "\($0.name) → \($0.nameservers.joined(separator: ","))\($0.port.map { ":\($0)" } ?? "")（无进程监听）"
            }.joined(separator: "；")
            result.append(Finding(
                severity: .warning, title: "发现失效的 /etc/resolver 文件",
                detail: list + "。这些域名的解析会一直超时。通常是其他代理工具退出后残留，确认不需要后可删除对应文件。"))
        }
        if !facts.otherProxyProcesses.isEmpty {
            result.append(Finding(
                severity: .warning, title: "检测到其他代理/VPN 进程",
                detail: facts.otherProxyProcesses.prefix(6).joined(separator: "；")
                    + "。它们如果也开了 TUN 或改了系统 DNS，会与本 App 冲突。"))
        }
        return result
    }

    // MARK: 6. 自检

    static func selfTestFindings(_ input: DiagnosticsInput) -> [Finding] {
        var result: [Finding] = []
        for test in input.selfTest where !test.passed {
            if let concern = test.concern, input.isRunning {
                result.append(Finding(severity: .problem, title: "自检失败：\(test.name)", detail: concern))
            } else if !test.reachable {
                result.append(Finding(
                    severity: .problem, title: "自检失败：\(test.name) 不通",
                    detail: "\(test.url)：\(test.error ?? "无响应")，用时 \(test.milliseconds ?? 0) ms，"
                        + "期望\(test.expected?.rawValue ?? "?")，实际\(test.observed ?? "日志中没有记录")。"))
            } else if test.routeMismatch {
                result.append(Finding(
                    severity: .warning, title: "自检：\(test.name) 的走向和预期不同",
                    detail: "期望\(test.expected?.rawValue ?? "?")，实际 \(test.observed ?? "?")。"
                        + "如果是国内平台，应补充直连域名；如果是海外站点，说明命中了直连规则。"))
            }
        }
        return result
    }

    // MARK: 7. 核心日志里的高频错误

    static func logSignatureFindings(_ input: DiagnosticsInput) -> [Finding] {
        let logLines = input.coreLog.split(separator: "\n")
        let signatures: [(String, String)] = [
            ("i/o timeout", "连接超时：节点不可达或被网络阻断"),
            ("no such host", "域名解析失败：国内 DNS 不可用或域名不存在"),
            ("connection refused", "连接被拒绝：节点端口没有服务"),
            ("context deadline exceeded", "握手超时"),
            ("tls:", "TLS 握手/证书错误"),
            ("EOF", "连接被对端中断"),
        ]
        var result: [Finding] = []
        for (needle, meaning) in signatures {
            let hits = logLines.filter { $0.contains("level=error") || $0.contains("level=warning") }
                .filter { $0.contains(needle) }
            if hits.count >= 5, let last = hits.last {
                result.append(Finding(
                    severity: .info, title: "核心日志里 “\(needle)” 出现 \(hits.count) 次",
                    detail: "\(meaning)。例：\(String(last.suffix(160)))"))
            }
        }
        return result
    }

    // MARK: 8. 数据库新旧

    static func geoFindings(_ input: DiagnosticsInput) -> [Finding] {
        guard let manifest = input.installedManifest,
              let published = ISO8601DateFormatter().date(from: manifest.geoPublishedAt),
              input.now.timeIntervalSince(published) > 90 * 86400
        else { return [] }
        return [Finding(
            severity: .info, title: "内置地理数据库超过 90 天",
            detail: "发布于 \(manifest.geoPublishedAt)。国内 IP 归属可能不再准确，升级 App 即可更新。")]
    }

    // MARK: 格式化

    static func format(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "zh_CN")
        formatter.dateFormat = "yyyy-MM-dd HH:mm"
        return formatter.string(from: date)
    }

    static func bytes(_ value: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: value, countStyle: .binary)
    }
}
