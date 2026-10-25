import Foundation

public enum MihomoAPIError: LocalizedError {
    case http(Int, String)
    case invalidResponse
    case transport(String)

    public var errorDescription: String? {
        switch self {
        case let .http(code, body): return "控制接口返回 \(code)：\(body)"
        case .invalidResponse: return "控制接口返回了无法识别的内容"
        case let .transport(reason): return "无法连接控制接口：\(reason)"
        }
    }
}

public struct ProxyDelayHistory: Decodable, Equatable {
    public var time: String?
    public var delay: Int
}

public struct ProviderProxy: Decodable, Equatable {
    public var name: String
    public var type: String
    public var udp: Bool?
    public var history: [ProxyDelayHistory]?

    /// 最近一次测速的延迟（毫秒）；0 或没有记录表示不可用/未测。
    public var lastDelay: Int? {
        guard let value = history?.last?.delay, value > 0 else { return nil }
        return value
    }
}

public struct ProviderInfo: Decodable {
    public var name: String
    public var proxies: [ProviderProxy]
}

public struct ProxyGroupInfo: Decodable {
    public var name: String
    public var now: String?
    public var all: [String]?
}

public struct ConnectionMetadata: Decodable {
    public var host: String?
    public var destinationIP: String?
    public var destinationPort: String?
    public var network: String?
    public var type: String?
}

public struct ConnectionInfo: Decodable {
    public var id: String
    public var metadata: ConnectionMetadata
    public var chains: [String]
    public var rule: String
    public var rulePayload: String
}

public struct ConnectionsSnapshot: Decodable {
    public var uploadTotal: Int64
    public var downloadTotal: Int64
    public var connections: [ConnectionInfo]
}

/// `GET /configs` 里本 App 关心的几项。
public struct GeneralConfig: Decodable {
    public var mode: String?
    /// 出站绑定的网卡；空字符串表示由核心自动检测。
    public var interfaceName: String?

    enum CodingKeys: String, CodingKey {
        case mode
        case interfaceName = "interface-name"
    }
}

/// mihomo 控制接口（external-controller）的最小客户端，只用到 App 需要的那几个接口。
public struct MihomoAPI {
    public let port: Int
    public let secret: String
    private let session: URLSession

    public init(port: Int, secret: String) {
        self.port = port
        self.secret = secret
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = 5
        // 回环请求绝不能走系统代理。
        configuration.connectionProxyDictionary = [:]
        session = URLSession(configuration: configuration)
    }

    // MARK: 接口

    public func version() async throws -> String {
        struct Version: Decodable { var version: String }
        return try await send("GET", "/version", as: Version.self).version
    }

    public func provider(_ name: String) async throws -> ProviderInfo {
        try await send("GET", "/providers/proxies/\(name)", as: ProviderInfo.self)
    }

    public func updateProvider(_ name: String) async throws {
        _ = try await raw("PUT", "/providers/proxies/\(name)")
    }

    public func group(_ name: String) async throws -> ProxyGroupInfo {
        try await send("GET", "/proxies/\(name)", as: ProxyGroupInfo.self)
    }

    public func select(group: String, proxy: String) async throws {
        _ = try await raw("PUT", "/proxies/\(group)", body: ["name": proxy])
    }

    /// 单个节点测速，返回毫秒。
    public func delay(of proxy: String, url: String, timeoutMs: Int = 5000) async throws -> Int {
        struct Delay: Decodable { var delay: Int }
        return try await send(
            "GET", "/proxies/\(proxy)/delay",
            query: [URLQueryItem(name: "url", value: url), URLQueryItem(name: "timeout", value: String(timeoutMs))],
            as: Delay.self, timeout: TimeInterval(timeoutMs) / 1000 + 3
        ).delay
    }

    /// 整组并发测速，返回 节点名 → 毫秒（失败的节点不在结果里）。
    public func groupDelay(_ group: String, url: String, timeoutMs: Int = 5000) async throws -> [String: Int] {
        try await send(
            "GET", "/group/\(group)/delay",
            query: [URLQueryItem(name: "url", value: url), URLQueryItem(name: "timeout", value: String(timeoutMs))],
            as: [String: Int].self, timeout: TimeInterval(timeoutMs) / 1000 + 5)
    }

    public func connections() async throws -> ConnectionsSnapshot {
        try await send("GET", "/connections", as: ConnectionsSnapshot.self)
    }

    public func general() async throws -> GeneralConfig {
        try await send("GET", "/configs", as: GeneralConfig.self)
    }

    /// 网络切换/唤醒后清掉走旧出口的连接，让应用按新的网络重连。
    public func closeAllConnections() async throws {
        _ = try await raw("DELETE", "/connections")
    }

    // MARK: 传输

    private func url(_ path: String, query: [URLQueryItem]) -> URL {
        var components = URLComponents()
        components.scheme = "http"
        components.host = "127.0.0.1"
        components.port = port
        // 节点名常含中文、空格和 emoji，必须按路径段编码。
        let encoded = path.split(separator: "/", omittingEmptySubsequences: true).map {
            String($0).addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(CharacterSet(charactersIn: "/"))) ?? String($0)
        }
        components.percentEncodedPath = "/" + encoded.joined(separator: "/")
        if !query.isEmpty { components.queryItems = query }
        return components.url!
    }

    private func raw(
        _ method: String, _ path: String, query: [URLQueryItem] = [], body: [String: String]? = nil,
        timeout: TimeInterval? = nil
    ) async throws -> Data {
        var request = URLRequest(url: url(path, query: query))
        request.httpMethod = method
        request.setValue("Bearer \(secret)", forHTTPHeaderField: "Authorization")
        if let timeout { request.timeoutInterval = timeout }
        if let body {
            request.httpBody = try JSONSerialization.data(withJSONObject: body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        do {
            let (data, response) = try await session.data(for: request)
            guard let http = response as? HTTPURLResponse else { throw MihomoAPIError.invalidResponse }
            guard (200..<300).contains(http.statusCode) else {
                throw MihomoAPIError.http(http.statusCode, String(decoding: data.prefix(300), as: UTF8.self))
            }
            return data
        } catch let error as MihomoAPIError {
            throw error
        } catch {
            throw MihomoAPIError.transport(error.localizedDescription)
        }
    }

    private func send<T: Decodable>(
        _ method: String, _ path: String, query: [URLQueryItem] = [], as _: T.Type, timeout: TimeInterval? = nil
    ) async throws -> T {
        let data = try await raw(method, path, query: query, timeout: timeout)
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw MihomoAPIError.invalidResponse
        }
    }
}
