import Foundation
import MytiziKit
import Network

/// helper 用到的所有路径。默认是系统位置，测试时注入临时目录。
public struct HelperPaths {
    public var coreBinary: String
    public var coreHome: String
    public var networkState: String
    public var helperLog: String
    public var coreLog: String
    public var socket: String

    public static let system = HelperPaths(
        coreBinary: SystemPaths.coreBinary, coreHome: SystemPaths.coreHome,
        networkState: SystemPaths.networkState, helperLog: SystemPaths.helperLog,
        coreLog: SystemPaths.coreLog, socket: SystemPaths.socket)

    public init(coreBinary: String, coreHome: String, networkState: String, helperLog: String, coreLog: String, socket: String) {
        self.coreBinary = coreBinary
        self.coreHome = coreHome
        self.networkState = networkState
        self.helperLog = helperLog
        self.coreLog = coreLog
        self.socket = socket
    }

    var configPath: String { coreHome + "/config.yaml" }
}

/// 以 root 运行并看管 mihomo 进程。
///
/// 三条原则：
///   1. 核心进程与网络改动同生共死：核心退出（正常、崩溃、被杀）都会立刻恢复 DNS/IPv6；
///   2. 停止时先恢复 DNS 再杀核心，应用不会在中间窗口里解析到一个已经没人应答的地址；
///   3. 所有对外状态都来自本对象，App 看到的"已连接"一定意味着 TUN 网卡真的存在；
///   4. 核心的出站网卡固定在物理默认网卡上（见 RoutingTable），其他 VPN 抢了默认路由也抢不走代理流量。
public final class CoreSupervisor {
    private let paths: HelperPaths
    private let network: NetworkGuard
    private let log: RotatingLog
    private let coreLog: RotatingLog
    /// 返回当前的物理出口网卡；测试时注入。
    private let egressDetector: () -> String?

    /// 串行化 start / stop / updateFiles / reconcile。
    private let operation = NSLock()
    /// 保护下面的状态字段；绝不在持有它时做耗时操作。
    private let stateLock = NSLock()
    private var phase: HelperStatus.Phase = .stopped
    private var process: Process?
    private var generation = 0
    private var expectedExit = false
    private var exited: DispatchSemaphore?
    private var startedAt: Date?
    private var controllerPort: Int?
    private var controllerSecret: String?
    private var tunInterface: String?
    private var egressInterface: String?
    private var lastExit: CoreExit?
    private var activeParams: StartParams?
    private var reconcileTimer: DispatchSourceTimer?
    private var pathMonitor: NWPathMonitor?

    public init(
        paths: HelperPaths, network: NetworkGuard, log: RotatingLog, coreLog: RotatingLog,
        egressDetector: @escaping () -> String? = { RoutingTable.physicalEgress() }
    ) {
        self.paths = paths
        self.network = network
        self.log = log
        self.coreLog = coreLog
        self.egressDetector = egressDetector
    }

    // MARK: 状态

    public func status() -> HelperStatus {
        stateLock.lock(); defer { stateLock.unlock() }
        let running = phase == .running
        return HelperStatus(
            phase: phase, corePID: process.flatMap { $0.isRunning ? $0.processIdentifier : nil },
            startedAt: startedAt, controllerPort: controllerPort,
            controllerSecret: running ? controllerSecret : nil, tunInterface: tunInterface,
            egressInterface: egressInterface, changes: network.currentSummary(), lastExit: lastExit)
    }

    public var coreInstalled: Bool { FileManager.default.isExecutableFile(atPath: paths.coreBinary) }

    private func setPhase(_ new: HelperStatus.Phase) {
        stateLock.lock(); phase = new; stateLock.unlock()
    }

    // MARK: 启动

    public func start(_ params: StartParams) -> HelperResponse {
        operation.lock(); defer { operation.unlock() }

        if status().phase == .running || status().phase == .starting {
            log.line("收到 start，当前已在运行，先停止旧实例")
            stopLocked()
        }
        guard coreInstalled else {
            return .failure("core-missing", "内置核心没有安装，请在 App 里重新安装组件。")
        }
        for name in ["geoip.metadb", "geosite.dat"]
        where !FileManager.default.fileExists(atPath: paths.coreHome + "/" + name) {
            return .failure("core-missing", "缺少地理数据库 \(name)，请在 App 里重新安装组件。")
        }

        // 上次异常退出遗留的网络改动先还原，保证这次快照拿到的是用户的真实设置。
        let leftovers = network.restore()
        if !leftovers.isEmpty { log.line("启动前恢复遗留的网络设置时有失败：\(leftovers)") }

        setPhase(.starting)
        stateLock.lock()
        lastExit = nil
        tunInterface = nil
        egressInterface = nil
        stateLock.unlock()

        do {
            try write(files: params.files)
            try write(config: params.configYAML)
        } catch {
            setPhase(.stopped)
            return .failure("write-failed", "写入配置失败：\(error)")
        }

        let check = Shell.run(paths.coreBinary, ["-t", "-d", paths.coreHome, "-f", paths.configPath], timeout: 30)
        guard check.succeeded else {
            setPhase(.stopped)
            log.line("配置校验失败：\(check.combined)")
            return .failure("config-invalid", "配置没有通过核心的校验。", detail: String(check.combined.suffix(2000)))
        }

        guard let launched = launchCore(params) else {
            setPhase(.stopped)
            return .failure("launch-failed", "无法启动内置核心。", detail: coreLog.tail(lines: 20))
        }

        // 等控制接口可用
        if !waitForController(port: params.controllerPort, secret: params.controllerSecret, generation: launched, timeout: 15) {
            let tail = coreLog.tail(lines: 20)
            log.line("控制接口没有就绪：\(tail)")
            stopLocked()
            return .failure("controller-timeout", "核心启动了，但控制接口没有响应。", detail: tail)
        }

        // 等 TUN 网卡真正出现（内核层面的证据，而不是相信配置）
        var tun: String?
        if params.expectTUN {
            tun = waitForInterface(address: AppInfo.tunGateway, generation: launched, timeout: 10)
            guard tun != nil else {
                let tail = coreLog.tail(lines: 20)
                log.line("TUN 网卡没有出现：\(tail)")
                stopLocked()
                return .failure("tun-missing", "没能创建虚拟网卡（TUN）。", detail: tail)
            }
        }

        // 出口固定：配置里写的是 App 生成配置那一刻的物理出口，这里按当前路由表再核对一次。
        let egress = pinEgress(current: params.egressInterface, port: params.controllerPort, secret: params.controllerSecret)

        if params.takeOverDNS || params.disableIPv6 {
            do {
                try network.apply(takeOverDNS: params.takeOverDNS, disableIPv6: params.disableIPv6)
            } catch {
                log.line("接管网络设置失败：\(error)")
                stopLocked()
                return .failure("network-failed", "修改系统网络设置失败，已撤销。", detail: "\(error)")
            }
        }

        stateLock.lock()
        phase = .running
        startedAt = Date()
        controllerPort = params.controllerPort
        controllerSecret = params.controllerSecret
        tunInterface = tun
        egressInterface = egress
        activeParams = params
        stateLock.unlock()
        startReconcileTimer()
        log.line("已连接（核心 pid \(process?.processIdentifier ?? 0)，TUN \(tun ?? "无")，出口 \(egress ?? "自动")）")
        return HelperResponse(ok: true, status: status())
    }

    private func launchCore(_ params: StartParams) -> Int? {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: paths.coreBinary)
        process.arguments = ["-d", paths.coreHome, "-f", paths.configPath]
        process.environment = ["PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "SAFE_PATHS": paths.coreHome]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe

        stateLock.lock()
        generation += 1
        let current = generation
        expectedExit = false
        let semaphore = DispatchSemaphore(value: 0)
        exited = semaphore
        stateLock.unlock()

        process.terminationHandler = { [weak self] finished in
            self?.handleExit(finished, generation: current, semaphore: semaphore)
        }
        coreLog.line("---- 启动核心 \(Date()) ----")
        let reader = pipe.fileHandleForReading
        Thread.detachNewThread { [coreLog] in
            while true {
                let data = reader.availableData
                if data.isEmpty { break }
                coreLog.write(data)
            }
        }
        do {
            try process.run()
        } catch {
            log.line("启动核心失败：\(error)")
            return nil
        }
        stateLock.lock(); self.process = process; stateLock.unlock()
        return current
    }

    private func handleExit(_ finished: Process, generation exitedGeneration: Int, semaphore: DispatchSemaphore) {
        stateLock.lock()
        let stale = exitedGeneration != generation
        let expected = expectedExit
        if !stale { process = nil }
        stateLock.unlock()
        defer { semaphore.signal() }
        guard !stale else { return }

        let bySignal = finished.terminationReason == .uncaughtSignal
        let exit = CoreExit(
            at: Date(), code: finished.terminationStatus, bySignal: bySignal, expected: expected,
            logTail: expected ? nil : coreLog.tail(lines: 20))
        stateLock.lock()
        lastExit = exit
        stateLock.unlock()
        log.line("核心退出：code \(exit.code)\(bySignal ? "（信号）" : "")\(expected ? "，预期内" : "，非预期")")
        guard !expected else { return }

        // 非预期退出：立刻还原 DNS/IPv6，否则用户会因为 DNS 指向一个已经不存在的地址而断网。
        stopReconcileTimer()
        let failures = network.restore()
        if !failures.isEmpty { log.line("核心退出后恢复网络失败：\(failures)") }
        setPhase(.crashed)
    }

    // MARK: 停止

    public func stop() -> HelperResponse {
        operation.lock(); defer { operation.unlock() }
        stopLocked()
        return HelperResponse(ok: true, status: status())
    }

    /// 程序退出（SIGTERM）前调用：停核心并还原网络。
    public func shutdown() {
        operation.lock(); defer { operation.unlock() }
        stopLocked()
    }

    private func stopLocked() {
        stopReconcileTimer()
        stateLock.lock()
        expectedExit = true
        let running = process
        let semaphore = exited
        if phase == .running || phase == .starting { phase = .stopping }
        stateLock.unlock()

        // 先还原 DNS / IPv6，再停核心。
        let failures = network.restore()
        if !failures.isEmpty { log.line("停止时恢复网络失败：\(failures)") }

        if let running, running.isRunning {
            running.terminate()
            if semaphore?.wait(timeout: .now() + 5) == .timedOut {
                log.line("核心 5 秒内没有退出，强制结束")
                kill(running.processIdentifier, SIGKILL)
                _ = semaphore?.wait(timeout: .now() + 3)
            }
        }
        stateLock.lock()
        phase = .stopped
        process = nil
        controllerSecret = nil
        controllerPort = nil
        tunInterface = nil
        egressInterface = nil
        startedAt = nil
        activeParams = nil
        stateLock.unlock()
    }

    // MARK: 文件

    public func updateFiles(_ files: [HelperFile]) -> HelperResponse {
        operation.lock(); defer { operation.unlock() }
        do {
            try write(files: files)
            return HelperResponse(ok: true, status: status())
        } catch {
            return .failure("write-failed", "写入文件失败：\(error)")
        }
    }

    static func isAllowedFilePath(_ path: String) -> Bool {
        path.range(of: "^providers/[A-Za-z0-9._-]{1,64}$", options: .regularExpression) != nil && !path.contains("..")
    }

    private func write(files: [HelperFile]) throws {
        for file in files {
            guard Self.isAllowedFilePath(file.path) else { throw CocoaError(.fileWriteInvalidFileName, userInfo: [NSFilePathErrorKey: file.path]) }
            guard let data = Data(base64Encoded: file.base64) else { throw CocoaError(.fileReadCorruptFile) }
            try atomicWrite(data, to: paths.coreHome + "/" + file.path)
        }
    }

    private func write(config: String) throws {
        try atomicWrite(Data(config.utf8), to: paths.configPath)
    }

    private func atomicWrite(_ data: Data, to path: String) throws {
        let directory = (path as NSString).deletingLastPathComponent
        try FileManager.default.createDirectory(atPath: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o755])
        let temp = path + ".tmp"
        guard FileManager.default.createFile(atPath: temp, contents: data, attributes: [.posixPermissions: 0o600]) else {
            throw CocoaError(.fileWriteUnknown, userInfo: [NSFilePathErrorKey: temp])
        }
        if rename(temp, path) != 0 { throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO) }
    }

    // MARK: 就绪检查

    private func isCurrent(_ gen: Int) -> Bool {
        stateLock.lock(); defer { stateLock.unlock() }
        return gen == generation && process?.isRunning == true
    }

    private func waitForController(port: Int, secret: String, generation gen: Int, timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            guard isCurrent(gen) else { return false }
            if Self.controllerResponds(port: port, secret: secret) { return true }
            Thread.sleep(forTimeInterval: 0.15)
        }
        return false
    }

    static func controllerResponds(port: Int, secret: String) -> Bool {
        controllerRequest(port: port, secret: secret, method: "GET", path: "/version", timeout: 1) == 200
    }

    /// `PATCH /configs`：核心运行中改 interface-name 等通用项。成功返回 204。
    static func patchGeneral(port: Int, secret: String, values: [String: String]) -> Bool {
        let body = try? JSONSerialization.data(withJSONObject: values)
        let status = controllerRequest(port: port, secret: secret, method: "PATCH", path: "/configs", body: body, timeout: 3)
        return (200..<300).contains(status)
    }

    /// 清掉所有连接：出口换了之后，绑在旧网卡上的连接都已失效，让应用立刻重连。
    static func closeConnections(port: Int, secret: String) {
        _ = controllerRequest(port: port, secret: secret, method: "DELETE", path: "/connections", timeout: 3)
    }

    /// 同步地向控制接口发一个请求，返回 HTTP 状态码（失败为 0）。
    private static func controllerRequest(
        port: Int, secret: String, method: String, path: String, body: Data? = nil, timeout: TimeInterval
    ) -> Int {
        var request = URLRequest(url: URL(string: "http://127.0.0.1:\(port)\(path)")!)
        request.httpMethod = method
        request.setValue("Bearer \(secret)", forHTTPHeaderField: "Authorization")
        if let body {
            request.httpBody = body
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        request.timeoutInterval = timeout
        let configuration = URLSessionConfiguration.ephemeral
        configuration.connectionProxyDictionary = [:]
        let session = URLSession(configuration: configuration)
        defer { session.invalidateAndCancel() }
        let done = DispatchSemaphore(value: 0)
        var status = 0
        session.dataTask(with: request) { _, response, _ in
            status = (response as? HTTPURLResponse)?.statusCode ?? 0
            done.signal()
        }.resume()
        _ = done.wait(timeout: .now() + timeout + 1)
        return status
    }

    private func waitForInterface(address: String, generation gen: Int, timeout: TimeInterval) -> String? {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            guard isCurrent(gen) else { return nil }
            if let name = Self.interfaceName(forIPv4: address) { return name }
            Thread.sleep(forTimeInterval: 0.2)
        }
        return nil
    }

    /// 通过 getifaddrs 找到持有某个 IPv4 地址的网卡（例如 utun7）。
    public static func interfaceName(forIPv4 target: String) -> String? {
        var head: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&head) == 0, let first = head else { return nil }
        defer { freeifaddrs(head) }
        var cursor: UnsafeMutablePointer<ifaddrs>? = first
        while let entry = cursor {
            defer { cursor = entry.pointee.ifa_next }
            guard let address = entry.pointee.ifa_addr, address.pointee.sa_family == sa_family_t(AF_INET) else { continue }
            var buffer = [CChar](repeating: 0, count: Int(INET_ADDRSTRLEN))
            address.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { pointer in
                var sin = pointer.pointee.sin_addr
                inet_ntop(AF_INET, &sin, &buffer, socklen_t(INET_ADDRSTRLEN))
            }
            if String(cString: buffer) == target { return String(cString: entry.pointee.ifa_name) }
        }
        return nil
    }

    // MARK: 出口网卡

    /// 核对出口：路由表里的物理默认网卡与核心当前绑定的不一致时，通过控制接口改过去，并清掉旧连接。
    /// 返回之后生效的出口。暂时没有默认路由（离线、切换中）时不动，避免在过渡期来回抖动。
    private func pinEgress(current: String?, port: Int, secret: String) -> String? {
        guard let detected = egressDetector() else { return current }
        guard detected != current else { return current }
        guard Self.patchGeneral(port: port, secret: secret, values: ["interface-name": detected]) else {
            log.line("设置出口网卡 \(detected) 失败，保持 \(current ?? "自动检测")")
            return current
        }
        log.line("出口网卡：\(current ?? "自动检测") → \(detected)")
        if current != nil { Self.closeConnections(port: port, secret: secret) }
        return detected
    }

    /// 网络变化时（NWPathMonitor 回调）与定时器都走这里；start/stop 正在进行时跳过，下一次再核对。
    public func reconcileEgress() {
        guard operation.`try`() else { return }
        defer { operation.unlock() }
        reconcileEgressLocked()
    }

    private func reconcileEgressLocked() {
        stateLock.lock()
        let running = phase == .running
        let current = egressInterface
        let port = controllerPort
        let secret = controllerSecret
        stateLock.unlock()
        guard running, let port, let secret else { return }
        let updated = pinEgress(current: current, port: port, secret: secret)
        stateLock.lock()
        if phase == .running { egressInterface = updated }
        stateLock.unlock()
    }

    // MARK: 周期性接管新网卡

    /// 连接期间插上 USB 网卡或切换到新的网络服务时，它们的 DNS 不会自动指向 TUN，
    /// 这里每 20 秒补一次（NetworkGuard 对已记录的服务是幂等的），顺带核对出口网卡；
    /// 网络路径一变化（NWPathMonitor）也立刻核对出口，不用等定时器。
    private func startReconcileTimer() {
        stopReconcileTimer()
        let timer = DispatchSource.makeTimerSource(queue: .global(qos: .utility))
        timer.schedule(deadline: .now() + 20, repeating: 20)
        timer.setEventHandler { [weak self] in
            guard let self, self.operation.`try`() else { return }
            defer { self.operation.unlock() }
            self.reconcileEgressLocked()
            self.stateLock.lock()
            let params = self.activeParams
            let running = self.phase == .running
            self.stateLock.unlock()
            guard running, let params, params.takeOverDNS || params.disableIPv6 else { return }
            do {
                try self.network.apply(takeOverDNS: params.takeOverDNS, disableIPv6: params.disableIPv6)
            } catch {
                self.log.line("补充接管新网卡失败：\(error)")
            }
        }
        timer.resume()
        let monitor = NWPathMonitor()
        monitor.pathUpdateHandler = { [weak self] _ in self?.reconcileEgress() }
        monitor.start(queue: .global(qos: .utility))
        stateLock.lock()
        reconcileTimer = timer
        pathMonitor = monitor
        stateLock.unlock()
    }

    private func stopReconcileTimer() {
        stateLock.lock()
        let timer = reconcileTimer
        let monitor = pathMonitor
        reconcileTimer = nil
        pathMonitor = nil
        stateLock.unlock()
        timer?.cancel()
        monitor?.cancel()
    }
}
