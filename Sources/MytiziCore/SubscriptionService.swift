import Foundation

public enum SubscriptionError: LocalizedError, Equatable {
    case invalidURL
    case unreachable(String)
    case http(Int)
    case empty
    case notASubscription(String)
    case noNodes

    public var errorDescription: String? {
        switch self {
        case .invalidURL:
            return "这不是有效的订阅链接，请粘贴以 http:// 或 https:// 开头的完整地址。"
        case let .unreachable(reason):
            return "连不上订阅服务器：\(reason)"
        case let .http(code):
            switch code {
            case 401, 403: return "订阅服务器拒绝访问（\(code)），链接可能已失效或被停用。"
            case 404: return "订阅地址不存在（404），请检查链接是否复制完整。"
            default: return "订阅服务器返回了错误（HTTP \(code)）。"
            }
        case .empty:
            return "订阅内容是空的。"
        case let .notASubscription(hint):
            return hint
        case .noNodes:
            return "订阅里没有找到可用的线路。"
        }
    }
}

public struct FetchedSubscription: Equatable {
    public var body: Data
    public var usage: SubscriptionUsage?
    public var title: String?
    public var updateIntervalHours: Int?
}

public struct SubscriptionService {
    /// 订阅面板通常按 UA 决定返回 Clash YAML 还是通用 base64；带上 clash.meta / mihomo 关键字拿到的格式最全。
    public static let userAgent = "clash.meta/1.19 mihomo/1.19 MyTizi/0.1"
    public static let maxBodyBytes = 20 * 1024 * 1024

    public init() {}

    // MARK: 输入整理

    /// 接受用户粘贴的各种形态：首尾空白、换行、以及 clash:// 之类的导入链接。
    public static func normalizedURL(_ input: String) throws -> URL {
        var text = input.trimmingCharacters(in: .whitespacesAndNewlines)
            .replacingOccurrences(of: "\n", with: "").replacingOccurrences(of: "\r", with: "")
        if let url = URL(string: text), let scheme = url.scheme?.lowercased(),
           !["http", "https"].contains(scheme),
           let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems,
           let inner = items.first(where: { $0.name == "url" })?.value
        {
            // clash://install-config?url=https%3A%2F%2F… 这类一键导入链接
            text = inner
        }
        guard let url = URL(string: text), let scheme = url.scheme?.lowercased(),
              ["http", "https"].contains(scheme), let host = url.host, !host.isEmpty
        else { throw SubscriptionError.invalidURL }
        return url
    }

    // MARK: 抓取

    public func fetch(url: URL, timeout: TimeInterval = 20) async throws -> FetchedSubscription {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = timeout
        configuration.timeoutIntervalForResource = timeout * 2
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        // 订阅请求不应该被系统代理设置影响（比如用户本机还开着别的代理）。
        configuration.connectionProxyDictionary = [:]
        let session = URLSession(configuration: configuration)
        defer { session.finishTasksAndInvalidate() }

        var request = URLRequest(url: url)
        request.setValue(Self.userAgent, forHTTPHeaderField: "User-Agent")
        request.setValue("*/*", forHTTPHeaderField: "Accept")

        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch let error as URLError {
            throw SubscriptionError.unreachable(Self.describe(error))
        } catch {
            throw SubscriptionError.unreachable(error.localizedDescription)
        }
        guard let http = response as? HTTPURLResponse else { throw SubscriptionError.unreachable("响应无效") }
        guard (200..<300).contains(http.statusCode) else { throw SubscriptionError.http(http.statusCode) }
        try Self.validate(body: data)

        func header(_ name: String) -> String? { http.value(forHTTPHeaderField: name) }
        return FetchedSubscription(
            body: data,
            usage: header("subscription-userinfo").flatMap(Self.parseUserInfo),
            title: Self.parseTitle(profileTitle: header("profile-title"), disposition: header("content-disposition")),
            updateIntervalHours: header("profile-update-interval").flatMap { Int($0.trimmingCharacters(in: .whitespaces)) }
        )
    }

    // MARK: 解析（纯函数，便于测试）

    public static func validate(body: Data) throws {
        guard !body.isEmpty else { throw SubscriptionError.empty }
        guard body.count <= maxBodyBytes else { throw SubscriptionError.notASubscription("订阅内容过大，不像是订阅文件。") }
        let head = String(decoding: body.prefix(512), as: UTF8.self)
            .trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if head.hasPrefix("<!doctype html") || head.hasPrefix("<html") || head.hasPrefix("<?xml") || head.hasPrefix("<head") {
            throw SubscriptionError.notASubscription("订阅地址返回的是一个网页，不是订阅内容。链接可能已过期，或需要先登录。")
        }
    }

    /// `upload=455727941; download=6174315083; total=1073741824000; expire=1671815872`
    public static func parseUserInfo(_ header: String) -> SubscriptionUsage? {
        var values: [String: Int64] = [:]
        for part in header.split(separator: ";") {
            let pair = part.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            if pair.count == 2, let number = Int64(pair[1]) { values[pair[0].lowercased()] = number }
        }
        guard values["total"] != nil || values["download"] != nil || values["upload"] != nil else { return nil }
        let expire = values["expire"].flatMap { $0 > 0 ? Date(timeIntervalSince1970: TimeInterval($0)) : nil }
        return SubscriptionUsage(
            upload: values["upload"] ?? 0, download: values["download"] ?? 0, total: values["total"] ?? 0, expire: expire)
    }

    /// profile-title 可能是 `base64:xxxx`；否则取 content-disposition 里的文件名。
    public static func parseTitle(profileTitle: String?, disposition: String?) -> String? {
        if var title = profileTitle?.trimmingCharacters(in: .whitespacesAndNewlines), !title.isEmpty {
            if title.lowercased().hasPrefix("base64:"),
               let data = Data(base64Encoded: String(title.dropFirst(7))),
               let decoded = String(data: data, encoding: .utf8)
            {
                title = decoded
            }
            return title
        }
        guard let disposition else { return nil }
        // filename*=UTF-8''%E8%8A%82%E7%82%B9.yaml 优先于 filename="x.yaml"
        for key in ["filename*=utf-8''", "filename=\""] {
            if let range = disposition.range(of: key, options: .caseInsensitive) {
                var rest = String(disposition[range.upperBound...])
                if let end = rest.firstIndex(where: { $0 == "\"" || $0 == ";" }) { rest = String(rest[..<end]) }
                let decoded = rest.removingPercentEncoding ?? rest
                let name = (decoded as NSString).deletingPathExtension
                if !name.isEmpty { return name }
            }
        }
        return nil
    }

    static func describe(_ error: URLError) -> String {
        switch error.code {
        case .timedOut: return "连接超时，请检查网络。"
        case .notConnectedToInternet: return "当前没有联网。"
        case .cannotFindHost, .dnsLookupFailed: return "找不到服务器，域名解析失败。"
        case .secureConnectionFailed, .serverCertificateUntrusted, .serverCertificateHasBadDate, .serverCertificateHasUnknownRoot:
            return "安全连接失败（证书问题）。"
        case .cannotConnectToHost: return "服务器拒绝连接。"
        default: return error.localizedDescription
        }
    }
}
