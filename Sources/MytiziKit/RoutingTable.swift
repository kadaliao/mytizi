import Foundation

/// `netstat -rn` 里的一条默认路由，按内核给出的优先级顺序排列（第一条就是系统实际使用的）。
public struct DefaultRoute: Codable, Equatable {
    public var gateway: String
    public var interface: String
    public var flags: String

    public init(gateway: String, interface: String, flags: String) {
        self.gateway = gateway
        self.interface = interface
        self.flags = flags
    }
}

/// 路由表里"谁是真正的出口"。
///
/// mihomo 在 macOS 上的 auto-route 不改默认路由，只往自己的 utun 加 1/8 … 128/1 八条子网路由；
/// 而 Tailscale 之类的 VPN 一旦启用 exit node，会把自己的 utun 排到默认路由的第一位。
/// mihomo 的 auto-detect-interface 取的正是第一条默认路由，于是所有出站（包括连代理节点本身）都会被那个 VPN 接走。
/// 所以本 App 自己找"物理出口"（enN / bridgeN 上的默认路由），把核心的 interface-name 固定在它上面。
public enum RoutingTable {
    /// 解析 `netstat -rn -f inet` 的输出，只保留 default 行。列位置从表头读，兼容带 Refs/Use 列的旧版本。
    public static func defaultRoutes(fromNetstat text: String) -> [DefaultRoute] {
        var result: [DefaultRoute] = []
        var gatewayColumn = 1
        var flagsColumn = 2
        var netifColumn = 3
        for line in text.components(separatedBy: .newlines) {
            let fields = line.split(whereSeparator: { $0 == " " || $0 == "\t" }).map(String.init)
            guard let first = fields.first else { continue }
            if first == "Destination" {
                if let index = fields.firstIndex(of: "Gateway") { gatewayColumn = index }
                if let index = fields.firstIndex(of: "Flags") { flagsColumn = index }
                if let index = fields.firstIndex(of: "Netif") { netifColumn = index }
                continue
            }
            guard first == "default", fields.count > max(gatewayColumn, flagsColumn, netifColumn) else { continue }
            result.append(DefaultRoute(gateway: fields[gatewayColumn], interface: fields[netifColumn], flags: fields[flagsColumn]))
        }
        return result
    }

    /// 物理网卡：以太网 / Wi-Fi / USB 网卡（enN）与雷雳桥接（bridgeN）。utun、ppp、ipsec 都是别人的隧道。
    public static func isPhysicalInterface(_ name: String) -> Bool {
        name.range(of: "^(en|bridge)[0-9]+$", options: .regularExpression) != nil
    }

    /// 第一条挂在物理网卡上的默认路由。没有默认路由（离线）时为 nil。
    public static func physicalEgress(in routes: [DefaultRoute]) -> String? {
        routes.first { isPhysicalInterface($0.interface) }?.interface
    }

    /// 读一次真实路由表（只读命令，普通用户即可）。
    public static func currentDefaultRoutes() -> [DefaultRoute] {
        let result = Shell.run("/usr/sbin/netstat", ["-rn", "-f", "inet"], timeout: 5)
        guard result.succeeded else { return [] }
        return defaultRoutes(fromNetstat: result.stdout)
    }

    public static func physicalEgress() -> String? {
        physicalEgress(in: currentDefaultRoutes())
    }
}
