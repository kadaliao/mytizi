import Foundation

public enum UnixSocketError: Error, CustomStringConvertible {
    case pathTooLong
    case system(String, Int32)
    case closed
    case tooLarge

    public var description: String {
        switch self {
        case .pathTooLong: return "socket 路径过长"
        case let .system(op, code): return "\(op) 失败：\(String(cString: strerror(code)))（errno \(code)）"
        case .closed: return "连接已关闭"
        case .tooLarge: return "消息过大"
        }
    }
}

/// 一条消息的上限。订阅内容也要经过这里，16 MiB 远大于正常订阅。
public let maxIPCMessageBytes = 16 * 1024 * 1024

private func makeAddress(_ path: String) throws -> sockaddr_un {
    var address = sockaddr_un()
    address.sun_family = sa_family_t(AF_UNIX)
    let capacity = MemoryLayout.size(ofValue: address.sun_path)
    guard path.utf8.count < capacity else { throw UnixSocketError.pathTooLong }
    withUnsafeMutablePointer(to: &address.sun_path) { pointer in
        pointer.withMemoryRebound(to: CChar.self, capacity: capacity) { _ = strlcpy($0, path, capacity) }
    }
    return address
}

private func setTimeouts(_ fd: Int32, seconds: TimeInterval) {
    var value = timeval(tv_sec: Int(seconds), tv_usec: Int32((seconds - floor(seconds)) * 1_000_000))
    setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &value, socklen_t(MemoryLayout<timeval>.size))
    setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &value, socklen_t(MemoryLayout<timeval>.size))
    var noSigPipe: Int32 = 1
    setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSigPipe, socklen_t(MemoryLayout<Int32>.size))
}

public enum UnixSocket {
    public static func connect(path: String, timeout: TimeInterval = 5) throws -> Int32 {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw UnixSocketError.system("socket", errno) }
        setTimeouts(fd, seconds: timeout)
        var address = try makeAddress(path)
        let result = withUnsafePointer(to: &address) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        guard result == 0 else {
            let code = errno
            close(fd)
            throw UnixSocketError.system("connect", code)
        }
        return fd
    }

    /// 创建监听 socket，并把权限设为 mode。调用方负责在 accept 后校验对端 uid。
    public static func listen(path: String, mode: mode_t) throws -> Int32 {
        unlink(path)
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw UnixSocketError.system("socket", errno) }
        var address = try makeAddress(path)
        let bound = withUnsafePointer(to: &address) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                bind(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        guard bound == 0 else {
            let code = errno
            close(fd)
            throw UnixSocketError.system("bind", code)
        }
        chmod(path, mode)
        guard Darwin.listen(fd, 16) == 0 else {
            let code = errno
            close(fd)
            throw UnixSocketError.system("listen", code)
        }
        return fd
    }

    public static func accept(_ listener: Int32, timeout: TimeInterval = 10) throws -> Int32 {
        let fd = Darwin.accept(listener, nil, nil)
        guard fd >= 0 else { throw UnixSocketError.system("accept", errno) }
        setTimeouts(fd, seconds: timeout)
        return fd
    }

    /// 对端的有效 uid（getpeereid），用于限制只有安装者能控制 helper。
    public static func peerUID(_ fd: Int32) -> uid_t? {
        var uid: uid_t = 0
        var gid: gid_t = 0
        return getpeereid(fd, &uid, &gid) == 0 ? uid : nil
    }

    public static func writeAll(_ fd: Int32, _ data: Data) throws {
        try data.withUnsafeBytes { (buffer: UnsafeRawBufferPointer) in
            var offset = 0
            while offset < buffer.count {
                let written = write(fd, buffer.baseAddress!.advanced(by: offset), buffer.count - offset)
                if written < 0 {
                    if errno == EINTR { continue }
                    throw UnixSocketError.system("write", errno)
                }
                offset += written
            }
        }
    }

    /// 读到换行为止（不含换行）。对端在读到任何数据前关闭则抛 closed。
    public static func readLine(_ fd: Int32, limit: Int = maxIPCMessageBytes) throws -> Data {
        var collected = Data()
        var chunk = [UInt8](repeating: 0, count: 64 * 1024)
        while true {
            let count = read(fd, &chunk, chunk.count)
            if count < 0 {
                if errno == EINTR { continue }
                throw UnixSocketError.system("read", errno)
            }
            if count == 0 {
                if collected.isEmpty { throw UnixSocketError.closed }
                return collected
            }
            if let newline = chunk[0..<count].firstIndex(of: 0x0A) {
                collected.append(contentsOf: chunk[0..<newline])
                return collected
            }
            collected.append(contentsOf: chunk[0..<count])
            if collected.count > limit { throw UnixSocketError.tooLarge }
        }
    }

    public static func request<T: Encodable, R: Decodable>(
        path: String, _ payload: T, as _: R.Type, timeout: TimeInterval = 5
    ) throws -> R {
        let fd = try connect(path: path, timeout: timeout)
        defer { close(fd) }
        var message = try IPCCoding.encoder.encode(payload)
        message.append(0x0A)
        try writeAll(fd, message)
        let line = try readLine(fd)
        return try IPCCoding.decoder.decode(R.self, from: line)
    }
}
