import Foundation
import MytiziKit

/// 当前用户目录下的文件位置。`root` 可注入，测试用临时目录。
public struct UserPaths {
    public let support: URL
    public let logs: URL
    public let caches: URL

    public init(home: URL = FileManager.default.homeDirectoryForCurrentUser) {
        support = home.appendingPathComponent("Library/Application Support/\(AppInfo.displayName)", isDirectory: true)
        logs = home.appendingPathComponent("Library/Logs/\(AppInfo.displayName)", isDirectory: true)
        caches = home.appendingPathComponent("Library/Caches/\(AppInfo.displayName)", isDirectory: true)
    }

    public var settings: URL { support.appendingPathComponent("settings.json") }
    /// 订阅原文（任意格式，由 mihomo 解析）。
    public var subscriptionBody: URL { support.appendingPathComponent("subscription.txt") }
    public var customRules: URL { support.appendingPathComponent("custom-rules.txt") }
    /// 最近一次发给 helper 的配置的脱敏副本，供排查使用；真实配置只存在于 root 目录。
    public var lastConfigRedacted: URL { support.appendingPathComponent("last-config.redacted.yaml") }
    public var appLog: URL { logs.appendingPathComponent("app.log") }

    public func prepare() throws {
        for dir in [support, logs, caches] {
            try FileManager.default.createDirectory(
                at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        }
    }
}

/// 原子写入：先写同目录临时文件再 rename，避免断电或崩溃留下半截文件。
public enum AtomicFile {
    public static func write(_ data: Data, to url: URL, permissions: Int = 0o600) throws {
        let temp = url.deletingLastPathComponent().appendingPathComponent(".\(url.lastPathComponent).\(UUID().uuidString.prefix(8)).tmp")
        guard FileManager.default.createFile(atPath: temp.path, contents: data, attributes: [.posixPermissions: permissions]) else {
            throw CocoaError(.fileWriteUnknown, userInfo: [NSFilePathErrorKey: temp.path])
        }
        do {
            let handle = try FileHandle(forWritingTo: temp)
            try handle.synchronize()
            try handle.close()
            if FileManager.default.fileExists(atPath: url.path) {
                _ = try FileManager.default.replaceItemAt(url, withItemAt: temp)
            } else {
                try FileManager.default.moveItem(at: temp, to: url)
            }
        } catch {
            try? FileManager.default.removeItem(at: temp)
            throw error
        }
    }
}
