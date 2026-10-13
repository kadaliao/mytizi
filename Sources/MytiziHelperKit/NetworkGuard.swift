import Foundation
import MytiziKit

public protocol CommandRunner {
    func run(_ executable: String, _ arguments: [String]) -> ShellResult
}

public struct SystemCommandRunner: CommandRunner {
    public init() {}
    public func run(_ executable: String, _ arguments: [String]) -> ShellResult {
        Shell.run(executable, arguments, timeout: 10)
    }
}

/// 恢复点：改动之前的原始状态。文件存在就意味着"系统里可能还有我们没撤销的改动"。
public struct NetworkState: Codable, Equatable {
    public struct DNSEntry: Codable, Equatable {
        public var service: String
        public var automatic: Bool
        public var servers: [String]
    }

    public struct IPv6Entry: Codable, Equatable {
        public var service: String
        public var original: IPv6Setting
    }

    public var createdAt: Date
    public var dns: [DNSEntry]
    public var ipv6: [IPv6Entry]
}

/// 连接期间对系统网络的两类改动，以及它们的恢复：
///   - DNS：把各网络服务的 DNS 指向 TUN 内部地址，这样所有解析都会被 mihomo 接管并返回 fake-ip。
///     不接管的话，路由器 DNS 在同一网段（on-link）里，包根本不会进入 TUN。
///   - IPv6：TUN 只接管 IPv4，应用拿到 IPv6 地址后会直接绕开规则（twimg.com 就是这样泄漏的），
///     所以连接期间在系统层关闭 IPv6。
///
/// 崩溃安全的关键顺序：先把原始状态写盘并 fsync，再修改系统。
/// 因此无论在哪一步崩溃/断电，下次 helper 启动时都能根据恢复点还原。
public final class NetworkGuard {
    public let statePath: String
    private let runner: CommandRunner
    private let dnsAddress: String
    private let lock = NSLock()
    private let log: (String) -> Void

    public init(
        statePath: String = SystemPaths.networkState, runner: CommandRunner = SystemCommandRunner(),
        dnsAddress: String = AppInfo.tunDNS, log: @escaping (String) -> Void = { _ in }
    ) {
        self.statePath = statePath
        self.runner = runner
        self.dnsAddress = dnsAddress
        self.log = log
    }

    public var hasPendingState: Bool { FileManager.default.fileExists(atPath: statePath) }

    public func currentSummary() -> NetworkChangeSummary {
        lock.lock(); defer { lock.unlock() }
        guard let state = loadState() else { return NetworkChangeSummary() }
        return NetworkChangeSummary(dnsServices: state.dns.map(\.service), ipv6Services: state.ipv6.map(\.service))
    }

    // MARK: 应用

    /// 对所有符合条件的网络服务应用改动。已经在恢复点里的服务不会重复快照，
    /// 所以也可以周期性调用，用来接管连接期间新插入的网卡。
    @discardableResult
    public func apply(takeOverDNS: Bool, disableIPv6: Bool) throws -> NetworkChangeSummary {
        lock.lock(); defer { lock.unlock() }

        let order = runner.run("/usr/sbin/networksetup", ["-listnetworkserviceorder"])
        guard order.succeeded else { throw GuardError.command("读取网络服务列表", order.combined) }
        let services = NetworkSetupParser.services(fromServiceOrder: order.stdout)

        var state = loadState() ?? NetworkState(createdAt: Date(), dns: [], ipv6: [])
        let known = Set(state.dns.map(\.service))
        let knownV6 = Set(state.ipv6.map(\.service))
        var dnsTargets: [String] = []
        var v6Targets: [String] = []

        // 1) 先快照：这一步不改系统。
        for service in services {
            if takeOverDNS, !known.contains(service.name) {
                let result = runner.run("/usr/sbin/networksetup", ["-getdnsservers", service.name])
                if result.succeeded {
                    let parsed = NetworkSetupParser.dnsServers(from: result.stdout)
                    state.dns.append(.init(service: service.name, automatic: parsed.automatic, servers: parsed.servers))
                    dnsTargets.append(service.name)
                } else {
                    log("跳过 \(service.name) 的 DNS：读取失败 \(result.combined)")
                }
            }
            if disableIPv6, !knownV6.contains(service.name) {
                let result = runner.run("/usr/sbin/networksetup", ["-getinfo", service.name])
                if result.succeeded, let setting = NetworkSetupParser.ipv6Setting(fromInfo: result.stdout) {
                    // 已经是关闭状态的服务不需要记录，也不应该在恢复时被"打开"。
                    if setting.mode != .off {
                        state.ipv6.append(.init(service: service.name, original: setting))
                        v6Targets.append(service.name)
                    }
                } else {
                    log("跳过 \(service.name) 的 IPv6：无法识别当前设置")
                }
            }
        }
        guard !dnsTargets.isEmpty || !v6Targets.isEmpty else { return summary(of: state) }

        // 2) 恢复点落盘（fsync），然后才改系统。
        try save(state)

        // 3) 修改。任何一步失败都立即整体回滚，不留下半改的状态。
        do {
            for name in dnsTargets {
                try check(runner.run("/usr/sbin/networksetup", ["-setdnsservers", name, dnsAddress]), "设置 \(name) 的 DNS")
            }
            for name in v6Targets {
                try check(runner.run("/usr/sbin/networksetup", ["-setv6off", name]), "关闭 \(name) 的 IPv6")
            }
        } catch {
            _ = restoreLocked()
            throw error
        }
        flushCaches()
        log("网络接管完成：DNS \(dnsTargets) IPv6 \(v6Targets)")
        return summary(of: state)
    }

    // MARK: 恢复

    /// 还原恢复点里的所有改动。返回无法还原的条目描述；全部成功才会删除恢复点，
    /// 否则保留，下次启动继续重试。
    @discardableResult
    public func restore() -> [String] {
        lock.lock(); defer { lock.unlock() }
        return restoreLocked()
    }

    private func restoreLocked() -> [String] {
        guard let state = loadState() else {
            if hasPendingState { try? FileManager.default.removeItem(atPath: statePath) }
            return []
        }
        var failures: [String] = []
        for entry in state.dns {
            let arguments = ["-setdnsservers", entry.service] + (entry.automatic || entry.servers.isEmpty ? ["Empty"] : entry.servers)
            let result = runner.run("/usr/sbin/networksetup", arguments)
            if !result.succeeded { failures.append("恢复 \(entry.service) 的 DNS 失败：\(result.combined)") }
        }
        for entry in state.ipv6 {
            let result = runner.run("/usr/sbin/networksetup", Self.restoreIPv6Arguments(service: entry.service, original: entry.original))
            if !result.succeeded { failures.append("恢复 \(entry.service) 的 IPv6 失败：\(result.combined)") }
        }
        flushCaches()
        if failures.isEmpty {
            try? FileManager.default.removeItem(atPath: statePath)
            log("网络设置已恢复")
        } else {
            failures.forEach { log($0) }
        }
        return failures
    }

    static func restoreIPv6Arguments(service: String, original: IPv6Setting) -> [String] {
        switch original.mode {
        case .automatic: return ["-setv6automatic", service]
        case .off: return ["-setv6off", service]
        case .linkLocal: return ["-setv6LinkLocal", service]
        case .manual:
            guard let address = original.address, let prefix = original.prefixLength else {
                return ["-setv6automatic", service]
            }
            return ["-setv6manual", service, address, prefix] + (original.router.map { [$0] } ?? [])
        }
    }

    // MARK: 内部

    public enum GuardError: Error, CustomStringConvertible {
        case command(String, String)
        case state(String)
        public var description: String {
            switch self {
            case let .command(what, output): return "\(what)失败：\(output)"
            case let .state(reason): return "保存恢复点失败：\(reason)"
            }
        }
    }

    private func check(_ result: ShellResult, _ what: String) throws {
        // networksetup 出错时仍可能返回 0，但会输出 "** Error:"。
        if !result.succeeded || result.combined.contains("** Error") {
            throw GuardError.command(what, result.combined)
        }
    }

    private func flushCaches() {
        _ = runner.run("/usr/bin/dscacheutil", ["-flushcache"])
        _ = runner.run("/usr/bin/killall", ["-HUP", "mDNSResponder"])
    }

    private func summary(of state: NetworkState) -> NetworkChangeSummary {
        NetworkChangeSummary(dnsServices: state.dns.map(\.service), ipv6Services: state.ipv6.map(\.service))
    }

    private func loadState() -> NetworkState? {
        guard let data = FileManager.default.contents(atPath: statePath) else { return nil }
        return try? IPCCoding.decoder.decode(NetworkState.self, from: data)
    }

    private func save(_ state: NetworkState) throws {
        do {
            let data = try IPCCoding.encoder.encode(state)
            let directory = (statePath as NSString).deletingLastPathComponent
            try FileManager.default.createDirectory(atPath: directory, withIntermediateDirectories: true)
            let temp = statePath + ".tmp"
            guard FileManager.default.createFile(atPath: temp, contents: data, attributes: [.posixPermissions: 0o600]) else {
                throw GuardError.state("无法写入 \(temp)")
            }
            let handle = try FileHandle(forWritingTo: URL(fileURLWithPath: temp))
            try handle.synchronize()
            try handle.close()
            if rename(temp, statePath) != 0 { throw GuardError.state(String(cString: strerror(errno))) }
        } catch let error as GuardError {
            throw error
        } catch {
            throw GuardError.state(error.localizedDescription)
        }
    }
}
