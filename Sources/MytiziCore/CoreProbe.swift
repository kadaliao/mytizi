import Foundation
import MytiziKit

public enum FreePort {
    /// 在回环地址上找一个空闲端口：先试偏好端口，再让系统分配。
    public static func find(preferred: [Int] = []) -> Int {
        for port in preferred where isFree(port) { return port }
        return isFree(0) ? lastAssigned : 0
    }

    private static var lastAssigned = 0

    private static func isFree(_ port: Int) -> Bool {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        guard fd >= 0 else { return false }
        defer { close(fd) }
        var address = sockaddr_in()
        address.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        address.sin_family = sa_family_t(AF_INET)
        address.sin_port = in_port_t(port).bigEndian
        address.sin_addr = in_addr(s_addr: inet_addr("127.0.0.1"))
        let bound = withUnsafePointer(to: &address) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        guard bound == 0 else { return false }
        if port == 0 {
            var assigned = sockaddr_in()
            var length = socklen_t(MemoryLayout<sockaddr_in>.size)
            _ = withUnsafeMutablePointer(to: &assigned) {
                $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(fd, $0, &length) }
            }
            lastAssigned = Int(UInt16(bigEndian: assigned.sin_port))
        }
        return true
    }
}

/// 订阅探测：用内置 mihomo 以普通用户身份起一个"不监听任何端口、不开 TUN"的临时实例，
/// 让 mihomo 自己解析订阅（Clash YAML、base64 链接列表、各种协议都支持），再从控制接口读出节点。
/// 好处是 App 不需要自己实现任何订阅格式的解析，而且和真正连接时使用的解析器完全一致。
public enum CoreProbe {
    public enum ProbeError: LocalizedError {
        case coreMissing
        case coreExited(String)
        case timedOut(String)

        public var errorDescription: String? {
            switch self {
            case .coreMissing: return "找不到内置核心，请重新安装 App。"
            case let .coreExited(log): return "解析订阅时核心异常退出：\(log)"
            case let .timedOut(log): return "解析订阅超时：\(log)"
            }
        }
    }

    public static func nodes(
        in subscription: Data, core: URL, workDirectory: URL, timeout: TimeInterval = 10
    ) async throws -> [ProbedNode] {
        guard FileManager.default.isExecutableFile(atPath: core.path) else { throw ProbeError.coreMissing }

        let directory = workDirectory.appendingPathComponent("probe-\(UUID().uuidString.prefix(8))", isDirectory: true)
        try FileManager.default.createDirectory(
            at: directory.appendingPathComponent("providers"), withIntermediateDirectories: true,
            attributes: [.posixPermissions: 0o700])
        defer { try? FileManager.default.removeItem(at: directory) }
        try subscription.write(to: directory.appendingPathComponent("providers/sub.yaml"))

        let port = FreePort.find()
        let secret = UUID().uuidString
        try ConfigBuilder.buildProbe(controllerPort: port, secret: secret).render()
            .write(to: directory.appendingPathComponent("config.yaml"), atomically: true, encoding: .utf8)

        let logURL = directory.appendingPathComponent("core.log")
        FileManager.default.createFile(atPath: logURL.path, contents: nil)
        let logHandle = try FileHandle(forWritingTo: logURL)
        defer { try? logHandle.close() }

        let process = Process()
        process.executableURL = core
        process.arguments = ["-d", directory.path, "-f", directory.appendingPathComponent("config.yaml").path]
        process.standardOutput = logHandle
        process.standardError = logHandle
        try process.run()
        defer {
            if process.isRunning { process.terminate() }
        }

        func logTail() -> String {
            let text = (try? String(contentsOf: logURL, encoding: .utf8)) ?? ""
            return String(text.split(separator: "\n").suffix(6).joined(separator: "\n"))
        }

        let api = MihomoAPI(port: port, secret: secret)
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if !process.isRunning { throw ProbeError.coreExited(logTail()) }
            if let provider = try? await api.provider(ConfigBuilder.providerName) {
                return provider.proxies
                    .filter { !["Direct", "Reject", "RejectDrop", "Compatible", "Pass", "Dns"].contains($0.type) }
                    .map { ProbedNode(name: $0.name, type: $0.type, udp: $0.udp ?? false) }
            }
            try? await Task.sleep(nanoseconds: 100_000_000)
        }
        throw ProbeError.timedOut(logTail())
    }
}
