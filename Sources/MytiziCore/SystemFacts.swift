import Foundation
import MytiziKit

// 诊断用的系统事实：全部是只读命令，每条都带超时；解析函数是纯函数，用真实输出做测试样本。

public struct DNSResolver: Equatable {
    public var index: Int
    public var nameservers: [String]
    public var domain: String?
    public var port: Int?
    public var scoped: Bool
}

public struct ResolverFile: Equatable {
    public var name: String
    public var content: String
    public var nameservers: [String]
    public var port: Int?
}

public struct ServiceNetworkInfo: Equatable {
    public var name: String
    public var device: String
    public var dnsServers: [String]
    public var dnsAutomatic: Bool
    public var ipv6: String
}

public struct SystemProxyInfo: Equatable {
    public var http: String?
    public var https: String?
    public var socks: String?
    public var pac: String?
    public var isAnyEnabled: Bool { http != nil || https != nil || socks != nil || pac != nil }
}

public struct TunInterface: Equatable {
    public var name: String
    public var addresses: [String]
}

public struct SystemFacts {
    public var macOS = ""
    public var arch = ""
    public var model = ""
    public var dnsResolvers: [DNSResolver] = []
    public var systemProxy = SystemProxyInfo()
    public var defaultRoute = ""
    /// `netstat -rn` 里所有 default 行，按优先级；第一条是 utun 就说明别的 VPN（如 Tailscale exit node）接管了默认路由。
    public var defaultRoutes: [DefaultRoute] = []
    public var tunInterfaces: [TunInterface] = []
    public var resolverFiles: [ResolverFile] = []
    /// resolver 文件里指向本机回环的端口里，实际没有进程监听的那些。
    public var deadLoopbackResolvers: [ResolverFile] = []
    public var services: [ServiceNetworkInfo] = []
    public var otherProxyProcesses: [String] = []
    public var globalIPv6Interfaces: [String] = []
    public var timestamp = Date()

    public init() {}
}

public enum SystemFactsParser {
    /// `scutil --dns`：只解析"DNS configuration"（非 scoped）下的 resolver，这才是系统实际使用的。
    public static func resolvers(fromScutilDNS text: String) -> [DNSResolver] {
        var result: [DNSResolver] = []
        var current: DNSResolver?
        var scoped = false
        for rawLine in text.components(separatedBy: .newlines) {
            let line = rawLine.trimmingCharacters(in: .whitespaces)
            if line.hasPrefix("DNS configuration") {
                scoped = line.contains("scoped")
                continue
            }
            if line.hasPrefix("resolver #") {
                if let current { result.append(current) }
                let number = Int(line.dropFirst("resolver #".count)) ?? 0
                current = DNSResolver(index: number, nameservers: [], domain: nil, port: nil, scoped: scoped)
                continue
            }
            guard current != nil, let colon = line.firstIndex(of: ":") else { continue }
            let key = line[..<colon].trimmingCharacters(in: .whitespaces)
            let value = line[line.index(after: colon)...].trimmingCharacters(in: .whitespaces)
            if key.hasPrefix("nameserver[") {
                current?.nameservers.append(value)
            } else if key == "domain" {
                current?.domain = value
            } else if key == "port" {
                current?.port = Int(value)
            }
        }
        if let current { result.append(current) }
        return result
    }

    /// `scutil --proxy`。
    public static func systemProxy(fromScutilProxy text: String) -> SystemProxyInfo {
        var values: [String: String] = [:]
        for line in text.components(separatedBy: .newlines) {
            let parts = line.split(separator: ":", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            if parts.count == 2 { values[parts[0]] = parts[1] }
        }
        func endpoint(_ enable: String, _ host: String, _ port: String) -> String? {
            guard values[enable] == "1", let h = values[host] else { return nil }
            return "\(h):\(values[port] ?? "?")"
        }
        let pac = values["ProxyAutoConfigEnable"] == "1" ? (values["ProxyAutoConfigURLString"] ?? "(启用)") : nil
        return SystemProxyInfo(
            http: endpoint("HTTPEnable", "HTTPProxy", "HTTPPort"),
            https: endpoint("HTTPSEnable", "HTTPSProxy", "HTTPSPort"),
            socks: endpoint("SOCKSEnable", "SOCKSProxy", "SOCKSPort"), pac: pac)
    }

    /// `ifconfig` 里带 IPv4 地址的 utun 网卡。
    public static func tunInterfaces(fromIfconfig text: String) -> [TunInterface] {
        var result: [TunInterface] = []
        var name: String?
        var addresses: [String] = []
        func flush() {
            if let name, name.hasPrefix("utun"), !addresses.isEmpty { result.append(TunInterface(name: name, addresses: addresses)) }
        }
        for line in text.components(separatedBy: .newlines) {
            if let first = line.first, !first.isWhitespace, let colon = line.firstIndex(of: ":") {
                flush()
                name = String(line[..<colon])
                addresses = []
            } else {
                let trimmed = line.trimmingCharacters(in: .whitespaces)
                if trimmed.hasPrefix("inet ") {
                    let parts = trimmed.split(separator: " ")
                    if parts.count > 1 { addresses.append(String(parts[1])) }
                }
            }
        }
        flush()
        return result
    }

    /// 带全局 IPv6 地址（非 fe80 链路本地）的网卡名。
    public static func globalIPv6Interfaces(fromIfconfig text: String) -> [String] {
        var result: [String] = []
        var name: String?
        for line in text.components(separatedBy: .newlines) {
            if let first = line.first, !first.isWhitespace, let colon = line.firstIndex(of: ":") {
                name = String(line[..<colon])
                continue
            }
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard trimmed.hasPrefix("inet6 "), let name, !name.hasPrefix("lo"), !name.hasPrefix("utun"), !name.hasPrefix("awdl"),
                  !name.hasPrefix("llw")
            else { continue }
            let address = trimmed.split(separator: " ").dropFirst().first.map(String.init) ?? ""
            if !address.lowercased().hasPrefix("fe80") && !result.contains(name) { result.append(name) }
        }
        return result
    }

    /// /etc/resolver/<域名> 文件内容。
    public static func resolverFile(name: String, content: String) -> ResolverFile {
        var servers: [String] = []
        var port: Int?
        for line in content.components(separatedBy: .newlines) {
            let parts = line.split(separator: " ", omittingEmptySubsequences: true).map(String.init)
            guard parts.count >= 2 else { continue }
            if parts[0] == "nameserver" { servers.append(parts[1]) }
            if parts[0] == "port" { port = Int(parts[1]) }
        }
        return ResolverFile(name: name, content: content, nameservers: servers, port: port)
    }

    /// 已知会与本 App 的 TUN 冲突的代理/VPN 进程（按可执行文件名匹配）。
    /// Tailscale 不在这里：它可以与本 App 共存，真正会冲突的两种情况（exit node 接管默认路由、覆盖系统 DNS）
    /// 由路由表与 DNS 的专门检查负责，给出的也是针对性的修法。
    public static let conflictingProcessNames: [String] = [
        "mihomo", "clash", "verge-mihomo", "sing-box", "xray", "v2ray", "surge", "clashx", "clash-verge", "shadowrocket",
        "stash", "wireguard", "openvpn", "hysteria", "tuic", "naive",
    ]

    public static func conflictingProcesses(fromPS text: String, excludingPIDs: Set<Int> = []) -> [String] {
        var found: [String] = []
        for line in text.components(separatedBy: .newlines) {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard let space = trimmed.firstIndex(of: " "), let pid = Int(trimmed[..<space]), !excludingPIDs.contains(pid) else { continue }
            let command = trimmed[trimmed.index(after: space)...].trimmingCharacters(in: .whitespaces)
            let base = (command as NSString).lastPathComponent.lowercased()
            // MyTizi 自己的核心位于 /Library/Application Support/MyTizi/，这里不算冲突。
            if command.contains("/MyTizi/") { continue }
            if conflictingProcessNames.contains(where: { base == $0 || base.hasPrefix($0 + "-") || base.hasPrefix($0 + ".") }) {
                found.append("\(pid) \(command)")
            }
        }
        return found
    }
}

public enum SystemFactsCollector {
    /// 采集一份快照。全部是只读命令；任何一条失败都只会让对应字段为空。
    public static func collect() -> SystemFacts {
        var facts = SystemFacts()
        facts.macOS = Shell.run("/usr/bin/sw_vers", ["-productVersion"], timeout: 3).stdout.trimmingCharacters(in: .whitespacesAndNewlines)
        facts.arch = Shell.run("/usr/bin/uname", ["-m"], timeout: 3).stdout.trimmingCharacters(in: .whitespacesAndNewlines)
        facts.model = Shell.run("/usr/sbin/sysctl", ["-n", "hw.model"], timeout: 3).stdout.trimmingCharacters(in: .whitespacesAndNewlines)

        facts.dnsResolvers = SystemFactsParser.resolvers(fromScutilDNS: Shell.run("/usr/sbin/scutil", ["--dns"], timeout: 5).stdout)
        facts.systemProxy = SystemFactsParser.systemProxy(fromScutilProxy: Shell.run("/usr/sbin/scutil", ["--proxy"], timeout: 5).stdout)

        let route = Shell.run("/sbin/route", ["-n", "get", "default"], timeout: 3).stdout
        facts.defaultRoute = route.components(separatedBy: .newlines)
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { $0.hasPrefix("gateway:") || $0.hasPrefix("interface:") }
            .joined(separator: "  ")
        facts.defaultRoutes = RoutingTable.currentDefaultRoutes()

        let ifconfig = Shell.run("/sbin/ifconfig", [], timeout: 5).stdout
        facts.tunInterfaces = SystemFactsParser.tunInterfaces(fromIfconfig: ifconfig)
        facts.globalIPv6Interfaces = SystemFactsParser.globalIPv6Interfaces(fromIfconfig: ifconfig)

        if let names = try? FileManager.default.contentsOfDirectory(atPath: "/etc/resolver") {
            for name in names.sorted() {
                guard let content = try? String(contentsOfFile: "/etc/resolver/\(name)", encoding: .utf8) else { continue }
                let file = SystemFactsParser.resolverFile(name: name, content: content)
                facts.resolverFiles.append(file)
                if file.nameservers.contains(where: { $0 == "127.0.0.1" || $0 == "::1" }), let port = file.port, isUDPPortFree(port) {
                    facts.deadLoopbackResolvers.append(file)
                }
            }
        }

        let order = Shell.run("/usr/sbin/networksetup", ["-listnetworkserviceorder"], timeout: 5).stdout
        for line in order.components(separatedBy: .newlines) where line.contains("Hardware Port:") {
            guard let deviceRange = line.range(of: "Device: ") else { continue }
            let device = line[deviceRange.upperBound...].trimmingCharacters(in: CharacterSet(charactersIn: ") \t"))
            guard device.hasPrefix("en") || device.hasPrefix("bridge") else { continue }
            guard let portStart = line.range(of: "Hardware Port: "), let comma = line.range(of: ", Device") else { continue }
            let name = String(line[portStart.upperBound..<comma.lowerBound])
            let dns = Shell.run("/usr/sbin/networksetup", ["-getdnsservers", name], timeout: 3).stdout
            let parsedDNS = dnsParts(dns)
            let info = Shell.run("/usr/sbin/networksetup", ["-getinfo", name], timeout: 3).stdout
            let v6 = info.components(separatedBy: .newlines).first { $0.hasPrefix("IPv6:") }?
                .dropFirst(5).trimmingCharacters(in: .whitespaces) ?? "?"
            facts.services.append(ServiceNetworkInfo(
                name: name, device: device, dnsServers: parsedDNS.servers, dnsAutomatic: parsedDNS.automatic, ipv6: v6))
        }

        let ps = Shell.run("/bin/ps", ["-axo", "pid=,comm="], timeout: 5).stdout
        facts.otherProxyProcesses = SystemFactsParser.conflictingProcesses(fromPS: ps)
        facts.timestamp = Date()
        return facts
    }

    private static func dnsParts(_ text: String) -> (automatic: Bool, servers: [String]) {
        let servers = text.components(separatedBy: .newlines)
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty && !$0.hasPrefix("There aren't any DNS Servers") }
        return (servers.isEmpty, servers)
    }

    /// 回环 UDP 端口没人占用 ⇒ 绑定会成功。
    static func isUDPPortFree(_ port: Int) -> Bool {
        let fd = socket(AF_INET, SOCK_DGRAM, 0)
        guard fd >= 0 else { return false }
        defer { close(fd) }
        var address = sockaddr_in()
        address.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        address.sin_family = sa_family_t(AF_INET)
        address.sin_port = in_port_t(port).bigEndian
        address.sin_addr = in_addr(s_addr: inet_addr("127.0.0.1"))
        return withUnsafePointer(to: &address) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        } == 0
    }
}
