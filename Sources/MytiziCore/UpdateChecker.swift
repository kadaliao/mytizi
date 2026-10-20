import Foundation
import MytiziKit

/// 一次“有新版本”的结论。
public struct UpdateInfo: Equatable {
    /// 去掉前导 v 的版本号，例如 0.2.0。
    public var version: String
    /// 上游 tag，例如 v0.2.0。
    public var tag: String
    public var releaseURL: URL
    /// 按发布约定拼出的 dmg 地址；不存在时退回 releaseURL。
    public var downloadURL: URL?

    public init(version: String, tag: String, releaseURL: URL, downloadURL: URL?) {
        self.version = version
        self.tag = tag
        self.releaseURL = releaseURL
        self.downloadURL = downloadURL
    }
}

public enum UpdateCheckError: LocalizedError, Equatable {
    case unreachable(String)
    case noRelease
    case unexpectedResponse(String)
    case invalidVersion(String)

    public var errorDescription: String? {
        switch self {
        case let .unreachable(reason): return "检查更新失败：\(reason)"
        case .noRelease: return "上游还没有发布版本。"
        case let .unexpectedResponse(reason): return "检查更新失败：\(reason)"
        case let .invalidVersion(text): return "无法识别上游版本号：\(text)"
        }
    }
}

/// 语义化版本（只要求“点分数字 + 可选 -预发布”）。不用第三方库，比较规则够用：
/// 数字逐段比较，数字相同时正式版比预发布版新。
public struct AppVersion: Comparable, Equatable, CustomStringConvertible {
    public var numbers: [Int]
    public var prerelease: String?

    public init?(_ text: String) {
        var value = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if value.first == "v" || value.first == "V" { value.removeFirst() }
        guard !value.isEmpty else { return nil }
        var prerelease: String?
        if let dash = value.firstIndex(of: "-") {
            prerelease = String(value[value.index(after: dash)...]).lowercased()
            value = String(value[..<dash])
        }
        let parts = value.split(separator: ".", omittingEmptySubsequences: false)
        guard !parts.isEmpty else { return nil }
        var numbers: [Int] = []
        for part in parts {
            // 允许 0.2.0+build 这种构建元数据，忽略 + 之后的内容。
            let digits = part.split(separator: "+", maxSplits: 1).first.map(String.init) ?? ""
            guard !digits.isEmpty, digits.allSatisfy(\.isNumber), let number = Int(digits) else { return nil }
            numbers.append(number)
        }
        self.numbers = numbers
        self.prerelease = prerelease?.isEmpty == true ? nil : prerelease
    }

    public var description: String {
        numbers.map(String.init).joined(separator: ".") + (prerelease.map { "-\($0)" } ?? "")
    }

    public static func < (lhs: Self, rhs: Self) -> Bool {
        let count = max(lhs.numbers.count, rhs.numbers.count)
        for index in 0..<count {
            let left = index < lhs.numbers.count ? lhs.numbers[index] : 0
            let right = index < rhs.numbers.count ? rhs.numbers[index] : 0
            if left != right { return left < right }
        }
        switch (lhs.prerelease, rhs.prerelease) {
        case (nil, nil): return false
        case (nil, _): return false   // 正式版比预发布版新
        case (_, nil): return true
        case let (left?, right?): return left < right
        }
    }
}

/// 更新检测。刻意不引入 Sparkle 之类的更新框架：
/// App 是 ad-hoc 签名、未公证，自动替换 App 会被 Gatekeeper 拦下；
/// 所以这里只负责“发现有新版本”，然后打开下载页让用户手动覆盖安装。
///
/// 用 `releases/latest` 的重定向拿版本号（`/releases/tag/vX.Y.Z`），
/// 不调 GitHub API，避免未登录调用每小时 60 次的限流。
public struct UpdateChecker {
    public static let defaultRepository = AppInfo.repository

    public let repository: String
    private let baseURL: URL
    private let session: URLSession

    public init(
        repository: String = UpdateChecker.defaultRepository,
        baseURL: URL = URL(string: "https://github.com")!,
        timeout: TimeInterval = 12
    ) {
        self.repository = repository
        self.baseURL = baseURL
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = timeout
        configuration.timeoutIntervalForResource = timeout * 2
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        configuration.connectionProxyDictionary = [:]
        session = URLSession(configuration: configuration)
    }

    public func latest() async throws -> UpdateInfo {
        guard let url = latestURL else { throw UpdateCheckError.unreachable("仓库地址无效") }
        var request = URLRequest(url: url)
        request.setValue("\(AppInfo.displayName)/update-check", forHTTPHeaderField: "User-Agent")
        request.setValue("text/html", forHTTPHeaderField: "Accept")

        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch let error as URLError {
            throw UpdateCheckError.unreachable(error.localizedDescription)
        } catch {
            throw UpdateCheckError.unreachable(error.localizedDescription)
        }
        guard let http = response as? HTTPURLResponse else { throw UpdateCheckError.unexpectedResponse("响应无效") }
        guard (200..<300).contains(http.statusCode) else {
            throw UpdateCheckError.unexpectedResponse("HTTP \(http.statusCode)")
        }
        guard let finalURL = http.url ?? response.url else { throw UpdateCheckError.unexpectedResponse("没有拿到跳转后的地址") }
        // 只接受跳转到同一个站点：避免本机代理把“检查更新”导向任意页面/下载地址。
        guard finalURL.host == baseURL.host, finalURL.scheme == baseURL.scheme else {
            throw UpdateCheckError.unexpectedResponse("跳转到了意外的地址")
        }

        // /releases/latest 会 302 到 /releases/tag/vX.Y.Z；没有正式版本时落到 /releases。
        let components = finalURL.pathComponents.filter { $0 != "/" }
        guard let index = components.firstIndex(of: "tag"), index + 1 < components.count else {
            throw UpdateCheckError.noRelease
        }
        let tag = components[index + 1]
        let version = Self.normalizedTag(tag)
        guard AppVersion(version) != nil else { throw UpdateCheckError.invalidVersion(tag) }

        // tag 会拼进下载 URL，必须是安全字符；否则只打开已经验证过的 tag 页面。
        let safeTag = tag.range(of: "^[A-Za-z0-9._-]+$", options: .regularExpression) != nil
        let release = safeTag ? URL(string: "\(baseURL.absoluteString)/\(repository)/releases/tag/\(tag)") : nil
        // 从发布页 HTML 里找真实的 dmg 链接（找不到就让界面退回发布页，不拼一个可能 404 的地址）。
        let download = Self.dmgURL(in: data, tag: tag, baseURL: baseURL)
        return UpdateInfo(version: version, tag: tag, releaseURL: release ?? finalURL, downloadURL: download)
    }

    /// GitHub 的发布页有时把资产列表放在 include-fragment 里，所以找不到是正常情况。
    static func dmgURL(in data: Data, tag: String, baseURL: URL) -> URL? {
        let html = String(decoding: data, as: UTF8.self)
        let pattern = #"href="([^"]*?/releases/download/[^"]*?\.dmg)""#
        guard let regex = try? NSRegularExpression(pattern: pattern) else { return nil }
        let range = NSRange(html.startIndex..., in: html)
        for match in regex.matches(in: html, range: range) {
            guard let linkRange = Range(match.range(at: 1), in: html) else { continue }
            let link = String(html[linkRange]).replacingOccurrences(of: "&amp;", with: "&")
            guard link.contains("/releases/download/\(tag)/"), let url = URL(string: link, relativeTo: baseURL) else { continue }
            return url.absoluteURL
        }
        return nil
    }

    public var latestURL: URL? {
        let component = "[A-Za-z0-9_-][A-Za-z0-9_.-]*"
        guard repository.range(of: "^\(component)/\(component)$", options: .regularExpression) != nil,
              !repository.contains("..")
        else { return nil }
        return URL(string: "\(baseURL.absoluteString)/\(repository)/releases/latest")
    }

    public static func normalizedTag(_ tag: String) -> String {
        var value = tag.trimmingCharacters(in: .whitespacesAndNewlines)
        if value.first == "v" || value.first == "V" { value.removeFirst() }
        return value
    }

    public static func isNewer(_ candidate: String, than current: String) -> Bool {
        guard let new = AppVersion(candidate), let existing = AppVersion(current) else { return false }
        return new > existing
    }

    public func isNewer(_ candidate: String, than current: String) -> Bool {
        Self.isNewer(candidate, than: current)
    }
}
