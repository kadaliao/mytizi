import Foundation
import MytiziKit

public enum HelperClientError: LocalizedError, Equatable {
    /// socket 不存在或没人监听：组件没装，或 helper 没在运行。
    case notRunning
    case protocolMismatch(helper: Int, app: Int)
    case helper(HelperError)
    case transport(String)

    public var errorDescription: String? {
        switch self {
        case .notRunning: return "后台组件没有运行，需要安装或修复。"
        case let .protocolMismatch(helper, app): return "后台组件版本过旧（协议 \(helper)，需要 \(app)），需要更新。"
        case let .helper(error): return error.message
        case let .transport(reason): return "与后台组件通信失败：\(reason)"
        }
    }

    public var helperError: HelperError? {
        if case let .helper(error) = self { return error }
        return nil
    }
}

/// App 侧的 helper 客户端。socket 调用是阻塞的，统一放到后台队列。
public struct HelperClient {
    public var socketPath: String

    public init(socketPath: String = SystemPaths.socket) { self.socketPath = socketPath }

    public func hello() async throws -> HelloInfo {
        let response = try await send(HelperRequest(method: .hello))
        guard let hello = response.hello else { throw HelperClientError.transport("响应缺少 hello") }
        guard hello.ipcVersion == AppInfo.ipcVersion else {
            throw HelperClientError.protocolMismatch(helper: hello.ipcVersion, app: AppInfo.ipcVersion)
        }
        return hello
    }

    public func status() async throws -> HelperStatus {
        try await statusOf(HelperRequest(method: .status))
    }

    public func start(_ params: StartParams) async throws -> HelperStatus {
        try await statusOf(HelperRequest(method: .start, start: params), timeout: 60)
    }

    public func stop() async throws -> HelperStatus {
        try await statusOf(HelperRequest(method: .stop), timeout: 20)
    }

    public func updateFiles(_ files: [HelperFile]) async throws {
        _ = try await send(HelperRequest(method: .updateFiles, files: files))
    }

    private func statusOf(_ request: HelperRequest, timeout: TimeInterval = 5) async throws -> HelperStatus {
        let response = try await send(request, timeout: timeout)
        guard let status = response.status else { throw HelperClientError.transport("响应缺少 status") }
        return status
    }

    private func send(_ request: HelperRequest, timeout: TimeInterval = 5) async throws -> HelperResponse {
        let path = socketPath
        return try await withCheckedThrowingContinuation { continuation in
            DispatchQueue.global(qos: .userInitiated).async {
                do {
                    let response = try UnixSocket.request(path: path, request, as: HelperResponse.self, timeout: timeout)
                    if response.ok {
                        continuation.resume(returning: response)
                    } else {
                        continuation.resume(throwing: HelperClientError.helper(
                            response.error ?? HelperError(code: "unknown", message: "后台组件返回了未知错误")))
                    }
                } catch let error as UnixSocketError {
                    if case let .system(op, code) = error, op == "connect", code == ENOENT || code == ECONNREFUSED {
                        continuation.resume(throwing: HelperClientError.notRunning)
                    } else {
                        continuation.resume(throwing: HelperClientError.transport("\(error)"))
                    }
                } catch {
                    continuation.resume(throwing: HelperClientError.transport("\(error)"))
                }
            }
        }
    }
}
