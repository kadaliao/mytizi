import Foundation

/// 追加写入、按大小轮转的日志文件。权限 0644：用户和排查用的 AI 不需要 root 就能读。
public final class RotatingLog {
    public let path: String
    private let maxBytes: Int
    private let keep: Int
    private let queue = DispatchQueue(label: "mytizi.log")
    private var handle: FileHandle?
    private var size = 0

    public init(path: String, maxBytes: Int = 5 * 1024 * 1024, keep: Int = 2) {
        self.path = path
        self.maxBytes = maxBytes
        self.keep = keep
        queue.sync { openLocked() }
    }

    public func write(_ data: Data) {
        queue.sync {
            if size + data.count > maxBytes { rotateLocked() }
            guard let handle else { return }
            try? handle.write(contentsOf: data)
            size += data.count
        }
    }

    /// 带时间戳的一行（helper 自己的日志）。
    public func line(_ message: String) {
        let stamp = ISO8601DateFormatter().string(from: Date())
        write(Data("\(stamp) \(message)\n".utf8))
    }

    /// 日志末尾 N 行，用于在错误里附带上下文。
    public func tail(lines count: Int, maxBytes limit: Int = 64 * 1024) -> String {
        queue.sync { Self.tail(ofFileAt: path, lines: count, maxBytes: limit) }
    }

    /// 读任意日志文件的末尾（不需要写权限，也不打开写句柄），诊断时读 root 写的日志用。
    public static func tail(ofFileAt path: String, lines count: Int, maxBytes limit: Int = 256 * 1024) -> String {
        guard let reader = FileHandle(forReadingAtPath: path) else { return "" }
        defer { try? reader.close() }
        let end = (try? reader.seekToEnd()) ?? 0
        let start = end > UInt64(limit) ? end - UInt64(limit) : 0
        try? reader.seek(toOffset: start)
        let data = (try? reader.readToEnd()) ?? Data()
        let text = String(decoding: data, as: UTF8.self)
        return text.split(separator: "\n", omittingEmptySubsequences: true).suffix(count).joined(separator: "\n")
    }

    private func openLocked() {
        let directory = (path as NSString).deletingLastPathComponent
        try? FileManager.default.createDirectory(
            atPath: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o755])
        if !FileManager.default.fileExists(atPath: path) {
            FileManager.default.createFile(atPath: path, contents: nil, attributes: [.posixPermissions: 0o644])
        }
        handle = FileHandle(forWritingAtPath: path)
        size = Int((try? handle?.seekToEnd()) ?? 0)
    }

    private func rotateLocked() {
        try? handle?.close()
        handle = nil
        for index in stride(from: keep, through: 1, by: -1) {
            let source = index == 1 ? path : "\(path).\(index - 1)"
            let target = "\(path).\(index)"
            try? FileManager.default.removeItem(atPath: target)
            try? FileManager.default.moveItem(atPath: source, toPath: target)
        }
        openLocked()
    }
}
