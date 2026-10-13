import Foundation

/// 系统里一个可以设置 DNS / IPv6 的网络服务（Wi-Fi、Ethernet 等）。
public struct NetworkService: Equatable {
    public var name: String
    public var device: String
}

public enum IPv6Mode: String, Codable, Equatable {
    case automatic, off, linkLocal, manual
}

public struct IPv6Setting: Codable, Equatable {
    public var mode: IPv6Mode
    public var address: String?
    public var prefixLength: String?
    public var router: String?
}

/// `networksetup` 输出的解析。全部是纯函数，用真实机器上抓取的输出做测试样本。
public enum NetworkSetupParser {
    /// `networksetup -listnetworkserviceorder`。
    /// 只保留已启用、且挂在物理网卡（enN / bridgeN）上的服务：VPN、Tailscale、USB 调制解调器
    /// 这类服务不是流量的出口，改它们的 DNS 没有意义，还可能影响对应软件。
    public static func services(fromServiceOrder text: String) -> [NetworkService] {
        let lines = text.components(separatedBy: .newlines)
        var result: [NetworkService] = []
        var index = 0
        while index < lines.count {
            let line = lines[index].trimmingCharacters(in: .whitespaces)
            index += 1
            guard line.hasPrefix("("), let close = line.firstIndex(of: ")") else { continue }
            let marker = String(line[line.index(after: line.startIndex)..<close])
            let name = line[line.index(after: close)...].trimmingCharacters(in: .whitespaces)
            // "(Hardware Port: Wi-Fi, Device: en1)" 在下一行
            guard marker != "*", Int(marker) != nil, !name.isEmpty, index < lines.count else { continue }
            let detail = lines[index]
            guard let range = detail.range(of: "Device: ") else { continue }
            let device = detail[range.upperBound...].trimmingCharacters(in: CharacterSet(charactersIn: ") \t"))
            if device.range(of: "^(en|bridge)[0-9]+$", options: .regularExpression) != nil {
                result.append(NetworkService(name: name, device: device))
            }
        }
        return result
    }

    /// `networksetup -getdnsservers <service>`：没有手动设置时输出一句话，视为"自动（DHCP）"。
    public static func dnsServers(from text: String) -> (automatic: Bool, servers: [String]) {
        let servers = text.components(separatedBy: .newlines)
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty && !$0.hasPrefix("There aren't any DNS Servers") }
        return (servers.isEmpty, servers)
    }

    /// `networksetup -getinfo <service>` 里的 IPv6 段。无法识别返回 nil，调用方应跳过该服务而不是乱改。
    public static func ipv6Setting(fromInfo text: String) -> IPv6Setting? {
        func value(_ key: String) -> String? {
            for line in text.components(separatedBy: .newlines) where line.hasPrefix(key + ":") {
                let v = line.dropFirst(key.count + 1).trimmingCharacters(in: .whitespaces)
                return v.isEmpty || v.lowercased() == "none" ? nil : v
            }
            return nil
        }
        guard let rawMode = value("IPv6")?.lowercased() else { return nil }
        let mode: IPv6Mode
        switch rawMode {
        case "automatic": mode = .automatic
        case "off": mode = .off
        case "manual": mode = .manual
        case let other where other.hasPrefix("link"): mode = .linkLocal
        default: return nil
        }
        return IPv6Setting(
            mode: mode, address: value("IPv6 IP address"), prefixLength: value("IPv6 Prefix Length"),
            router: value("IPv6 Router"))
    }
}
