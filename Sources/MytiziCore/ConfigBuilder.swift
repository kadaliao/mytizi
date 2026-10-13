import Foundation
import MytiziKit

public struct ConfigOptions {
    public var controllerPort: Int
    public var controllerSecret: String
    /// 本机回环上的 HTTP/SOCKS 端口，仅供 App 自检和"订阅刷新走代理"使用；nil 表示不监听。
    public var mixedPort: Int?
    public var tun: Bool
    /// provider 文件相对 core-home 的路径。
    public var providerPath: String
    /// 必须直连的域名（订阅所在主机），排在所有规则之前，保证节点全挂时还能刷新订阅。
    public var directHosts: [String]
    public var customRules: [String]
    public var healthCheckURL: String
    /// 原网络的本地 DNS（路由器），只用于 .lan 之类的本地域名。
    public var localDNS: [String]

    public init(
        controllerPort: Int, controllerSecret: String, mixedPort: Int? = nil, tun: Bool = true,
        providerPath: String = ConfigBuilder.providerPath, directHosts: [String] = [],
        customRules: [String] = [], healthCheckURL: String = ConfigBuilder.defaultHealthCheckURL,
        localDNS: [String] = []
    ) {
        self.controllerPort = controllerPort
        self.controllerSecret = controllerSecret
        self.mixedPort = mixedPort
        self.tun = tun
        self.providerPath = providerPath
        self.directHosts = directHosts
        self.customRules = customRules
        self.healthCheckURL = healthCheckURL
        self.localDNS = localDNS
    }
}

/// 把"极简设置"翻译成一份固定结构的 mihomo 配置。
///
/// 规则阶梯（先匹配先生效，每一级都有存在的理由，见 RuleCatalog）：
///   1. 用户自定义规则          —— 高级用户的覆盖口，小白不会看到
///   2. 订阅主机 DIRECT         —— 节点全挂时仍能刷新订阅
///   3. 内网/局域网 DIRECT      —— 含 Apple 推送（TCP 5223）
///   4. 国内平台与 CDN DIRECT   —— 纯域名，不受 DNS 漂移影响
///   5. 海外固定域名 PROXY      —— 先于 GeoSite，防止数据库过期/DNS 污染
///   6. GeoSite 国内 DIRECT、UDP 443 REJECT（让 QUIC 回退 TCP）、GeoSite 海外 PROXY
///   7. GEOIP,CN DIRECT         —— 兜底：域名没命中任何列表时按 IP 归属判断
///   8. MATCH PROXY
public enum ConfigBuilder {
    public static let providerName = "sub"
    public static let providerPath = "./providers/sub.yaml"
    public static let proxyGroup = "PROXY"
    public static let autoGroup = "AUTO"
    public static let defaultHealthCheckURL = "https://www.gstatic.com/generate_204"

    public static func build(_ options: ConfigOptions) -> YAML {
        var top: [(String, YAML)] = [
            ("allow-lan", false),
            ("bind-address", "127.0.0.1"),
            ("mode", "rule"),
            ("log-level", "info"),
            // IPv6 在三处同时关闭：核心、DNS（不返回 AAAA）、以及 helper 在系统层的关闭。
            ("ipv6", false),
            ("unified-delay", true),
            ("tcp-concurrent", true),
            ("find-process-mode", "off"),
            // GeoIP/GeoSite 随 App 内置，禁止核心联网更新，避免首次启动卡在下载上。
            ("geo-auto-update", false),
            ("geodata-mode", false),
            ("external-controller", .string("127.0.0.1:\(options.controllerPort)")),
            ("secret", .string(options.controllerSecret)),
            ("profile", .map([("store-selected", true), ("store-fake-ip", true)])),
        ]
        if let port = options.mixedPort {
            top.insert(("mixed-port", .int(port)), at: 0)
        }

        top.append(("tun", tun(options)))
        top.append(("dns", dns(options)))
        top.append(("sniffer", sniffer()))
        top.append(("proxy-providers", .map([(providerName, .map([
            ("type", "file"),
            ("path", .string(options.providerPath)),
            ("health-check", .map([
                ("enable", true), ("url", .string(options.healthCheckURL)),
                ("interval", 600), ("lazy", true),
            ])),
        ]))])))
        top.append(("proxy-groups", .array([
            .map([
                ("name", .string(autoGroup)), ("type", "url-test"),
                ("use", .strings([providerName])), ("url", .string(options.healthCheckURL)),
                ("interval", 300), ("tolerance", 50), ("lazy", true),
            ]),
            .map([
                ("name", .string(proxyGroup)), ("type", "select"),
                ("proxies", .strings([autoGroup])), ("use", .strings([providerName])),
            ]),
        ])))
        top.append(("rules", .strings(rules(options))))
        return .map(top)
    }

    static func tun(_ options: ConfigOptions) -> YAML {
        .map([
            ("enable", .bool(options.tun)),
            ("stack", "mixed"),
            ("auto-route", true),
            ("auto-detect-interface", true),
            ("strict-route", false),
            ("inet4-address", .strings(["\(AppInfo.tunGateway)/30"])),
            ("dns-hijack", .strings(["any:53", "tcp://any:53"])),
        ])
    }

    static func dns(_ options: ConfigOptions) -> YAML {
        var pairs: [(String, YAML)] = [
            ("enable", true),
            ("ipv6", false),
            ("use-hosts", true),
            ("enhanced-mode", "fake-ip"),
            ("fake-ip-range", "198.18.0.1/16"),
            ("fake-ip-filter", .strings(RuleCatalog.fakeIPFilter)),
            ("default-nameserver", .strings(RuleCatalog.domesticDNS)),
            ("nameserver", .strings(RuleCatalog.domesticDNS)),
            ("proxy-server-nameserver", .strings(RuleCatalog.domesticDNS)),
        ]
        let local = options.localDNS.filter { !$0.hasPrefix("198.18.") && $0 != "127.0.0.1" }
        if !local.isEmpty {
            pairs.append(("nameserver-policy", .map(
                ["+.lan", "+.home.arpa", "+.localdomain"].map { ($0, YAML.strings(local)) }
            )))
        }
        return .map(pairs)
    }

    static func sniffer() -> YAML {
        .map([
            ("enable", true),
            ("force-dns-mapping", true),
            ("parse-pure-ip", true),
            ("override-destination", true),
            ("sniff", .map([
                ("HTTP", .map([("ports", .array([80, "8080-8880"]))])),
                ("TLS", .map([("ports", .array([443, 8443]))])),
                ("QUIC", .map([("ports", .array([443, 8443]))])),
            ])),
            ("skip-domain", .strings(RuleCatalog.snifferSkip)),
        ])
    }

    public static func rules(_ options: ConfigOptions) -> [String] {
        var rules: [String] = []
        rules += options.customRules
        rules += options.directHosts.map { "DOMAIN,\($0),DIRECT" }
        rules.append("AND,((NETWORK,TCP),(DST-PORT,5223),(IP-CIDR,17.0.0.0/8)),DIRECT")
        rules += RuleCatalog.privateIPv4.map { "IP-CIDR,\($0),DIRECT,no-resolve" }
        rules.append("GEOSITE,private,DIRECT")
        rules += RuleCatalog.directDomains.map { "DOMAIN-SUFFIX,\($0),DIRECT" }
        rules += RuleCatalog.proxyDomains.map { "DOMAIN-SUFFIX,\($0),\(proxyGroup)" }
        rules += [
            "GEOSITE,apple@cn,DIRECT",
            "GEOSITE,microsoft@cn,DIRECT",
            "GEOSITE,cn,DIRECT",
            "AND,((NETWORK,UDP),(DST-PORT,443)),REJECT",
            "GEOSITE,geolocation-!cn,\(proxyGroup)",
            "GEOIP,CN,DIRECT",
            "MATCH,\(proxyGroup)",
        ]
        return rules
    }

    /// 仅用于解析订阅的探测配置：不监听任何端口、不启用 TUN/DNS，由 App 以普通用户身份运行。
    public static func buildProbe(controllerPort: Int, secret: String, providerPath: String = providerPath) -> YAML {
        .map([
            ("log-level", "warning"),
            ("ipv6", false),
            ("geo-auto-update", false),
            ("external-controller", .string("127.0.0.1:\(controllerPort)")),
            ("secret", .string(secret)),
            ("dns", .map([("enable", false)])),
            ("proxy-providers", .map([(providerName, .map([
                ("type", "file"), ("path", .string(providerPath)),
            ]))])),
            ("rules", .strings(["MATCH,DIRECT"])),
        ])
    }
}

// MARK: 自定义规则

public struct CustomRuleError: Error, Equatable, CustomStringConvertible {
    public var line: Int
    public var message: String
    public var description: String { "第 \(line) 行：\(message)" }
}

/// 解析 custom-rules.txt。每行一条规则，`#` 开头为注释，行尾 ` # 说明` 也会被忽略。
/// 目标只允许 PROXY / DIRECT / REJECT（也接受 代理 / 直连 / 拒绝），其余在这里直接报行号，
/// 不让一份写坏的规则拖到 `mihomo -t` 才整体失败。
public enum CustomRules {
    static let allowedTypes: Set<String> = [
        "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "IP-CIDR", "IP-CIDR6", "GEOIP", "GEOSITE",
        "PROCESS-NAME", "DST-PORT",
    ]
    static let targetAliases: [String: String] = [
        "PROXY": "PROXY", "DIRECT": "DIRECT", "REJECT": "REJECT",
        "代理": "PROXY", "直连": "DIRECT", "拒绝": "REJECT",
    ]

    public static func parse(_ text: String) throws -> [String] {
        var result: [String] = []
        for (index, raw) in text.components(separatedBy: .newlines).enumerated() {
            var line = raw.trimmingCharacters(in: .whitespaces)
            if let range = line.range(of: " #") { line = String(line[..<range.lowerBound]).trimmingCharacters(in: .whitespaces) }
            if line.isEmpty || line.hasPrefix("#") { continue }
            let number = index + 1
            let parts = line.split(separator: ",", omittingEmptySubsequences: false)
                .map { $0.trimmingCharacters(in: .whitespaces) }
            guard parts.count >= 3 else {
                throw CustomRuleError(line: number, message: "格式应为 类型,值,目标，例如 DOMAIN-SUFFIX,example.com,PROXY")
            }
            let type = parts[0].uppercased()
            guard allowedTypes.contains(type) else {
                throw CustomRuleError(line: number, message: "不支持的规则类型 \(parts[0])")
            }
            guard !parts[1].isEmpty else { throw CustomRuleError(line: number, message: "缺少匹配值") }
            guard let target = targetAliases[parts[2].uppercased()] ?? targetAliases[parts[2]] else {
                throw CustomRuleError(line: number, message: "目标必须是 PROXY、DIRECT 或 REJECT，收到 \(parts[2])")
            }
            var rule = [type, parts[1], target]
            if parts.count > 3 {
                guard parts.count == 4, parts[3].lowercased() == "no-resolve", type.hasPrefix("IP-CIDR") || type == "GEOIP" else {
                    throw CustomRuleError(line: number, message: "多余的参数 \(parts[3...].joined(separator: ","))")
                }
                rule.append("no-resolve")
            }
            result.append(rule.joined(separator: ","))
        }
        return result
    }
}
