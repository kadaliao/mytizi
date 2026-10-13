import Foundation
import MytiziKit

public struct Finding: Equatable {
    public enum Severity: Int, Comparable {
        case info = 0, warning = 1, problem = 2
        public static func < (a: Self, b: Self) -> Bool { a.rawValue < b.rawValue }
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
        func add(_ severity: Finding.Severity, _ title: String, _ detail: String) {
            result.append(Finding(severity: severity, title: title, detail: detail))
        }
        let facts = input.facts
        let status = input.helperStatus

        // 1. 后台组件
        switch input.installState {
        case .notInstalled:
            add(.problem, "后台组件没有安装", "没有找到 /Library/Application Support/MyTizi/components.json。点击连接时会请求一次管理员授权来安装。")
        case let .outdated(reason):
            add(.warning, "后台组件需要更新", reason)
        case .upToDate:
            break
        }
        // 没安装时 socket 本来就不存在，已经由上一条说明了，不重复报。
        if let error = input.helperError, input.installState != .notInstalled {
            add(.problem, "无法连接后台组件", "\(error.trimmingCharacters(in: CharacterSet(charactersIn: "。")))。检查 `launchctl print system/\(AppInfo.helperLabel)` 与 \(SystemPaths.logDir)/helper.stderr.log。")
        }

        // 2. 订阅
        if let subscription = input.settings.subscription {
            if subscription.nodes.isEmpty { add(.problem, "订阅里没有线路", "订阅 \(subscription.host) 解析出 0 个节点。") }
            if let updated = subscription.updatedAt, input.now.timeIntervalSince(updated) > 7 * 86400 {
                add(.warning, "订阅超过 7 天没有更新", "上次更新 \(Self.format(updated))。节点信息可能已经过期。")
            }
            if let usage = subscription.usage {
                if let expire = usage.expire, expire < input.now { add(.problem, "订阅已过期", "到期时间 \(Self.format(expire))。") }
                if usage.total > 0, usage.used >= usage.total { add(.problem, "订阅流量已用完", "已用 \(Self.bytes(usage.used)) / 总量 \(Self.bytes(usage.total))。") }
            }
        } else {
            add(.problem, "还没有添加订阅", "settings.json 里没有订阅链接。")
        }
        if let error = input.customRulesError { add(.warning, "自定义规则有错误（已被忽略）", error) }

        // 3. 核心状态
        if status?.phase == .crashed {
            let exit = status?.lastExit
            add(.problem, "核心意外退出",
                "退出码 \(exit?.code ?? -1)\(exit?.bySignal == true ? "（被信号终止）" : "")，时间 \(exit.map { Self.format($0.at) } ?? "?")。网络设置已由后台组件还原。详见核心日志末尾。")
        }

        // 4. 运行中的一致性检查
        if input.isRunning {
            if !facts.tunInterfaces.contains(where: { $0.addresses.contains(AppInfo.tunGateway) }) {
                add(.problem, "找不到 TUN 网卡", "后台组件报告运行中，但系统里没有地址为 \(AppInfo.tunGateway) 的 utun 网卡。")
            }
            let rivals = facts.tunInterfaces.filter { $0.addresses.contains { $0.hasPrefix("198.18.") && $0 != AppInfo.tunGateway } }
            if !rivals.isEmpty {
                add(.problem, "检测到另一个 TUN 在使用同一地址段",
                    "\(rivals.map { "\($0.name) \($0.addresses.joined(separator: ","))" }.joined(separator: "；"))。两个代理同时开 TUN 会互相抢路由和 DNS，请关闭另一个。")
            }
            if input.settings.takeOverDNS, let primary = facts.dnsResolvers.first(where: { !$0.scoped && $0.domain == nil }) ?? facts.dnsResolvers.first(where: { !$0.scoped }),
               primary.nameservers.first != AppInfo.tunDNS
            {
                add(.problem, "系统 DNS 没有指向 TUN",
                    "当前主 DNS 是 \(primary.nameservers.joined(separator: ", "))，应为 \(AppInfo.tunDNS)。DNS 查询不经过 mihomo，fake-ip 和域名规则会失效。")
            }
            if input.settings.disableIPv6, !facts.globalIPv6Interfaces.isEmpty {
                add(.warning, "IPv6 仍然开启", "网卡 \(facts.globalIPv6Interfaces.joined(separator: ", ")) 仍有全局 IPv6 地址，IPv6 流量会绕过 TUN。")
            }
            if facts.systemProxy.isAnyEnabled {
                add(.warning, "系统代理仍然开启",
                    "HTTP \(facts.systemProxy.http ?? "-") HTTPS \(facts.systemProxy.https ?? "-") SOCKS \(facts.systemProxy.socks ?? "-") PAC \(facts.systemProxy.pac ?? "-")。使用 TUN 时通常不需要系统代理；如果它指向一个已经关闭的端口，浏览器会全部打不开。")
            }
            if !input.nodes.isEmpty, input.nodes.allSatisfy({ $0.delay == nil }) {
                add(.problem, "所有线路测速都失败", "\(input.nodes.count) 个线路都没有可用的延迟记录。可能是节点全部不可用，或者这个网络环境挡住了代理协议。")
            }
        } else if status?.phase != .starting {
            // 未连接却还指向 TUN DNS ⇒ 用户会断网
            let stranded = facts.services.filter { $0.dnsServers.contains(AppInfo.tunDNS) }
            if !stranded.isEmpty {
                add(.problem, "DNS 还指向 \(AppInfo.tunDNS) 但没有在连接",
                    "\(stranded.map(\.name).joined(separator: ", "))：这会导致断网。重新连接再断开即可还原；或执行 `networksetup -setdnsservers \"\(stranded[0].name)\" Empty`。")
            }
            if let changes = status?.changes, !changes.isEmpty {
                add(.warning, "后台组件还保留着未恢复的网络改动", "DNS：\(changes.dnsServices.joined(separator: ", "))；IPv6：\(changes.ipv6Services.joined(separator: ", "))。")
            }
        }

        // 5. 与其他工具的冲突
        if !facts.deadLoopbackResolvers.isEmpty {
            add(.warning, "发现失效的 /etc/resolver 文件",
                facts.deadLoopbackResolvers.map { "\($0.name) → \($0.nameservers.joined(separator: ","))\($0.port.map { ":\($0)" } ?? "")（无进程监听）" }.joined(separator: "；")
                    + "。这些域名的解析会一直超时。通常是其他代理工具退出后残留，确认不需要后可删除对应文件。")
        }
        if !facts.otherProxyProcesses.isEmpty {
            add(.warning, "检测到其他代理/VPN 进程", facts.otherProxyProcesses.prefix(6).joined(separator: "；") + "。它们如果也开了 TUN 或改了系统 DNS，会与本 App 冲突。")
        }

        // 6. 自检
        for test in input.selfTest where !test.passed {
            if let concern = test.concern, input.isRunning {
                add(.problem, "自检失败：\(test.name)", concern)
            } else if !test.reachable {
                add(.problem, "自检失败：\(test.name) 不通", "\(test.url)：\(test.error ?? "无响应")，用时 \(test.milliseconds ?? 0) ms，期望\(test.expected?.rawValue ?? "?")，实际\(test.observed ?? "日志中没有记录")。")
            } else if test.routeMismatch {
                add(.warning, "自检：\(test.name) 的走向和预期不同",
                    "期望\(test.expected?.rawValue ?? "?")，实际 \(test.observed ?? "?")。如果是国内平台，应补充直连域名；如果是海外站点，说明命中了直连规则。")
            }
        }

        // 7. 核心日志里的高频错误
        let logLines = input.coreLog.split(separator: "\n")
        let signatures: [(String, String)] = [
            ("i/o timeout", "连接超时：节点不可达或被网络阻断"),
            ("no such host", "域名解析失败：国内 DNS 不可用或域名不存在"),
            ("connection refused", "连接被拒绝：节点端口没有服务"),
            ("context deadline exceeded", "握手超时"),
            ("tls:", "TLS 握手/证书错误"),
            ("EOF", "连接被对端中断"),
        ]
        for (needle, meaning) in signatures {
            let hits = logLines.filter { $0.contains("level=error") || $0.contains("level=warning") }.filter { $0.contains(needle) }
            if hits.count >= 5 {
                add(.info, "核心日志里 “\(needle)” 出现 \(hits.count) 次", "\(meaning)。例：\(String(hits.last!.suffix(160)))")
            }
        }

        // 8. 数据库新旧
        if let manifest = input.installedManifest, let published = ISO8601DateFormatter().date(from: manifest.geoPublishedAt),
           input.now.timeIntervalSince(published) > 90 * 86400
        {
            add(.info, "内置地理数据库超过 90 天", "发布于 \(manifest.geoPublishedAt)。国内 IP 归属可能不再准确，升级 App 即可更新。")
        }

        return result.sorted { $0.severity > $1.severity }
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
