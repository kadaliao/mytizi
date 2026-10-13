import Foundation
import MytiziKit

/// helper 的 Unix socket 服务：一次连接一问一答。
///
/// 访问控制有两层：socket 文件只属于安装者（0600），并且每个连接都用 getpeereid 校验对端 uid。
/// 即使文件权限被改坏，其他用户的进程也无法控制 root 进程。
public final class HelperServer {
    private let paths: HelperPaths
    private let ownerUID: uid_t
    private let supervisor: CoreSupervisor
    private let log: RotatingLog
    private var listener: Int32 = -1

    public init(paths: HelperPaths, ownerUID: uid_t, supervisor: CoreSupervisor, log: RotatingLog) {
        self.paths = paths
        self.ownerUID = ownerUID
        self.supervisor = supervisor
        self.log = log
    }

    /// 阻塞运行 accept 循环；出错返回由调用方决定是否退出。
    public func run() throws {
        listener = try UnixSocket.listen(path: paths.socket, mode: 0o600)
        // 当前用户（以及 root）才能连接。
        chown(paths.socket, ownerUID, 20)
        log.line("helper 已就绪，socket \(paths.socket)，owner uid \(ownerUID)")
        while true {
            let client: Int32
            do {
                client = try UnixSocket.accept(listener, timeout: 15)
            } catch {
                if errno == EINTR { continue }
                throw error
            }
            DispatchQueue.global(qos: .userInitiated).async { [self] in
                defer { close(client) }
                serve(client)
            }
        }
    }

    public func stopListening() {
        if listener >= 0 { close(listener) }
        listener = -1
        unlink(paths.socket)
    }

    private func serve(_ fd: Int32) {
        guard let uid = UnixSocket.peerUID(fd), uid == ownerUID || uid == 0 else {
            log.line("拒绝来自 uid \(UnixSocket.peerUID(fd).map(String.init) ?? "?") 的连接")
            return
        }
        let response: HelperResponse
        do {
            let line = try UnixSocket.readLine(fd)
            let request = try IPCCoding.decoder.decode(HelperRequest.self, from: line)
            response = handle(request)
        } catch UnixSocketError.closed {
            return
        } catch {
            response = .failure("bad-request", "无法解析请求：\(error)")
        }
        do {
            var data = try IPCCoding.encoder.encode(response)
            data.append(0x0A)
            try UnixSocket.writeAll(fd, data)
        } catch {
            log.line("回复失败：\(error)")
        }
    }

    public func handle(_ request: HelperRequest) -> HelperResponse {
        switch request.method {
        case .hello:
            return HelperResponse(ok: true, hello: HelloInfo(
                ipcVersion: AppInfo.ipcVersion, helperPID: getpid(), ownerUID: ownerUID,
                coreInstalled: supervisor.coreInstalled))
        case .status:
            return HelperResponse(ok: true, status: supervisor.status())
        case .start:
            guard let params = request.start else { return .failure("bad-request", "缺少 start 参数") }
            log.line("start：controller \(params.controllerPort)，DNS 接管 \(params.takeOverDNS)，关闭 IPv6 \(params.disableIPv6)")
            return supervisor.start(params)
        case .stop:
            log.line("stop")
            return supervisor.stop()
        case .updateFiles:
            return supervisor.updateFiles(request.files ?? [])
        }
    }
}
