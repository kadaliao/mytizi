import Foundation

public struct ProbedNode: Codable, Equatable, Hashable {
    public var name: String
    /// mihomo 报告的类型，例如 Shadowsocks、Vmess、Trojan、Http。
    public var type: String
    public var udp: Bool

    public init(name: String, type: String, udp: Bool = false) {
        self.name = name
        self.type = type
        self.udp = udp
    }
}

public struct SubscriptionUsage: Codable, Equatable {
    public var upload: Int64
    public var download: Int64
    public var total: Int64
    public var expire: Date?

    public init(upload: Int64, download: Int64, total: Int64, expire: Date?) {
        self.upload = upload
        self.download = download
        self.total = total
        self.expire = expire
    }

    public var used: Int64 { upload + download }
}

public struct SubscriptionRecord: Codable, Equatable {
    public var url: String
    public var name: String
    public var addedAt: Date
    public var updatedAt: Date?
    public var nodes: [ProbedNode]
    public var usage: SubscriptionUsage?
    /// 订阅服务端通过 profile-update-interval 建议的刷新间隔（小时）。
    public var updateIntervalHours: Int?

    public init(
        url: String, name: String, addedAt: Date = Date(), updatedAt: Date? = nil,
        nodes: [ProbedNode] = [], usage: SubscriptionUsage? = nil, updateIntervalHours: Int? = nil
    ) {
        self.url = url
        self.name = name
        self.addedAt = addedAt
        self.updatedAt = updatedAt
        self.nodes = nodes
        self.usage = usage
        self.updateIntervalHours = updateIntervalHours
    }

    public var host: String { URL(string: url)?.host ?? "" }
}

public struct AppSettings: Codable, Equatable {
    /// 产品只支持一个订阅："只需要添加一个远端的订阅链接"。
    public var subscription: SubscriptionRecord?
    /// 连接期间在系统层关闭 IPv6（断开后恢复）。默认开启：IPv6 流量不经过 TUN，会绕过规则。
    public var disableIPv6: Bool
    /// 连接期间把系统 DNS 指向 TUN，保证所有解析都被接管。
    public var takeOverDNS: Bool
    public var healthCheckURL: String
    public var autoConnectOnLaunch: Bool
    /// 每天自动检测一次新版本（只检测，不自动替换 App）。
    public var autoCheckUpdates: Bool
    public var lastUpdateCheck: Date?
    /// 用户点了“忽略此版本”的版本号。
    public var skippedUpdateVersion: String?

    public init(
        subscription: SubscriptionRecord? = nil, disableIPv6: Bool = true, takeOverDNS: Bool = true,
        healthCheckURL: String = ConfigBuilder.defaultHealthCheckURL, autoConnectOnLaunch: Bool = false,
        autoCheckUpdates: Bool = true, lastUpdateCheck: Date? = nil, skippedUpdateVersion: String? = nil
    ) {
        self.subscription = subscription
        self.disableIPv6 = disableIPv6
        self.takeOverDNS = takeOverDNS
        self.healthCheckURL = healthCheckURL
        self.autoConnectOnLaunch = autoConnectOnLaunch
        self.autoCheckUpdates = autoCheckUpdates
        self.lastUpdateCheck = lastUpdateCheck
        self.skippedUpdateVersion = skippedUpdateVersion
    }

    // 逐项容错解码：以后加字段不会让旧的 settings.json 读失败。
    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        subscription = try container.decodeIfPresent(SubscriptionRecord.self, forKey: .subscription)
        disableIPv6 = try container.decodeIfPresent(Bool.self, forKey: .disableIPv6) ?? true
        takeOverDNS = try container.decodeIfPresent(Bool.self, forKey: .takeOverDNS) ?? true
        healthCheckURL = try container.decodeIfPresent(String.self, forKey: .healthCheckURL) ?? ConfigBuilder.defaultHealthCheckURL
        autoConnectOnLaunch = try container.decodeIfPresent(Bool.self, forKey: .autoConnectOnLaunch) ?? false
        autoCheckUpdates = try container.decodeIfPresent(Bool.self, forKey: .autoCheckUpdates) ?? true
        lastUpdateCheck = try container.decodeIfPresent(Date.self, forKey: .lastUpdateCheck)
        skippedUpdateVersion = try container.decodeIfPresent(String.self, forKey: .skippedUpdateVersion)
    }
}

public struct SettingsStore {
    public let paths: UserPaths

    public init(paths: UserPaths = UserPaths()) { self.paths = paths }

    public func load() -> AppSettings {
        guard let data = try? Data(contentsOf: paths.settings),
              let settings = try? Self.decoder.decode(AppSettings.self, from: data)
        else { return AppSettings() }
        return settings
    }

    public func save(_ settings: AppSettings) throws {
        try paths.prepare()
        try AtomicFile.write(Self.encoder.encode(settings), to: paths.settings)
    }

    public func loadSubscriptionBody() -> Data? { try? Data(contentsOf: paths.subscriptionBody) }

    public func saveSubscriptionBody(_ data: Data) throws {
        try paths.prepare()
        try AtomicFile.write(data, to: paths.subscriptionBody)
    }

    public func loadCustomRules() throws -> [String] {
        guard let text = try? String(contentsOf: paths.customRules, encoding: .utf8) else { return [] }
        return try CustomRules.parse(text)
    }

    static let encoder: JSONEncoder = {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        encoder.dateEncodingStrategy = .iso8601
        return encoder
    }()

    static let decoder: JSONDecoder = {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return decoder
    }()
}
